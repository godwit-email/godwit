package imapbackend

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

// When APPEND's synchronizing literal is refused with NO before '+', go-imap
// closes the underlying client connection while Apply returns Outcome: Failed
// with a nil error (#894). A subsequent Sync on the same Receiver must dial a
// fresh session instead of failing with "use of closed network connection".
func TestReceiverReconnectsAfterRejectedAppendLiteral(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	var logins atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.Write([]byte("* OK [CAPABILITY IMAP4rev1 UIDPLUS] ready\r\n"))
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					f := strings.Fields(line)
					if len(f) < 2 {
						return
					}
					tag, verb := f[0], strings.ToUpper(f[1])
					if verb == "UID" && len(f) >= 3 {
						verb = "UID " + strings.ToUpper(f[2])
					}
					switch verb {
					case "LOGIN":
						logins.Add(1)
						c.Write([]byte(tag + " OK [CAPABILITY IMAP4rev1 UIDPLUS] logged in\r\n"))
					case "SELECT", "EXAMINE":
						c.Write([]byte("* 0 EXISTS\r\n* OK [UIDVALIDITY 1] ok\r\n" + tag + " OK selected\r\n"))
					case "UID SEARCH":
						c.Write([]byte("* SEARCH\r\n" + tag + " OK search done\r\n"))
					case "APPEND":
						c.Write([]byte(tag + " NO [CANNOT] Sent Messages is server-managed\r\n"))
					case "LIST":
						c.Write([]byte("* LIST () \"/\" INBOX\r\n* LIST (\\Sent) \"/\" \"Sent Messages\"\r\n" + tag + " OK list done\r\n"))
					case "UID FETCH":
						c.Write([]byte(tag + " OK fetch done\r\n"))
					default:
						c.Write([]byte(tag + " OK done\r\n"))
					}
				}
			}(c)
		}
	}()

	r := New(Config{
		Host:     "127.0.0.1",
		Port:     ln.Addr().(*net.TCPAddr).Port,
		Security: conn.None,
		Credentials: func(context.Context) (backend.Credentials, error) {
			return backend.Credentials{Username: "alice", Password: "secret"}, nil
		},
	})
	defer r.Close()

	raw := []byte("Message-ID: <m1@example.com>\r\nSubject: hi\r\n\r\nhi\r\n")
	res, err := r.Apply(t.Context(), backend.Append{
		To:        "Sent Messages",
		Raw:       raw,
		Flags:     backend.Flags{Seen: true},
		MessageID: "m1@example.com",
	})
	if err != nil {
		t.Fatalf("Apply(Append) err = %v, want nil with Failed outcome", err)
	}
	if res.Outcome != backend.Failed {
		t.Fatalf("Apply(Append) outcome = %q, want %q", res.Outcome, backend.Failed)
	}

	var folders int
	if _, err := r.Sync(t.Context(), backend.SyncRequest{}, func(p backend.Page) error {
		folders += len(p.Changes)
		return nil
	}); err != nil {
		t.Fatalf("Sync after closed APPEND session failed: %v", err)
	}
	if folders != 2 {
		t.Errorf("Sync reported %d folders, want 2", folders)
	}
	if got := logins.Load(); got != 2 {
		t.Errorf("Logins = %d, want 2 (reconnected after dead session)", got)
	}
}
