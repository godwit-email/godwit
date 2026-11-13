package ews

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Azure/go-ntlmssp"

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

// signer signs a request's Basic credentials in the way the server's
// WWW-Authenticate offers: NTLM (NTLMv2, also under Negotiate) before Basic.
// A request goes anonymously first, as NTLM may have signed its kept-alive
// connection in already. Each leg's error is returned as it is. The login
// name goes as typed: DOMAIN\user is split into domain and user, a UPN goes
// as the user.
type signer struct{ *http.Transport }

func (t signer) RoundTrip(req *http.Request) (*http.Response, error) {
	user, pass, ok := req.BasicAuth()
	if !ok {
		return t.Transport.RoundTrip(req)
	}
	if req.Body != nil && req.GetBody != nil {
		req.Body.Close() // each leg sends a copy from GetBody
	}
	leg := func(auth string) (*http.Response, error) {
		r := req.Clone(req.Context())
		r.Header.Del("Authorization")
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		if req.GetBody != nil {
			var err error
			if r.Body, err = req.GetBody(); err != nil {
				return nil, err
			}
		}
		return t.Transport.RoundTrip(r)
	}
	resp, err := leg("")
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	scheme, _ := offered(resp)
	switch scheme {
	case "":
		return resp, nil
	case "Basic":
		drain(resp)
		return leg("Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	}
	drain(resp)
	negotiate, err := ntlmssp.NewNegotiateMessage("", "")
	if err != nil {
		return nil, err
	}
	if resp, err = leg(scheme + " " + base64.StdEncoding.EncodeToString(negotiate)); err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	s, challenge := offered(resp)
	if s != scheme || challenge == nil {
		return resp, nil // refused without a challenge
	}
	drain(resp)
	authenticate, err := ntlmssp.NewAuthenticateMessage(challenge, user, pass, nil)
	if err != nil {
		return nil, err
	}
	return leg(scheme + " " + base64.StdEncoding.EncodeToString(authenticate))
}

// offered is the scheme resp's WWW-Authenticate prefers (NTLM, Negotiate,
// then Basic; "" for none of them) and its NTLM token, if any. A value may
// list several challenges (RFC 7235 4.1), and schemes match in any case.
func offered(resp *http.Response) (scheme string, token []byte) {
	rank := map[string]int{"NTLM": 3, "Negotiate": 2, "Basic": 1}
	for _, v := range resp.Header.Values("WWW-Authenticate") {
		for _, c := range challenges(v) {
			s, data, _ := strings.Cut(c, " ")
			for name := range rank {
				if strings.EqualFold(s, name) {
					s = name
				}
			}
			if rank[s] > rank[scheme] {
				scheme, token = s, nil
				if s != "Basic" && data != "" {
					token, _ = base64.StdEncoding.DecodeString(strings.TrimSpace(data))
				}
			}
		}
	}
	return scheme, token
}

// challenges splits a WWW-Authenticate value into its challenges: a comma
// outside quotes ends an item, and an item whose first word holds "=" is a
// param of the challenge before it, not a scheme.
func challenges(v string) []string {
	var items []string
	start, quoted := 0, false
	for i := 0; i <= len(v); i++ {
		switch {
		case i < len(v) && v[i] == '"':
			quoted = !quoted
		case i == len(v) || v[i] == ',' && !quoted:
			items = append(items, strings.TrimSpace(v[start:i]))
			start = i + 1
		}
	}
	var out []string
	for _, it := range items {
		word, _, _ := strings.Cut(it, " ")
		if it == "" || strings.Contains(word, "=") && len(out) > 0 {
			continue // a param of the challenge before
		}
		out = append(out, it)
	}
	return out
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// takesBasic reports that the server takes the account's password over
// Basic alone. It sends a GetFolder of the inbox, a read, so the probe
// changes nothing whatever the server does with it. Only the add-account
// check asks, after an NTLM sign-in was refused: elsewhere a refused
// sign-in is a wrong password, and one more failed logon could lock the
// account out.
// shortcut: finds Extended Protection only where Basic is on too; without
// it, the check says the password is wrong.
func (c *client) takesBasic(ctx context.Context) bool {
	cr, err := c.cfg.Credentials(ctx)
	if err != nil {
		return false
	}
	body, err := xml.Marshal(newRequest(getFolderRequest{BaseShape: "IdOnly", FolderIDs: []folderID{distinguished("inbox")}}))
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(append([]byte(xml.Header), body...)))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.SetBasicAuth(cr.Username, cr.Password)
	resp, err := c.plain.RoundTrip(req)
	if err != nil {
		return false
	}
	drain(resp)
	return resp.StatusCode == http.StatusOK
}
