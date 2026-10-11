package testmail

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"log"
	"maps"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"
)

type config struct {
	move, uidplus, copyuid, noIdle, requireID bool
	xoauth2                                   func(token string) bool // accepts an XOAUTH2 token; nil = not offered
	sent                                      *IMAP                   // SMTP only: SaveToSent target
}

// Option customises StartIMAP and StartSMTP; each option says which servers it affects.
type Option func(*config)

// WithXOAuth2 makes the IMAP and SMTP servers offer XOAUTH2 and accept token for alice.
func WithXOAuth2(token string) Option {
	return WithXOAuth2Func(func(t string) bool { return t == token })
}

// WithXOAuth2Func is WithXOAuth2 with a validator, such as a testoauth server's
// ValidAccess. The XOAUTH2 user may be alice or an alice@ address.
func WithXOAuth2Func(valid func(token string) bool) Option {
	return func(c *config) { c.xoauth2 = valid }
}

// RequireID makes the IMAP server refuse SELECT with "NO Unsafe Login" until the
// client has sent ID, as NetEase does.
func RequireID() Option { return func(c *config) { c.requireID = true } }

// WithoutMove removes MOVE from the advertised capabilities.
func WithoutMove() Option { return func(c *config) { c.move = false } }

// WithoutUIDPlus removes UIDPLUS and suppresses COPYUID/APPENDUID response codes
// (the memory backend always returns them).
func WithoutUIDPlus() Option { return func(c *config) { c.uidplus, c.copyuid = false, false } }

// WithoutCOPYUID keeps UIDPLUS advertised but suppresses COPYUID response codes.
func WithoutCOPYUID() Option { return func(c *config) { c.copyuid = false } }

// WithoutIdle removes IDLE from the advertised capabilities (the server adds it
// on its own, so the connection strips it from CAPABILITY lines).
func WithoutIdle() Option { return func(c *config) { c.noIdle = true } }

// IMAP is a running in-memory IMAP server with one user, alice/secret.
type IMAP struct {
	password   atomic.Value // string; SetPassword changes it
	blockLogin atomic.Pointer[loginBlock]

	Addr string
	User *imapmemserver.User

	cfg         config
	srv         *imapserver.Server
	mu          sync.Mutex
	ln          net.Listener
	conn        map[*tconn]struct{}
	sent        atomic.Int64 // bytes written to clients
	drop        atomic.Int32 // FETCHes to let through + 1; 0 = no drop pending
	cmds        atomic.Int64 // MOVE and COPY commands handled
	dropAppend  atomic.Bool
	dropExpunge atomic.Bool
	logins      atomic.Int64
	ids         []map[string]string // guarded by mu
}

// Logins is how many LOGIN and AUTHENTICATE commands the server has handled.
func (s *IMAP) Logins() int64 { return s.logins.Load() }

// IDs returns the parameters of every ID command received, in order.
func (s *IMAP) IDs() []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]string, len(s.ids))
	for i, id := range s.ids {
		out[i] = maps.Clone(id)
	}
	return out
}

// DropAfterNextAppend makes the next APPEND store the message and then close the
// client's connection before the tagged OK.
func (s *IMAP) DropAfterNextAppend() { s.dropAppend.Store(true) }

// DropOnNextUIDExpunge makes the next UID EXPUNGE close the client's
// connection without expunging anything.
func (s *IMAP) DropOnNextUIDExpunge() { s.dropExpunge.Store(true) }

// MovesAndCopies is how many MOVE and COPY commands the server has handled.
func (s *IMAP) MovesAndCopies() int64 { return s.cmds.Load() }

// uidCodes matches the COPYUID code of a status line.
var uidCodes = regexp.MustCompile(`^\* OK \[COPYUID [^\]]*\] `)

// capLine matches the capability list of an untagged CAPABILITY response or a
// [CAPABILITY] response code.
var capLine = regexp.MustCompile(`^(\* |[^ ]+ OK \[)CAPABILITY [^\]\r\n]*`)

// literal matches a line announcing a literal ({n}, {n+}, ~{n}) and captures n.
var literal = regexp.MustCompile(`\{(\d+)\+?\}\r?\n$`)

func literalLen(line []byte) int {
	m := literal.FindSubmatch(line)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(string(m[1]))
	return n
}

// idCmd matches an ID command line from the client.
var idCmd = regexp.MustCompile(`(?i)^([^ ]+) ID (.*)\r?\n$`)

// idString matches a quoted string in ID parameters.
var idString = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

// tconn is an accepted connection that can be killed mid-FETCH. Both directions
// are parsed into lines and literals; only lines are ever touched. On the way out
// it adds ID to (and with noIdle strips IDLE from) capability lists and, while
// stripCopyUID is set, removes the COPYUID code from untagged OK lines. On the way
// in it answers ID itself, since imapserver has no ID command.
type tconn struct {
	net.Conn
	fetch        atomic.Bool
	stripCopyUID atomic.Bool
	noIdle       bool
	written      atomic.Int32
	sent         *atomic.Int64
	s            *IMAP
	r            *bufio.Reader
	pending      []byte // read from r, not yet returned
	rlit         int    // client literal bytes still to pass through
	midCmd       bool   // the next client line continues a command (after a literal)
	wline        []byte // server line not yet complete
	wlit         int    // server literal bytes still to pass through
	gotID        atomic.Bool
}

func (c *tconn) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		if c.rlit > 0 {
			n, err := c.r.Read(p[:min(len(p), c.rlit)])
			c.rlit -= n
			return n, err
		}
		line, err := c.r.ReadBytes('\n') // whole line, however long, so {n} is never split
		if len(line) == 0 {
			return 0, err
		}
		if err == nil {
			if m := idCmd.FindSubmatch(line); m != nil && !c.midCmd {
				c.handleID(string(m[1]), string(m[2]))
				continue
			}
			c.rlit = literalLen(line)
		}
		c.midCmd = c.rlit > 0
		c.pending = line
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *tconn) handleID(tag, args string) {
	id := map[string]string{}
	unq := strings.NewReplacer(`\"`, `"`, `\\`, `\`)
	ss := idString.FindAllStringSubmatch(args, -1)
	for i := 0; i+1 < len(ss); i += 2 {
		id[unq.Replace(ss[i][1])] = unq.Replace(ss[i+1][1])
	}
	c.s.mu.Lock()
	c.s.ids = append(c.s.ids, id)
	c.s.mu.Unlock()
	c.gotID.Store(true)
	c.Conn.Write([]byte("* ID NIL\r\n" + tag + " OK ID completed\r\n"))
}

func (c *tconn) Write(b []byte) (int, error) {
	if c.fetch.Load() && c.written.Add(1) > 1 {
		c.Conn.Close()
		return 0, net.ErrClosed
	}
	out := c.rewrite(b)
	n, err := c.Conn.Write(out)
	c.sent.Add(int64(n))
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

// rewrite edits complete server lines, passing literals through untouched. A
// partial line is held until its end arrives (the server always ends a flush
// with CRLF unless its buffer filled mid-line).
func (c *tconn) rewrite(b []byte) []byte {
	var out []byte
	for len(b) > 0 {
		if c.wlit > 0 {
			n := min(c.wlit, len(b))
			out, b, c.wlit = append(out, b[:n]...), b[n:], c.wlit-n
			continue
		}
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			c.wline = append(c.wline, b...)
			break
		}
		line := append(c.wline, b[:i+1]...)
		c.wline, b = nil, b[i+1:]
		c.wlit = literalLen(line)
		line = capLine.ReplaceAllFunc(line, func(l []byte) []byte {
			if c.noIdle {
				l = bytes.ReplaceAll(l, []byte(" IDLE"), nil)
			}
			return append(l[:len(l):len(l)], " ID"...) // copy: l aliases line; ID is answered by Read
		})
		if c.stripCopyUID.Load() {
			line = uidCodes.ReplaceAll(line, []byte("* OK "))
		}
		out = append(out, line...)
	}
	return out
}

type tln struct {
	net.Listener
	s *IMAP
}

func (l tln) Accept() (net.Conn, error) {
	nc, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	c := &tconn{Conn: nc, sent: &l.s.sent, noIdle: l.s.cfg.noIdle, s: l.s, r: bufio.NewReader(nc)}
	l.s.mu.Lock()
	l.s.conn[c] = struct{}{}
	l.s.mu.Unlock()
	return c, nil
}

type session struct {
	imapserver.Session
	s    *IMAP
	conn *tconn
}

// Copy drops the COPYUID data when COPYUID is disabled.
func (s session) Copy(set imap.NumSet, dest string) (*imap.CopyData, error) {
	s.s.cmds.Add(1)
	d, err := s.Session.Copy(set, dest)
	if !s.s.cfg.copyuid {
		d = nil
	}
	return d, err
}

// Append drops the APPENDUID data when UIDPLUS is disabled.
func (s session) Append(mb string, r imap.LiteralReader, o *imap.AppendOptions) (*imap.AppendData, error) {
	d, err := s.Session.Append(mb, r, o)
	if err == nil && s.s.dropAppend.CompareAndSwap(true, false) {
		s.conn.Conn.Close()
		return nil, errors.New("dropped")
	}
	if !s.s.cfg.uidplus {
		d = nil
	}
	return d, err
}

// Expunge drops the connection instead of a UID EXPUNGE when asked to.
func (s session) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	if uids != nil && s.s.dropExpunge.CompareAndSwap(true, false) {
		s.conn.Conn.Close()
		return errors.New("dropped")
	}
	return s.Session.Expunge(w, uids)
}

// Move cannot return nil data (MoveWriter is concrete), so the connection strips COPYUID.
func (s session) Move(w *imapserver.MoveWriter, set imap.NumSet, dest string) error {
	s.s.cmds.Add(1)
	if !s.s.cfg.copyuid {
		s.conn.stripCopyUID.Store(true)
		defer s.conn.stripCopyUID.Store(false)
	}
	return s.Session.(imapserver.SessionMove).Move(w, set, dest)
}

// SetPassword changes alice's password, as a user does on the provider's website.
func (s *IMAP) SetPassword(pw string) { s.password.Store(pw) }

type loginBlock struct {
	entered, release chan struct{}
	skip             atomic.Int32
}

// BlockLogin is BlockLoginAfter(0).
func (s *IMAP) BlockLogin() (entered <-chan struct{}, release func()) { return s.BlockLoginAfter(0) }

// BlockLoginAfter lets n logins (LOGIN or XOAUTH2) through and holds the next
// one until release is called (release may be called more than once); the held
// login checks its password or token only then. entered fires once it is held.
func (s *IMAP) BlockLoginAfter(n int) (entered <-chan struct{}, release func()) {
	b := &loginBlock{entered: make(chan struct{}), release: make(chan struct{})}
	b.skip.Store(int32(n))
	s.blockLogin.Store(b)
	var once sync.Once
	return b.entered, func() { once.Do(func() { close(b.release) }) }
}

// holdLogin holds a login BlockLoginAfter picked.
func (s *IMAP) holdLogin() {
	b := s.blockLogin.Load()
	if b == nil || b.skip.Add(-1) >= 0 || !s.blockLogin.CompareAndSwap(b, nil) {
		return
	}
	close(b.entered)
	<-b.release
}

// Login counts logins and checks the current password.
func (s session) Login(username, password string) error {
	s.s.logins.Add(1)
	s.s.holdLogin()
	if password != s.s.password.Load().(string) {
		return imapserver.ErrAuthFailed
	}
	return s.Session.Login(username, "secret")
}

// AuthenticateMechanisms offers XOAUTH2 when configured.
func (s session) AuthenticateMechanisms() []string {
	if s.s.cfg.xoauth2 != nil {
		return []string{sasl.Plain, "XOAUTH2"}
	}
	return []string{sasl.Plain}
}

// Authenticate counts XOAUTH2 attempts as logins; PLAIN goes through Login.
func (s session) Authenticate(mech string) (sasl.Server, error) {
	switch {
	case mech == sasl.Plain:
		return sasl.NewPlainServer(func(_, user, pass string) error { return s.Login(user, pass) }), nil
	case mech == "XOAUTH2" && s.s.cfg.xoauth2 != nil:
		s.s.logins.Add(1)
		valid := func(tok string) bool { s.s.holdLogin(); return s.s.cfg.xoauth2(tok) }
		return &xoauth2Server{valid: valid, login: func(user string) error {
			return s.Session.Login(user, "secret")
		}}, nil
	}
	return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "SASL mechanism not supported"}
}

// Select refuses until ID was sent when RequireID is set.
func (s session) Select(mb string, o *imap.SelectOptions) (*imap.SelectData, error) {
	if s.s.cfg.requireID && !s.conn.gotID.Load() {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "Unsafe Login"}
	}
	return s.Session.Select(mb, o)
}

// Idle polls for updates instead of using the memory backend's tracker, which
// ignores updates queued before IDLE began; real servers report those at once.
func (s session) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		if err := s.Session.Poll(w, true); err != nil {
			return err
		}
		select {
		case <-stop:
			return nil
		case <-t.C:
		}
	}
}

func (s session) Namespace() (*imap.NamespaceData, error) {
	return s.Session.(imapserver.SessionNamespace).Namespace()
}

func (s session) Fetch(w *imapserver.FetchWriter, set imap.NumSet, o *imap.FetchOptions) error {
	for v := s.s.drop.Load(); v > 0; v = s.s.drop.Load() {
		if s.s.drop.CompareAndSwap(v, v-1) {
			if v == 1 {
				s.conn.fetch.Store(true)
			}
			break
		}
	}
	return s.Session.Fetch(w, set, o)
}

// quietLog drops the errors of connections the test closed, which every
// test ends with, and logs the rest.
type quietLog struct{}

func (quietLog) Printf(format string, args ...any) {
	if msg := fmt.Sprintf(format, args...); !strings.Contains(msg, net.ErrClosed.Error()) {
		log.Print(msg)
	}
}

// StartIMAP starts a server on 127.0.0.1:0 with INBOX, Sent, Trash and Archive.
func StartIMAP(t *testing.T, opts ...Option) *IMAP {
	t.Helper()
	s := &IMAP{cfg: config{move: true, uidplus: true, copyuid: true}, conn: map[*tconn]struct{}{}}
	s.password.Store("secret")
	for _, o := range opts {
		o(&s.cfg)
	}
	s.User = imapmemserver.NewUser("alice", "secret")
	for _, m := range []string{"INBOX", "Sent", "Trash", "Archive"} {
		if err := s.User.Create(m, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem := imapmemserver.New()
	mem.AddUser(s.User)

	caps := imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapMove: {}, imap.CapUIDPlus: {}}
	if !s.cfg.move {
		delete(caps, imap.CapMove)
	}
	if !s.cfg.uidplus {
		delete(caps, imap.CapUIDPlus)
	}
	s.srv = imapserver.New(&imapserver.Options{
		NewSession: func(c *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			tc, _ := c.NetConn().(*tconn)
			return session{Session: mem.NewSession(), s: s, conn: tc}, nil, nil
		},
		Caps:         caps,
		InsecureAuth: true,
		Logger:       quietLog{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.Addr = ln.Addr().String()
	s.serve(ln)
	t.Cleanup(s.Stop)
	return s
}

func (s *IMAP) serve(ln net.Listener) {
	s.mu.Lock()
	s.ln = tln{ln, s}
	l := s.ln
	s.mu.Unlock()
	go s.srv.Serve(l)
}

// BytesSent is the total written to all client connections so far.
func (s *IMAP) BytesSent() int64 { return s.sent.Load() }

// Stop closes the listener and every live connection. Data is kept.
func (s *IMAP) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		s.ln.Close()
		s.ln = nil
	}
	for c := range s.conn {
		c.Conn.Close()
		delete(s.conn, c)
	}
}

// Restart listens again on the same address with the same data.
func (s *IMAP) Restart() {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		panic(err)
	}
	s.serve(ln)
}

// DropNextFetch makes the next FETCH write at most one write of response and
// then close the client's connection.
func (s *IMAP) DropNextFetch() { s.DropFetchAfter(0) }

// DropFetchAfter lets n FETCH commands complete and drops the connection during
// the one after, like DropNextFetch.
func (s *IMAP) DropFetchAfter(n int) { s.drop.Store(int32(n) + 1) }

// xoauth2Server checks a SASL XOAUTH2 exchange. A bad token gets the JSON error
// challenge, and the exchange fails after the client's empty reply, as Gmail does.
type xoauth2Server struct {
	valid  func(token string) bool
	login  func(user string) error
	failed bool
}

func (x *xoauth2Server) Next(resp []byte) ([]byte, bool, error) {
	switch {
	case x.failed:
		return nil, true, errors.New("invalid credentials")
	case resp == nil:
		return nil, false, nil // ask for the initial response
	}
	var user, auth string
	for _, f := range strings.Split(string(resp), "\x01") {
		if v, ok := strings.CutPrefix(f, "user="); ok {
			user = v
		} else if v, ok := strings.CutPrefix(f, "auth="); ok {
			auth = v
		}
	}
	user, _, _ = strings.Cut(user, "@")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || !x.valid(token) || x.login(user) != nil {
		x.failed = true
		return []byte(`{"status":"401","schemes":"bearer","scope":"https://mail.google.com/"}`), false, nil
	}
	return nil, true, nil
}
