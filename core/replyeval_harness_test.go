package core

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// The reply-date eval: unreplied conversations, each with the reply date its
// newest email asks for, delivered to a test IMAP server, synced, summarized
// as the product does (the summarize skill with replyByFormat, in the case's
// time zone), read and checked as the product does (parseReplyBy,
// replyBy.check) and scored against those dates. The score is the model's:
// whether the product then writes the date also turns on reply status, which
// unit tests cover (TestSummaryCarriesReplyDate) and which the cases' fixed
// dates would leave behind. testdata/replyeval/README.md has the format.

// The pass threshold, set before the first run (#845): under 5% of the
// cases get a wrong reply date, over at least 200 cases from 2 mailboxes.
const (
	replyPass         = 0.05
	replyMinCases     = 200
	replyMinMailboxes = 2
	replySampleEvery  = 10 // with GODWIT_LIVE_REPLY_SAMPLES, every tenth case is also summarized without the lines
)

// replyCase is one conversation, oldest message first, and its reply date.
type replyCase struct {
	ID       string   `json:"id"`
	Mailbox  string   `json:"mailbox"` // which mailbox it came from: a verdict needs two
	Zone     string   `json:"zone"`    // the user's IANA time zone, which the date is on
	Note     string   `json:"note"`
	Messages []string `json:"messages"` // whole RFC 5322 messages
	Expect   string   `json:"expect"`   // as a REPLY BY line has it: YYYY-MM-DD [HH:MM], or none
	want     replyBy
	loc      *time.Location
}

// loadReplySuite reads the *.jsonl cases in dir and returns every problem
// found at once.
func loadReplySuite(dir string) ([]replyCase, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	var cases []replyCase
	var errs []error
	where := map[string]string{}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(path)
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(nil, 64<<20)
		for n := 1; sc.Scan(); n++ {
			if strings.TrimSpace(sc.Text()) == "" {
				continue
			}
			at := fmt.Sprintf("%s:%d", name, n)
			var c replyCase
			d := json.NewDecoder(strings.NewReader(sc.Text()))
			d.DisallowUnknownFields()
			if err := d.Decode(&c); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", at, err))
				continue
			}
			at += " " + c.ID
			bad := func(f string, a ...any) { errs = append(errs, fmt.Errorf("%s: "+f, append([]any{at}, a...)...)) }
			switch prev, dup := where[c.ID]; {
			case c.ID == "":
				bad("no id")
			case dup:
				bad("id also used at %s", prev)
			default:
				where[c.ID] = at
			}
			if c.Mailbox == "" {
				bad("no mailbox")
			}
			if c.loc, err = time.LoadLocation(c.Zone); err != nil || c.Zone == "" {
				bad("zone %q is not an IANA time zone", c.Zone)
			}
			if c.want, _, err = parseReplyBy("REPLY BY: " + c.Expect + "\nQUOTE: the label"); err != nil {
				bad("expect %q is not YYYY-MM-DD [HH:MM] or none", c.Expect)
			}
			if len(c.Messages) == 0 {
				bad("no messages")
			}
			for i, m := range c.Messages {
				if !strings.Contains(m, "\r\n") {
					c.Messages[i] = strings.ReplaceAll(m, "\n", "\r\n")
				}
			}
			cases = append(cases, c)
		}
		if err := sc.Err(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	if len(cases) == 0 && len(errs) == 0 {
		errs = append(errs, fmt.Errorf("%s: no cases", dir))
	}
	return cases, errors.Join(errs...)
}

// replyWay is one small model: setup points a new Service at it.
type replyWay struct {
	name  string
	setup func(t *testing.T, s *Service)
}

// replyRun is the cases summarized one way.
type replyRun struct {
	way   replyWay
	cases []replyCase
	got   map[string]replyGot // by case ID
	took  time.Duration
	err   error // the run stopped: not scored
}

// replyGot is what a case's summary gave.
type replyGot struct {
	r    replyBy // checked; zero is no date
	why  string  // why lines gave no date: they did not read or did not check
	text string  // the summary without the lines
	base string  // a sampled case's summary as production asks it
}

// runReplyEval summarizes the cases each way; with sample > 0, every
// sample-th case is summarized again without replyByFormat (summaryBare).
func runReplyEval(t *testing.T, cases []replyCase, ways []replyWay, sample int) []replyRun {
	keyring.MockInit()
	runs := make([]replyRun, len(ways))
	for i, w := range ways {
		runs[i] = replyRun{way: w, cases: cases, err: errors.New("did not finish")}
		t.Run(w.name, func(t *testing.T) { runReply(t, &runs[i], sample) })
	}
	return runs
}

func runReply(t *testing.T, r *replyRun, sample int) {
	s, err := New(bg, Options{DataDir: t.TempDir(), SystemLanguage: "en", ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)
	acc, err := s.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "alice@example.com", true))
	require.NoError(t, err)
	require.NoError(t, s.SyncNow(bg, acc.ID))
	r.way.setup(t, s)
	setEvalSettings(t, s, func(st *Settings) {
		st.AutoLabel = false // summaries are the only calls
		if lang := os.Getenv("GODWIT_LIVE_READING_LANGUAGE"); lang != "" {
			st.ReadingLanguage = lang
		}
	})

	r.got = map[string]replyGot{}
	start := time.Now()
	for i, c := range r.cases {
		rows, err := s.db.Query(bg, "SELECT COALESCE(MAX(id), 0) FROM messages")
		require.NoError(t, err)
		last := store.Int(rows.Values[0][0])
		for _, m := range c.Messages {
			_, err := im.User.Append("INBOX", strings.NewReader(m), &imap.AppendOptions{})
			require.NoError(t, err)
		}
		require.NoError(t, s.SyncNow(bg, acc.ID))
		rows, err = s.db.Query(bg, "SELECT id, thread_id FROM messages WHERE id > ? ORDER BY date, id", last)
		require.NoError(t, err)
		require.Len(t, rows.Values, len(c.Messages), "%s: synced into the Inbox", c.ID)
		var ids []int64
		threads := map[int64]bool{}
		for _, v := range rows.Values {
			ids = append(ids, store.Int(v[0]))
			threads[store.Int(v[1])] = true
		}
		require.Len(t, threads, 1, "%s: one conversation", c.ID)
		tg := Target{Kind: "message", ID: ids[len(ids)-1]}
		if len(ids) > 1 {
			tg = Target{Kind: "thread", ID: store.Int(rows.Values[0][1])}
		}

		ctx := WithTimeZone(bg, c.Zone)
		shown, err := replySummary(ctx, s, tg)
		if err != nil {
			r.err = fmt.Errorf("%s: %w", c.ID, err)
			return
		}
		// The reader gets the summary without the lines; the cache keeps them.
		rows, err = s.db.Query(bg, "SELECT output FROM ai_cache ORDER BY rowid DESC LIMIT 1")
		require.NoError(t, err)
		var g replyGot
		rb, _, err := parseReplyBy(store.Str(rows.Values[0][0]))
		if err == nil && rb.Date != "" {
			// The text that went to the model, as runSkill builds it: the newest is last.
			items, cerr := s.mailContext(bg, &gateway.Scope{}, ids, defaultMailBudget)
			require.NoError(t, cerr)
			err = rb.check(items[len(items)-1], c.loc)
		}
		g.text = strings.TrimSpace(shown)
		if err != nil {
			g.why = err.Error()
		} else {
			g.r = rb
		}
		if sample > 0 && i%sample == 0 {
			s.summaryBare = true
			g.base, err = replySummary(ctx, s, tg)
			s.summaryBare = false
			if err != nil {
				r.err = fmt.Errorf("%s: without the lines: %w", c.ID, err)
				return
			}
		}
		r.got[c.ID] = g
	}
	r.took, r.err = time.Since(start), nil
}

// replySummary is summarize's text for tg, as the reader gets it.
func replySummary(ctx context.Context, s *Service, tg Target) (string, error) {
	ch, err := s.RunSkill(ctx, "summarize", tg)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for ev := range ch {
		switch ev.Kind {
		case "text":
			b.WriteString(ev.Text)
		case "error":
			return "", ev.Err
		}
	}
	return b.String(), nil
}

// replyScore counts a run's cases: right (the date, or none, as labelled),
// a wrong date, a date where there should be none, and a date missed;
// dropped is how many gave lines that did not read or check (scored as none).
type replyScore struct {
	n, mailboxes                                 int
	right, wrongDate, falseDate, missed, dropped int
}

// match is the rule a date is right by: the same day, and the label's time
// when it has one.
func (g replyGot) match(want replyBy) bool {
	return g.r.Date == want.Date && (want.Time == "" || g.r.Time == want.Time)
}

func (r replyRun) score() replyScore {
	sc := replyScore{n: len(r.cases)}
	boxes := map[string]bool{}
	for _, c := range r.cases {
		boxes[c.Mailbox] = true
		g := r.got[c.ID]
		if g.why != "" {
			sc.dropped++
		}
		switch {
		case g.match(c.want):
			sc.right++
		case g.r.Date == "":
			sc.missed++
		case c.want.Date == "":
			sc.falseDate++
		default:
			sc.wrongDate++
		}
	}
	sc.mailboxes = len(boxes)
	return sc
}

// wrong is the share of cases with a wrong reply date: a wrong date or one
// where there should be none.
func (sc replyScore) wrong() float64 { return ratio(sc.wrongDate+sc.falseDate, sc.n) }

func (sc replyScore) verdict() string {
	switch {
	case sc.n < replyMinCases || sc.mailboxes < replyMinMailboxes:
		return fmt.Sprintf("no verdict: it needs %d cases from %d mailboxes", replyMinCases, replyMinMailboxes)
	case sc.wrong() < replyPass:
		return fmt.Sprintf("PASS: under %.0f%% wrong reply dates", replyPass*100)
	default:
		return fmt.Sprintf("FAIL: not under %.0f%% wrong reply dates", replyPass*100)
	}
}

// replyReport is the runs as plain text: counts, the verdict and case IDs
// with dates, never mail content.
func replyReport(runs []replyRun, skipped []string) string {
	var b strings.Builder
	for _, s := range skipped {
		fmt.Fprintf(&b, "skipped %s\n", s)
	}
	for _, r := range runs {
		fmt.Fprintf(&b, "\n== %s ==\n", r.way.name)
		if r.err != nil {
			fmt.Fprintf(&b, "run failed, not scored: %v\n", r.err)
			continue
		}
		sc := r.score()
		fmt.Fprintf(&b, "%d cases from %d mailboxes in %v\n", sc.n, sc.mailboxes, r.took.Round(time.Second))
		fmt.Fprintf(&b, "wrong reply dates %d of %d (%.1f%%): %d a wrong date, %d a date where none · %d right · %d missed · %d dropped by the check\n",
			sc.wrongDate+sc.falseDate, sc.n, sc.wrong()*100, sc.wrongDate, sc.falseDate, sc.right, sc.missed, sc.dropped)
		fmt.Fprintln(&b, sc.verdict())
		var wrong, none, missed, dropped []string
		for _, c := range r.cases {
			g := r.got[c.ID]
			got := strings.TrimSpace(g.r.Date + " " + g.r.Time)
			switch {
			case g.match(c.want):
			case g.r.Date == "":
				missed = append(missed, c.ID)
			case c.want.Date == "":
				none = append(none, fmt.Sprintf("%s (%s)", c.ID, got))
			default:
				wrong = append(wrong, fmt.Sprintf("%s (%s, want %s)", c.ID, got, c.Expect))
			}
			if g.why != "" {
				dropped = append(dropped, fmt.Sprintf("%s (%s)", c.ID, g.why))
			}
		}
		for _, l := range []struct {
			name string
			ids  []string
		}{{"wrong date", wrong}, {"date where none", none}, {"missed", missed}, {"dropped", dropped}} {
			if len(l.ids) > 0 {
				fmt.Fprintf(&b, "  %s: %s\n", l.name, strings.Join(l.ids, ", "))
			}
		}
	}
	return b.String()
}

// writeReplySamples writes, for each run, the sampled cases' summaries with
// and without the lines to <dir>/<way>.md, for a person to compare: mail
// content, so dir belongs outside the repository.
func writeReplySamples(dir string, runs []replyRun) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, r := range runs {
		var b strings.Builder
		fmt.Fprintf(&b, "# %s: summaries with the reply-date lines, then without\n", r.way.name)
		for _, c := range r.cases {
			if g := r.got[c.ID]; g.base != "" {
				fmt.Fprintf(&b, "\n## %s\n\n### With (reply date %s)\n\n%s\n\n### Without\n\n%s\n", c.ID, cmp.Or(strings.TrimSpace(g.r.Date+" "+g.r.Time), "none"), g.text, g.base)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, strings.ReplaceAll(r.way.name, "/", "_")+".md"), []byte(b.String()), 0o600); err != nil {
			return err
		}
	}
	return nil
}
