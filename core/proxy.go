package core

import (
	"context"
	"net/netip"
	"net/url"
	"strings"
)

// Proxy modes (Settings.ProxyMode): the system's proxy, or none.
const (
	ProxySystem = "system"
	ProxyOff    = "off"
)

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
	if st, _ := s.loadSettings(context.Background()); st.ProxyMode == ProxyOff {
		return nil
	}
	return s.opts.SystemProxy(u)
}
