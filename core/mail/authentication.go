package mail

import (
	"slices"
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

// ReceiverAuthenticationResults returns the topmost Authentication-Results
// group. Receiving services prepend theirs above the trace, so it sits above
// anything the sender wrote; Gmail and iCloud put it below their own Received.
// shortcut: a receiver that adds no result of its own leaves the sender's on top;
// trust only configured authserv-ids if that shows up in practice.
func ReceiverAuthenticationResults(trace []AuthenticationHeader, _ ...string) string {
	start := slices.IndexFunc(trace, func(h AuthenticationHeader) bool { return strings.EqualFold(h.Key, "Authentication-Results") })
	if start < 0 {
		return ""
	}
	group := trace[start:]
	// The group ends at the next hop: results below it are an earlier hop's or the sender's.
	if end := slices.IndexFunc(group, func(h AuthenticationHeader) bool { return strings.EqualFold(h.Key, "Received") }); end >= 0 {
		group = group[:end]
	}
	return authenticationGroup(group)
}

// authenticationGroup merges one hop's results. A service may write one field
// per check under its own authserv-id (iCloud: bimi.icloud.com, dmarc.icloud.com).
func authenticationGroup(headers []AuthenticationHeader) string {
	identity := ""
	var results []authres.Result
	for _, h := range headers {
		if !strings.EqualFold(h.Key, "Authentication-Results") {
			continue
		}
		id, rr, err := parseSecurityAuthenticationResults(h.Value)
		if err != nil || id == "" {
			return ""
		} // malformed newest result is not a reason to trust an older one
		if identity == "" {
			identity = id
		}
		if len(rr) == 0 {
			return authres.Format(identity, nil)
		}
		results = append(results, rr...)
	}
	if identity == "" {
		return ""
	}
	return authres.Format(identity, results)
}
