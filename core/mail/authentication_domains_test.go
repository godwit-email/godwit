package mail

import (
	"strings"
	"testing"
)

func TestAuthenticationDomainPackaging(t *testing.T) {
	for _, value := range []string{
		`malt.com`, `MALT.COM`, `"malt.com"`, `"\"malt.com\""`, `"'malt.com'"`, `'malt.com'`,
		`"  malt.com  "`, `""malt.com""`, `malt.com.`, `(sender domain) "malt.com" (checked)`,
	} {
		for _, property := range []string{"dmarc=pass header.from=", "dkim=pass header.d="} {
			t.Run(property+value, func(t *testing.T) {
				header := "mx.receiver.test; " + property + value
				rep := EvaluateSecurity("Malt <hello@malt.com>", header)
				if rep.Status != SecurityVerifiedDomain {
					t.Fatalf("%q: %+v", header, rep)
				}
				raw := "Authentication-Results: " + header + "\r\nReceived: from sender.invalid by mx.receiver.test; now\r\nFrom: Malt <hello@malt.com>\r\n\r\nhello"
				p, err := Parse([]byte(raw))
				if err != nil {
					t.Fatal(err)
				}
				if got := EvaluateSecurity(p.From, p.AuthenticationResults); got.Status != SecurityVerifiedDomain {
					t.Fatalf("roundtrip %q: %+v", p.AuthenticationResults, got)
				}
			})
		}
	}
	for _, header := range []string{
		`mx.receiver.test; dkim=pass header.i="user@malt.com"`,
		`mx.receiver.test; dkim=pass header.i="\"user@malt.com\""`,
		`mx.receiver.test; dkim=pass header.i="@malt.com"`,
		`mx.receiver.test; dkim=pass header.i="first last"@malt.com`,
		"mx.receiver.test; dmarc=pass header.from=\"\r\n\tmalt.com\"",
		"mx.receiver.test; (check (nested)) dmarc = pass\r\n\theader.from = \"malt.com\"",
		`mx.receiver.test; dmarc=pass reason="comment; header.from=evil.invalid" header.from="malt.com"`,
	} {
		if got := EvaluateSecurity("Malt <hello@malt.com>", header); got.Status != SecurityVerifiedDomain {
			t.Errorf("%q: %+v", header, got)
		}
	}
	for _, value := range []string{`"malt.com"`, `"\"malt.com\""`, `" bounce@malt.com "`, `(envelope) "bounce@malt.com"`, `"<bounce@malt.com>"`} {
		rep := EvaluateSecurity("Malt <hello@malt.com>", "mx.receiver.test; spf=pass smtp.mailfrom="+value)
		if !rep.SPFPass || rep.Status != SecurityNeutral {
			t.Errorf("SPF %q: %+v", value, rep)
		}
	}
}

func TestAuthenticationDomainPackagingRejectsAmbiguity(t *testing.T) {
	for _, fields := range []string{
		`dmarc=pass header.from="mal t.com"`,
		`dmarc="none; dkim=pass header.d=malt.com ignored=x" header.from=evil.invalid`,
		`dkim=pass header.i="evil@other@malt.com"`,
		`dmarc=pass (unterminated header.from=malt.com`,
		"dmarc=pass header.from=malt.com\nInjected: value",
		`dmarc=pass header.from="malt.com.evil.invalid"`,
		`dmarc=pass header.from="evil-malt.com"`,
		`dmarc=pass header.from="malt.com/evil"`,
		`dmarc=pass header.from="malt.com@evil.invalid"`,
		`dmarc=pass header.from="malt.com`,
		`dmarc=pass header.from=malt.com header.from=evil.invalid`,
		`dmarc=pass header.from=evil.invalid header.from=malt.com`,
		`dmarc=pass reason="header.from=malt.com" header.from=evil.invalid`,
		`dmarc=pass (header.from=malt.com) header.from=evil.invalid`,
		`dmarc=pass header.from=mal(comment)t.com`,
		`dkim=pass header.d=evil.invalid header.i="user@malt.com"`,
	} {
		if got := EvaluateSecurity("Malt <hello@malt.com>", "mx.receiver.test; "+fields); got.Status != SecurityNeutral {
			t.Errorf("%q: %+v", fields, got)
		}
	}
	for _, fields := range []string{`dmarc=fail header.from="\"malt.com\""`, `dkim=fail header.d="malt.com"; spf=fail smtp.mailfrom="bounce@malt.com"`} {
		if got := EvaluateSecurity("Malt <hello@malt.com>", "mx.receiver.test; "+fields); got.WarningKind != WarnAuthFailed {
			t.Errorf("lost failure %q: %+v", fields, got)
		}
	}
}

func TestAuthenticationInternationalDomainPackaging(t *testing.T) {
	for _, from := range []string{"hello@bücher.de", "hello@xn--bcher-kva.de"} {
		for _, domain := range []string{`"bücher.de"`, `"XN--BCHER-KVA.DE."`} {
			got := EvaluateSecurity(from, "receiver.test; dmarc=pass header.from="+domain)
			if got.Status != SecurityVerifiedDomain {
				t.Errorf("%s / %s: %+v", from, domain, got)
			}
		}
	}
}

func TestAuthenticationSPFHeloPackaging(t *testing.T) {
	got := EvaluateSecurity("hello@malt.com", `receiver.test; spf=pass smtp.helo="\"MALT.COM.\""`)
	if !got.SPFPass || got.Status != SecurityNeutral {
		t.Fatalf("%+v", got)
	}
}

func TestAuthenticationCommentAndQuoteCannotInjectResult(t *testing.T) {
	for _, header := range []string{
		`receiver.test; dmarc=none reason="nothing; dkim=pass header.d=malt.com ignored=x"`,
		`receiver.test; dmarc=none (nothing; dkim=pass header.d=malt.com)`,
		`"receiver.test; dkim=pass header.d=malt.com ignored=x"; none`,
	} {
		if got := EvaluateSecurity("hello@malt.com", header); got.Status != SecurityNeutral {
			t.Errorf("%q: %+v", header, got)
		}
	}
}

func TestAuthenticationNamelessResultPreservesValidChecks(t *testing.T) {
	for _, header := range []string{
		`strato.com; dmarc=pass header.from="\"github.com\""; =none smtp.remote-ip=192.30.252.130; dkim=pass header.d=github.com`,
		`strato.com; dmarc=pass header.from="\"github.com\""; =pass smtp.remote-ip=192.30.252.130; dkim=pass header.d=github.com`,
		`strato.com; =none smtp.remote-ip=192.30.252.130; dkim=pass header.d="\"github.com\""`,
		`strato.com; dkim=pass header.d="github.com"; =pass smtp.remote-ip=192.30.252.130`,
		`strato.com; dkim=pass header.d="github.com"; =none reason="ignored; dmarc=fail header.from=github.com"`,
	} {
		t.Run(header, func(t *testing.T) {
			got := EvaluateSecurity("GitHub <notifications@github.com>", header)
			if got.Status != SecurityVerifiedDomain || !got.DKIMPass || got.DMARCPass != strings.Contains(header, "dmarc=pass") {
				t.Fatalf("direct: %+v", got)
			}
			raw := "Authentication-Results: " + header + "\r\nReceived: from sender.invalid by receiver.test; now\r\nFrom: GitHub <notifications@github.com>\r\n\r\nhello"
			parsed, err := Parse([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			got = EvaluateSecurity(parsed.From, parsed.AuthenticationResults)
			if got.Status != SecurityVerifiedDomain || !got.DKIMPass || got.DMARCPass != strings.Contains(header, "dmarc=pass") {
				t.Fatalf("combined %q: %+v", parsed.AuthenticationResults, got)
			}
			// The same results below the reception boundary remain untrusted.
			raw = "Received: from sender.invalid by receiver.test; now\r\nAuthentication-Results: " + header + "\r\nFrom: GitHub <notifications@github.com>\r\n\r\nhello"
			parsed, err = Parse([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if got := EvaluateSecurity(parsed.From, parsed.AuthenticationResults); got.Status != SecurityNeutral {
				t.Fatalf("untrusted result accepted: %+v", got)
			}
		})
	}
}

func TestAuthenticationNamelessResultGrantsNoEvidence(t *testing.T) {
	for _, fields := range []string{
		`=pass header.from=github.com header.d=github.com`,
		`=pass dmarc=pass header.from=github.com`,
		`=none reason="ignored; dkim=pass header.d=github.com"`,
		`dmarc=none header.from=github.com; =pass header.d=github.com`,
		`=pass header.from=github.com; dkim=pass header.d=evil.invalid`,
	} {
		if got := EvaluateSecurity("GitHub <notifications@github.com>", "receiver.test; "+fields); got.Status != SecurityNeutral || got.DMARCPass || got.DKIMPass || got.BIMIPass {
			t.Errorf("%q granted evidence: %+v", fields, got)
		}
	}
	got := EvaluateSecurity("GitHub <notifications@github.com>", `receiver.test; dmarc=fail header.from="\"github.com\""; =pass smtp.remote-ip=192.30.252.130; dkim=pass header.d=github.com`)
	if got.WarningKind != WarnAuthFailed {
		t.Fatalf("lost native failure: %+v", got)
	}
}

func TestAuthenticationNamelessDMARCOnly(t *testing.T) {
	for _, header := range []string{
		`strato.com; dmarc=pass header.from="\"github.com\""; =none smtp.remote-ip=192.30.252.130`,
		`strato.com; =pass smtp.remote-ip=192.30.252.130; dmarc=pass header.from="\"github.com\""`,
	} {
		got := EvaluateSecurity("notifications@github.com", header)
		if got.Status != SecurityVerifiedDomain || !got.DMARCPass || got.DKIMPass || got.BIMIPass {
			t.Fatalf("%q: %+v", header, got)
		}
	}
}

func TestAuthenticationNamelessNewestCannotBorrowOlderPass(t *testing.T) {
	raw := "Authentication-Results: receiver.test; =pass header.d=github.com\r\nAuthentication-Results: receiver.test; dkim=pass header.d=github.com\r\nReceived: from sender.invalid by receiver.test; now\r\nFrom: notifications@github.com\r\n\r\nhello"
	parsed, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := EvaluateSecurity(parsed.From, parsed.AuthenticationResults); got.Status != SecurityNeutral {
		t.Fatalf("borrowed older pass: %+v", got)
	}
}
