package mail

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var linkSubject = regexp.MustCompile(`(?i)\b(?:sign|log)[ -]?in\b|\bmagic link\b|登录|登入`)

// A button's own words, whole: "Sign in", "Log in to Acme", "Click here to
// sign in", "登录". "Sign in activity" in a security alert is not one.
var linkLabel = regexp.MustCompile(`(?i)^\s*(?:(?:click|tap) (?:here )?to |continue to )?(?:(?:sign|log)[ -]?in|magic link)(?:\s+(?:to|with)\s+\S.*)?\s*[.!]?\s*$|^\s*(?:点击|立即)?(?:登录|登入)(?:到|至)?\s*[A-Za-z0-9 ._-]*$`)

var plainURL = regexp.MustCompile(`https://[^\s<>"'）)]+`)

// SignInLink recognizes the one sign-in link of a sign-in mail: the subject
// asks to sign in and exactly one https link says so. Conflicting links are
// left to the reader rather than guessed.
func SignInLink(subject, text, htmlBody string) string {
	if !linkSubject.MatchString(subject) {
		return ""
	}
	text, doc := unquoted(text, htmlBody)
	if htmlBody != "" && doc == nil {
		return ""
	}
	link, conflict := "", false
	add := func(raw string) {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return
		}
		if link != "" && link != u.String() {
			conflict = true
		}
		link = u.String()
	}
	if doc != nil {
		walkNodes(doc, func(n *html.Node) {
			if n.Type != html.ElementNode || n.Data != "a" {
				return
			}
			for _, attr := range n.Attr {
				if attr.Key == "href" && linkLabel.MatchString(label(n)) {
					add(attr.Val)
				}
			}
		})
	} else {
		// Plain text has no link labels: the URL must follow a colon on a line
		// that asks to sign in ("Log in here: https://…"), or come on the line
		// after one ending in a colon. A caveat ("If you did not sign in,
		// visit https://…") has none.
		lines := strings.Split(text, "\n")
		quoted := func(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), ">") }
		for i, line := range lines {
			if quoted(line) || !linkSubject.MatchString(line) {
				continue
			}
			var urls []string
			if loc := plainURL.FindStringIndex(line); loc != nil {
				if before := strings.TrimSpace(line[:loc[0]]); strings.HasSuffix(before, ":") || strings.HasSuffix(before, "：") {
					urls = plainURL.FindAllString(line, -1)
				}
			} else if t := strings.TrimSpace(line); (strings.HasSuffix(t, ":") || strings.HasSuffix(t, "：")) && i+1 < len(lines) && !quoted(lines[i+1]) {
				urls = plainURL.FindAllString(lines[i+1], -1)
			}
			for _, u := range urls {
				add(strings.TrimRight(u, ".,;:!?")) // the sentence's punctuation, not the link's
			}
		}
	}
	if conflict {
		return ""
	}
	return link
}

// label is all the text inside n, spaces collapsed.
func label(n *html.Node) string {
	var b strings.Builder
	walkNodes(n, func(c *html.Node) {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	})
	return strings.Join(strings.Fields(b.String()), " ")
}

// LinkHost is where a link really goes, shown before it opens.
func LinkHost(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return link
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}
