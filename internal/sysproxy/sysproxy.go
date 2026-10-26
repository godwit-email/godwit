// Package sysproxy is the proxy the user set for this computer: the usual
// *_PROXY variables (NO_PROXY honoured), else the system's settings (macOS
// network settings, Windows Internet Options) with their bypass list. An
// app started from the Dock or the Start menu has no *_PROXY variables;
// elsewhere the desktop's proxy reaches apps through them.
package sysproxy

import (
	"net"
	"net/netip"
	"net/url"
	"path"
	"strings"

	"golang.org/x/net/http/httpproxy"
)

// For is the proxy for a request to u (http or https), nil for none. Only
// http:// and socks5:// proxies are given.
func For(u *url.URL) *url.URL {
	if env := httpproxy.FromEnvironment(); env.HTTPProxy != "" || env.HTTPSProxy != "" {
		p, err := env.ProxyFunc()(u)
		if err != nil {
			return nil
		}
		return p
	}
	s := system()
	if bypassed(u.Hostname(), s.bypass) {
		return nil
	}
	if u.Scheme == "https" && s.https != nil {
		return s.https
	}
	if u.Scheme == "http" && s.http != nil {
		return s.http
	}
	return s.socks
}

// settings are the system's proxies by the scheme they serve, and the hosts
// that go around them.
type settings struct {
	http, https, socks *url.URL
	bypass             []string
}

// bypassed is whether host is in the bypass list: a name, a *-pattern, a
// .suffix, an address or CIDR range ("169.254/16" as macOS writes it), or
// <local> (a name without a dot, as Windows means it).
func bypassed(host string, list []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	ip, ipErr := netip.ParseAddr(host)
	for _, e := range list {
		e = strings.ToLower(strings.TrimSpace(e))
		switch {
		case e == "":
		case e == "<local>":
			if ipErr != nil && !strings.Contains(host, ".") {
				return true
			}
		case strings.Contains(e, "/"):
			if p, err := netip.ParsePrefix(fullPrefix(e)); err == nil && ipErr == nil && p.Contains(ip.Unmap()) {
				return true
			}
		case strings.Contains(e, "*"):
			if ok, _ := path.Match(e, host); ok {
				return true
			}
		case strings.HasPrefix(e, "."):
			if strings.HasSuffix(host, e) {
				return true
			}
		case host == e || strings.HasSuffix(host, "."+e):
			return true
		}
	}
	return false
}

// fullPrefix completes an IPv4 range written short, "169.254/16" →
// "169.254.0.0/16".
func fullPrefix(e string) string {
	addr, bits, _ := strings.Cut(e, "/")
	if !strings.Contains(addr, ":") {
		for strings.Count(addr, ".") < 3 {
			addr += ".0"
		}
	}
	return addr + "/" + bits
}

// parseWindows reads Internet Options' ProxyServer ("host:port", or
// "http=host:port;https=host:port;socks=host:port") and ProxyOverride
// ("a;*.b;<local>").
func parseWindows(server, override string) settings {
	var s settings
	entry := func(v, scheme string) *url.URL {
		v = strings.TrimSpace(v)
		if i := strings.Index(v, "://"); i >= 0 {
			v = v[i+3:]
		}
		if _, _, err := net.SplitHostPort(v); err != nil || v == "" {
			return nil
		}
		return &url.URL{Scheme: scheme, Host: v}
	}
	if !strings.Contains(server, "=") {
		s.http = entry(server, "http")
		s.https = s.http
	}
	for _, part := range strings.Split(server, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "http":
			s.http = entry(v, "http")
		case "https":
			s.https = entry(v, "http")
		case "socks":
			s.socks = entry(v, "socks5")
		}
	}
	for _, e := range strings.Split(override, ";") {
		if e = strings.TrimSpace(e); e != "" {
			s.bypass = append(s.bypass, e)
		}
	}
	return s
}
