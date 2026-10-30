package mail

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestSecurityStoredNeutralIsAuthoritative(t *testing.T) {
	got := DBToSecurityReport("PayPal <me@mycompany.com>", "mx.receiver.test; dmarc=fail header.from=mycompany.com", "neutral", "unverified")
	if got.Status != SecurityNeutral || got.WarningKind != WarnNone {
		t.Fatalf("outgoing neutral verdict overwritten: %+v", got)
	}
}

type vmcFixture struct {
	root            *x509.Certificate
	rootKey         *ecdsa.PrivateKey
	intermediate    *x509.Certificate
	intermediateKey *ecdsa.PrivateKey
	roots           *x509.CertPool
	now             time.Time
}

func newVMCFixture(t *testing.T, issuerBrand bool) vmcFixture {
	t.Helper()
	now := time.Now()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "VMC Test Root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, UnknownExtKeyUsage: []asn1.ObjectIdentifier{brandIndicatorOID}}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	ik, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	it := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "VMC Test Issuer"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	if issuerBrand {
		it.UnknownExtKeyUsage = []asn1.ObjectIdentifier{brandIndicatorOID}
	} else {
		it.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, it, root, &ik.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	intermediate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	return vmcFixture{root, rootKey, intermediate, ik, roots, now}
}
func (f vmcFixture) leaf(t *testing.T, brand, chain, expired bool) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(3), DNSNames: []string{"brand.example.com"}, Subject: pkix.Name{CommonName: "brand.example.com"}, NotBefore: f.now.Add(-time.Hour), NotAfter: f.now.Add(30 * time.Minute), KeyUsage: x509.KeyUsageDigitalSignature}
	if expired {
		leaf.NotAfter = f.now.Add(-time.Minute)
	}
	if brand {
		leaf.UnknownExtKeyUsage = []asn1.ObjectIdentifier{brandIndicatorOID}
	} else {
		leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	issuer, keyIssuer := f.root, f.rootKey
	if chain {
		issuer, keyIssuer = f.intermediate, f.intermediateKey
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, issuer, &key.PublicKey, keyIssuer)
	if err != nil {
		t.Fatal(err)
	}
	out := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if chain {
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.intermediate.Raw})...)
	}
	return out
}
func TestSecurityVMCValidation(t *testing.T) {
	f := newVMCFixture(t, true)
	for _, tc := range []struct {
		name   string
		body   []byte
		domain string
		roots  *x509.CertPool
		want   bool
	}{
		{"direct BIMI issuance", f.leaf(t, true, false, false), "brand.example.com", f.roots, true},
		{"complete BIMI chain", f.leaf(t, true, true, false), "brand.example.com", f.roots, true},
		{"TLS certificate is not VMC", f.leaf(t, false, true, false), "brand.example.com", f.roots, false},
		{"expired certificate", f.leaf(t, true, true, true), "brand.example.com", f.roots, false},
		{"wrong sender domain", f.leaf(t, true, true, false), "other.example.com", f.roots, false},
		{"untrusted chain", f.leaf(t, true, true, false), "brand.example.com", x509.NewCertPool(), false},
		{"malformed PEM", []byte("not a certificate"), "brand.example.com", f.roots, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expires, ok := verifyVMC(tc.body, tc.domain, tc.roots, f.now)
			if ok != tc.want {
				t.Fatalf("valid=%v; want %v", ok, tc.want)
			}
			if ok && expires.After(f.now.Add(30*time.Minute)) {
				t.Fatal("cache exceeds certificate validity")
			}
		})
	}
	badIssuer := newVMCFixture(t, false)
	if _, ok := verifyVMC(badIssuer.leaf(t, true, true, false), "brand.example.com", badIssuer.roots, badIssuer.now); ok {
		t.Fatal("non-BIMI issuer accepted")
	}
	full := f.leaf(t, true, true, false)
	block, _ := pem.Decode(full)
	if _, ok := verifyVMC(pem.EncodeToMemory(block), "brand.example.com", f.roots, f.now); ok {
		t.Fatal("missing intermediate accepted")
	}
}

func TestSecurityBIMICacheExpiry(t *testing.T) {
	const domain = "cache-probe.example.com"
	defer bimiCache.Delete(domain)
	bimiCache.Store(domain, bimiCacheEntry{expires: time.Now().Add(-time.Second)})
	if cachedBIMI(domain) {
		t.Fatal("expired cache accepted")
	}
	bimiCache.Store(domain, bimiCacheEntry{expires: time.Now().Add(time.Minute)})
	if got := EvaluateSecurity("a@"+domain, "mx.receiver.test; dmarc=pass header.from="+domain); got.Status != SecurityCertifiedBrand {
		t.Fatal(got)
	}
	if got := EvaluateSecurity("a@"+domain, "mx.receiver.test; dkim=pass header.d="+domain); got.Status == SecurityCertifiedBrand {
		t.Fatal("cache bypassed DMARC requirement")
	}
}
