package mail

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"
	"github.com/godwit-email/godwit/core/internal/testmail"
)

func TestLocalDKIMCryptographicVerification(t *testing.T) {
	f := testmail.NewDKIMFixture(t)
	from := "Alice <alice@sender.example>"
	raw := f.Sign(t, from, "sender.example")
	f.Records["_dmarc.sender.example"] = []string{"v=DMARC1; p=reject; adkim=s"}
	tampered := bytes.Replace(raw, []byte("Original body."), []byte("Forged body."), 1)
	forgedFrom := bytes.Replace(raw, []byte(from), []byte("PayPal <service@paypal.com>"), 1)
	unrelated := f.Sign(t, "PayPal <service@paypal.com>", "attacker.example")
	expired := f.Sign(t, from, "sender.example", func(o *dkim.SignOptions) { o.Expiration = time.Now().Add(-time.Hour) })
	for _, tc := range []struct {
		name, from string
		raw        []byte
		want       SecurityStatus
		dmarc      bool
	}{
		{"valid aligned signature", from, raw, SecurityVerifiedDomain, true},
		{"changed message body", from, tampered, SecurityNeutral, false},
		{"changed signed sender", "PayPal <service@paypal.com>", forgedFrom, SecurityNeutral, false},
		{"valid signature for unrelated domain", "PayPal <service@paypal.com>", unrelated, SecurityNeutral, false},
		{"raw sender must match displayed sender", "other@sender.example", raw, SecurityNeutral, false},
		{"duplicate From headers", from, append([]byte("From: mallory@attacker.example\r\n"), raw...), SecurityNeutral, false},
		{"expired signature", from, expired, SecurityNeutral, false},
		{"fake signature header", from, []byte("DKIM-Signature: v=1; a=rsa-sha256; d=sender.example; s=unit; b=invalid\r\nFrom: " + from + "\r\n\r\nattack"), SecurityNeutral, false},
		{"unsigned forged provider claim", "PayPal <service@paypal.com>", []byte("Received: from attacker.invalid by mx.receiver.test; Thu, 29 Oct 2026 00:00:00 +0000\r\nAuthentication-Results: mx.receiver.test; dmarc=pass header.from=paypal.com; bimi=pass\r\nFrom: PayPal <service@paypal.com>\r\n\r\nattack"), SecurityNeutral, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep, err := VerifyMessageDKIM(t.Context(), tc.from, tc.raw, f.LookupTXT)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Status != tc.want || rep.DMARCPass != tc.dmarc {
				t.Fatalf("got %+v; want status=%v dmarc=%v", rep, tc.want, tc.dmarc)
			}
		})
	}
}

func TestLocalDKIMCannotBorrowBIMICache(t *testing.T) {
	f := testmail.NewDKIMFixture(t)
	raw := f.Sign(t, "PayPal <service@paypal.com>", "attacker.example")
	bimiCache.Store("paypal.com", bimiCacheEntry{expires: time.Now().Add(time.Hour)})
	defer bimiCache.Delete("paypal.com")
	rep, err := VerifyMessageDKIM(t.Context(), "PayPal <service@paypal.com>", raw, f.LookupTXT)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != SecurityNeutral || rep.DMARCPass || rep.BIMIPass {
		t.Fatalf("unrelated signature borrowed real brand certificate: %+v", rep)
	}
}

func TestLocalDKIMAlignmentAndPolicy(t *testing.T) {
	f := testmail.NewDKIMFixture(t)
	raw := f.Sign(t, "alice@billing.sender.example", "mail.sender.example")
	f.Records["_dmarc.sender.example"] = []string{"v=DMARC1; p=reject; adkim=s"}
	rep, err := VerifyMessageDKIM(t.Context(), "alice@billing.sender.example", raw, f.LookupTXT)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != SecurityNeutral || rep.DMARCPass {
		t.Fatalf("strict DMARC alignment ignored: %+v", rep)
	}
	f.Records["_dmarc.sender.example"] = []string{"v=DMARC1; p=reject; adkim=r"}
	rep, err = VerifyMessageDKIM(t.Context(), "alice@billing.sender.example", raw, f.LookupTXT)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != SecurityVerifiedDomain || !rep.DMARCPass {
		t.Fatalf("relaxed organizational alignment failed: %+v", rep)
	}
	direct := f.Sign(t, "alice@sender.example", "sender.example")
	delete(f.Records, "_dmarc.sender.example")
	rep, err = VerifyMessageDKIM(t.Context(), "alice@sender.example", direct, f.LookupTXT)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != SecurityVerifiedDomain || rep.DMARCPass {
		t.Fatalf("DKIM-only result must not claim DMARC: %+v", rep)
	}
}

func TestLocalDKIMTransientDNSFailureRetries(t *testing.T) {
	f := testmail.NewDKIMFixture(t)
	raw := f.Sign(t, "alice@sender.example", "sender.example")
	rep, err := VerifyMessageDKIM(t.Context(), "alice@sender.example", raw, func(context.Context, string) ([]string, error) {
		return nil, &net.DNSError{Err: "timeout", IsTimeout: true, IsTemporary: true}
	})
	if err == nil || rep.Status != SecurityNeutral {
		t.Fatalf("temporary failure consumed: %+v err=%v", rep, err)
	}
}

func TestLocalDKIMRejectsRevokedKey(t *testing.T) {
	f := testmail.NewDKIMFixture(t)
	raw := f.Sign(t, "alice@sender.example", "sender.example")
	f.Records["unit._domainkey.sender.example"] = []string{"v=DKIM1; k=rsa; p="}
	rep, err := VerifyMessageDKIM(t.Context(), "alice@sender.example", raw, f.LookupTXT)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != SecurityNeutral || rep.DKIMPass {
		t.Fatalf("revoked DKIM key granted badge: %+v", rep)
	}
}
func TestLocalDKIMMultipleSignatures(t *testing.T) {
	f := testmail.NewDKIMFixture(t)
	raw := f.Sign(t, "alice@sender.example", "sender.example")
	other := f.Sign(t, "alice@sender.example", "attacker.example")
	// Prepend an unrelated invalid signature without permitting it to replace
	// the aligned valid signature on the original message.
	end := bytes.Index(other, []byte("\r\nFrom:"))
	if end < 0 {
		t.Fatal("signature fixture missing boundary")
	}
	multi := append(append([]byte{}, other[:end+2]...), raw...)
	rep, err := VerifyMessageDKIM(t.Context(), "alice@sender.example", multi, f.LookupTXT)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != SecurityVerifiedDomain || !rep.DKIMPass {
		t.Fatalf("valid aligned signature lost: %+v", rep)
	}
}
