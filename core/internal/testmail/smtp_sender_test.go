package testmail_test

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/emersion/go-smtp"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/backendtest"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/backend/smtpbackend"
	"github.com/godwit-email/godwit/core/internal/testmail"
)

type smtpServer struct {
	addr string
	in   *testmail.Inbox
}

func (s smtpServer) Sender(*testing.T) backend.Sender {
	host, p, _ := net.SplitHostPort(s.addr)
	port, _ := strconv.Atoi(p)
	return smtpbackend.New(smtpbackend.Config{Host: host, Port: port, Security: conn.None,
		Credentials: func(context.Context) (backend.Credentials, error) {
			return backend.Credentials{Username: "alice", Password: "secret"}, nil
		}})
}

func (s smtpServer) Accepted(*testing.T) []backendtest.Delivery {
	var out []backendtest.Delivery
	for _, m := range s.in.Messages() {
		out = append(out, backendtest.Delivery{Rcpts: m.To, Raw: m.Data})
	}
	return out
}

func (s smtpServer) Refuse(_ *testing.T, rcpt string) {
	s.in.Reply(rcpt, testmail.AtRcpt, &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such user"}, false)
}
func (s smtpServer) LoseVerdict(*testing.T) { s.in.FailAfterData(1) }
func (s smtpServer) RejectAuth(*testing.T)  { s.in.RejectAuth(true) }

func TestSMTPBackendContract(t *testing.T) {
	backendtest.RunSender(t, func(t *testing.T) backendtest.SendServer {
		addr, in := testmail.StartSMTP(t)
		return smtpServer{addr, in}
	})
}
