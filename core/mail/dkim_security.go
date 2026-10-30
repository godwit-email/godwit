package mail

import (
	"bytes"
	"context"
	"errors"
	"net"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-msgauth/dmarc"
	"golang.org/x/net/publicsuffix"
)

// NeedsDKIMVerification schedules an automatic local check only when there is
// a signature and no trusted positive/native warning or freemail exclusion.
func NeedsDKIMVerification(from, authResults string, signed bool) bool {
	if !signed {
		return false
	}
	rep := EvaluateSecurity(from, authResults)
	return rep.Status == SecurityNeutral && !rep.SuspiciousMismatch && rep.ActualDomain != "" && !IsFreemail(rep.ActualDomain)
}

// VerifyMessageDKIM verifies complete original message bytes, never the cached
// snippet/sanitized HTML. Only public DKIM/DMARC DNS records leave the device.
// A failed or unrelated signature grants no badge; temporary DNS errors retry.
func VerifyMessageDKIM(ctx context.Context, from string, raw []byte, lookup func(context.Context, string) ([]string, error)) (SecurityReport, error) {
	rep := EvaluateSecurity(from, "")
	if rep.Status == SecurityWarning || rep.SuspiciousMismatch || IsFreemail(rep.ActualDomain) {
		return rep, nil
	}
	if err := ctx.Err(); err != nil {
		return rep, err
	}
	if len(raw) == 0 || len(raw) > maxAttachmentBytes {
		return rep, nil
	}
	m, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil || len(m.Header["From"]) != 1 {
		return rep, nil
	}
	actual, err := netmail.ParseAddressList(m.Header.Get("From"))
	if err != nil || len(actual) != 1 {
		return rep, nil
	}
	expected, err := netmail.ParseAddress(from)
	if err != nil || !strings.EqualFold(actual[0].Address, expected.Address) {
		return rep, nil
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	var lookupErr error
	// Verification may query multiple signatures concurrently. Capture errors by
	// inspecting verification results instead of mutating shared callback state.
	query := func(name string) ([]string, error) { return lookup(c, name) }
	vv, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{LookupTXT: query, MaxVerifications: 5})
	if err != nil && !errors.Is(err, dkim.ErrTooManySignatures) {
		return rep, nil
	}
	var domains []string
	for _, v := range vv {
		if v.Err != nil {
			if dkim.IsTempFail(v.Err) {
				lookupErr = errors.Join(lookupErr, v.Err)
			}
			continue
		}
		domain := strings.ToLower(v.Domain)
		if sameOrganizationalDomain(domain, rep.ActualDomain) {
			domains = append(domains, domain)
		}
	}
	if err := c.Err(); err != nil {
		return rep, err
	}
	if len(domains) == 0 {
		return rep, lookupErr
	}
	// The green badge's direct DKIM path keeps the original From-domain scope.
	for _, domain := range domains {
		if domainMatches(domain, []string{rep.ActualDomain}) || domainMatches(rep.ActualDomain, []string{domain}) {
			rep.DKIMPass = true
		}
	}
	policy, err := dmarc.LookupWithOptions(rep.ActualDomain, &dmarc.LookupOptions{LookupTXT: query})
	if errors.Is(err, dmarc.ErrNoPolicy) {
		if root, e := publicsuffix.EffectiveTLDPlusOne(rep.ActualDomain); e == nil && root != rep.ActualDomain {
			policy, err = dmarc.LookupWithOptions(root, &dmarc.LookupOptions{LookupTXT: query})
		}
	}
	if err != nil && !errors.Is(err, dmarc.ErrNoPolicy) {
		return rep, err
	}
	if policy != nil {
		for _, domain := range domains {
			if domain == rep.ActualDomain || policy.DKIMAlignment == dmarc.AlignmentRelaxed && sameOrganizationalDomain(domain, rep.ActualDomain) {
				rep.DMARCPass = true
				rep.DKIMPass = true
				break
			}
		}
	}
	if rep.DKIMPass {
		rep.Status = SecurityVerifiedDomain
	}
	return rep, nil
}

func sameOrganizationalDomain(a, b string) bool {
	a = strings.ToLower(strings.TrimSuffix(a, "."))
	b = strings.ToLower(strings.TrimSuffix(b, "."))
	if a == b && a != "" {
		return true
	}
	da, ea := publicsuffix.EffectiveTLDPlusOne(a)
	db, eb := publicsuffix.EffectiveTLDPlusOne(b)
	return ea == nil && eb == nil && da == db
}
