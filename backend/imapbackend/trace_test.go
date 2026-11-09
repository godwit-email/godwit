package imapbackend

import (
	"bufio"
	"context"
	"log"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

// A sync that fails on a response go-imap cannot parse logs the session's
// masked traffic itself (#450): the error report then shows what the server sent.
func TestSyncLogsTraceOnParseError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte("* OK [CAPABILITY IMAP4rev1] hi\r\n"))
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
			switch strings.ToUpper(f[1]) {
			case "LOGIN":
				c.Write([]byte(f[0] + " OK [CAPABILITY IMAP4rev1] logged in\r\n"))
			case "LIST":
				c.Write([]byte("* LIST () \"/\" Secret\r\n* (Seen)\r\n"))
			default:
				c.Write([]byte(f[0] + " OK done\r\n"))
			}
		}
	}()
	var buf strings.Builder
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	r := New(Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Security: conn.None,
		Credentials: func(context.Context) (backend.Credentials, error) {
			return backend.Credentials{Username: "alice", Password: "hunter2"}, nil
		}})
	defer r.Close()
	if _, err := r.Sync(t.Context(), backend.SyncRequest{}, func(backend.Page) error { return nil }); err == nil {
		t.Fatal("Sync succeeded on a response it cannot parse")
	}
	got := buf.String()
	if !strings.Contains(got, "imap: traffic before") || !strings.Contains(got, "* (xxxx)") {
		t.Errorf("no masked trace logged: %q", got)
	}
	if strings.Contains(got, "Secret") || strings.Contains(got, "hunter2") {
		t.Errorf("the trace is not masked: %q", got)
	}
}
