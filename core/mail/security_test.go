package mail

import (
	"testing"
)

func TestEvaluateSecurity(t *testing.T) {
	tests := []struct {
		name        string
		from        string
		authResults string
		wantStatus  SecurityStatus
		wantWarn    WarningKind
		wantBrand   string
		untrusted   bool
	}{
		{
			name:        "Legit PayPal with DMARC pass",
			from:        "\"PayPal\" <service@paypal.com>",
			authResults: "mx.google.com; dkim=pass header.i=@paypal.com; dmarc=pass header.from=paypal.com; spf=pass",
			wantStatus:  SecurityVerified,
			wantWarn:    WarnNone,
		},
		{
			name:        "Legit PayPal subdomain with DMARC pass",
			from:        "\"PayPal Security\" <alert@mail.paypal.de>",
			authResults: "mx.google.com; dkim=pass header.i=@mail.paypal.de; dmarc=pass header.from=paypal.de",
			wantStatus:  SecurityVerified,
			wantWarn:    WarnNone,
		},
		{
			name:        "PayPal spoofing via Gmail",
			from:        "\"PayPal Support\" <phisher@gmail.com>",
			authResults: "mx.google.com; dkim=pass header.i=@gmail.com; spf=pass",
			wantStatus:  SecurityWarning,
			wantWarn:    WarnDisplaySpoof,
			wantBrand:   "PayPal",
		},
		{
			name:        "Apple spoofing via attacker domain",
			from:        "\"Apple iCloud Kundendienst\" <billing@evil-server.xyz>",
			authResults: "",
			wantStatus:  SecurityWarning,
			wantWarn:    WarnDisplaySpoof,
			wantBrand:   "Apple",
		},
		{
			name:        "Embedded email in display name",
			from:        "\"support@paypal.com\" <hacker123@freemail.ru>",
			authResults: "",
			wantStatus:  SecurityWarning,
			wantWarn:    WarnEmailInName,
			wantBrand:   "support@paypal.com",
		},
		{
			name:        "Sparkasse spoofing",
			from:        "\"Ihre Sparkasse\" <info@konto-verifizieren-sicherheit.com>",
			authResults: "",
			wantStatus:  SecurityWarning,
			wantWarn:    WarnDisplaySpoof,
			wantBrand:   "Sparkasse",
		},
		{
			name:        "Legitimate normal user without auth headers",
			from:        "\"Alice Schmidt\" <alice@example.com>",
			authResults: "",
			wantStatus:  SecurityNeutral,
			wantWarn:    WarnNone,
		},
		{
			name:        "Legitimate normal user with DKIM pass",
			from:        "\"Bob Meyer\" <bob@company.com>",
			authResults: "mx.google.com; dkim=pass header.i=@company.com",
			wantStatus:  SecurityVerified,
			wantWarn:    WarnNone,
		},
		{
			name:        "Auth failure (DMARC fail)",
			from:        "service@paypal.com",
			authResults: "mx.google.com; dmarc=fail (p=REJECT) header.from=paypal.com; spf=fail",
			wantStatus:  SecurityWarning,
			wantWarn:    WarnAuthFailed,
		},
		{
			name:        "Applejack should not trigger Apple brand match",
			from:        "\"Applejack Brown\" <applejack@farm.org>",
			authResults: "",
			wantStatus:  SecurityNeutral,
			wantWarn:    WarnNone,
		},
		{
			name:        "ADAC homoglyph spoofing with Cyrillic A and Greek lunate sigma C",
			from:        "АDAϹ <angebot@umfrageadacdeutschlandkontakt0545568802.de>",
			authResults: "strato.com; dmarc=none header.from=\"umfrageadacdeutschlandkontakt0545568802.de\"",
			wantStatus:  SecurityWarning,
			wantWarn:    WarnDisplaySpoof,
			wantBrand:   "ADAC",
		},
		{
			name:        "PayPal homoglyph spoofing with Cyrillic a",
			from:        "Pаypаl Support <phish@attackdomain.com>",
			authResults: "strato.com; dmarc=none header.from=\"attackdomain.com\"",
			wantStatus:  SecurityWarning,
			wantWarn:    WarnDisplaySpoof,
			wantBrand:   "PayPal",
		},
		{
			name:        "Attacker claims own authserv-id (RFC 8601 untrusted)",
			untrusted:   true,
			from:        "\"Alice Attacker\" <alice@evil.com>",
			authResults: "mx.evil.com; dmarc=pass header.from=evil.com; dkim=pass header.i=@evil.com",
			wantStatus:  SecurityNeutral,
			wantWarn:    WarnNone,
		},
		{
			name:        "Unaligned DMARC header.from cannot grant verified domain (RFC 8601 spoof)",
			from:        "\"Alice Attacker\" <alice@evil.com>",
			authResults: "mx.google.com; dmarc=pass header.from=paypal.com; dkim=pass header.i=@paypal.com",
			wantStatus:  SecurityNeutral,
			wantWarn:    WarnNone,
		},
		{
			name:        "BIMI pass without DMARC pass cannot grant certified brand",
			from:        "\"Bob\" <bob@example.com>",
			authResults: "mx.google.com; bimi=pass; dkim=pass header.i=@example.com",
			wantStatus:  SecurityVerifiedDomain,
			wantWarn:    WarnNone,
		},
		{
			name:        "bimi=pass in comment string is not parsed as bimi pass",
			from:        "\"Bob\" <bob@example.com>",
			authResults: "mx.google.com; dkim=none (comment bimi=pass); dmarc=none",
			wantStatus:  SecurityNeutral,
			wantWarn:    WarnNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			trace := []AuthenticationHeader{{Key: "Authentication-Results", Value: tc.authResults}, {Key: "Received", Value: "from sender.invalid by mx.google.com; Thu, 29 Oct 2026 00:00:00 +0000"}}
			if tc.untrusted {
				trace[0], trace[1] = trace[1], trace[0]
			}
			rep := EvaluateSecurity(tc.from, ReceiverAuthenticationResults(trace, "mx.google.com"))
			if rep.Status != tc.wantStatus {
				t.Errorf("Status = %v, want %v", rep.Status, tc.wantStatus)
			}
			if rep.WarningKind != tc.wantWarn {
				t.Errorf("WarningKind = %v, want %v", rep.WarningKind, tc.wantWarn)
			}
			if tc.wantBrand != "" && rep.BrandName != tc.wantBrand {
				t.Errorf("BrandName = %q, want %q", rep.BrandName, tc.wantBrand)
			}
		})
	}
}

func TestSecurityDBConversions(t *testing.T) {
	rep := SecurityReport{
		Status:      SecurityWarning,
		WarningKind: WarnDisplaySpoof,
	}
	status, reason := SecurityReportToDB(rep)
	if status != "warning" || reason != "spoofing" {
		t.Fatalf("SecurityReportToDB = (%q, %q), want (warning, spoofing)", status, reason)
	}

	restored := DBToSecurityReport("\"PayPal\" <phish@attack.com>", "", status, reason)
	if restored.Status != SecurityWarning || restored.WarningKind != WarnDisplaySpoof {
		t.Fatalf("DBToSecurityReport = %+v, want SecurityWarning WarnDisplaySpoof", restored)
	}

	// Test NeedsReview (Gelb)
	repReview := SecurityReport{Status: SecurityNeedsReview, WarningKind: WarnSuspicious}
	sReview, rReview := SecurityReportToDB(repReview)
	if sReview != "needs_review" || rReview != "ai_suspicious" {
		t.Fatalf("SecurityReportToDB review = (%q, %q), want (needs_review, ai_suspicious)", sReview, rReview)
	}
	restoredReview := DBToSecurityReport("User <user@example.com>", "mx.example.com; dmarc=pass header.from=example.com", sReview, rReview)
	if restoredReview.Status != SecurityNeedsReview || restoredReview.WarningKind != WarnSuspicious || !restoredReview.DMARCPass {
		t.Fatalf("restoredReview = %+v", restoredReview)
	}
}

func TestSecurityFiveStatesAndPriority(t *testing.T) {
	// 1. Rank priority: Rot > Gelb > Blau > Grün > Grau
	if !(SecurityRank(SecurityFraud) > SecurityRank(SecurityNeedsReview) &&
		SecurityRank(SecurityNeedsReview) > SecurityRank(SecurityCertifiedBrand) &&
		SecurityRank(SecurityCertifiedBrand) > SecurityRank(SecurityVerifiedDomain) &&
		SecurityRank(SecurityVerifiedDomain) > SecurityRank(SecurityNeutral)) {
		t.Fatalf("Invalid priority rank order")
	}

	// 2. Case: AFFiNE product name differs from company domain, but domain is authenticated -> Grün (Verified Domain)
	affineHeader := "mx.receiver.test; dmarc=pass header.from=\"toeverything.info\"; dkim=pass header.d=\"toeverything.info\""
	repAffine := EvaluateSecurity("AFFiNE Team <noreply@toeverything.info>", affineHeader)
	if repAffine.Status != SecurityVerifiedDomain {
		t.Fatalf("AFFiNE should be SecurityVerifiedDomain (Grün), got %v", repAffine.Status)
	}

	// 3. Case: Sparkasse brand imitation on arbitrary domain -> Rot (SecurityFraud) even with valid DKIM
	sparkasseHeader := "mx.receiver.test; dkim=pass header.d=\"attacker2939439239.de\"; dmarc=pass header.from=\"attacker2939439239.de\""
	repSparkasse := EvaluateSecurity("Sparkasse Team <buchhaltung@attacker2939439239.de>", sparkasseHeader)
	if repSparkasse.Status != SecurityFraud || repSparkasse.WarningKind != WarnDisplaySpoof {
		t.Fatalf("Sparkasse imitation must be SecurityFraud (Rot), got status=%v warn=%v", repSparkasse.Status, repSparkasse.WarningKind)
	}

	// 4. Case: Unauthenticated generic company name mismatch -> Gelb (SecurityNeedsReview)
	repUnauth := EvaluateSecurity("Acme Support Team <nobody@unverified-unknown.net>", "")
	if repUnauth.Status != SecurityNeedsReview || repUnauth.WarningKind != WarnSuspicious {
		t.Fatalf("Unauthenticated mismatch should be SecurityNeedsReview (Gelb), got status=%v warn=%v", repUnauth.Status, repUnauth.WarningKind)
	}

	// 5. Case: Ordinary private mail without authentication -> Grau (SecurityNeutral)
	repPrivate := EvaluateSecurity("Maximilian Mustermann <max.mustermann@web.de>", "")
	if repPrivate.Status != SecurityNeutral || repPrivate.WarningKind != WarnNone {
		t.Fatalf("Private mail should be SecurityNeutral (Grau), got status=%v warn=%v", repPrivate.Status, repPrivate.WarningKind)
	}

	// 6. Case: Zero-width evasion with disposable DGA domain -> Rot (SecurityFraud)
	repEasy := EvaluateSecurity("\"Eas\u200dB\u200can\u200bk\u200b\u200d(DE\u2060)\" <umfrage@medicarekitdoctodeutschlandangebot132975423.de>", "")
	if repEasy.Status != SecurityFraud || repEasy.WarningKind != WarnDisplaySpoof {
		t.Fatalf("Zero-width evasion on disposable domain must be SecurityFraud (Rot), got status=%v warn=%v", repEasy.Status, repEasy.WarningKind)
	}

	repDoctolib := EvaluateSecurity("\"D\u200coct\u043e\u200ci\u200bb\" <medicare@doctolibkitdemedicare-umfrage123555720535.de>", "")
	if repDoctolib.Status != SecurityFraud || repDoctolib.WarningKind != WarnDisplaySpoof {
		t.Fatalf("Zero-width homoglyph evasion must be SecurityFraud (Rot), got status=%v warn=%v", repDoctolib.Status, repDoctolib.WarningKind)
	}
}
