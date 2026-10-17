package mail

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var linkSubject = regexp.MustCompile(`(?i)\b(?:sign|log)[ -]?in\b|\b(?:magic|secure) link\b|登录|登入`)

// A button's own words, whole: "Sign in", "Log in to Acme", "Click here to
// sign in", "登录". "Sign in activity" in a security alert is not one.
var linkLabel = regexp.MustCompile(`(?i)^\s*(?:(?:click|tap) (?:here )?to |continue to )?(?:(?:sign|log)[ -]?in|magic link)(?:\s+(?:to|with)\s+\S.*)?\s*[.!]?\s*$|^\s*(?:点击|立即)?(?:登录|登入)(?:到|至)?\s*[A-Za-z0-9 ._-]*$`)

var plainURL = regexp.MustCompile(`https://[^\s<>"'）)」】\]]+`)

// "by clicking on this link", "using the button below": how, not what.
const byLink = ` (?:by clicking|by tapping|using|with|via)(?: on)? (?:this|the)(?: (?:link|button))?(?: below)?`

// What a plain-text line says right before its colon: an action, not a
// report ("Review sign-in activity:").
var linkLead = regexp.MustCompile(`(?i)(?:(?:sign|log)[ -]?in(?: here| now| below| link| to [^:：]+| with [^:：]+|` + byLink + `)?|magic link|(?:登录|登入)(?:链接)?)\s*[:：]\s*$`)

// A button that confirms the address an account was made with: "Confirm
// your email", "Verify email address", "确认您的电子邮件".
var confirmLabel = regexp.MustCompile(`(?i)^\s*(?:(?:click|tap) (?:here )?to )?(?:confirm|verify)\s+(?:your\s+)?(?:e-?mail(?:\s+address)?|account)\s*[.!]?\s*$|^\s*(?:点击)?(?:确认|验证)(?:您|你)?的?(?:电子邮件|电子邮件地址|邮箱|邮箱地址|邮件地址|账户|帐户|账号)\s*$`)
var confirmLead = regexp.MustCompile(`(?i)(?:confirm|verify)\s+(?:your\s+)?(?:e-?mail(?:\s+address)?|account)(?: here| below|` + byLink + `)?\s*[:：]\s*$|(?:确认|验证)(?:您|你)?的?(?:电子邮件|电子邮件地址|邮箱|邮箱地址|邮件地址|账户|帐户|账号)\s*[:：]\s*$`)

// SignInLink recognizes the one sign-in link of a sign-in mail: the subject
// asks to sign in and exactly one https link says so. Conflicting links are
// left to the reader rather than guessed.
func SignInLink(subject, text, htmlBody string) string {
	if !linkSubject.MatchString(subject) {
		return ""
	}
	return oneLink(text, htmlBody, linkLabel, linkLead)
}

// ConfirmLink recognizes the one link that confirms a new account's address.
// Its button says so outright, so the subject ("Welcome to …") need not.
func ConfirmLink(text, htmlBody string) string {
	return oneLink(text, htmlBody, confirmLabel, confirmLead)
}

// oneLink is the one https link whose button label matches labelRE, or in
// plain text that follows a colon after leadRE; "" for none or several.
func oneLink(text, htmlBody string, labelRE, leadRE *regexp.Regexp) string {
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
				if attr.Key == "href" && labelRE.MatchString(label(n)) {
					add(attr.Val)
				}
			}
		})
	}
	// Plain text has no link labels, and HTML may write the address out
	// as its own label ("Confirm your email address by clicking on this
	// link:" then the URL): the URL must follow a sign-in action
	// and a colon ("Log in here: https://…"), on its line or the one
	// before (the next with text). A caveat ("If you did not sign in, visit https://…") or a
	// report ("Review sign-in activity: https://…") is neither.
	if link != "" || conflict { // a labelled button says it best; text the HTML writes out is a fallback
		return pick(link, conflict)
	}
	lines := strings.Split(text, "\n")
	quoted := func(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), ">") }
	for i, line := range lines {
		if quoted(line) {
			continue
		}
		var urls []string
		if loc := plainURL.FindStringIndex(line); loc != nil {
			if leadRE.MatchString(strings.TrimRight(line[:loc[0]], " \t<\"'(（「【[")) {
				urls = plainURL.FindAllString(line, -1)
			}
		} else if leadRE.MatchString(line) {
			next := i + 1
			for next < len(lines) && strings.TrimSpace(lines[next]) == "" { // paragraphs have a blank line between
				next++
			}
			if next < len(lines) && !quoted(lines[next]) {
				urls = plainURL.FindAllString(lines[next], -1)
			}
		}
		for _, u := range urls {
			add(strings.TrimRight(u, ".,;:!?。，；：！？、")) // the sentence's punctuation, not the link's
		}
	}
	return pick(link, conflict)
}

func pick(link string, conflict bool) string {
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
