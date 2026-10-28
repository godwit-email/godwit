package core

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// threadDigestFormat is what summarize asks of a thread on top of its body:
// where it has got to, step by step with each step's email, and what its
// newest email wants of the user (ParseThreadDigest).
const threadDigestFormat = `This is a thread. Instead of bullet points, answer in exactly this plain-text form, and nothing else:
PROGRESS
- <one sentence: a step of the thread> [message:<id>]
TODO: <one sentence: what the newest email asks the user to do, by when>
Write one PROGRESS line per step, oldest first, three to six in all; <id> is the number in the label of the email the step comes from. ` +
	`Leave out the TODO line when the newest email asks nothing of the user.`

// ThreadDigest is a thread's summary as summarize writes it for a thread:
// its steps, oldest first, and what the user is to do ("" for nothing).
type ThreadDigest struct {
	Progress []DigestStep
	Todo     string
}

// DigestStep is a step of a thread and the message it comes from (0: none
// that is the thread's).
type DigestStep struct {
	Text    string
	Message int64
}

var digestSource = regexp.MustCompile(`\s*\[message:\s*(\d+)\]\s*$`)

// ParseThreadDigest reads a thread's summary in threadDigestFormat; a
// source that is not one of ids is dropped, its step kept. ok is false for
// an answer with no step: the caller shows it as written.
func ParseThreadDigest(text string, ids []int64) (d ThreadDigest, ok bool) {
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if todo, found := strings.CutPrefix(line, "TODO:"); found {
			d.Todo = strings.TrimSpace(todo)
			continue
		}
		step, found := strings.CutPrefix(line, "- ")
		if !found {
			continue // PROGRESS, and whatever else the model wrote
		}
		var id int64
		if m := digestSource.FindStringSubmatch(step); m != nil {
			step = step[:len(step)-len(m[0])]
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && slices.Contains(ids, n) {
				id = n
			}
		}
		if step = strings.TrimSpace(step); step != "" {
			d.Progress = append(d.Progress, DigestStep{step, id})
		}
	}
	return d, len(d.Progress) > 0
}
