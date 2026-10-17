package mail

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var linkSubject = regexp.MustCompile(`(?i)\b(?:sign|log)[ -]?in\b|\bmagic link\b|登录|登入`)

// A button's own words: "Sign in", "Log in to Acme", "Click here to sign in",
// "登录". "Review sign-in activity" in a security alert is not one.
var linkLabel = regexp.MustCompile(`(?i)^\s*(?:(?:click|tap) (?:here )?to |continue to )?(?:(?:sign|log)[ -]?in\b|magic link\b)|^\s*(?:点击|立即)?(?:登录|登入)`)

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
		// Plain text has no link labels: a URL on a line that asks to sign in,
		// or on the line after one.
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), ">") || !linkSubject.MatchString(line) {
				continue
			}
			urls := plainURL.FindAllString(line, -1)
			if len(urls) == 0 && i+1 < len(lines) {
				urls = plainURL.FindAllString(lines[i+1], -1)
			}
			for _, u := range urls {
				add(u)
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
