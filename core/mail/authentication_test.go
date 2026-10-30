package mail

import (
	"strings"
	"testing"
)

func TestReceiverAuthenticationResults(t *testing.T) {
	const fail = "Authentication-Results: mx.receiver.test; dmarc=fail header.from=company.example\r\n"
	const pass = "Authentication-Results: mx.receiver.test; dkim=pass header.d=company.example\r\n"
	const received = "Received: from sender.invalid by mx.receiver.test; Thu, 29 Oct 2026 00:00:00 +0000\r\n"
	const original = "From: Alice <alice@company.example>\r\nSubject: probe\r\n\r\nhello"
	for _, tc := range []struct {
		name, headers string
		hosts         []string
		want          SecurityStatus
	}{
		{"receiver prepends result", pass + received, nil, SecurityVerifiedDomain},
		{"unknown provider needs no table", "Authentication-Results: independently-operated-service; dkim=pass header.d=company.example\r\nReceived: from sender.invalid by mx.new-provider.example; Thu, 29 Oct 2026 00:00:00 +0000\r\n", nil, SecurityVerifiedDomain},
		{"receiver result below its own Received", received + pass, nil, SecurityVerifiedDomain},
		{"receiver result shields a lower injected pass", "Authentication-Results: mx.receiver.test; none\r\n" + received + "Authentication-Results: attacker.invalid; dmarc=pass header.from=company.example\r\n", nil, SecurityNeutral},
		{"same authserv-id below the next hop is not merged", "Authentication-Results: mx.receiver.test; dkim=none\r\n" + received + pass, nil, SecurityNeutral},
		// Known limit: a receiver that adds no result of its own leaves the sender's on top.
		{"receiver without results of its own", received + "Authentication-Results: attacker.invalid; dmarc=pass header.from=company.example\r\n", nil, SecurityVerifiedDomain},
		{"split provider results", "Authentication-Results: independent.test; dmarc=none header.from=company.example\r\nAuthentication-Results: independent.test; arc=none\r\nAuthentication-Results: independent.test; dkim=pass header.d=company.example\r\n" + received, nil, SecurityVerifiedDomain},
		{"newest fail beats forwarded pass", fail + received + "Authentication-Results: previous.test; dmarc=pass header.from=company.example\r\n", nil, SecurityWarning},
		{"none cannot inherit an older pass", "Authentication-Results: mx.receiver.test; none\r\n" + pass + received, nil, SecurityNeutral},
		{"malformed newest result cannot expose older pass", "Authentication-Results: bad; dkim=\r\n" + pass + received, nil, SecurityNeutral},
		{"internal receiving relay", "Received: from relay.receiver.test by mailbox.receiver.test; Thu, 29 Oct 2026 00:00:00 +0000\r\n" + received + pass, nil, SecurityVerifiedDomain},
		{"gmail places its result below its own Received", "Received: by 2002:a05:1234::1 with SMTP id abc; Thu, 29 Oct 2026 00:00:00 +0000\r\nReceived: from sender.invalid by mx.google.com; Thu, 29 Oct 2026 00:00:00 +0000\r\nAuthentication-Results: mx.google.com; dkim=pass header.d=company.example\r\n", nil, SecurityVerifiedDomain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Parse([]byte(tc.headers + original))
			if err != nil {
				t.Fatal(err)
			}
			selected := ReceiverAuthenticationResults(p.AuthenticationTrace, tc.hosts...)
			got := EvaluateSecurity("Alice <alice@company.example>", selected)
			if got.Status != tc.want {
				t.Fatalf("selected %q: got %+v; want %v", selected, got, tc.want)
			}
		})
	}
	p, err := Parse([]byte(received + original + "\r\n" + pass))
	if err != nil {
		t.Fatal(err)
	}
	if p.AuthenticationResults != "" {
		t.Fatal("body text was treated as an authentication header")
	}
}

func TestAuthenticationTraceDoesNotTrustOriginalHeaderClaims(t *testing.T) {
	raw := "Received: from sender.invalid by mx.receiver.test; Thu, 29 Oct 2026 00:00:00 +0000\r\nFrom: Alice <alice@company.example>\r\nAuthentication-Results: mx.receiver.test; dkim=pass header.d=company.example\r\n\r\nhello"
	p, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := ReceiverAuthenticationResults(p.AuthenticationTrace, "mx.receiver.test"); got != "" {
		t.Fatalf("original sender header accepted: %q", got)
	}
	oversized := strings.Repeat("Authentication-Results: mx.receiver.test; dkim=pass header.d=company.example\r\n", 129) + "Received: by mx.receiver.test; Thu, 29 Oct 2026 00:00:00 +0000\r\n\r\n"
	p, err = Parse([]byte(oversized))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.AuthenticationTrace) != 0 {
		t.Fatal("oversized authentication trace was accepted")
	}
}
