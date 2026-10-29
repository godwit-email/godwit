package core

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// threadDigestFormat is what summarize asks of a thread on top of its body:
// where it has got to, step by step, citing its emails as Wit's answers do
// (citeTag), and what its newest email wants of the user (ParseThreadDigest).
const threadDigestFormat = `This is a thread. Instead of bullet points, answer in exactly this plain-text form, and nothing else:
PROGRESS
- <a step of the thread>
TODO: <what the newest email asks the user to do, by when>
Write one PROGRESS line per step where the thread moved on, oldest first, at most six: ` +
	`an email that only repeats an earlier one (a resend, a duplicate, a reminder) is no step of its own, and one line is right for a thread with one step. ` +
	`Right after what an email says, cite it with its tag <mail id="N"/>, N the number in its source label (message:N), ` +
	`for example: Bob sent the code <mail id="12"/> and resent it twice <mail id="13"/><mail id="14"/>. ` +
	`The TODO line is the one place to say what the user is to do; leave it out when the newest email asks nothing of them.`

// ThreadDigest is a thread's summary as summarize writes it for a thread:
// its steps, oldest first, and what the user is to do (nil for nothing).
type ThreadDigest struct {
	Progress []DigestLine
	Todo     DigestLine
}

// DigestLine is a line of a digest in runs: text, or a cite of the thread's
// message Message (Text "").
type DigestLine []DigestRun

// DigestRun is a run of a DigestLine.
type DigestRun struct {
	Text    string
	Message int64
}

var digestCite = regexp.MustCompile(`\s*<mail\s+id="?(\d+)"?\s*/?>(?:\s*</mail>)?`)

// ParseThreadDigest reads a thread's summary in threadDigestFormat; a cite
// of a message not in ids is dropped. ok is false for an answer with no
// step: the caller shows it as written.
func ParseThreadDigest(text string, ids []int64) (d ThreadDigest, ok bool) {
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if todo, found := strings.CutPrefix(line, "TODO:"); found {
			d.Todo = digestLine(todo, ids)
			continue
		}
		if step, found := strings.CutPrefix(line, "- "); found {
			if l := digestLine(step, ids); l != nil {
				d.Progress = append(d.Progress, l)
			}
		} // else PROGRESS, and whatever else the model wrote
	}
	return d, len(d.Progress) > 0
}

// digestLine is s in runs, its cites of ids as such; nil when it has no text.
func digestLine(s string, ids []int64) DigestLine {
	var l DigestLine
	s = strings.TrimSpace(s)
	text := func(t string) { // a cite takes the space before it, the one after stays the text's
		if strings.TrimSpace(t) != "" {
			l = append(l, DigestRun{Text: t})
		}
	}
	at := 0
	for _, m := range digestCite.FindAllStringSubmatchIndex(s, -1) {
		text(s[at:m[0]])
		at = m[1]
		if n, err := strconv.ParseInt(s[m[2]:m[3]], 10, 64); err == nil && slices.Contains(ids, n) {
			l = append(l, DigestRun{Message: n})
		}
	}
	text(s[at:])
	if !slices.ContainsFunc(l, func(r DigestRun) bool { return r.Text != "" }) {
		return nil
	}
	return l
}
