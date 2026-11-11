package conn

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/godwit-email/godwit/internal/sysproxy"
)

// HTTPTransport is the HTTP backends' transport (JMAP, EWS): it connects as
// IMAP and SMTP do, through the user's proxy, but to a server on this
// computer directly, and trusts TLSRoots.
func HTTPTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.TLSClientConfig = &tls.Config{RootCAs: TLSRoots}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, _ := net.SplitHostPort(addr); !sysproxy.OnThisComputer(host) {
			if p := SystemProxy(&url.URL{Scheme: "https", Host: addr}); p != nil {
				return sysproxy.Dial(ctx, p, addr)
			}
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	return t
}

// ErrStalled is a response body that went too long without a byte arriving.
var ErrStalled = errors.New("the server stopped sending")

// StallBody is body, failing with ErrStalled once d passes with no byte
// arriving; cancel, which should cancel the request, is called then and on
// Close. It bounds a download or event stream that an overall request
// timeout would cut off.
func StallBody(body io.ReadCloser, d time.Duration, cancel context.CancelFunc) io.ReadCloser {
	b := &stallBody{ReadCloser: body, d: d, cancel: cancel}
	b.t = time.AfterFunc(d, func() { b.stalled.Store(true); cancel() })
	return b
}

type stallBody struct {
	io.ReadCloser
	d       time.Duration
	cancel  context.CancelFunc
	t       *time.Timer
	stalled atomic.Bool
}

func (b *stallBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.stalled.Load() {
		return n, ErrStalled
	}
	if n > 0 {
		b.t.Reset(b.d)
	}
	return n, err
}

func (b *stallBody) Close() error {
	b.t.Stop()
	b.cancel()
	return b.ReadCloser.Close()
}
