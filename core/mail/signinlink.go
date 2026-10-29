package mail

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/idna"
)

// The kinds of action link ActionLink finds.
const (
	LinkSignIn  = "signin"  // signs in: a magic link
	LinkConfirm = "confirm" // confirms the address a new account was made with
	LinkReset   = "reset"   // sets a new password
)

var (
	clickTo  = words(func(l lexicon) string { return l.clickTo })
	leadTail = words(func(l lexicon) string { return l.leadTail })
	confirms = words(func(l lexicon) string { return l.confirm })
	resets   = words(func(l lexicon) string { return l.reset })
)

// A magic link's mail speaks of the link itself: "This link expires in 10
// minutes", "your sign-in link", "登录链接". A reminder to go and sign in
// ("请登录您的 Google 相册账号") leads to a sign-in page, not a link that signs in.
var linkDelivery = ci(words(func(l lexicon) string { return l.delivery }))

// A security alert speaks of signing in but its links review activity.
var linkAlert = ci(words(func(l lexicon) string { return l.alert }))

var linkSubject = ci(words(func(l lexicon) string { return l.signInSubject }))

// A button's own words, whole: "Sign in", "Log in to Acme", "Click here to
// sign in", "登录". "Sign in activity" in a security alert is not one.
var linkLabel = ci(`^\s*` + words(func(l lexicon) string { return l.signInLabel }) + `\s*[.!]?\s*$`)

var plainURL = regexp.MustCompile(`https://[^\s<>"'）)」】\]]+`)

// What a line says right before its colon: an action, not a report
// ("Review sign-in activity:").
var linkLead = ci(words(func(l lexicon) string { return l.signInLead }) + `\s*[:：]\s*$`)

// A button that confirms the address an account was made with: "Confirm
// your email", "Verify email address", "确认您的电子邮件".
var confirmLabel = ci(`^\s*(?:` + clickTo + `)?` + confirms + `\s*[.!]?\s*$`)
var confirmLead = ci(confirms + `(?:` + leadTail + `)?\s*[:：]\s*$`)

// A mail about confirming an address, for the button fallback below.
var confirmContext = ci(confirms + `|` + words(func(l lexicon) string { return l.confirmContext }))

// Setting a new password: "Reset password", "若要重置密码，请访问下面的链接：".
var resetLabel = ci(`^\s*(?:` + clickTo + `)?` + resets + `\s*[.!]?\s*$`)
var resetLead = ci(resets + `[^:：\n]{0,30}[:：]\s*$`)
var resetContext = ci(resets + `|` + words(func(l lexicon) string { return l.resetContext }))

// The line many mails put under a button that is only an image: "If the
// button does not work, use this link:", "Button not working? Use this link
// instead." What the link does comes from the mail around it.
var buttonFallback = ci(words(func(l lexicon) string { return l.fallback }))
var fallbackLabel = ci(`^\s*` + words(func(l lexicon) string { return l.fallbackLabel }) + `\s*$`)

// ActionLink recognizes the one link a mail asks the reader to open to sign
// in, to confirm a new account's address, or to set a new password, and
// which it is. Conflicting links are left to the reader rather than guessed.
func ActionLink(subject, text, htmlBody string) (link, kind string) {
	subject = simplified(subject)
	if replySubject.MatchString(subject) { // a reply's subject is the old mail's
		subject = ""
	}
	body := bodyText(text, htmlBody)
	if linkSubject.MatchString(subject) && !linkAlert.MatchString(subject) && (linkDelivery.MatchString(subject) || linkDelivery.MatchString(body)) {
		if l := oneLink(text, htmlBody, linkLabel, linkLead, true); l != "" {
			return l, LinkSignIn
		}
	}
	// An explicit button or lead stands alone; a button's fallback line
	// ("use this link") counts only in mail that is about that, quotes left out.
	if l := oneLink(text, htmlBody, confirmLabel, confirmLead, confirmContext.MatchString(subject) || confirmContext.MatchString(body)); l != "" {
		return l, LinkConfirm
	}
	if l := oneLink(text, htmlBody, resetLabel, resetLead, resetContext.MatchString(subject) || resetContext.MatchString(body)); l != "" {
		return l, LinkReset
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
	plain := text
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
	quoted := func(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), ">") }
	scan := func(text string) {
		lines := strings.Split(simplified(text), "\n")
		for i, line := range lines {
			if quoted(line) {
				continue
			}
			var urls []string
			if loc := plainURL.FindStringIndex(line); loc != nil {
				if leads(strings.TrimRight(line[:loc[0]], " \t<\"'(（「【[")) {
					urls = urlsOf(line)
				}
			} else if leads(line) {
				next := i + 1
				for next < len(lines) && strings.TrimSpace(lines[next]) == "" { // paragraphs have a blank line between
					next++
				}
				if next < len(lines) && !quoted(lines[next]) {
					urls = urlsOf(lines[next])
				}
			}
			for _, u := range urls {
				add(strings.TrimRight(u, ".,;:!?。，；：！？、")) // the sentence's punctuation, not the link's
			}
		}
	}
	scan(text)
	if link == "" && !conflict && htmlBody != "" && plain != "" { // an image-only button may leave the URL to the text part
		scan(beforeReply(plain))
	}
	return pick(link, conflict)
}

// urlsOf are a line's https URLs, leaving out a "(https://…)" that annotates
// the link before it with its real address, as mail text writes "label (href)".
func urlsOf(line string) []string {
	var out []string
	for _, loc := range plainURL.FindAllStringIndex(line, -1) {
		if len(out) > 0 && loc[0] > 0 && line[loc[0]-1] == '(' {
			continue
		}
		out = append(out, line[loc[0]:loc[1]])
	}
	return out
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
	return strings.TrimPrefix(ASCIIHost(u.Hostname()), "www.")
}

// ASCIIHost is host as shown before a link is followed: in punycode (xn--…)
// when it has a non-ASCII letter, so a look-alike such as Cyrillic
// "аpple.com" does not pass for apple.com.
func ASCIIHost(host string) string {
	for i := range len(host) {
		if host[i] >= utf8.RuneSelf {
			if a, err := idna.Punycode.ToASCII(host); err == nil {
				return a
			}
			return strings.Trim(strconv.QuoteToASCII(host), `"`)
		}
	}
	return host
}
