package conn_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/internal/sysproxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tunnel starts a proxy that speaks scheme (http CONNECT or socks5), sends
// every tunnel to backend, and records the target it was asked for.
func tunnel(t *testing.T, scheme, backend string) (*url.URL, *atomic.Value) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	var asked atomic.Value
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				br := bufio.NewReader(c)
				if scheme == "http" {
					req, err := http.ReadRequest(br)
					if err != nil || req.Method != http.MethodConnect {
						c.Close()
						return
					}
					asked.Store(req.Host)
					io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n")
				} else {
					// greeting: ver, nmethods, methods; request: ver, cmd, rsv, atyp=3, len, name, port
					hdr := make([]byte, 2)
					io.ReadFull(br, hdr)
					io.ReadFull(br, make([]byte, hdr[1]))
					c.Write([]byte{5, 0})
					req := make([]byte, 5)
					io.ReadFull(br, req)
					name := make([]byte, req[4]+2)
					io.ReadFull(br, name)
					asked.Store(net.JoinHostPort(string(name[:req[4]]), strconv.Itoa(int(binary.BigEndian.Uint16(name[req[4]:])))))
					c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				}
				up, err := net.Dial("tcp", backend)
				if err != nil {
					c.Close()
					return
				}
				go func() { io.Copy(up, br); up.Close() }()
				io.Copy(c, up)
				c.Close()
			}()
		}
	}()
	return &url.URL{Scheme: scheme, Host: ln.Addr().String()}, &asked
}

func TestDialThroughSystemProxy(t *testing.T) {
	im := testmail.StartIMAP(t)
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			proxy, asked := tunnel(t, scheme, im.Addr)
			var got *url.URL
			conn.SystemProxy = func(u *url.URL) *url.URL { got = u; return proxy }
			defer func() { conn.SystemProxy = sysproxy.For }()

			// an unresolvable name: only a proxy that is handed the name can reach it
			e := endpoint(t, im.Addr)
			e.Host = "imap.example"
			c, err := conn.DialIMAP(context.Background(), e)
			require.NoError(t, err)
			c.Close()
			assert.Equal(t, net.JoinHostPort("imap.example", strconv.Itoa(e.Port)), asked.Load())
			assert.Equal(t, "https://"+asked.Load().(string), got.String())
		})
	}
}

func TestDialLoopbackDirect(t *testing.T) {
	im := testmail.StartIMAP(t)
	conn.SystemProxy = func(*url.URL) *url.URL { return &url.URL{Scheme: "socks5", Host: "127.0.0.1:1"} }
	defer func() { conn.SystemProxy = sysproxy.For }()
	c, err := conn.DialIMAP(context.Background(), endpoint(t, im.Addr))
	require.NoError(t, err)
	c.Close()
}

func TestDialCancelWhileProxyStalls(t *testing.T) {
	stalled := fake(t, stall)
	conn.SystemProxy = func(*url.URL) *url.URL {
		return &url.URL{Scheme: "http", Host: net.JoinHostPort(stalled.Host, strconv.Itoa(stalled.Port))}
	}
	defer func() { conn.SystemProxy = sysproxy.For }()
	returnsPromptly(t, func(ctx context.Context) error {
		_, err := conn.DialIMAP(ctx, endpoint(t, "imap.example:143"))
		return err
	})
}

// rejecting starts a proxy that turns every tunnel down: an http one answers
// status; a socks5 one fails the password check for 407, else replies
// "connection not allowed by ruleset".
func rejecting(t *testing.T, scheme string, status int) *url.URL {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				if scheme == "http" {
					http.ReadRequest(br)
					io.WriteString(c, "HTTP/1.1 "+strconv.Itoa(status)+" "+http.StatusText(status)+"\r\n\r\n")
					return
				}
				hdr := make([]byte, 2)
				io.ReadFull(br, hdr)
				io.ReadFull(br, make([]byte, hdr[1]))
				if status == http.StatusProxyAuthRequired {
					c.Write([]byte{5, 2}) // username/password: ver, ulen, user, plen, pass
					b := make([]byte, 2)
					io.ReadFull(br, b)
					io.ReadFull(br, make([]byte, b[1]))
					io.ReadFull(br, b[:1])
					io.ReadFull(br, make([]byte, b[0]))
					c.Write([]byte{1, 1})
					return
				}
				c.Write([]byte{5, 0})
				req := make([]byte, 5)
				io.ReadFull(br, req)
				io.ReadFull(br, make([]byte, req[4]+2))
				c.Write([]byte{5, 2, 0, 1, 0, 0, 0, 0, 0, 0})
			}()
		}
	}()
	return &url.URL{Scheme: scheme, Host: ln.Addr().String(), User: url.UserPassword("u", "p")}
}

func TestClassifyProxyFailures(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closed := l.Addr().String()
	l.Close()
	defer func() { conn.SystemProxy = sysproxy.For }()
	for _, scheme := range []string{"http", "socks5"} {
		for name, tc := range map[string]struct {
			proxy *url.URL
			want  conn.FailureKind
		}{
			"unreachable": {&url.URL{Scheme: scheme, Host: closed}, conn.FailureProxyUnreachable},
			"refused":     {rejecting(t, scheme, http.StatusForbidden), conn.FailureProxyRefused},
			"auth":        {rejecting(t, scheme, http.StatusProxyAuthRequired), conn.FailureProxyAuth},
		} {
			t.Run(scheme+"/"+name, func(t *testing.T) {
				conn.SystemProxy = func(*url.URL) *url.URL { return tc.proxy }
				_, err := conn.DialIMAP(context.Background(), endpoint(t, "imap.example:143"))
				assert.Equal(t, tc.want, conn.Classify(err), "%v", err)
				assert.NotErrorIs(t, err, conn.ErrAuthFailed)
			})
		}
	}
}
