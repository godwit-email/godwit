package core

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// Proxy modes (Settings.ProxyMode): the system's proxy, the user's own
// (Settings.ProxyURL), or none.
const (
	ProxySystem = "system"
	ProxyCustom = "custom"
	ProxyOff    = "off"
)

// ParseProxyURL checks a custom proxy (Settings.ProxyURL): http or socks5, a
// host, a port from 1 to 65535, and nothing else.
func ParseProxyURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return nil, err
	case u.Scheme != "http" && u.Scheme != "socks5":
		return nil, errors.New("proxy type must be http or socks5")
	case u.Hostname() == "":
		return nil, errors.New("proxy host is empty")
	case u.User != nil || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return nil, errors.New("proxy address has more than a host and port")
	}
	if h := u.Hostname(); strings.Contains(h, ":") {
		if _, err := netip.ParseAddr(h); err != nil {
			return nil, errors.New("proxy host is invalid")
		}
	}
	if p, err := strconv.Atoi(u.Port()); err != nil || p < 1 || p > 65535 {
		return nil, errors.New("proxy port must be between 1 and 65535")
	}
	return u, nil
}

// Proxy is the proxy for a connection to u (http or https; a mail server is
// asked as https://host:port), nil to connect direct. Mail, remote images,
// unsubscribes and the update check all take it from here. Not exposed over
// RPC: a custom proxy's URL will carry its password.
func (s *Service) Proxy(u *url.URL) *url.URL {
	// a server on this computer (a bridge such as Proton's) never goes through a proxy
	h := u.Hostname()
	if ip, err := netip.ParseAddr(h); strings.EqualFold(h, "localhost") || err == nil && ip.Unmap().IsLoopback() {
		return nil
	}
	// every dial asks again, so a saved mode applies to the next connection
	switch st, _ := s.loadSettings(context.Background()); st.ProxyMode {
	case ProxyOff:
		return nil
	case ProxyCustom:
		p, _ := ParseProxyURL(st.ProxyURL) // checked when saved
		return p
	}
	return s.opts.SystemProxy(u)
}
