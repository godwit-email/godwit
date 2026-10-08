package testmail

import (
	"net"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

type config struct{ move, uidplus bool }

// Option customises StartIMAP.
type Option func(*config)

// WithoutMove removes MOVE from the advertised capabilities.
func WithoutMove() Option { return func(c *config) { c.move = false } }

// WithoutUIDPlus removes UIDPLUS and suppresses COPYUID/APPENDUID response codes
// (the memory backend always returns them).
func WithoutUIDPlus() Option { return func(c *config) { c.uidplus = false } }

// IMAP is a running in-memory IMAP server with one user, alice/secret.
type IMAP struct {
	Addr string
	User *imapmemserver.User

	cfg  config
	srv  *imapserver.Server
	mu   sync.Mutex
	ln   net.Listener
	conn map[*tconn]struct{}
	sent atomic.Int64 // bytes written to clients
	drop atomic.Int32 // FETCHes to let through + 1; 0 = no drop pending
}

// uidCodes matches the COPYUID code of a status line.
var uidCodes = regexp.MustCompile(`^\* OK \[COPYUID [^\]]*\] `)

// tconn is an accepted connection that can be killed mid-FETCH. While stripCopyUID
// is set (only during a MOVE, which writes nothing but status lines and EXPUNGEs, never
// a literal) it removes the COPYUID code from the untagged OK line.
type tconn struct {
	net.Conn
	fetch        atomic.Bool
	stripCopyUID atomic.Bool
	written      atomic.Int32
	sent         *atomic.Int64
}

func (c *tconn) Write(b []byte) (int, error) {
	if c.fetch.Load() && c.written.Add(1) > 1 {
		c.Conn.Close()
		return 0, net.ErrClosed
	}
	if c.stripCopyUID.Load() && uidCodes.Match(b) {
		if _, err := c.Conn.Write(uidCodes.ReplaceAll(b, []byte("* OK "))); err != nil {
			return 0, err
		}
		return len(b), nil
	}
	n, err := c.Conn.Write(b)
	c.sent.Add(int64(n))
	return n, err
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
	c := &tconn{Conn: nc, sent: &l.s.sent}
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

// Copy drops the COPYUID data when UIDPLUS is disabled.
func (s session) Copy(set imap.NumSet, dest string) (*imap.CopyData, error) {
	d, err := s.Session.Copy(set, dest)
	if !s.s.cfg.uidplus {
		d = nil
	}
	return d, err
}

// Append drops the APPENDUID data when UIDPLUS is disabled.
func (s session) Append(mb string, r imap.LiteralReader, o *imap.AppendOptions) (*imap.AppendData, error) {
	d, err := s.Session.Append(mb, r, o)
	if !s.s.cfg.uidplus {
		d = nil
	}
	return d, err
}

// Move cannot return nil data (MoveWriter is concrete), so the connection strips COPYUID.
func (s session) Move(w *imapserver.MoveWriter, set imap.NumSet, dest string) error {
	if !s.s.cfg.uidplus {
		s.conn.stripCopyUID.Store(true)
		defer s.conn.stripCopyUID.Store(false)
	}
	return s.Session.(imapserver.SessionMove).Move(w, set, dest)
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

// StartIMAP starts a server on 127.0.0.1:0 with INBOX, Sent, Trash and Archive.
func StartIMAP(t *testing.T, opts ...Option) *IMAP {
	t.Helper()
	s := &IMAP{cfg: config{move: true, uidplus: true}, conn: map[*tconn]struct{}{}}
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
