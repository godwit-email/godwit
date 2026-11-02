package core

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/godwit-email/godwit/internal/sysproxy"
	"github.com/zalando/go-keyring"
)

// Proxy modes (Settings.ProxyMode): the system's proxy, the user's own
// (Settings.ProxyURL), or none.
const (
	ProxySystem = "system"
	ProxyCustom = "custom"
	ProxyOff    = "off"
)

// MaxProxyBypass caps Settings.ProxyBypass.
const MaxProxyBypass = 200

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
// RPC: a custom proxy's URL carries its password.
func (s *Service) Proxy(u *url.URL) *url.URL {
	// a server on this computer (a bridge such as Proton's) never goes through a proxy
	h := u.Hostname()
	if sysproxy.OnThisComputer(h) {
		return nil
	}
	switch c := s.proxy.Load(); c.mode {
	case ProxyOff:
		return nil
	case ProxyCustom:
		if c.url == nil || sysproxy.Bypassed(h, c.bypass) {
			return nil
		}
		p := *c.url
		return &p
	}
	return s.opts.SystemProxy(u)
}

// proxyConfig is the saved proxy Proxy decides by, kept in memory
// (Service.proxy) so a dial reads neither the database nor the keychain.
type proxyConfig struct {
	mode     string
	url      *url.URL // ProxyCustom's, with its credentials
	bypass   []string
	password string
}

// setProxy makes st's proxy, with password, the one Proxy uses.
func (s *Service) setProxy(st Settings, password string) {
	c := &proxyConfig{mode: st.ProxyMode, bypass: st.ProxyBypass, password: password}
	if st.ProxyMode == ProxyCustom {
		c.url, _ = ParseProxyURL(st.ProxyURL) // checked when saved
		switch {
		case c.url == nil:
		case st.ProxyPasswordSet:
			c.url.User = url.UserPassword(st.ProxyUser, password)
		case st.ProxyUser != "":
			c.url.User = url.User(st.ProxyUser)
		}
	}
	s.proxy.Store(c)
}

// loadProxy reads the saved proxy, and its password, for Proxy (at New).
func (s *Service) loadProxy(ctx context.Context) error {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	var pw string
	if st.ProxyPasswordSet {
		pw, _ = keychainGet(s.proxyKeyringUser()) // a lost one: the proxy says so (FailureProxyAuth)
	}
	s.setProxy(st, pw)
	return nil
}

// proxyKeyringUser is the keychain entry of 自定义's proxy password.
func (s *Service) proxyKeyringUser() string { return s.installID + "/proxy" }

// saveProxyPassword puts password, when not "", in the keychain, or deletes
// the stored one when st turns ProxyPasswordSet off.
func (s *Service) saveProxyPassword(password string, st, stored Settings) error {
	var err error
	switch {
	case password != "":
		err = keychainSet(s.proxyKeyringUser(), password)
	case stored.ProxyPasswordSet && !st.ProxyPasswordSet:
		if err = keychainDelete(s.proxyKeyringUser()); errors.Is(err, keyring.ErrNotFound) {
			err = nil
		}
	}
	if err != nil {
		return fmt.Errorf("save the proxy password to keychain: %w", err)
	}
	return nil
}
