package mail

import (
	"errors"
	"net/mail"
	"strings"

	"github.com/emersion/go-msgauth/authres"
	"golang.org/x/net/idna"
)

var errAuthenticationSyntax = errors.New("invalid authentication result syntax")

// parseSecurityAuthenticationResults keeps the result library's typed results,
// but tokenizes quoting, folding and comments before handing it security fields.
// The library itself splits on whitespace/semicolons, even inside quoted values.
// Unrelated metadata (e.g. a reason containing "header.from=...") is discarded.
func parseSecurityAuthenticationResults(header string) (string, []authres.Result, error) {
	tokens, err := authenticationTokens(header)
	if err != nil || len(tokens) < 3 {
		return "", nil, errAuthenticationSyntax
	}
	end := 0
	for end < len(tokens) && tokens[end] != ";" {
		end++
	}
	if end < 1 || end > 2 || end == len(tokens) {
		return "", nil, errAuthenticationSyntax
	}
	if !authenticationAtom(tokens[0]) {
		return "", nil, errAuthenticationSyntax
	}
	var canonical strings.Builder
	canonical.WriteString(strings.Join(tokens[:end], " "))
	for i := end; i < len(tokens); {
		if tokens[i] != ";" {
			return "", nil, errAuthenticationSyntax
		}
		i++
		if i == len(tokens) {
			break
		}
		// Some receiver results (and previously formatted unknown methods)
		// contain a nameless clause such as "=none smtp.remote-ip=...".
		// It proves nothing: discard the whole clause, retaining valid named
		// checks on either side. The tokenizer distinguishes real delimiters
		// from semicolons inside quoted values/comments.
		if tokens[i] == "=" {
			for i < len(tokens) && tokens[i] != ";" {
				i++
			}
			continue
		}
		method := strings.ToLower(tokens[i])
		if !authenticationAtom(method) {
			return "", nil, errAuthenticationSyntax
		}
		i++
		canonical.WriteString("; ")
		canonical.WriteString(method)
		if method == "none" && (i == len(tokens) || tokens[i] == ";") {
			continue
		}
		if i+1 >= len(tokens) || tokens[i] != "=" || tokens[i+1] == ";" || tokens[i+1] == "=" || !authenticationAtom(tokens[i+1]) {
			return "", nil, errAuthenticationSyntax
		}
		canonical.WriteByte('=')
		canonical.WriteString(tokens[i+1])
		i += 2
		seen := make(map[string]bool)
		for i < len(tokens) && tokens[i] != ";" {
			if i+2 >= len(tokens) || tokens[i+1] != "=" || tokens[i+2] == ";" || tokens[i+2] == "=" {
				return "", nil, errAuthenticationSyntax
			}
			key, value := strings.ToLower(tokens[i]), tokens[i+2]
			i += 3
			if !authenticationAtom(key) || seen[key] {
				return "", nil, errAuthenticationSyntax
			}
			seen[key] = true
			mailbox := method == "dkim" && key == "header.i" || method == "spf" && key == "smtp.mailfrom"
			relevant := mailbox || method == "dkim" && key == "header.d" || method == "dmarc" && key == "header.from" || method == "spf" && key == "smtp.helo"
			if !relevant {
				continue
			}
			value, ok := authenticationDomain(value, mailbox)
			if !ok {
				return "", nil, errAuthenticationSyntax
			}
			if value != "" {
				canonical.WriteByte(' ')
				canonical.WriteString(key)
				canonical.WriteByte('=')
				canonical.WriteString(value)
			}
		}
	}
	id, results, err := authres.Parse(canonical.String())
	// v0.7.0 can parse ARC but cannot format its typed result; preserve the
	// method when the receiver group is combined and parsed a second time.
	for i, result := range results {
		if r, ok := result.(*authres.ARCResult); ok {
			results[i] = &authres.GenericResult{Method: "arc", Value: r.Value}
		}
	}
	return id, results, err
}

// authenticationTokens preserves quoted strings as one token, including spaces
// and semicolons. Comments are separators, never authentication properties.
func authenticationTokens(s string) ([]string, error) {
	if len(s) > 64*1024 {
		return nil, errAuthenticationSyntax
	}
	// Validate folding before unfolding, including inside quotes and comments.
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' {
			if i+2 >= len(s) || s[i+1] != '\n' || s[i+2] != ' ' && s[i+2] != '\t' {
				return nil, errAuthenticationSyntax
			}
			i++
		} else if s[i] < 32 && s[i] != '\t' || s[i] == 127 {
			return nil, errAuthenticationSyntax
		}
	}
	s = strings.ReplaceAll(s, "\r\n", "")
	var tokens []string
	for i := 0; i < len(s); {
		switch s[i] {
		case ' ', '\t':
			i++
		case '(':
			depth := 1
			i++
			for i < len(s) && depth > 0 {
				switch s[i] {
				case '\\':
					i++
					if i == len(s) {
						return nil, errAuthenticationSyntax
					}
				case '(':
					depth++
					if depth > 32 {
						return nil, errAuthenticationSyntax
					}
				case ')':
					depth--
				}
				i++
			}
			if depth != 0 {
				return nil, errAuthenticationSyntax
			}
		case ';', '=':
			tokens = append(tokens, s[i:i+1])
			i++
		default:
			start, quoted := i, false
			for i < len(s) {
				c := s[i]
				if !quoted && strings.ContainsRune(" \t\r\n(;=", rune(c)) {
					break
				}
				if c == '"' {
					quoted = !quoted
				}
				if quoted && c == '\\' {
					i++
					if i == len(s) {
						return nil, errAuthenticationSyntax
					}
				}
				if c < 32 && c != '\t' {
					return nil, errAuthenticationSyntax
				}
				i++
			}
			if quoted {
				return nil, errAuthenticationSyntax
			}
			tokens = append(tokens, s[start:i])
		}
	}
	return tokens, nil
}

// authenticationDomain removes balanced transport wrappers, not arbitrary
// interior quotes/whitespace. A malformed value must never become a valid match.
func authenticationDomain(s string, mailbox bool) (string, bool) {
	for layers := 0; ; layers++ {
		s = strings.Trim(s, " \t")
		if len(s) < 2 || (s[0] != '"' && s[0] != '\'') || s[len(s)-1] != s[0] {
			break
		}
		if layers >= 16 {
			return "", false
		}
		quote := s[0]
		s = s[1 : len(s)-1]
		if quote == '"' {
			var b strings.Builder
			for i := 0; i < len(s); i++ {
				if s[i] == '\\' {
					i++
					if i == len(s) {
						return "", false
					}
				}
				b.WriteByte(s[i])
			}
			s = b.String()
		}
	}
	if s == "" || mailbox && s == "<>" {
		return "", true
	}
	if mailbox {
		if strings.ContainsRune(s, '@') && !strings.HasPrefix(s, "@") {
			address, err := mail.ParseAddress(s)
			if err != nil {
				return "", false
			}
			s = address.Address
		}
		if at := strings.LastIndexByte(s, '@'); at >= 0 {
			s = s[at+1:]
		}
	}

	s = strings.TrimSuffix(s, ".")
	// IDNA handles internationalized domains without confusable substitution.
	domain, err := idna.Lookup.ToASCII(s)
	if err != nil || len(domain) == 0 || len(domain) > 253 {
		return "", false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return "", false
			}
		}
	}
	return strings.ToLower(domain), true
}

// Never let a quoted method/value inject a second result into the downstream
// library's semicolon/whitespace parser. Quoting is only accepted in properties.
func authenticationAtom(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._/-", c)) {
			return false
		}
	}
	return true
}
