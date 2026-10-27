package mail

import (
	"cmp"
	"fmt"
	"html"
	"net/mail"
	"strings"

	"github.com/godwit-email/godwit/internal/i18n"
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
func QuoteHTML(orig *Parsed) string { return QuoteHTMLIn(orig, "") }

// QuoteHTMLIn formats QuoteHTML with the attribution line in lang ("" or zh-Hans defaults to Simplified Chinese).
func QuoteHTMLIn(orig *Parsed, lang string) string {
	body := StripMarkers(SanitizeCompose(orig.HTML)) // the quoted mail's own markers are content
	if orig.HTML == "" {
		body = strings.ReplaceAll(html.EscapeString(strings.ReplaceAll(orig.Text, "\r\n", "\n")), "\n", "<br>")
	}
	date, from := i18n.FullDateIn(orig.Date, cmp.Or(lang, "zh-Hans"))+orig.Date.Format(" 15:04"), html.EscapeString(orig.From)
	return "<p>" + quoteLead(lang, date, from) + "</p><blockquote>" + body + "</blockquote>"
}

func quoteLead(lang, date, from string) string {
	t := strings.ToLower(strings.TrimSpace(lang))
	if strings.HasPrefix(t, "zh-tw") || strings.HasPrefix(t, "zh-hant") || strings.HasPrefix(t, "zh-hk") || strings.HasPrefix(t, "zh-mo") {
		return fmt.Sprintf("於 %s，%s 寫道：", date, from)
	}
	base, _, _ := strings.Cut(t, "-")
	switch base {
	case "", "zh":
		return fmt.Sprintf("于 %s，%s 写道：", date, from)
	case "ja":
		return fmt.Sprintf("%s、%s さんは書きました：", date, from)
	case "ko":
		return fmt.Sprintf("%s에 %s 님이 작성:", date, from)
	case "de":
		return fmt.Sprintf("Am %s schrieb %s:", date, from)
	case "fr":
		return fmt.Sprintf("Le %s, %s a écrit :", date, from)
	case "es":
		return fmt.Sprintf("El %s, %s escribió:", date, from)
	case "pt":
		return fmt.Sprintf("Em %s, %s escreveu:", date, from)
	case "it":
		return fmt.Sprintf("Il %s, %s ha scritto:", date, from)
	case "ru":
		return fmt.Sprintf("%s, %s пишет:", date, from)
	case "nl":
		return fmt.Sprintf("Op %s schreef %s:", date, from)
	case "tr":
		return fmt.Sprintf("%s tarihinde %s yazdı:", date, from)
	case "vi":
		return fmt.Sprintf("Vào %s, %s đã viết:", date, from)
	default:
		return fmt.Sprintf("On %s, %s wrote:", date, from)
	}
}

func hasPrefix(s string, prefixes ...string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, p := range prefixes {
		if strings.HasPrefix(l, p+":") || strings.HasPrefix(l, p+"：") {
			return true
		}
	}
	return false
}

func ReplySubject(s string) string {
	if hasPrefix(s, "re", "aw", "res", "antw", "ynt", "回复", "答复", "回覆", "답장") {
		return s
	}
	return "Re: " + s
}

func ForwardSubject(s string) string {
	if hasPrefix(s, "fwd", "fw", "wg", "tr", "rv", "enc", "dr", "ilt", "转发", "轉寄", "轉發", "전달") {
		return s
	}
	return "Fwd: " + s
}
