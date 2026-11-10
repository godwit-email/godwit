package conn

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"

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
