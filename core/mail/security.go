package mail

import (
	"bytes"
	"cmp"
	"context"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/emersion/go-msgauth/authres"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/text/unicode/norm"
)

// SecurityStatus indicates the authentication and spoofing assessment of a sender.
type SecurityStatus int

const (
	SecurityNeutral        SecurityStatus = iota // ⚪ Grau: Nicht bestätigt
	SecurityVerifiedDomain                       // 🟢 Grün: Domain bestätigt
	SecurityCertifiedBrand                       // 🔵 Blau: Zertifizierte Marke
	SecurityNeedsReview                          // 🟡 Gelb: Prüfung empfohlen
	SecurityFraud                                // 🔴 Rot: Starker Betrugsverdacht
)

// SecurityWarning is kept as an alias for backwards compatibility.
const SecurityWarning = SecurityFraud

// SecurityRank returns display priority: Rot (5) > Gelb (4) > Blau (3) > Grün (2) > Grau (1).
func SecurityRank(s SecurityStatus) int {
	switch s {
	case SecurityFraud:
		return 5
	case SecurityNeedsReview:
		return 4
	case SecurityCertifiedBrand:
		return 3
	case SecurityVerifiedDomain:
		return 2
	case SecurityNeutral:
		return 1
	default:
		return 0
	}
}

// SecurityVerified is kept as an alias for SecurityVerifiedDomain.
const SecurityVerified = SecurityVerifiedDomain

// WarningKind specifies why a warning was triggered.
type WarningKind int

const (
	WarnNone WarningKind = iota
	WarnDisplaySpoof
	WarnEmailInName
	WarnAuthFailed
	WarnAIFraud
	WarnSuspicious
)

// SecurityReport holds the evaluation results for an email sender.
type SecurityReport struct {
	Status             SecurityStatus
	WarningKind        WarningKind
	BrandName          string
	ActualDomain       string
	DKIMPass           bool
	SPFPass            bool
	DMARCPass          bool
	BIMIPass           bool
	SuspiciousMismatch bool
}

type brandEntry struct {
	name    string
	pattern *regexp.Regexp
	domains []string
}

var knownBrands = []brandEntry{
	{name: "PayPal", pattern: regexp.MustCompile(`(?i)\bpaypal\b`), domains: []string{"paypal.com", "paypal.de", "paypal.fr", "paypal.co.uk", "paypal.it", "paypal.es", "paypal.at", "paypal.ch", "paypal.nl", "paypal.be"}},
	{name: "Apple", pattern: regexp.MustCompile(`(?i)\b(apple|icloud)\b`), domains: []string{"apple.com", "icloud.com", "me.com", "mac.com"}},
	{name: "Amazon", pattern: regexp.MustCompile(`(?i)\bamazon\b`), domains: []string{"amazon.com", "amazon.de", "amazon.co.uk", "amazon.fr", "amazon.it", "amazon.es", "amazon.co.jp", "amazon.ca", "amazon.at", "amazonaws.com", "aws.com"}},
	{name: "Google", pattern: regexp.MustCompile(`(?i)\b(google|gmail)\b`), domains: []string{"google.com", "googlemail.com", "gmail.com"}},
	{name: "Microsoft", pattern: regexp.MustCompile(`(?i)\b(microsoft|office\s*365|outlook)\b`), domains: []string{"microsoft.com", "office.com", "office365.com", "outlook.com", "live.com", "hotmail.com"}},
	{name: "Netflix", pattern: regexp.MustCompile(`(?i)\bnetflix\b`), domains: []string{"netflix.com"}},
	{name: "eBay", pattern: regexp.MustCompile(`(?i)\bebay\b`), domains: []string{"ebay.com", "ebay.de", "ebay.co.uk", "ebay.fr", "ebay.it", "ebay.es", "ebay.at", "ebay.ch"}},
	{name: "Sparkasse", pattern: regexp.MustCompile(`(?i)\bsparkasse\b`), domains: []string{"sparkasse.de"}},
	{name: "Volksbank", pattern: regexp.MustCompile(`(?i)\b(volksbank|raiffeisenbank)\b`), domains: []string{"volksbank.de", "vr.de", "fiducia.de"}},
	{name: "Deutsche Bank", pattern: regexp.MustCompile(`(?i)\bdeutsche\s+bank\b`), domains: []string{"deutsche-bank.de", "db.com"}},
	{name: "Postbank", pattern: regexp.MustCompile(`(?i)\bpostbank\b`), domains: []string{"postbank.de"}},
	{name: "Commerzbank", pattern: regexp.MustCompile(`(?i)\bcommerzbank\b`), domains: []string{"commerzbank.de", "commerzbank.com"}},
	{name: "ING", pattern: regexp.MustCompile(`(?i)\b(ing-diba|ing\s+di\s*ba)\b`), domains: []string{"ing.de", "ing.com", "ing-diba.de"}},
	{name: "DKB", pattern: regexp.MustCompile(`(?i)\bdkb\b`), domains: []string{"dkb.de"}},
	{name: "DHL", pattern: regexp.MustCompile(`(?i)\bdhl\b`), domains: []string{"dhl.com", "dhl.de"}},
	{name: "FedEx", pattern: regexp.MustCompile(`(?i)\bfedex\b`), domains: []string{"fedex.com"}},
	{name: "UPS", pattern: regexp.MustCompile(`(?i)\bups\b`), domains: []string{"ups.com"}},
	{name: "DPD", pattern: regexp.MustCompile(`(?i)\bdpd\b`), domains: []string{"dpd.com", "dpd.de"}},
	{name: "GLS", pattern: regexp.MustCompile(`(?i)\bgls\b`), domains: []string{"gls-group.com", "gls-pakete.de"}},
	{name: "Hermes", pattern: regexp.MustCompile(`(?i)\bhermes\b`), domains: []string{"myhermes.de", "hermesworld.com"}},
	{name: "Meta", pattern: regexp.MustCompile(`(?i)\b(meta|facebook|instagram|whatsapp)\b`), domains: []string{"meta.com", "facebook.com", "facebookmail.com", "instagram.com", "mail.instagram.com", "whatsapp.com", "metamail.com"}},
	{name: "LinkedIn", pattern: regexp.MustCompile(`(?i)\blinkedin\b`), domains: []string{"linkedin.com"}},
	{name: "Dropbox", pattern: regexp.MustCompile(`(?i)\bdropbox\b`), domains: []string{"dropbox.com", "dropboxmail.com"}},
	{name: "DocuSign", pattern: regexp.MustCompile(`(?i)\bdocusign\b`), domains: []string{"docusign.com", "docusign.net"}},
	{name: "Adobe", pattern: regexp.MustCompile(`(?i)\badobe\b`), domains: []string{"adobe.com"}},
	{name: "Lexware", pattern: regexp.MustCompile(`(?i)\b(lexware|haufe)\b`), domains: []string{"lexware.de", "lexware.com", "haufe.de", "haufe-lexware.com"}},
	{name: "DATEV", pattern: regexp.MustCompile(`(?i)\bdatev\b`), domains: []string{"datev.de", "datev.com"}},
	{name: "sevdesk", pattern: regexp.MustCompile(`(?i)\bsevdesk\b`), domains: []string{"sevdesk.de", "sevdesk.com"}},
	{name: "FastBill", pattern: regexp.MustCompile(`(?i)\bfastbill\b`), domains: []string{"fastbill.com", "fastbill.de"}},
	{name: "STRATO", pattern: regexp.MustCompile(`(?i)\bstrato\b`), domains: []string{"strato.de", "strato.com", "stratoserver.net"}},
	{name: "ADAC", pattern: regexp.MustCompile(`(?i)\badac\b`), domains: []string{"adac.de"}},
	{name: "Telekom", pattern: regexp.MustCompile(`(?i)\b(telekom|t-mobile)\b`), domains: []string{"telekom.de", "telekom.com", "t-mobile.de"}},
	{name: "Vodafone", pattern: regexp.MustCompile(`(?i)\bvodafone\b`), domains: []string{"vodafone.de", "vodafone.com"}},
	{name: "IONOS", pattern: regexp.MustCompile(`(?i)\b(1(&|und)1|ionos)\b`), domains: []string{"1und1.de", "ionos.de", "ionos.com"}},
	{name: "Lidl", pattern: regexp.MustCompile(`(?i)\blidl\b`), domains: []string{"lidl.de", "lidl.com", "lidl-reisen.de", "lidl-plus.de"}},
	{name: "Klarna", pattern: regexp.MustCompile(`(?i)\bklarna\b`), domains: []string{"klarna.com", "klarna.de"}},
	{name: "Chase", pattern: regexp.MustCompile(`(?i)\bchase\b`), domains: []string{"chase.com"}},
	{name: "Bank of America", pattern: regexp.MustCompile(`(?i)\bbank\s+of\s+america\b`), domains: []string{"bankofamerica.com"}},
	{name: "Wells Fargo", pattern: regexp.MustCompile(`(?i)\bwells\s+fargo\b`), domains: []string{"wellsfargo.com"}},
	{name: "Steam", pattern: regexp.MustCompile(`(?i)\b(steam|valve)\b`), domains: []string{"steampowered.com", "valvesoftware.com"}},
}

var freemailDomains = map[string]bool{
	"yahoo.com": true, "yahoo.de": true, "yahoo.fr": true, "yahoo.co.uk": true, "yahoo.es": true, "yahoo.it": true,
	"ymail.com": true, "rocketmail.com": true,
	"gmail.com": true, "googlemail.com": true,
	"outlook.com": true, "hotmail.com": true, "live.com": true, "msn.com": true, "passport.com": true,
	"gmx.de": true, "gmx.net": true, "gmx.at": true, "gmx.ch": true, "gmx.com": true,
	"web.de":      true,
	"freenet.de":  true,
	"t-online.de": true,
	"icloud.com":  true, "me.com": true, "mac.com": true,
	"proton.me": true, "protonmail.com": true,
	"aol.com": true, "aim.com": true,
	"zoho.com": true,
	"mail.ru":  true, "yandex.ru": true, "yandex.com": true,
}

// IsFreemail returns true if the domain is a known public webmail provider.
func IsFreemail(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if freemailDomains[domain] {
		return true
	}
	for fd := range freemailDomains {
		if strings.HasSuffix(domain, "."+fd) {
			return true
		}
	}
	return false
}

type bimiCacheEntry struct{ expires time.Time }

var bimiCache sync.Map

func cachedBIMI(domain string) bool {
	if v, ok := bimiCache.Load(domain); ok {
		if entry, ok := v.(bimiCacheEntry); ok && time.Now().Before(entry.expires) {
			return true
		}
		bimiCache.Delete(domain)
	}
	return false
}

// LookupBIMI checks whether the domain has a published BIMI record with a valid, verified
// mark certificate (VMC) signed by an approved authority (DigiCert or Entrust).
// According to the BIMI specification, BIMI can only be valid if DMARC passes.
func LookupBIMI(ctx context.Context, domain string, dmarcPass bool) bool {
	if !dmarcPass {
		return false
	}
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || IsFreemail(domain) {
		return false
	}
	if cachedBIMI(domain) {
		return true
	}
	has := checkBIMIHost(ctx, domain)
	if !has {
		if root, err := publicsuffix.EffectiveTLDPlusOne(domain); err == nil && root != domain {
			has = LookupBIMI(ctx, root, true)
			if has {
				if entry, ok := bimiCache.Load(root); ok {
					bimiCache.Store(domain, entry)
				}
			}
		}
	}
	return has
}

func parseBIMIAuthorityURL(txt string) string {
	parts := strings.Split(txt, ";")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) != "v=BIMI1" {
		return ""
	}
	authority := ""
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "a=") {
			u := strings.TrimSpace(strings.TrimPrefix(p, "a="))
			if authority != "" || !strings.HasPrefix(u, "https://") {
				return ""
			}
			authority = u
		}
	}
	return authority
}

func verifyVMCURL(ctx context.Context, domain, certURL string) bool {
	u, err := url.Parse(certURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, certURL, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2500 * time.Millisecond, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.User != nil || len(via) >= 5 {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(body) == 0 || len(body) > 64*1024 {
		return false
	}

	expires, ok := verifyVMC(body, domain, BIMICertPool(), time.Now())
	if ok {
		bimiCache.Store(domain, bimiCacheEntry{expires: expires})
	}
	return ok
}

var brandIndicatorOID = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 31}

// verifyVMC checks the complete issuance chain and the BIMI-specific purpose.
// Go does not recognize this EKU, so enforce it explicitly on the leaf and
// issuing CAs while using ExtKeyUsageAny for the remaining PKIX checks.
func verifyVMC(body []byte, domain string, roots *x509.CertPool, now time.Time) (time.Time, bool) {
	var certs []*x509.Certificate
	for len(bytes.TrimSpace(body)) > 0 {
		block, rest := pem.Decode(body)
		if block == nil || block.Type != "CERTIFICATE" || len(certs) >= 10 {
			return time.Time{}, false
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return time.Time{}, false
		}
		certs = append(certs, cert)
		body = rest
	}
	if len(certs) == 0 || certs[0].IsCA || !hasBrandEKU(certs[0]) {
		return time.Time{}, false
	}
	intermediates := x509.NewCertPool()
	for _, cert := range certs[1:] {
		intermediates.AddCert(cert)
	}
	chains, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: domain, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	if err != nil {
		return time.Time{}, false
	}
	for _, chain := range chains {
		valid := true
		expires := now.Add(time.Hour)
		for i, cert := range chain {
			if cert.NotAfter.Before(expires) {
				expires = cert.NotAfter
			}
			// Non-root issuers must be dedicated to BIMI, as must a root signing a leaf directly.
			if (i < len(chain)-1 || i == 1 && len(chain) == 2) && !hasBrandEKU(cert) {
				valid = false
			}
		}
		if valid {
			return expires, true
		}
	}
	return time.Time{}, false
}

func hasBrandEKU(cert *x509.Certificate) bool {
	return slices.ContainsFunc(cert.UnknownExtKeyUsage, func(oid asn1.ObjectIdentifier) bool { return oid.Equal(brandIndicatorOID) })
}

func checkBIMIHost(ctx context.Context, domain string) bool {
	c, cancel := context.WithTimeout(ctx, 3000*time.Millisecond)
	defer cancel()
	txts, err := net.DefaultResolver.LookupTXT(c, "default._bimi."+domain)
	if err != nil {
		return false
	}
	for _, t := range txts {
		t = strings.TrimSpace(t)
		if strings.HasPrefix(t, "v=BIMI1") {
			certURL := parseBIMIAuthorityURL(t)
			if certURL != "" && verifyVMCURL(c, domain, certURL) {
				return true
			}
		}
	}
	return false
}

var orgKeywords = regexp.MustCompile(`(?i)\b(gmbh|ag|inc|corp|ltd|llc|ug|service|support|team|billing|rechnung|versand|kundenservice|kundendienst|sicherheit|security|kanzlei|clinic|hospital|doktor|doctor|umfrage|survey)\b|(?i)bank\b`)

// HasZeroWidthEvasion checks if a string contains invisible zero-width or directional control characters
// commonly used by attackers to evade spam filters and tokenizers.
func HasZeroWidthEvasion(s string) bool {
	for _, r := range s {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff', '\u00ad':
			return true
		case '\u202a', '\u202b', '\u202c', '\u202d', '\u202e', '\u2066', '\u2067', '\u2068', '\u2069':
			return true
		}
	}
	return false
}

var suspiciousDigitsRegex = regexp.MustCompile(`\d{6,}`)

// HasSuspiciousDomainPattern checks for machine-generated / disposable spam domain patterns,
// such as long digit sequences (>= 6 digits) often used in disposable campaign domains.
func HasSuspiciousDomainPattern(domain string) bool {
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return false
	}
	sld := parts[len(parts)-2]
	return suspiciousDigitsRegex.MatchString(sld)
}

// HasDisplayDomainMismatch checks if the display name represents an organization or brand
// that clearly contradicts the sender's actual domain.
func HasDisplayDomainMismatch(displayName, domain string) bool {
	displayName = strings.TrimSpace(displayName)
	domain = strings.ToLower(strings.TrimSpace(domain))
	if displayName == "" || domain == "" {
		return false
	}

	normName := NormalizeConfusables(displayName)

	parts := strings.Split(domain, ".")
	var domainCore string
	if len(parts) >= 2 {
		domainCore = parts[len(parts)-2]
	} else {
		domainCore = domain
	}

	// If display name contains company/service/financial keywords
	if orgKeywords.MatchString(displayName) || orgKeywords.MatchString(normName) {
		words := regexp.MustCompile(`[\p{L}\p{N}]+`).FindAllString(strings.ToLower(normName), -1)
		for _, w := range words {
			if orgKeywords.MatchString(w) || len(w) < 3 {
				continue
			}
			if strings.Contains(domain, w) || strings.Contains(w, domainCore) {
				return false // Matches the domain
			}
		}
		// Found org keywords, but none of the core brand words matched the domain
		return true
	}

	return false
}

var emailRegex = regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)

// IsKnownBrandSpoof checks if a display name imitates a known brand on an unauthorized domain.
func IsKnownBrandSpoof(displayName, domain string) (bool, string) {
	normName := NormalizeConfusables(displayName)
	for _, b := range knownBrands {
		if b.pattern.MatchString(displayName) || b.pattern.MatchString(normName) {
			if !domainMatches(domain, b.domains) {
				return true, b.name
			}
		}
	}
	return false, ""
}

func domainMatches(domain string, allowed []string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	for _, a := range allowed {
		a = strings.ToLower(a)
		if domain == a || strings.HasSuffix(domain, "."+a) {
			return true
		}
	}
	return false
}

var homoglyphMap = map[rune]rune{
	// Cyrillic uppercase
	'\u0410': 'A', // А
	'\u0412': 'B', // В
	'\u0421': 'C', // С
	'\u0415': 'E', // Е
	'\u041D': 'H', // Н
	'\u0406': 'I', // І
	'\u0408': 'J', // Ј
	'\u041A': 'K', // К
	'\u041C': 'M', // М
	'\u041E': 'O', // О
	'\u0420': 'P', // Р
	'\u0422': 'T', // Т
	'\u0425': 'X', // Х
	'\u04AE': 'Y', // Ү
	// Cyrillic lowercase
	'\u0430': 'a', // а
	'\u0441': 'c', // с
	'\u0435': 'e', // е
	'\u0456': 'i', // і
	'\u0458': 'j', // ј
	'\u043A': 'k', // к
	'\u043E': 'o', // о
	'\u0440': 'p', // р
	'\u0445': 'x', // х
	'\u0443': 'y', // у
	// Greek uppercase
	'\u0391': 'A', // Α
	'\u0392': 'B', // Β
	'\u0395': 'E', // Ε
	'\u0396': 'Z', // Ζ
	'\u0397': 'H', // Η
	'\u0399': 'I', // Ι
	'\u039A': 'K', // Κ
	'\u039C': 'M', // Μ
	'\u039D': 'N', // Ν
	'\u039F': 'O', // Ο
	'\u03A1': 'P', // Ρ
	'\u03A4': 'T', // Τ
	'\u03A5': 'Y', // Υ
	'\u03A7': 'X', // Χ
	'\u03F9': 'C', // Ϲ (Greek lunate sigma symbol)
	// Greek lowercase
	'\u03B1': 'a', // α
	'\u03B5': 'e', // ε
	'\u03B9': 'i', // ι
	'\u03BA': 'k', // κ
	'\u03BF': 'o', // ο
	'\u03C1': 'p', // ρ
	'\u03C4': 't', // τ
	'\u03C5': 'u', // υ
	'\u03BD': 'v', // ν
	'\u03C7': 'x', // χ
	'\u03F2': 'c', // ϲ (Greek lunate sigma)
}

// NormalizeConfusables normalizes display names by converting common homoglyphs / confusables
// (such as Cyrillic or Greek lookalikes and fullwidth characters) into ASCII Latin equivalents,
// and removes invisible zero-width and directional control characters.
func NormalizeConfusables(s string) string {
	if s == "" {
		return ""
	}
	// First pass: filter out invisible zero-width and format control runes immediately,
	// and replace known confusables/homoglyphs before NFKD decomposition.
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) {
			continue // skip zero-width and format control runes
		}
		if mapped, ok := homoglyphMap[r]; ok {
			sb.WriteRune(mapped)
		} else {
			sb.WriteRune(r)
		}
	}

	// Second pass: NFKD compatibility decomposition (handles fullwidth, bold/math runes, accents)
	decomposed := norm.NFKD.String(sb.String())
	sb.Reset()
	sb.Grow(len(decomposed))
	for _, r := range decomposed {
		if mapped, ok := homoglyphMap[r]; ok {
			sb.WriteRune(mapped)
		} else if unicode.Is(unicode.Cf, r) {
			continue // skip zero-width and format control runes
		} else if !unicode.Is(unicode.Mn, r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// EvaluateSecurity assesses whether the sender shows signs of spoofing or has verified authentication.
// authResults must be the receiving service's result selected by ReceiverAuthenticationResults,
// rather than an arbitrary Authentication-Results field from the message.
func EvaluateSecurity(from string, authResults string) SecurityReport {
	a, err := mail.ParseAddress(from)
	if err != nil {
		return SecurityReport{Status: SecurityNeutral}
	}
	displayName := strings.TrimSpace(a.Name)
	addr := strings.ToLower(strings.TrimSpace(a.Address))
	var domain string
	if at := strings.LastIndex(addr, "@"); at >= 0 && at < len(addr)-1 {
		domain = addr[at+1:]
	}
	domain, validDomain := authenticationDomain(domain, false)
	if !validDomain || domain == "" {
		return SecurityReport{Status: SecurityNeutral}
	}

	report := SecurityReport{
		Status:       SecurityNeutral,
		ActualDomain: domain,
	}

	// Structural indicators: intentional zero-width evasion and disposable DGA domains
	hasZeroWidth := HasZeroWidthEvasion(displayName)
	hasSuspiciousDomain := HasSuspiciousDomainPattern(domain)

	// 1. Check for an email address embedded inside the display name
	if displayName != "" {
		normName := NormalizeConfusables(displayName)
		if hasZeroWidth && (HasDisplayDomainMismatch(displayName, domain) || hasSuspiciousDomain) {
			report.Status = SecurityWarning
			report.WarningKind = WarnDisplaySpoof
			report.BrandName = normName
			return report
		}

		if embeddedEmail := emailRegex.FindString(normName); embeddedEmail != "" {
			if at := strings.LastIndex(embeddedEmail, "@"); at >= 0 && at < len(embeddedEmail)-1 {
				nameDomain := strings.ToLower(embeddedEmail[at+1:])
				if nameDomain != domain && !strings.HasSuffix(domain, "."+nameDomain) {
					report.Status = SecurityWarning
					report.WarningKind = WarnEmailInName
					report.BrandName = embeddedEmail
					return report
				}
			}
		}

		// 2. Check for Display Name Spoofing with well-known brands. The brand
		// list is never complete, so a name alone only asks for review below.
		for _, b := range knownBrands {
			if (b.pattern.MatchString(displayName) || b.pattern.MatchString(normName)) && !domainMatches(domain, b.domains) {
				report.BrandName = b.name
				break
			}
		}

		if HasDisplayDomainMismatch(displayName, domain) || HasDisplayDomainMismatch(normName, domain) {
			report.SuspiciousMismatch = true
		}
	}

	// 3. Parse Authentication-Results header if present (RFC 8601 aligned)
	var dkimPass, spfPass, dmarcPass, bimiPass bool
	var dkimFail, spfFail, dmarcFail bool

	if authResults != "" {
		authservID, results, err := parseSecurityAuthenticationResults(authResults)
		authservID = strings.TrimSpace(strings.ToLower(authservID))
		if err == nil && authservID != "" {
			for _, res := range results {
				switch r := res.(type) {
				case *authres.DMARCResult:
					dmarcDomain := strings.ToLower(strings.TrimSpace(r.From))
					aligned := dmarcDomain != "" && (dmarcDomain == domain || strings.HasSuffix(domain, "."+dmarcDomain) || strings.HasSuffix(dmarcDomain, "."+domain))
					if aligned && r.Value == authres.ResultPass {
						dmarcPass = true
					} else if (aligned || dmarcDomain == "") && r.Value == authres.ResultFail {
						dmarcFail = true
					}
				case *authres.DKIMResult:
					dkimDomain := strings.ToLower(cmp.Or(r.Domain, strings.TrimPrefix(r.Identifier, "@")))
					if at := strings.LastIndex(dkimDomain, "@"); at >= 0 {
						dkimDomain = dkimDomain[at+1:]
					}
					aligned := dkimDomain != "" && (dkimDomain == domain || strings.HasSuffix(domain, "."+dkimDomain) || strings.HasSuffix(dkimDomain, "."+domain))
					if aligned && r.Value == authres.ResultPass {
						dkimPass = true
					} else if (aligned || dkimDomain == "") && r.Value == authres.ResultFail {
						dkimFail = true
					}
				case *authres.SPFResult:
					spfDomain := strings.ToLower(cmp.Or(r.From, r.Helo))
					if at := strings.LastIndex(spfDomain, "@"); at >= 0 {
						spfDomain = spfDomain[at+1:]
					}
					aligned := spfDomain != "" && (spfDomain == domain || strings.HasSuffix(domain, "."+spfDomain) || strings.HasSuffix(spfDomain, "."+domain))
					if aligned && r.Value == authres.ResultPass {
						spfPass = true
					} else if (aligned || spfDomain == "") && (r.Value == authres.ResultFail || r.Value == authres.ResultHardFail) {
						spfFail = true
					}
				}
			}
		}
	}

	// A bimi=pass header alone does not prove a mark certificate was validated.
	if dmarcPass {
		bimiPass = cachedBIMI(domain)
	}

	report.DKIMPass = dkimPass
	report.SPFPass = spfPass
	report.DMARCPass = dmarcPass
	report.BIMIPass = bimiPass

	if dmarcFail || (spfFail && dkimFail) {
		report.Status = SecurityFraud
		report.WarningKind = WarnAuthFailed
		return report
	}

	// Senders with a suspicious display name mismatch or structural evasion:
	// If domain has valid cryptographic authentication (DMARC or DKIM pass on legitimate domain),
	// a differing product/organization name alone (e.g. AFFiNE Team on everything.info)
	// does NOT prevent verification.
	// Only if the domain is NOT authenticated, or has an unresolved mismatch, it falls to NeedsReview (Gelb) or Warning (Rot).
	isAuthDomain := (dmarcPass || dkimPass) && !IsFreemail(domain) && !hasSuspiciousDomain

	// A brand name on an authenticated domain of its own may be a domain the
	// list misses (amazonaws.com once): review, not fraud.
	if report.BrandName != "" {
		report.Status = SecurityFraud
		if isAuthDomain {
			report.Status = SecurityNeedsReview
		}
		report.WarningKind = WarnDisplaySpoof
		return report
	}

	if (report.SuspiciousMismatch || hasZeroWidth || hasSuspiciousDomain) && !isAuthDomain {
		if hasZeroWidth || hasSuspiciousDomain {
			report.Status = SecurityWarning
			report.WarningKind = WarnDisplaySpoof
			return report
		}
		report.Status = SecurityNeedsReview
		report.WarningKind = WarnSuspicious
		return report
	}

	// Public freemail domains (yahoo.com, gmail.com, etc.) must NEVER receive
	// a verified domain or certified brand badge, even if DKIM passes.
	if IsFreemail(domain) {
		report.Status = SecurityNeutral
		return report
	}

	if hasSuspiciousDomain || hasZeroWidth {
		report.Status = SecurityNeedsReview
		report.WarningKind = WarnSuspicious
		return report
	}

	// Stufe 3: Certified Brand (DMARC pass + valid BIMI/VMC)
	if dmarcPass && bimiPass {
		report.Status = SecurityCertifiedBrand
		return report
	}

	// Stufe 2: Verified Domain (DMARC or DKIM pass on legitimate domain)
	if dmarcPass || dkimPass {
		report.Status = SecurityVerifiedDomain
		return report
	}

	return report
}

// SecurityReportToDB converts a SecurityReport into (status, reason) strings for SQL persistence.
func SecurityReportToDB(rep SecurityReport) (status string, reason string) {
	switch rep.Status {
	case SecurityCertifiedBrand:
		if rep.BIMIPass {
			return "certified_brand", "bimi_pass"
		}
		return "certified_brand", "bimi_dns"
	case SecurityVerifiedDomain:
		if rep.DMARCPass {
			return "verified_domain", "dmarc_pass"
		}
		return "verified_domain", "dkim_pass"
	case SecurityNeedsReview:
		switch rep.WarningKind {
		case WarnSuspicious:
			return "needs_review", "suspicious"
		case WarnDisplaySpoof:
			return "needs_review", "display_mismatch"
		default:
			return "needs_review", "review_recommended"
		}
	case SecurityFraud:
		switch rep.WarningKind {
		case WarnDisplaySpoof:
			return "warning", "spoofing"
		case WarnEmailInName:
			return "warning", "email_in_name"
		case WarnAuthFailed:
			return "warning", "auth_failed"
		case WarnAIFraud:
			return "warning", "ai_fraud"
		default:
			return "warning", "warning"
		}
	default:
		return "neutral", "unverified"
	}
}

// DBToSecurityReport restores a SecurityReport from stored DB values and raw header info.
func DBToSecurityReport(from, authResults, status, reason string) SecurityReport {
	if status == "neutral" {
		rep := EvaluateSecurity(from, "")
		return SecurityReport{Status: SecurityNeutral, ActualDomain: rep.ActualDomain}
	}
	rep := EvaluateSecurity(from, authResults)
	switch status {
	case "certified_brand":
		rep.Status = SecurityCertifiedBrand
	case "verified_domain":
		rep.Status = SecurityVerifiedDomain
	case "needs_review":
		rep.Status = SecurityNeedsReview
		switch reason {
		case "ai_suspicious", "suspicious":
			rep.WarningKind = WarnSuspicious
		case "display_mismatch":
			rep.WarningKind = WarnDisplaySpoof
		}
	case "warning", "fraud":
		rep.Status = SecurityFraud
		switch reason {
		case "spoofing":
			rep.WarningKind = WarnDisplaySpoof
		case "email_in_name":
			rep.WarningKind = WarnEmailInName
		case "auth_failed":
			rep.WarningKind = WarnAuthFailed
		case "ai_fraud":
			rep.WarningKind = WarnAIFraud
		}
	}
	return rep
}
