package mail

import (
	"strings"

	gomail "github.com/emersion/go-message/mail"
	"github.com/emersion/go-msgauth/authres"
)

// AuthenticationHeader preserves reception headers in their original wire order.
type AuthenticationHeader struct{ Key, Value string }

func authenticationTrace(h *gomail.Header) []AuthenticationHeader {
	var trace []AuthenticationHeader
	size := 0
	fields := h.Fields()
	for fields.Next() {
		key := strings.ToLower(fields.Key())
		switch key {
		case "from", "subject", "date", "message-id", "mime-version", "content-type":
			return trace // Do not use authentication claims buried in original message headers.
		case "received", "authentication-results":
			value := strings.TrimSpace(fields.Value())
			size += len(value)
			if len(trace) >= 128 || size > 64*1024 {
				return nil
			}
			trace = append(trace, AuthenticationHeader{Key: fields.Key(), Value: value})
		}
	}
	return trace
}

// ReceiverAuthenticationResults accepts only the receiving service's result
// group prepended above its first Received trace. Below that boundary the same
// bytes may be supplied by the sender, even when the service ID matches MX/IMAP.
// Such messages require local cryptographic verification instead. Legacy host
// hints are deliberately ignored: server-name matching is not authentication.
func ReceiverAuthenticationResults(trace []AuthenticationHeader, _ ...string) string {
	for i, h := range trace {
		if strings.EqualFold(h.Key, "Received") {
			result, _ := authenticationGroup(trace[:i], nil)
			return result
		}
	}
	return ""
}

func authenticationGroup(headers []AuthenticationHeader, accept func(string) bool) (string, bool) {
	identity := ""
	var results []authres.Result
	for _, h := range headers {
		if !strings.EqualFold(h.Key, "Authentication-Results") {
			continue
		}
		id, rr, err := parseSecurityAuthenticationResults(h.Value)
		if err != nil || id == "" {
			return "", true
		} // malformed newest result is not a reason to trust an older one
		if identity == "" {
			if accept != nil && !accept(id) {
				return "", true
			}
			identity = id
		} else if !strings.EqualFold(id, identity) {
			break
		}
		if len(rr) == 0 {
			return authres.Format(identity, nil), true
		}
		results = append(results, rr...)
	}
	if identity == "" {
		return "", false
	}
	return authres.Format(identity, results), true
}
