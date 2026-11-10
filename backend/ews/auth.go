package ews

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Azure/go-ntlmssp"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

// ReasonExtendedProtection is the add-account check's refusal of a server
// that requires Extended Protection (NTLM bound to the TLS channel), which
// the client does not do.
const ReasonExtendedProtection = "ews.extended_protection"

// ErrExtendedProtection is a sign-in the server refused over NTLM but took
// over Basic: NTLM without channel binding is what it refuses. Its message
// is ReasonExtendedProtection.
var ErrExtendedProtection = errors.New(ReasonExtendedProtection)

// transport is the HTTP backends' transport kept to HTTP/1.1: NTLM signs in
// a TCP connection, not a request, so it needs keep-alive, and IIS refuses
// it over HTTP/2.
func transport() *http.Transport {
	t := conn.HTTPTransport()
	t.Protocols = new(http.Protocols)
	t.Protocols.SetHTTP1(true)
	return t
}

// negotiator signs a request's Basic credentials in the way the server's
// WWW-Authenticate offers: NTLM (NTLMv2, also under Negotiate) before Basic.
// The login name goes as typed: DOMAIN\user is split into domain and user,
// a UPN goes as the user.
type negotiator struct{ ntlmssp.Negotiator }

// CloseIdleConnections lets http.Client's reach the transport.
func (n negotiator) CloseIdleConnections() { n.RoundTripper.(legs).CloseIdleConnections() }

// legs is the transport under negotiator. It keeps each leg's error in the
// *error its request's context holds under legErrKey: the negotiator answers
// a later leg's failure with the first leg's 401, which would read as a
// wrong password rather than a lost connection.
type legs struct{ *http.Transport }

type legErrKey struct{}

func (t legs) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.Transport.RoundTrip(req)
	if p, ok := req.Context().Value(legErrKey{}).(*error); ok {
		*p = err
	}
	return resp, err
}

// refused is a 401 resp to a request with body, signed in as cr:
// ErrExtendedProtection when the server refused NTLM yet takes the password
// over Basic, ErrAuthFailed otherwise. Only status codes count.
// shortcut: finds Extended Protection only where Basic is on too; without
// it, it is ErrAuthFailed.
func (c *client) refused(ctx context.Context, resp *http.Response, body []byte, cr backend.Credentials) error {
	var ntlm, basic bool
	for _, v := range resp.Header.Values("WWW-Authenticate") {
		scheme, _, _ := strings.Cut(v, " ")
		ntlm = ntlm || strings.EqualFold(scheme, "NTLM") || strings.EqualFold(scheme, "Negotiate")
		basic = basic || strings.EqualFold(scheme, "Basic")
	}
	if ntlm && basic {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "text/xml; charset=utf-8")
			req.SetBasicAuth(cr.Username, cr.Password)
			if r, err := c.plain.RoundTrip(req); err == nil {
				io.Copy(io.Discard, r.Body)
				r.Body.Close()
				if r.StatusCode != http.StatusUnauthorized {
					return ErrExtendedProtection
				}
			}
		}
	}
	return fmt.Errorf("ews: %w", backend.ErrAuthFailed)
}
