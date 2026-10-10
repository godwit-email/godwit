package auth

import (
	"context"
	"fmt"
	"net"
	"net/mail"
	"sort"
	"strings"

	"github.com/godwit-email/godwit/core/conn"
)

type Source string

const (
	SourceDomain Source = "domain"
	SourceMX     Source = "mx"
	SourceSRV    Source = "srv"
	SourceGuess  Source = "guess"
)

// Resolver is satisfied by *net.Resolver.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupSRV(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
}

// Detect finds the provider for email: domain table, then MX, then SRV, then a
// guess. DNS errors fall through; only a malformed address is an error.
func Detect(ctx context.Context, r Resolver, email string) (Provider, Source, error) {
	a, err := mail.ParseAddress(email)
	if err != nil {
		return Provider{}, "", fmt.Errorf("invalid email address %q", email)
	}
	i := strings.LastIndexByte(a.Address, '@')
	domain := strings.ToLower(a.Address[i+1:])

	for _, p := range table() {
		for _, d := range p.Domains {
			if d == domain {
				if d == "yeah.net" {
					p.IMAP.Host, p.SMTP.Host = "imap.yeah.net", "smtp.yeah.net"
				}
				return p, SourceDomain, nil
			}
		}
	}

	if mxs, err := r.LookupMX(ctx, domain); err == nil && len(mxs) > 0 {
		sort.SliceStable(mxs, func(i, j int) bool { return mxs[i].Pref < mxs[j].Pref })
		host := strings.ToLower(mxs[0].Host)
		id := ""
		switch {
		case inDomain(host, "google.com.") || inDomain(host, "googlemail.com."):
			id = "gmail"
		case inDomain(host, "qq.com.") && strings.HasPrefix(host, "mxbiz"):
			id = "exmail"
		case inDomain(host, "messagingengine.com."):
			id = "fastmail"
		case inDomain(host, "feishu.cn."):
			id = "feishu"
		case inDomain(host, "em.dingtalk.com."):
			id = "dingtalk"
		}
		if p, ok := ByID(id); ok {
			return p, SourceMX, nil
		}
	}

	other := Provider{ID: "other", Name: domain, Login: "password"}
	if imap, ok := lookupSRV(ctx, r, "imaps", domain); ok {
		other.IMAP = imap
		other.SMTP, ok = lookupSRV(ctx, r, "submissions", domain)
		if !ok {
			other.SMTP, ok = lookupSRV(ctx, r, "submission", domain)
		}
		if ok {
			return other, SourceSRV, nil
		}
	}
	other.IMAP = tls("imap." + domain)
	other.SMTP = smtps("smtp." + domain)
	return other, SourceGuess, nil
}

func lookupSRV(ctx context.Context, r Resolver, service, domain string) (Server, bool) {
	_, srvs, err := r.LookupSRV(ctx, service, "tcp", domain)
	if err != nil {
		return Server{}, false
	}
	sort.SliceStable(srvs, func(i, j int) bool { return srvs[i].Priority < srvs[j].Priority })
	for _, s := range srvs {
		host := strings.TrimSuffix(s.Target, ".")
		if host == "" { // "." target: service not offered
			continue
		}
		sec := conn.TLS
		if service == "submission" {
			sec = conn.StartTLS
		}
		return Server{host, int(s.Port), sec}, true
	}
	return Server{}, false
}

// inDomain reports whether host is domain or a subdomain of it (label boundary).
func inDomain(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}
