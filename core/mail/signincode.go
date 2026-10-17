package mail

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

const codeHint = `(?:\b(?:verification|security|authentication|confirmation|login|sign[ -]?in|one[ -]time)\s+(?:verification\s+)?code\b|\bOTP\b|\byour code\b|验证码|校验码|动态码|一次性密码)`
const codeToken = `([0-9]{3}[ \t-][0-9]{3}|[A-Za-z0-9]{4,10})`

var codeHints = regexp.MustCompile(`(?i)` + codeHint)
var codeAfter = regexp.MustCompile(`(?i)^\s*(?:(?:is|为|是)\s*)?[:：=\s"'「【]*` + codeToken)

// Hints that other mail uses too ("Your code is SAVE20", "Booking
// confirmation code", "card security code") need sign-in words nearby, or a
// code shaped like an OTP.
var bareHint = regexp.MustCompile(`(?i)^(?:your|confirmation|security)\s+code$`)
var signInContext = regexp.MustCompile(`(?i)\b(?:verif\w*|sign[ -]?in|log[ -]?in|authenticat\w*|two[ -]factor|2fa|password|passcode)\b|\bconfirm (?:your )?(?:email|account|identity)\b|登录|登入|验证`)

// signInNear: the subject (s's first line), or the hint's line and the one
// before it, are about signing in; a footer elsewhere is not.
func signInNear(s string, start, end int) bool {
	subject, _, _ := strings.Cut(s, "\n")
	from := strings.LastIndexByte(s[:start], '\n')
	if from > 0 {
		from = strings.LastIndexByte(s[:from], '\n')
	}
	to := strings.IndexByte(s[end:], '\n')
	if to < 0 {
		to = len(s) - end
	}
	return signInContext.MatchString(subject) || signInContext.MatchString(s[from+1:end+to])
}

var explicitCode = regexp.MustCompile(`(?i)[:：=]|\bis\b|为|是`)
var codeBefore = regexp.MustCompile(`(?i)` + codeToken + `\s+(?:is your|is the)\s+` + codeHint)

// SignInCode recognizes an explicit code in the subject or unquoted text.
// Conflicting candidates are left to the reader rather than guessed.
func SignInCode(subject, text, htmlBody string) string {
	text, doc := unquoted(text, htmlBody)
	if htmlBody != "" && doc == nil {
		return ""
	}
	var lines []string
	for line := range strings.SplitSeq(subject+"\n"+text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), ">") {
			lines = append(lines, line)
		}
	}
	s := strings.Join(lines, "\n")
	code := ""
	var candidates [][2]int
	for _, hint := range codeHints.FindAllStringIndex(s, -1) {
		ambiguous := bareHint.MatchString(s[hint[0]:hint[1]]) && !signInNear(s, hint[0], hint[1])
		if m := codeAfter.FindStringSubmatchIndex(s[hint[1]:]); m != nil {
			// A word with no digit is a code only when the same line says so
			// ("code: XQWERT"); one on the next line is prose ("IMPORTANT").
			gap, token := s[hint[1]:hint[1]+m[2]], s[hint[1]+m[2]:hint[1]+m[3]]
			if !strings.ContainsFunc(token, unicode.IsDigit) && (strings.Contains(gap, "\n") || !explicitCode.MatchString(gap)) {
				continue
			}
			// Without sign-in words nearby, only an OTP's shape will do: six or
			// more digits, not a CVV (1234) or a booking reference (AB12CD).
			if digits := strings.NewReplacer(" ", "", "\t", "", "-", "").Replace(token); ambiguous && (len(digits) < 6 || strings.Trim(digits, "0123456789") != "") {
				continue
			}
			candidates = append(candidates, [2]int{hint[1] + m[2], hint[1] + m[3]})
		}
	}
	for _, m := range codeBefore.FindAllStringSubmatchIndex(s, -1) {
		candidates = append(candidates, [2]int{m[2], m[3]})
	}
	for _, candidate := range candidates {
		start, end := candidate[0], candidate[1]
		// A token embedded in an address, URL or longer identifier is not a code.
		if start > 0 && (asciiCodeByte(s[start-1]) || strings.ContainsRune("/@_.-", rune(s[start-1]))) {
			continue
		}
		if end < len(s) && (asciiCodeByte(s[end]) || strings.ContainsRune("/@_-", rune(s[end])) || s[end] == '.' && end+1 < len(s) && asciiCodeByte(s[end+1])) {
			continue
		}
		v := strings.NewReplacer(" ", "", "\t", "", "-", "").Replace(s[start:end])
		if !strings.ContainsFunc(v, unicode.IsDigit) && (len(v) < 6 || v != strings.ToUpper(v)) {
			continue
		}
		if code != "" && code != v {
			return ""
		}
		code = v
	}
	return code
}

// unquoted is the message text without quoted replies; with an HTML body,
// also its parsed document. An unparsable body gives "" and nil.
func unquoted(text, htmlBody string) (string, *html.Node) {
	if htmlBody == "" {
		return beforeReply(text), nil
	}
	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err != nil {
		return "", nil
	}
	var stripQuotes func(*html.Node)
	stripQuotes = func(n *html.Node) {
		for child := n.FirstChild; child != nil; {
			next := child.NextSibling
			quoted := child.Type == html.ElementNode && child.Data == "blockquote"
			for _, attr := range child.Attr {
				// Outlook quotes without markup: everything from its reply header on is the old message.
				if attr.Key == "id" && (attr.Val == "divRplyFwdMsg" || attr.Val == "appendonsend") {
					for c := child; c != nil; {
						after := c.NextSibling
						n.RemoveChild(c)
						c = after
					}
					return
				}
				if attr.Key == "class" {
					for token := range strings.FieldsSeq(attr.Val) {
						if token == "gmail_quote" || token == "yahoo_quoted" || token == "protonmail_quote" {
							quoted = true
						}
					}
				}
			}
			if quoted {
				n.RemoveChild(child)
			} else {
				stripQuotes(child)
			}
			child = next
		}
	}
	stripQuotes(doc)
	var clean strings.Builder
	if err := html.Render(&clean, doc); err != nil {
		return "", nil
	}
	return beforeReply(HTMLToText(clean.String())), doc
}

var replyHeader = regexp.MustCompile(`(?i)^\s*(?:-{2,}\s*(?:original message|forwarded message)\s*-{2,}|on .+ wrote:|在.+写道[:：])\s*$`)
var replyFrom = regexp.MustCompile(`(?i)^\s*(?:from|发件人)\s*[:：]`)
var replySent = regexp.MustCompile(`(?i)^\s*(?:sent|date|发送时间|日期)\s*[:：]`)

// beforeReply cuts a reply's quoted message that has no "> " marks: from
// "-----Original Message-----", "On … wrote:", or an Outlook header block
// (From: then Sent: or Date:) on.
func beforeReply(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if replyHeader.MatchString(line) || replyFrom.MatchString(line) && i+1 < len(lines) && replySent.MatchString(lines[i+1]) {
			return strings.Join(lines[:i], "\n")
		}
	}
	return text
}

func asciiCodeByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}
