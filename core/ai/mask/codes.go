package mask

import (
	"regexp"
	"strings"
	"sync"

	"github.com/godwit-email/godwit/core/mail"
)

// What goes out is text only, so mail's recognizers read each mail in it
// as its text: the part from one <untrusted_email> to the next, with the
// Subject: line in it as its subject. Text with closing tags in it is read
// as HTML too, for the code it sets apart.

var subjectLine = regexp.MustCompile(`(?m)^Subject: (.*)$`)

// mails calls f with each mail in s and where it starts.
func mails(s string, f func(at int, subject, text string)) {
	for at := 0; at < len(s); {
		end := len(s)
		if i := strings.Index(s[at+1:], "<untrusted_email"); i >= 0 {
			end = at + 1 + i
		}
		text, subject := s[at:end], ""
		if m := subjectLine.FindStringSubmatch(text); m != nil {
			subject = m[1]
		}
		f(at, subject, text)
		at = end
	}
}

// codes is where each mail's sign-in code stands in s, however it is spaced.
func codes(s string) [][]int {
	var out [][]int
	mails(s, func(at int, subject, text string) {
		code := mail.SignInCode(subject, text, "")
		if code == "" && strings.Contains(text, "</") {
			code = mail.EmphasizedCode(subject, "", text)
		}
		if code == "" {
			return
		}
		var re strings.Builder
		for i, r := range code {
			if i > 0 {
				re.WriteString(`[ \t-]?`)
			}
			re.WriteString(regexp.QuoteMeta(string(r)))
		}
		for _, loc := range regexp.MustCompile(re.String()).FindAllStringIndex(text, -1) {
			out = append(out, []int{at + loc[0], at + loc[1]})
		}
	})
	return out
}

// links is where each mail's link that signs in, confirms an address or
// resets a password stands in s. One longer than 80 characters went as a
// godwit://link/ reference already.
func links(s string) [][]int {
	if !strings.Contains(s, "https://") { // ActionLink takes only these: skip its slow scan
		return nil
	}
	var out [][]int
	mails(s, func(at int, subject, text string) {
		link, _ := mail.ActionLink(subject, text, "")
		for i := 0; link != ""; {
			j := strings.Index(text[i:], link)
			if j < 0 {
				break
			}
			out = append(out, []int{at + i + j, at + i + j + len(link)})
			i += j + len(link)
		}
	})
	return out
}

// memo is f, which reads s alone, remembering what it found: a run sends
// its whole history again at each step, and mail's recognizers are slow.
func memo(f func(s string) [][]int) func(s string) [][]int {
	var mu sync.Mutex
	seen, size := map[string][][]int{}, 0
	return func(s string) [][]int {
		mu.Lock()
		locs, ok := seen[s]
		mu.Unlock()
		if ok {
			return locs
		}
		locs = f(s)
		mu.Lock()
		defer mu.Unlock()
		if size += len(s); size > memoBytes {
			clear(seen) // shortcut: forgets everything at once, enough while one history fits
			size = len(s)
		}
		seen[s] = locs
		return locs
	}
}

// memoBytes bounds the text memo keeps.
const memoBytes = 16 << 20
