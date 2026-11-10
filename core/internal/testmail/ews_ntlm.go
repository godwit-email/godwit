package testmail

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"unicode/utf16"

	"golang.org/x/crypto/md4" //nolint:staticcheck // NTLM is built on MD4
)

// ewsConn is one TCP connection's NTLM state: NTLM signs in a connection,
// not a request, as IIS does.
type ewsConn struct {
	challenge []byte // the CHALLENGE sent on it, nil before
	signedIn  bool
}

type ewsConnKey struct{}

// connContext gives each connection its own NTLM state.
func connContext(ctx context.Context, _ net.Conn) context.Context {
	return context.WithValue(ctx, ewsConnKey{}, &ewsConn{})
}

// UseNTLM makes the server offer Negotiate and NTLM (NTLMv2) instead of
// Basic, as Exchange's EWS directory does by default. It takes alice as
// EXAMPLE\alice, alice (no domain) or alice@example.com.
func (s *EWS) UseNTLM() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ntlm = true
}

// DropNextChallenge makes the server read the whole next NTLM NEGOTIATE
// request and close the connection instead of challenging it: the request
// went out, but nothing was sent.
func (s *EWS) DropNextChallenge() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropChallenge = true
}

// RequireExtendedProtection makes the server also offer Basic and refuse
// every NTLM sign-in that does not bind the TLS channel, as IIS does when
// Extended Protection is Required.
// shortcut: refuses every NTLM sign-in, since the client sends no channel
// binding; check MsvAvChannelBindings when it does.
func (s *EWS) RequireExtendedProtection() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epRequired = true
}

// signIn says whether r may go on; when not, it has answered r. Call with
// s.mu held.
func (s *EWS) signIn(w http.ResponseWriter, r *http.Request) bool {
	basic := !s.ntlm || s.epRequired
	if user, pass, ok := r.BasicAuth(); ok && basic {
		domain, name, ok := strings.Cut(user, `\`)
		if !ok {
			domain, name = "", user
		}
		if isAlice(name, domain) && pass == "secret" {
			return true
		}
		s.unauthorized(w)
		return false
	}
	if !s.ntlm {
		s.unauthorized(w)
		return false
	}
	c := r.Context().Value(ewsConnKey{}).(*ewsConn)
	if c.signedIn {
		return true
	}
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	msg, err := base64.StdEncoding.DecodeString(token)
	if (scheme != "NTLM" && scheme != "Negotiate") || err != nil || len(msg) < 12 || string(msg[:8]) != "NTLMSSP\x00" {
		s.unauthorized(w)
		return false
	}
	switch binary.LittleEndian.Uint32(msg[8:]) {
	case 1: // NEGOTIATE: challenge it on this connection
		if s.dropChallenge {
			s.dropChallenge = false
			io.Copy(io.Discard, r.Body)
			if c, _, err := http.NewResponseController(w).Hijack(); err == nil {
				c.Close()
			}
			return false
		}
		c.challenge = make([]byte, 8)
		rand.Read(c.challenge)
		w.Header().Set("WWW-Authenticate", scheme+" "+base64.StdEncoding.EncodeToString(challengeMessage(c.challenge)))
		w.WriteHeader(http.StatusUnauthorized)
		return false
	case 3: // AUTHENTICATE
		if c.challenge != nil && !s.epRequired && s.authenticates(msg, c.challenge) {
			c.signedIn = true
			return true
		}
	}
	c.challenge = nil
	s.unauthorized(w)
	return false
}

// unauthorized refuses a request with the schemes the server offers.
func (s *EWS) unauthorized(w http.ResponseWriter) {
	if s.ntlm {
		w.Header().Add("WWW-Authenticate", "Negotiate")
		w.Header().Add("WWW-Authenticate", "NTLM")
	}
	if !s.ntlm || s.epRequired {
		w.Header().Add("WWW-Authenticate", `Basic realm="mail.example"`)
	}
	w.WriteHeader(http.StatusUnauthorized)
}

// challengeMessage is an NTLM CHALLENGE (MS-NLMP 2.2.1.2) asking for
// NTLMv2 in Unicode, with the domain EXAMPLE in its target info.
func challengeMessage(challenge []byte) []byte {
	domain := utf16le("EXAMPLE")
	info := binary.LittleEndian.AppendUint16(nil, 2) // MsvAvNbDomainName
	info = binary.LittleEndian.AppendUint16(info, uint16(len(domain)))
	info = append(info, domain...)
	info = append(info, 0, 0, 0, 0) // MsvAvEOL
	const head = 48
	const flags = 1 | 1<<2 | 1<<9 | 1<<15 | 1<<19 | 1<<23 // Unicode, request target, NTLM, always sign, extended session security, target info
	m := []byte("NTLMSSP\x00")
	m = binary.LittleEndian.AppendUint32(m, 2)
	m = appendField(m, len(domain), head)
	m = binary.LittleEndian.AppendUint32(m, flags)
	m = append(m, challenge...)
	m = append(m, make([]byte, 8)...)
	m = appendField(m, len(info), head+len(domain))
	return append(append(m, domain...), info...)
}

func appendField(m []byte, n, offset int) []byte {
	m = binary.LittleEndian.AppendUint16(m, uint16(n))
	m = binary.LittleEndian.AppendUint16(m, uint16(n))
	return binary.LittleEndian.AppendUint32(m, uint32(offset))
}

// authenticates checks an AUTHENTICATE (MS-NLMP 2.2.1.3) against alice's
// password by its NTLMv2 response (MS-NLMP 3.3.2).
func (s *EWS) authenticates(msg, challenge []byte) bool {
	field := func(at int) []byte {
		if len(msg) < at+8 {
			return nil
		}
		n, off := int(binary.LittleEndian.Uint16(msg[at:])), int(binary.LittleEndian.Uint32(msg[at+4:]))
		if off+n > len(msg) {
			return nil
		}
		return msg[off : off+n]
	}
	nt, domain, user := field(20), fromUTF16le(field(28)), fromUTF16le(field(36))
	if !isAlice(user, domain) || len(nt) <= 16 {
		return false
	}
	h := md4.New()
	h.Write(utf16le("secret"))
	key := hmacMD5(h.Sum(nil), utf16le(strings.ToUpper(user)+domain))
	return hmac.Equal(nt[:16], hmacMD5(key, challenge, nt[16:]))
}

// isAlice says whether user in domain is the mailbox's: alice, in EXAMPLE or
// in none, or alice@example.com.
func isAlice(user, domain string) bool {
	user = strings.ToLower(user)
	return user == "alice" && (domain == "" || strings.EqualFold(domain, "EXAMPLE")) || user == "alice@example.com" && domain == ""
}

func hmacMD5(key []byte, data ...[]byte) []byte {
	m := hmac.New(md5.New, key)
	for _, d := range data {
		m.Write(d)
	}
	return m.Sum(nil)
}

func utf16le(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return b
}

func fromUTF16le(b []byte) string {
	u := make([]uint16, len(b)/2)
	binary.Read(bytes.NewReader(b), binary.LittleEndian, u)
	return string(utf16.Decode(u))
}
