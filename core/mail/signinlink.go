package mail

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// The kinds of action link ActionLink finds.
const (
	LinkSignIn  = "signin"  // signs in: a magic link
	LinkConfirm = "confirm" // confirms the address a new account was made with
	LinkReset   = "reset"   // sets a new password
)

// A magic link's mail speaks of the link itself: "This link expires in 10
// minutes", "your sign-in link", "登录链接". A reminder to go and sign in
// ("请登录您的 Google 相册账号") leads to a sign-in page, not a link that signs in.
var linkDelivery = regexp.MustCompile(`(?i)\b(?:magic|secure|sign[ -]?in|log[ -]?in|login|one[ -]time|single[ -]use)\s+link\b|\bthis link\b|\blink\b[^\n]{0,60}\b(?:expires?|valid|used once)\b|(?:登录|一次性|安全)链接|链接[^\n]{0,30}(?:有效|过期|失效)`)

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
const confirmWhat = `(?:confirm|verify|activate)\s+(?:your\s+)?(?:e-?mail(?:\s+address)?|account)`
const confirmWhatZh = `(?:确认|验证|激活)(?:您|你)?的?(?:电子邮件|电子邮件地址|邮箱|邮箱地址|邮件地址|账户|帐户|账号)`

var confirmLabel = regexp.MustCompile(`(?i)^\s*(?:(?:click|tap) (?:here )?to )?` + confirmWhat + `\s*[.!]?\s*$|^\s*(?:点击)?` + confirmWhatZh + `\s*$`)
var confirmLead = regexp.MustCompile(`(?i)` + confirmWhat + `(?: here| below|` + byLink + `)?\s*[:：]\s*$|` + confirmWhatZh + `\s*[:：]\s*$`)

// A mail about confirming an address, for the button fallback below.
var confirmContext = regexp.MustCompile(`(?i)` + confirmWhat + `|\bemail verification\b|` + confirmWhatZh)

// Setting a new password: "Reset password", "若要重置密码，请访问下面的链接：".
const resetWhat = `(?:reset|change)\s+(?:your\s+)?password|(?:重置|重设|找回)(?:您的|你的)?密码`

var resetLabel = regexp.MustCompile(`(?i)^\s*(?:(?:click|tap) (?:here )?to )?(?:` + resetWhat + `)\s*[.!]?\s*$`)
var resetLead = regexp.MustCompile(`(?i)(?:` + resetWhat + `)[^:：\n]{0,30}[:：]\s*$`)
var resetContext = regexp.MustCompile(`(?i)` + resetWhat + `|password reset`)

// The line many mails put under a button that is only an image: "If the
// button does not work, use this link:", "Button not working? Use this link
// instead." What the link does comes from the mail around it.
var buttonFallback = regexp.MustCompile(`(?i)(?:button|link)\s+(?:does\s*n[o']t|is\s*n[o']t|won'?t|not)\s+work.*\b(?:use|copy|paste|open)\b.*\b(?:link|url|address)\b[^:：\n]*[:：]?\s*$|trouble clicking.*(?:copy|paste).*\b(?:url|link)\b|(?:无法|不能)点击.*(?:按钮|链接).*[:：]\s*$`)
var fallbackLabel = regexp.MustCompile(`(?i)^\s*use this link\s*$`)

// ActionLink recognizes the one link a mail asks the reader to open to sign
// in, to confirm a new account's address, or to set a new password, and
// which it is. Conflicting links are left to the reader rather than guessed.
func ActionLink(subject, text, htmlBody string) (link, kind string) {
	subject = simplified(subject)
	if linkSubject.MatchString(subject) && (linkDelivery.MatchString(subject) || linkDelivery.MatchString(bodyText(text, htmlBody))) {
		if l := oneLink(text, htmlBody, linkLabel, linkLead, true); l != "" {
			return l, LinkSignIn
		}
	}
	confirm := confirmContext.MatchString(subject) || confirmContext.MatchString(simplified(text))
	if l := oneLink(text, htmlBody, confirmLabel, confirmLead, confirm); l != "" {
		return l, LinkConfirm
	}
	if resetContext.MatchString(subject) {
		if l := oneLink(text, htmlBody, resetLabel, resetLead, true); l != "" {
			return l, LinkReset
		}
	}
	return "", ""
}

// bodyText is the mail's unquoted text, Traditional read as Simplified.
func bodyText(text, htmlBody string) string {
	text, _ = unquoted(text, htmlBody)
	return simplified(text)
}

// oneLink is the one https link whose button label matches labelRE, or that
// follows a colon after leadRE in the text (or after a button fallback line,
// when fallback); "" for none or several.
func oneLink(text, htmlBody string, labelRE, leadRE *regexp.Regexp, fallback bool) string {
	text, doc := unquoted(text, htmlBody)
	if htmlBody != "" && doc == nil {
		return ""
	}
	text = simplified(text)
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
			l := simplified(label(n))
			for _, attr := range n.Attr {
				if attr.Key == "href" && (labelRE.MatchString(l) || fallback && fallbackLabel.MatchString(l)) {
					add(attr.Val)
				}
			}
		})
	}
	if link != "" || conflict { // a labelled button says it best; text the HTML writes out is a fallback
		return pick(link, conflict)
	}
	// Plain text has no link labels, and HTML may write the address out as
	// its own label ("Confirm your email address by clicking on this link:"
	// then the URL): the URL must follow the action and a colon ("Log in
	// here: https://…"), on its line or the next with text. A caveat ("If you
	// did not sign in, visit https://…") or a report ("Review sign-in
	// activity: https://…") is neither.
	leads := func(s string) bool { return leadRE.MatchString(s) || fallback && buttonFallback.MatchString(s) }
	lines := strings.Split(text, "\n")
	quoted := func(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), ">") }
	for i, line := range lines {
		if quoted(line) {
			continue
		}
		var urls []string
		if loc := plainURL.FindStringIndex(line); loc != nil {
			if leads(strings.TrimRight(line[:loc[0]], " \t<\"'(（「【[")) {
				urls = plainURL.FindAllString(line, -1)
			}
		} else if leads(line) {
			next := i + 1
			for next < len(lines) && strings.TrimSpace(lines[next]) == "" { // paragraphs have a blank line between
				next++
			}
			if next < len(lines) && !quoted(lines[next]) {
				urls = plainURL.FindAllString(lines[next], 1) // the URL written out, not its "(https://…)" repeat
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
