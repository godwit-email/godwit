package testmail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"
)

// DKIMFixture signs real message bytes and serves only deterministic public DNS
// test records. No real mailbox, key or external DNS service is used.
type DKIMFixture struct {
	key     *rsa.PrivateKey
	Records map[string][]string
}

func NewDKIMFixture(t testing.TB) *DKIMFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &DKIMFixture{key: key, Records: make(map[string][]string)}
}
func (f *DKIMFixture) Sign(t testing.TB, from, domain string, options ...func(*dkim.SignOptions)) []byte {
	t.Helper()
	pub, err := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	f.Records["unit._domainkey."+domain] = []string{"v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(pub)}
	raw := fmt.Sprintf("From: %s\r\nTo: recipient@example.test\r\nSubject: signed probe\r\nMessage-ID: <%s@%s>\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nOriginal body.\r\n", from, rand.Text(), domain, time.Now().Format(time.RFC1123Z))
	opt := &dkim.SignOptions{Domain: domain, Selector: "unit", Signer: f.key, HeaderKeys: []string{"From", "To", "Subject", "Message-ID", "Date", "MIME-Version", "Content-Type"}}
	for _, configure := range options {
		configure(opt)
	}
	var signed bytes.Buffer
	if err := dkim.Sign(&signed, strings.NewReader(raw), opt); err != nil {
		t.Fatal(err)
	}
	return signed.Bytes()
}
func (f *DKIMFixture) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if values, ok := f.Records[strings.ToLower(name)]; ok {
		return values, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}
