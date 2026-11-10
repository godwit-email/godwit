package conn

import (
	"bufio"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A server refusing STLS fails the dial with an error, not a panic.
func TestDialPOP3RefusedSTLS(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte("+OK ready\r\n"))
		bufio.NewReader(c).ReadString('\n')
		c.Write([]byte("-ERR no TLS here\r\n"))
	}()
	port := l.Addr().(*net.TCPAddr).Port
	_, _, err = DialPOP3(t.Context(), Endpoint{Host: "127.0.0.1", Port: port, Security: StartTLS, Username: "a", Password: "p"})
	assert.Error(t, err)
}
