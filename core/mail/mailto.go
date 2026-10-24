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
// Values are percent-decoded and + stays + (it is not form encoding); the
// body's line breaks become \n, the subject's spaces.
func ParseMailto(link string) (Mailto, error) {
	scheme, rest, ok := strings.Cut(link, ":")
	if !ok || !strings.EqualFold(scheme, "mailto") {
		return Mailto{}, errors.New("not a mailto link")
	}
	rest, _, _ = strings.Cut(rest, "#")
	path, query, _ := strings.Cut(rest, "?")
	var m Mailto
	addrs := func(dst *[]string, s string) error {
		v, err := url.PathUnescape(s)
		for _, a := range strings.Split(v, ",") {
			if a = strings.TrimSpace(a); a != "" {
				*dst = append(*dst, a)
			}
		}
		return err
	}
	err := addrs(&m.To, path)
	for _, kv := range strings.Split(query, "&") {
		k, v, _ := strings.Cut(kv, "=")
		var e error
		switch strings.ToLower(k) {
		case "to":
			e = addrs(&m.To, v)
		case "cc":
			e = addrs(&m.Cc, v)
		case "bcc":
			e = addrs(&m.Bcc, v)
		case "subject":
			m.Subject, e = url.PathUnescape(v)
		case "body":
			m.Body, e = url.PathUnescape(v)
		}
		err = errors.Join(err, e)
	}
	if err != nil {
		return Mailto{}, errors.New("bad mailto link: " + err.Error())
	}
	m.Subject = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(m.Subject)
	m.Body = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(m.Body)
	return m, nil
}
