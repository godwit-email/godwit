package conn

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A server that answers ID with a list, as Outlook, Gmail and iCloud do, then
// goes silent until Godwit aborts the connection (as the IDLE watcher does when
// it stops): the failure is what happened, not a parse error the ID response
// left behind in the decoder (#749). Before the go-imap fork fixed
// readID, every later failure on such a connection read
// `in response: cannot read tag: imapwire: expected atom, got "("`.
func TestIDListLeavesNoStaleParseError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte("* OK [CAPABILITY IMAP4rev1 ID IDLE] The Microsoft Exchange IMAP4 service is ready.\r\n"))
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
				c.Write([]byte(f[0] + " OK [CAPABILITY IMAP4rev1 ID IDLE] LOGIN completed.\r\n"))
			case "ID":
				c.Write([]byte(`* ID ("name" "Microsoft.Exchange.Imap4.Imap4Server" "version" "15.20")` + "\r\n" + f[0] + " OK ID completed.\r\n"))
			case "NOOP":
				<-done // silent: no answer until the client gives up
				return
			default:
				c.Write([]byte(f[0] + " OK completed.\r\n"))
			}
		}
	}()
	c, abort, err := DialIMAPAbortable(context.Background(), Endpoint{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port,
		Security: None, Username: "alice", Password: "hunter2"}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	cmd := c.Noop()
	abort()
	err = cmd.Wait()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "expected atom", "a stale decoder error from the ID response")
	assert.False(t, protocolErr(err), "a dropped connection is not a parse error: %v", err)
}
