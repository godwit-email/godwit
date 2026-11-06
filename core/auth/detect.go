package auth

import (
	"context"
	"fmt"
	"net"
	"net/mail"
	"slices"
	"sort"
	"strings"

	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/mailservers"
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

// Detect finds the provider for email: domain table, then the providers we sign
// in to by MX, then the mailservers database by domain and by MX, then SRV, then
// a guess. DNS errors fall through; only a malformed address is an error.
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

	other := Provider{ID: "other", Name: domain, Login: "password"}
	mx := ""
	if mxs, err := r.LookupMX(ctx, domain); err == nil && len(mxs) > 0 {
		sort.SliceStable(mxs, func(i, j int) bool { return mxs[i].Pref < mxs[j].Pref })
		host := strings.ToLower(mxs[0].Host)
		mx = host
		id := ""
		switch {
		case inDomain(host, "google.com.") || inDomain(host, "googlemail.com."):
			id = "gmail"
		case inDomain(host, "qq.com.") && strings.HasPrefix(host, "mxbiz"):
			id = "exmail"
		case inDomain(host, "feishu.cn."):
			id = "feishu"
		case inDomain(host, "larksuite.com."):
			id = "lark"
		case inDomain(host, "em.dingtalk.com."):
			id = "dingtalk"
		}
		if p, ok := ByID(id); ok {
			return p, SourceMX, nil
		}
	}
	if p, ok := mailservers.ByDomain(domain); ok {
		if o, ok := withServers(other, p, a.Address); ok {
			return o, SourceDomain, nil
		}
	}
	if p, ok := mailservers.ByMX(domain, mx); ok {
		// the provider's login template is for its own addresses, not a customer's domain
		if o, ok := withServers(other, p, ""); ok {
			return o, SourceMX, nil
		}
	}

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

// withServers returns o with p's servers, unless p has none that take a password
// over an encrypted connection; a non-empty email also fills in p's username for it.
func withServers(o Provider, p mailservers.Provider, email string) (Provider, bool) {
	imap, ok := passwordServer(p.IMAP)
	smtp, ok2 := passwordServer(p.SMTP)
	if !ok || !ok2 {
		return o, false
	}
	o.IMAP = Server{imap.Host, imap.Port, conn.Security(imap.Security)}
	o.SMTP = Server{smtp.Host, smtp.Port, conn.Security(smtp.Security)}
	if u := mailservers.Login(imap, email); email != "" && u != email {
		o.Username = u
	}
	return o, true
}

func passwordServer(list []mailservers.Server) (mailservers.Server, bool) {
	for _, s := range list {
		encrypted := s.Security == string(conn.TLS) || s.Security == string(conn.StartTLS)
		if encrypted && slices.ContainsFunc(s.Auth, func(a string) bool { return strings.HasPrefix(a, "password") }) {
			return s, true
		}
	}
	return mailservers.Server{}, false
}

// inDomain reports whether host is domain or a subdomain of it (label boundary).
func inDomain(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}
