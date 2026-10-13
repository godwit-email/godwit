// Package avatar discovers ordinary sender avatars without asserting brand trust.
package avatar

import (
	"net/netip"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// PersonalDomain excludes exact public mailbox domains supplied by core and the
// additional public providers absent from its account-discovery list.
func PersonalDomain(domain string, providerDomains []string) bool {
	domain = normalizeDomain(domain)
	if domain == "" {
		return false
	}
	for _, provider := range providerDomains {
		if domain == normalizeDomain(provider) {
			return false
		}
	}
	for _, provider := range []string{"proton.me", "protonmail.com", "pm.me", "fastmail.com", "hey.com", "gmx.com", "gmx.de", "gmx.net", "web.de", "mail.com", "tutanota.com", "tuta.io", "yandex.com", "yandex.ru", "mail.ru"} {
		if domain == provider {
			return false
		}
	}
	return true
}

// FaviconSites lists the sender domain followed by its distinct organizational domain.
// Each FindFavicon call has a five-second budget, so two sites take at most ten seconds.
func FaviconSites(domain string) []string {
	domain = normalizeDomain(domain)
	if domain == "" {
		return nil
	}
	org, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil {
		return nil
	}
	if org == domain {
		return []string{domain}
	}
	return []string{domain, org}
}

func normalizeDomain(domain string) string {
	domain, err := idna.Lookup.ToASCII(strings.TrimSpace(domain))
	if err != nil {
		return ""
	}
	domain = strings.TrimSuffix(strings.ToLower(domain), ".")
	if len(domain) > 253 {
		return ""
	}
	if _, err := netip.ParseAddr(domain); err == nil {
		return ""
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return ""
			}
		}
	}
	if _, err := publicsuffix.EffectiveTLDPlusOne(domain); err != nil {
		return ""
	}
	return domain
}
