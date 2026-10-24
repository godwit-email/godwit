package mail

import (
	"errors"
	"net/url"
	"strings"
)

// Mailto is a mailto: URI's draft (RFC 6068).
type Mailto struct {
	To, Cc, Bcc   []string
	Subject, Body string
}

// ParseMailto reads a mailto: URI: the addresses of its path and its to
// field are To; cc, bcc, subject and body are kept, other fields ignored.
// Values are percent-decoded and + stays + (it is not form encoding); one
// with a bad escape ("50% off") is kept as written. The body's line breaks
// become \n, the subject's spaces.
func ParseMailto(link string) (Mailto, error) {
	scheme, rest, ok := strings.Cut(link, ":")
	if !ok || !strings.EqualFold(scheme, "mailto") {
		return Mailto{}, errors.New("not a mailto link")
	}
	rest, _, _ = strings.Cut(rest, "#")
	path, query, _ := strings.Cut(rest, "?")
	var m Mailto
	addrs := func(dst *[]string, s string) {
		for _, a := range strings.Split(unescape(s), ",") {
			if a = strings.TrimSpace(a); a != "" {
				*dst = append(*dst, a)
			}
		}
	}
	addrs(&m.To, path)
	for _, kv := range strings.Split(query, "&") {
		k, v, _ := strings.Cut(kv, "=")
		switch strings.ToLower(k) {
		case "to":
			addrs(&m.To, v)
		case "cc":
			addrs(&m.Cc, v)
		case "bcc":
			addrs(&m.Bcc, v)
		case "subject":
			m.Subject = unescape(v)
		case "body":
			m.Body = unescape(v)
		}
	}
	m.Subject = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(m.Subject)
	m.Body = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(m.Body)
	return m, nil
}

func unescape(s string) string {
	if v, err := url.PathUnescape(s); err == nil {
		return v
	}
	return s
}
