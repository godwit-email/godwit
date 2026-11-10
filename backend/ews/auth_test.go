package ews_test

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/backend/ews"
)

// NTLM signs in a connection, which IIS refuses over HTTP/2: the client
// speaks HTTP/1.1 to a server that offers HTTP/2.
func TestHTTP1Only(t *testing.T) {
	protos := make(chan string, 4)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protos <- r.Proto
		w.WriteHeader(http.StatusUnauthorized)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	conn.TLSRoots = x509.NewCertPool()
	conn.TLSRoots.AddCert(srv.Certificate())
	t.Cleanup(func() { conn.TLSRoots = nil })

	r := ews.New(ews.Config{URL: srv.URL + "/EWS/Exchange.asmx", Credentials: func(context.Context) (backend.Credentials, error) {
		return backend.Credentials{Username: `EXAMPLE\alice`, Password: "secret"}, nil
	}})
	defer r.Close()
	if _, err := r.Describe(t.Context()); !errors.Is(err, backend.ErrAuthFailed) {
		t.Fatalf("Describe = %v, want ErrAuthFailed", err)
	}
	if p := <-protos; p != "HTTP/1.1" {
		t.Errorf("request over %s, want HTTP/1.1", p)
	}
}
