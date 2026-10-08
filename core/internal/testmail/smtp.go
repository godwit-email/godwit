// Package testmail provides in-memory IMAP and SMTP servers for tests.
package testmail

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"

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
	mu   sync.Mutex
	msgs []Received
	fail int
}

// Messages returns a copy of the received messages.
func (in *Inbox) Messages() []Received {
	in.mu.Lock()
	defer in.mu.Unlock()
	return append([]Received(nil), in.msgs...)
}

// FailAfterData makes the next n sessions read the full DATA and then drop the
// connection without replying. Dropped messages are not recorded.
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

func (s *smtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }

func (s *smtpSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, user, pass string) error {
		if user != "alice" || pass != "secret" {
			return errors.New("invalid credentials")
		}
		s.auth = true
		return nil
	}), nil
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
	defer s.in.mu.Unlock()
	if s.in.fail > 0 {
		s.in.fail--
		s.conn.Close()
		return errors.New("dropped")
	}
	s.in.msgs = append(s.in.msgs, s.cur)
	return nil
}

// StartSMTP starts a plaintext SMTP server accepting only alice/secret (PLAIN).
func StartSMTP(t *testing.T) (addr string, in *Inbox) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	in = &Inbox{}
	srv := smtp.NewServer(backend{in})
	srv.Domain = "localhost"
	srv.AllowInsecureAuth = true
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String(), in
}
