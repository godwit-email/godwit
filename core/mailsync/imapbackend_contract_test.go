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

// writeParts is the contract's write-back part, the one a missing MOVE,
// UIDPLUS or COPYUID changes.
var writeParts = []string{"Describe", "FirstSync", "CursorResumes", "FolderReset", "Find", "SetFlags", "Move", "Copy", "Failed", "Unsupported"}

// The whole contract runs against a full server; the write-back part also
// runs against the servers whose missing extensions change how a move or a
// purge is done.
func TestIMAPBackendContract(t *testing.T) {
	backendtest.Run(t, func(t *testing.T) backendtest.Server { return imapServer{testmail.StartIMAP(t)} })
	for name, opts := range map[string][]testmail.Option{
		"NoMove":          {testmail.WithoutMove()},
		"NoCOPYUID":       {testmail.WithoutCOPYUID()},
		"NoMoveNoUIDPlus": {testmail.WithoutMove(), testmail.WithoutUIDPlus()},
		"NoMoveNoCOPYUID": {testmail.WithoutMove(), testmail.WithoutCOPYUID()},
		"MoveNoUIDPlus":   {testmail.WithoutUIDPlus()},
	} {
		t.Run(name, func(t *testing.T) {
			backendtest.RunOnly(t, func(t *testing.T) backendtest.Server { return imapServer{testmail.StartIMAP(t, opts...)} }, writeParts...)
		})
	}
}

func TestIMAPBackendContractGmail(t *testing.T) {
	backendtest.Run(t, func(t *testing.T) backendtest.Server {
		return gmailServer{imapServer{testmail.StartIMAP(t, testmail.Gmail())}}
	})
	t.Run("NoMove", func(t *testing.T) {
		backendtest.RunOnly(t, func(t *testing.T) backendtest.Server {
			return gmailServer{imapServer{testmail.StartIMAP(t, testmail.WithoutMove(), testmail.Gmail())}}
		}, writeParts...)
	})
}

// Without IDLE the server cannot push: Watch says so.
func TestIMAPBackendContractNoIdle(t *testing.T) {
	backendtest.RunOnly(t, func(t *testing.T) backendtest.Server {
		return imapServer{testmail.StartIMAP(t, testmail.WithoutIdle())}
	}, "Watch")
}
