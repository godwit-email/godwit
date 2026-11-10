package ews

import (
	"bytes"
	"net/http"
	"testing"
)

// A server or proxy may list several challenges in one WWW-Authenticate
// value (RFC 7235 4.1), spell schemes in any case, and give Basic params
// that hold commas.
func TestOffered(t *testing.T) {
	for _, c := range []struct {
		name   string
		values []string
		scheme string
		token  []byte
	}{
		{"one value each", []string{"Negotiate", "NTLM", `Basic realm="Exchange"`}, "NTLM", nil},
		{"combined", []string{"Negotiate, NTLM"}, "NTLM", nil},
		{"basic first", []string{`Basic realm="Exchange", NTLM`}, "NTLM", nil},
		{"basic params", []string{`Basic realm="a, b", charset="UTF-8", Negotiate`}, "Negotiate", nil},
		{"case", []string{"ntlm"}, "NTLM", nil},
		{"challenge token", []string{`Basic realm="x", NTLM TlRMTVNTUAACAA==`}, "NTLM", []byte("NTLMSSP\x00\x02\x00")},
		{"basic only", []string{`Basic realm="x"`}, "Basic", nil},
		{"none", []string{`Bearer realm="x"`}, "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{"Www-Authenticate": c.values}}
			scheme, token := offered(resp)
			if scheme != c.scheme || !bytes.Equal(token, c.token) {
				t.Errorf("offered(%q) = %q, %q; want %q, %q", c.values, scheme, token, c.scheme, c.token)
			}
		})
	}
}
