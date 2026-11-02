package core

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/zalando/go-keyring"
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
		if p != nil && (st.ProxyUser != "" || st.ProxyPasswordSet) {
			p.User = url.User(st.ProxyUser)
			if st.ProxyPasswordSet {
				pw, _ := keychainGet(s.proxyKeyringUser()) // a lost one: the proxy says so (FailureProxyAuth)
				p.User = url.UserPassword(st.ProxyUser, pw)
			}
		}
		return p
	}
	return s.opts.SystemProxy(u)
}

// proxyKeyringUser is the keychain entry of 自定义's proxy password.
func (s *Service) proxyKeyringUser() string { return s.installID + "/proxy" }

// saveProxyPassword puts st's new proxy password in the keychain, or
// deletes the stored one when st turns ProxyPasswordSet off, and takes the
// password out of st.
func (s *Service) saveProxyPassword(st *Settings, stored Settings) error {
	var err error
	switch {
	case st.ProxyPassword != "":
		err, st.ProxyPasswordSet = keychainSet(s.proxyKeyringUser(), st.ProxyPassword), true
	case stored.ProxyPasswordSet && !st.ProxyPasswordSet:
		if err = keychainDelete(s.proxyKeyringUser()); errors.Is(err, keyring.ErrNotFound) {
			err = nil
		}
	}
	st.ProxyPassword = ""
	if err != nil {
		return fmt.Errorf("save the proxy password to keychain: %w", err)
	}
	return nil
}
