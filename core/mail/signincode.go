package mail

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

var (
	strongHints    = words(func(l lexicon) string { return l.codeHints })
	ambiguousHints = words(func(l lexicon) string { return l.ambiguous })
	copulas        = words(func(l lexicon) string { return l.copulas })
	codeHint       = "(?:" + strongHints + "|" + ambiguousHints + ")"
)

const codeToken = `([0-9]{3}[ \t-][0-9]{3}|[A-Za-z0-9]{4,10})`

var codeHints = ci(codeHint)

// Strong and ambiguous hints are found apart: "Votre code de vérification"
// holds both, and one search would stop at the ambiguous "Votre code".
var hintSearches = []*regexp.Regexp{ci(strongHints), ci(ambiguousHints)}
var codeAfter = ci(`^\s*(?:` + copulas + `\s*)?[:：=\s"'「【\[—]*` + codeToken)

// Hints that other mail uses too ("Your code is SAVE20", "Booking
// confirmation code", "card security code") need sign-in words nearby, or a
// code shaped like an OTP.
var bareHint = ci(`^` + ambiguousHints + `$`)
var signInContext = ci(words(func(l lexicon) string { return l.signIn }))

// otpShaped: six or more digits, as one-time codes are; not a CVV (1234),
// a booking reference (AB12CD) or a coupon (SAVE20).
func otpShaped(token string) bool {
	digits := strings.NewReplacer(" ", "", "\t", "", "-", "").Replace(token)
	return len(digits) >= 6 && strings.Trim(digits, "0123456789") == ""
}

// plausible: a token after (or before) the hint at s[start:end] is a code
// unless the hint is one other mail uses too, with no sign-in words near and
// a token not shaped like an OTP.
func plausible(s string, start, end int, token string) bool {
	return !bareHint.MatchString(s[start:end]) || signInNear(s, start, end) || otpShaped(token)
}

// A reply's or forward's subject repeats the old mail's, code and all.
var replySubject = ci(`^\s*` + words(func(l lexicon) string { return l.replyPrefix }) + `\s*[:：]`)

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

var aloneCode = regexp.MustCompile(`^(?:[0-9]{6,8}|[0-9]{3}[ -][0-9]{3})$`)
var codeEnter = ci(words(func(l lexicon) string { return l.enter }))
var codeWord = ci(words(func(l lexicon) string { return l.codeWords }))
var explicitCode = ci(`[:：=]|` + copulas)
var codeBefore = ci(codeToken + `\s*` + words(func(l lexicon) string { return l.codeBefore }) + `\s*` + codeHint)

// SignInCode recognizes an explicit code in the subject or unquoted text.
// Conflicting candidates are left to the reader rather than guessed.
func SignInCode(subject, text, htmlBody string) string {
	code := signInCode(subject, text, htmlBody)
	if code == "" && text != "" && strings.Contains(strings.ToLower(htmlBody), "<img") { // an HTML part may show the code as an image; its text part spells it out
		code = signInCode(subject, text, "")
	}
	return code
}

func signInCode(subject, text, htmlBody string) string {
	text, doc := unquoted(text, htmlBody)
	if htmlBody != "" && doc == nil {
		return ""
	}
	if replySubject.MatchString(simplified(subject)) {
		subject = ""
	}
	var lines []string
	for line := range strings.SplitSeq(subject+"\n"+text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), ">") {
			lines = append(lines, line)
		}
	}
	s := simplified(strings.Join(lines, "\n")) // 驗證碼 reads as 验证码; candidates are offsets into s itself
	code := ""
	var candidates [][2]int
	for _, hint := range allHints(s) {
		if m := codeAfter.FindStringSubmatchIndex(s[hint[1]:]); m != nil {
			// A word with no digit is a code only when the same line says so
			// ("code: XQWERT"); one on the next line is prose ("IMPORTANT").
			gap, token := s[hint[1]:hint[1]+m[2]], s[hint[1]+m[2]:hint[1]+m[3]]
			if !strings.ContainsFunc(token, unicode.IsDigit) && (strings.Contains(gap, "\n") || !explicitCode.MatchString(gap)) {
				continue
			}
			if !plausible(s, hint[0], hint[1], token) {
				continue
			}
			candidates = append(candidates, [2]int{hint[1] + m[2], hint[1] + m[3]})
		}
	}
	// A code alone on its line, under one that tells to enter it: "Copy
	// this code and enter it in your browser." then "118169". OTP-shaped
	// only, and a coupon ("Use this code at checkout:") does not say enter.
	offset, prev := 0, ""
	for line := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			if aloneCode.MatchString(t) && codeWord.MatchString(prev) {
				start := offset + strings.Index(line, t)
				if codeEnter.MatchString(prev) || signInNear(s, start, start+len(t)) {
					candidates = append(candidates, [2]int{start, start + len(t)})
				}
			}
			prev = t
		}
		offset += len(line) + 1
	}
	for _, m := range codeBefore.FindAllStringSubmatchIndex(s, -1) {
		hint := codeHints.FindStringIndex(s[m[3]:m[1]])
		if hint != nil && !plausible(s, m[3]+hint[0], m[3]+hint[1], s[m[2]:m[3]]) { // "SAVE20 is your code"
			continue
		}
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

// allHints are where every strong and every ambiguous hint stands in s.
func allHints(s string) [][]int {
	var out [][]int
	for _, re := range hintSearches {
		out = append(out, re.FindAllStringIndex(s, -1)...)
	}
	return out
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

var replyHeader = ci(`^\s*` + words(func(l lexicon) string { return l.replyHeader }) + `\s*$`)
var replyFrom = ci(`^\s*` + words(func(l lexicon) string { return l.replyFrom }) + `\s*[:：]`)
var replySent = ci(`^\s*` + words(func(l lexicon) string { return l.replySent }) + `\s*[:：]`)

// beforeReply cuts a reply's quoted message that has no "> " marks: from
// "-----Original Message-----", "On … wrote:", or an Outlook header block
// (From: then Sent: or Date:) on.
func beforeReply(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines { // matched as the lexicons' words are written: normalised
		if replyHeader.MatchString(simplified(line)) || replyFrom.MatchString(simplified(line)) && i+1 < len(lines) && replySent.MatchString(simplified(lines[i+1])) {
			return strings.Join(lines[:i], "\n")
		}
	}
	return text
}

// simplified writes the Traditional characters these rules look for in their
// Simplified form, so "驗證碼" and "重置密碼" read as "验证码" and "重置密码",
// and folds Turkish İ/ı to i. Offsets are taken in its result, never mapped
// back to the input.
func simplified(s string) string { return traditional.Replace(s) }

var traditional = strings.NewReplacer(
	"連結", "链接", // a word whose characters map apart (連 alone is 连); first, as keys match in order
	// Turkish İ and ı, which case-insensitive matching keeps apart from i:
	// "GİRİŞ YAP" reads as "giriş yap".
	"İ", "i", "ı", "i",
	"驗", "验", "證", "证", "碼", "码", "動", "动", "態", "态", "確", "确", "認", "认",
	"錄", "录", "郵", "邮", "電", "电", "帳", "帐", "賬", "账", "戶", "户", "號", "号",
	"鏈", "链", "結", "结", "連", "连", "設", "设", "點", "点", "擊", "击", "請", "请",
	"無", "无", "鈕", "钮", "過", "过", "該", "该", "條", "条",
	"訪", "访", "問", "问", "載", "载", "啟", "启", "啓", "启", "這", "这", "個", "个",
	"從", "从", "於", "于", "後", "后", "時", "时", "鐘", "钟", "內", "内", "開", "开",
	"復", "复", "製", "制", "輸", "输", "續", "续",
)

func asciiCodeByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// GroupCode splits a six-digit code in two, as people read it ("482 913").
func GroupCode(code string) string {
	if len(code) == 6 && strings.Trim(code, "0123456789") == "" {
		return code[:3] + " " + code[3:]
	}
	return code
}
