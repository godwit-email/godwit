package conn

import (
	"crypto/tls"
	"crypto/x509"
	"net/http/httptest"
	"testing"
)

// A server with RSA key exchange only (no ECDHE, no TLS 1.3), as
// imap.zmail300.cn is, still completes a handshake.
func TestTLSConfigReachesRSAOnlyServer(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	srv.TLS = &tls.Config{MaxVersion: tls.VersionTLS12, CipherSuites: []uint16{tls.TLS_RSA_WITH_AES_256_GCM_SHA384}}
	srv.StartTLS()
	defer srv.Close()

	cfg := tlsConfig("example.com") // a name on httptest's certificate
	cfg.RootCAs = x509.NewCertPool()
	cfg.RootCAs.AddCert(srv.Certificate())
	c, err := tls.Dial("tcp", srv.Listener.Addr().String(), cfg)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	c.Close()
}
