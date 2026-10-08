package mail

import (
	"fmt"
	"html"
	"net/mail"
	"strings"
)

// AddrKey is the lowercased bare address of an address string ("Bob <b@x>" → "b@x").
func AddrKey(s string) string {
	if a, err := mail.ParseAddress(s); err == nil {
		return strings.ToLower(a.Address)
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// ReplyRecipients picks reply targets: Reply-To if set, else From. With all,
// the original To/Cc are added to cc. self and duplicates are dropped.
func ReplyRecipients(orig *Parsed, self string, all bool) (to, cc []string) {
	seen := map[string]bool{AddrKey(self): true}
	add := func(dst *[]string, in ...string) {
		for _, s := range in {
			if s = strings.TrimSpace(s); s != "" && !seen[AddrKey(s)] {
				seen[AddrKey(s)] = true
				*dst = append(*dst, s)
			}
		}
	}
	if orig.ReplyTo != "" {
		add(&to, orig.ReplyTo)
	} else {
		add(&to, orig.From)
	}
	if all {
		add(&cc, orig.To...)
		add(&cc, orig.Cc...)
	}
	return
}

// QuoteHTML returns the "on {date}, {from} wrote:" line plus the original in a blockquote.
func QuoteHTML(orig *Parsed) string {
	body := StripMarkers(SanitizeCompose(orig.HTML)) // the quoted mail's own markers are content
	if orig.HTML == "" {
		body = strings.ReplaceAll(html.EscapeString(strings.ReplaceAll(orig.Text, "\r\n", "\n")), "\n", "<br>")
	}
	return fmt.Sprintf("<p>于 %s，%s 写道：</p><blockquote>%s</blockquote>",
		orig.Date.Format("2006-01-02 15:04"), html.EscapeString(orig.From), body)
}

func hasPrefix(s string, prefixes ...string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, p := range prefixes {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

func ReplySubject(s string) string {
	if hasPrefix(s, "re:", "回复：", "回复:") {
		return s
	}
	return "Re: " + s
}

func ForwardSubject(s string) string {
	if hasPrefix(s, "fwd:", "fw:", "转发：", "转发:") {
		return s
	}
	return "Fwd: " + s
}
