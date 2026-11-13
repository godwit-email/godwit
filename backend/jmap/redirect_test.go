package jmap_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/backend/jmap"
)

// A redirect from HTTPS to plain HTTP on the same host is refused: the
// credentials, sent with every request, would go in the clear.
func TestRedirectKeepsHTTPS(t *testing.T) {
	ts := httptest.NewTLSServer(http.NotFoundHandler())
	ts.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	host := ln.Addr().String()
	plain := make(chan string, 1)
	go http.Serve(eitherTLS{ln, ts.TLS}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			plain <- r.Header.Get("Authorization")
			return
		}
		http.Redirect(w, r, "http://"+host+"/session", http.StatusFound)
	}))
	conn.TLSRoots = x509.NewCertPool()
	conn.TLSRoots.AddCert(ts.Certificate())
	t.Cleanup(func() { conn.TLSRoots = nil })

	r := jmap.New(jmap.Config{SessionURL: "https://" + host + "/jmap/session", Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: "u", Password: "secret"}, nil
	}})
	defer r.Close()
	if _, err := r.Describe(t.Context()); err == nil {
		t.Error("followed a redirect from HTTPS to HTTP")
	}
	select {
	case auth := <-plain:
		t.Errorf("a plain HTTP request went out, Authorization %q", auth)
	default:
	}
}

// eitherTLS serves TLS and plain HTTP on one port, as a server redirecting
// from one to the other on the same host would.
type eitherTLS struct {
	net.Listener
	cfg *tls.Config
}

func (l eitherTLS) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	p := peeked{c, bufio.NewReader(c)}
	if b, err := p.r.Peek(1); err == nil && b[0] == 0x16 { // a TLS handshake record
		return tls.Server(p, l.cfg), nil
	}
	return p, nil
}

type peeked struct {
	net.Conn
	r *bufio.Reader
}

func (p peeked) Read(b []byte) (int, error) { return p.r.Read(b) }
