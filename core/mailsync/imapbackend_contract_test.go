package mailsync_test

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/backendtest"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/backend/imapbackend"
	"github.com/godwit-email/godwit/core/internal/testmail"
)

// imapServer runs the backend contract's fixture on a testmail server; the
// fixture lives here because testmail is internal to core.
type imapServer struct{ im *testmail.IMAP }

func (s imapServer) Receiver(t *testing.T) backend.Receiver {
	host, p, _ := net.SplitHostPort(s.im.Addr)
	port, _ := strconv.Atoi(p)
	r := imapbackend.New(imapbackend.Config{Host: host, Port: port, Security: conn.None,
		Credentials: func(context.Context) (backend.Credentials, error) {
			return backend.Credentials{Username: "alice", Password: "secret"}, nil
		}})
	t.Cleanup(func() { r.Close() })
	return r
}
func (s imapServer) Deliver(t *testing.T, folder string, raw []byte) {
	if _, err := s.im.User.Status(folder, &imap.StatusOptions{}); err != nil {
		if err := s.im.User.Create(folder, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.im.User.Append(folder, bytes.NewReader(raw), &imap.AppendOptions{}); err != nil {
		t.Fatal(err)
	}
}

func (s imapServer) Reset(t *testing.T, folder string) { s.im.ResetUIDValidity(folder) }

func (s imapServer) RejectAuth(t *testing.T) { s.im.SetPassword("rejected") }

// gmailServer delivers as Gmail does, one X-GM-MSGID per message.
type gmailServer struct{ imapServer }

func (s gmailServer) Deliver(t *testing.T, folder string, raw []byte) {
	if _, err := s.im.User.Status(folder, &imap.StatusOptions{}); err != nil {
		if err := s.im.User.Create(folder, nil); err != nil {
			t.Fatal(err)
		}
	}
	s.im.AddGmail(string(raw), nil, folder)
}

// imapParts is the contract the IMAP backend meets so far; #602 adds the
// write-back (Find, SetFlags, Move, Failed, Unsupported).
var imapParts = []string{"Describe", "FirstSync", "CursorResumes", "FolderReset", "Check", "Fetch", "Watch", "AuthFailed"}

func TestIMAPBackendContract(t *testing.T) {
	backendtest.RunOnly(t, func(t *testing.T) backendtest.Server { return imapServer{testmail.StartIMAP(t)} }, imapParts...)
}

func TestIMAPBackendContractGmail(t *testing.T) {
	backendtest.RunOnly(t, func(t *testing.T) backendtest.Server {
		return gmailServer{imapServer{testmail.StartIMAP(t, testmail.Gmail())}}
	}, imapParts...)
}

// Without IDLE the server cannot push: Watch says so.
func TestIMAPBackendContractNoIdle(t *testing.T) {
	backendtest.RunOnly(t, func(t *testing.T) backendtest.Server {
		return imapServer{testmail.StartIMAP(t, testmail.WithoutIdle())}
	}, "Watch")
}
