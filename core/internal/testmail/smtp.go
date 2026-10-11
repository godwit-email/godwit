// Package testmail provides in-memory IMAP and SMTP servers for tests.
package testmail

import (
	"bytes"
	"cmp"
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// Received is one message accepted by the test SMTP server.
type Received struct {
	From string
	To   []string
	Data []byte
}

// Inbox records what the test SMTP server received.
type Inbox struct {
	mu         sync.Mutex
	msgs       []Received
	fail       int
	sent       *IMAP // SaveToSent target
	rejectAuth bool
	password   string                  // alice's; "" means secret
	xoauth2    func(token string) bool // nil = XOAUTH2 not offered
	auths      int
	block      chan struct{} // closed by the BlockData release func
	entered    chan struct{}
}

// BlockData makes the next sessions hold their reply after reading the full DATA
// until release is called (release may be called more than once). entered
// receives once per held session.
func (in *Inbox) BlockData() (entered <-chan struct{}, release func()) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.block, in.entered = make(chan struct{}), make(chan struct{}, 16)
	block := in.block
	var once sync.Once
	return in.entered, func() { once.Do(func() { close(block) }) }
}

// RejectAuth makes the server refuse (or, with false, accept again) alice/secret.
func (in *Inbox) RejectAuth(reject bool) {
	in.mu.Lock()
	in.rejectAuth = reject
	in.mu.Unlock()
}

// SetPassword changes alice's password.
func (in *Inbox) SetPassword(pw string) {
	in.mu.Lock()
	in.password = pw
	in.mu.Unlock()
}

// AuthAttempts is how many AUTH exchanges the server has handled.
func (in *Inbox) AuthAttempts() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.auths
}

// SMTPOption customises StartSMTP; WithXOAuth2 applies to both servers.
type SMTPOption = Option

// SaveToSent makes the SMTP server file every accepted message into im's Sent
// folder, as providers that auto-save sent mail do.
func SaveToSent(im *IMAP) SMTPOption { return func(c *config) { c.sent = im } }

// Messages returns a copy of the received messages.
func (in *Inbox) Messages() []Received {
	in.mu.Lock()
	defer in.mu.Unlock()
	out := make([]Received, len(in.msgs))
	for i, m := range in.msgs {
		out[i] = Received{From: m.From, To: append([]string(nil), m.To...), Data: append([]byte(nil), m.Data...)}
	}
	return out
}

// FailAfterData makes the next n sessions read the full DATA and then drop the
// connection without replying. The server has the message, so it is recorded.
func (in *Inbox) FailAfterData(n int) {
	in.mu.Lock()
	in.fail = n
	in.mu.Unlock()
}

type backend struct{ in *Inbox }

func (b backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &smtpSession{in: b.in, conn: c.Conn()}, nil
}

type smtpSession struct {
	in   *Inbox
	conn net.Conn
	cur  Received
	auth bool
}

func (s *smtpSession) AuthMechanisms() []string {
	if s.in.xoauth2 != nil {
		return []string{sasl.Plain, "XOAUTH2"}
	}
	return []string{sasl.Plain}
}

func (s *smtpSession) Auth(mech string) (sasl.Server, error) {
	s.in.mu.Lock()
	s.in.auths++
	reject, password := s.in.rejectAuth, cmp.Or(s.in.password, "secret")
	s.in.mu.Unlock()
	login := func(user, pass string) error {
		if reject || user != "alice" || pass != password {
			return errors.New("invalid credentials")
		}
		s.auth = true
		return nil
	}
	switch {
	case mech == sasl.Plain:
		return sasl.NewPlainServer(func(_, user, pass string) error { return login(user, pass) }), nil
	case mech == "XOAUTH2" && s.in.xoauth2 != nil:
		return &xoauth2Server{valid: s.in.xoauth2, login: func(user string) error { return login(user, password) }}, nil
	}
	return nil, errors.New("unsupported mechanism")
}

func (s *smtpSession) Reset()        { s.cur = Received{} }
func (s *smtpSession) Logout() error { return nil }

func (s *smtpSession) Mail(from string, _ *smtp.MailOptions) error {
	if !s.auth {
		return smtp.ErrAuthRequired
	}
	s.cur.From = from
	return nil
}

func (s *smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.cur.To = append(s.cur.To, to)
	return nil
}

func (s *smtpSession) Data(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.cur.Data = data
	s.in.mu.Lock()
	block, entered := s.in.block, s.in.entered
	s.in.mu.Unlock()
	if block != nil {
		entered <- struct{}{}
		<-block
	}
	s.in.mu.Lock()
	defer s.in.mu.Unlock()
	s.in.msgs = append(s.in.msgs, s.cur)
	if s.in.fail > 0 {
		s.in.fail--
		s.conn.Close()
		return errors.New("dropped")
	}
	if s.in.sent != nil {
		if _, err := s.in.sent.User.Append("Sent", bytes.NewReader(data), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}}); err != nil {
			return err
		}
	}
	return nil
}

// StartSMTP starts a plaintext SMTP server accepting only alice/secret (PLAIN).
func StartSMTP(t *testing.T, opts ...SMTPOption) (addr string, in *Inbox) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	in = &Inbox{sent: cfg.sent, xoauth2: cfg.xoauth2}
	srv := smtp.NewServer(backend{in})
	srv.Domain = "localhost"
	srv.AllowInsecureAuth = true
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String(), in
}
