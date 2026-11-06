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

	other := Provider{ID: "other", Name: domain, Login: "password"}
	if mxs, err := r.LookupMX(ctx, domain); err == nil && len(mxs) > 0 {
		sort.SliceStable(mxs, func(i, j int) bool { return mxs[i].Pref < mxs[j].Pref })
		host := strings.ToLower(mxs[0].Host)
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
		if imap, smtp, ok := mxServers(host); ok {
			other.IMAP, other.SMTP = imap, smtp
			return other, SourceMX, nil
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

// mxServers returns the IMAP and SMTP servers for hosted corporate mail
// providers whose custom-domain CNAMEs do not carry the customer's domain on
// their TLS certificates.
func mxServers(host string) (imap, smtp Server, ok bool) {
	switch {
	case inDomain(host, "icoremail.net."):
		return tls("imap.icoremail.net"), smtps("smtp.icoremail.net"), true
	case inDomain(host, "mxhichina.com."), inDomain(host, "qiye.aliyun.com."):
		return tls("imap.qiye.aliyun.com"), smtps("smtp.qiye.aliyun.com"), true
	case inDomain(host, "qiye.163.com."),
		inDomain(host, "mxmail.netease.com.") && (strings.HasPrefix(host, "qiye163mx") || strings.HasPrefix(host, "hzmx")):
		return tls("imap.qiye.163.com"), smtps("smtp.qiye.163.com"), true
	case inDomain(host, "ym.163.com."):
		return tls("imap.ym.163.com"), smtps("smtp.ym.163.com"), true
	case inDomain(host, "263xmail.com."):
		return tls("imap.263.net"), smtps("smtp.263.net"), true
	case inDomain(host, "sina.net."), inDomain(host, "entmail.sina.com."):
		return tls("imap.sina.net"), smtps("smtp.sina.net"), true
	case inDomain(host, "messagingengine.com."):
		return tls("imap.fastmail.com"), smtps("smtp.fastmail.com"), true
	}
	for _, zoho := range []string{"zoho.com", "zoho.com.cn", "zoho.eu", "zoho.in", "zoho.com.au", "zoho.jp"} {
		if inDomain(host, zoho+".") {
			return tls("imappro." + zoho), smtps("smtppro." + zoho), true
		}
	}
	return Server{}, Server{}, false
}

// inDomain reports whether host is domain or a subdomain of it (label boundary).
func inDomain(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}
