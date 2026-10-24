package core

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

// The label eval: mail with the answer each preset label should get, run
// through the real classifier with a recorder around its judge, scored
// against those answers. testdata/labeleval/README.md has the format.

// evalLangs maps a preset language of the eval to the system language a
// first start seeds the presets in.
var evalLangs = map[string]string{"zh": "zh-CN", "en": "en", "ja": "ja"}

// evalCase is one mail and what each preset label should answer for it:
// yes, no, or either (not scored).
type evalCase struct {
	ID     string            `json:"id"`
	Lang   string            `json:"lang"`
	Note   string            `json:"note"`
	Mail   *evalMail         `json:"mail"`
	Raw    string            `json:"raw"` // a whole RFC 5322 message, instead of Mail
	Expect map[string]string `json:"expect"`
	Set    string            `json:"set"` // runs only under this label set
	msg    []byte
}

type evalMail struct {
	From        string   `json:"from"`
	To          []string `json:"to"`
	Cc          []string `json:"cc"`
	Subject     string   `json:"subject"`
	Date        string   `json:"date"` // RFC 3339; empty is now
	Text        string   `json:"text"`
	HTML        string   `json:"html"`
	Attachments []struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Size int    `json:"size"`
	} `json:"attachments"`
}

// evalSet rewrites some presets' names and descriptions, by preset language
// then slug. A paraphrase keeps every answer, so the cases with no set run
// under it too; any other set runs only the cases that name it.
type evalSet struct {
	name       string
	Paraphrase bool                                                     `json:"paraphrase"`
	Labels     map[string]map[string]struct{ Name, Description string } `json:"labels"`
}

type evalSuite struct {
	cases []evalCase
	sets  []evalSet
}

// loadEvalSuite reads the *.jsonl cases and *.labelset.json sets in dir and
// returns every problem found at once.
func loadEvalSuite(dir string) (evalSuite, error) {
	var suite evalSuite
	var errs []error
	files, err := os.ReadDir(dir)
	if err != nil {
		return suite, err
	}
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".labelset.json") {
			continue
		}
		set := evalSet{name: strings.TrimSuffix(name, ".labelset.json")}
		if err := decodeStrict(filepath.Join(dir, name), &set); err != nil {
			errs = append(errs, err)
			continue
		}
		if len(set.Labels) == 0 {
			errs = append(errs, fmt.Errorf("%s: no preset language: it would never run", name))
		}
		for lang, labels := range set.Labels {
			if evalLangs[lang] == "" {
				errs = append(errs, fmt.Errorf("%s: unknown preset language %q", name, lang))
				continue
			}
			for slug, o := range labels {
				at := fmt.Sprintf("%s: %s: %s", name, lang, slug)
				label := strings.TrimSpace(o.Name)
				switch {
				case !isPreset(slug):
					errs = append(errs, fmt.Errorf("%s: unknown label", at))
				case label == "" || utf8.RuneCountInString(label) > MaxLabelName:
					errs = append(errs, fmt.Errorf("%s: a name of 1 to %d characters", at, MaxLabelName))
				case utf8.RuneCountInString(strings.TrimSpace(o.Description)) > MaxLabelDescription:
					errs = append(errs, fmt.Errorf("%s: a description of at most %d characters", at, MaxLabelDescription))
				}
				for _, p := range presetLabels { // as UpdateLabel refuses it, before any model is called
					other, ok := labels[p.slug]
					if p.slug != slug && (strings.EqualFold(label, strings.TrimSpace(p.name.For(evalLangs[lang]))) ||
						ok && strings.EqualFold(label, strings.TrimSpace(other.Name))) {
						errs = append(errs, fmt.Errorf("%s: name %q is also %s's", at, label, p.slug))
					}
				}
			}
		}
		suite.sets = append(suite.sets, set)
	}
	where := map[string]string{}
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return suite, err
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(nil, 16<<20)
		for n := 1; sc.Scan(); n++ {
			if strings.TrimSpace(sc.Text()) == "" {
				continue
			}
			at := fmt.Sprintf("%s:%d", name, n)
			var c evalCase
			d := json.NewDecoder(strings.NewReader(sc.Text()))
			d.DisallowUnknownFields()
			if err := d.Decode(&c); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", at, err))
				continue
			}
			if c.ID != "" {
				at += " " + c.ID
			}
			switch prev, dup := where[c.ID]; {
			case c.ID == "":
				errs = append(errs, fmt.Errorf("%s: no id", at))
			case dup:
				errs = append(errs, fmt.Errorf("%s: id also used at %s", at, prev))
			default:
				where[c.ID] = at
			}
			if evalLangs[c.Lang] == "" {
				errs = append(errs, fmt.Errorf("%s: lang %q is not zh, en or ja", at, c.Lang))
			}
			if c.Set != "" && !slices.ContainsFunc(suite.sets, func(s evalSet) bool { return s.name == c.Set }) {
				errs = append(errs, fmt.Errorf("%s: unknown label set %q", at, c.Set))
			}
			for _, p := range presetLabels {
				if _, ok := c.Expect[p.slug]; !ok {
					errs = append(errs, fmt.Errorf("%s: no expectation for %s", at, p.slug))
				}
			}
			for slug, e := range c.Expect {
				if !isPreset(slug) {
					errs = append(errs, fmt.Errorf("%s: unknown label %q", at, slug))
				} else if e != "yes" && e != "no" && e != "either" {
					errs = append(errs, fmt.Errorf("%s: %s is %q, not yes, no or either", at, slug, e))
				}
			}
			if c.msg, err = c.message(); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", at, err))
			}
			suite.cases = append(suite.cases, c)
		}
		if err := sc.Err(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	if len(suite.cases) == 0 && len(errs) == 0 {
		errs = append(errs, fmt.Errorf("%s: no cases", dir))
	}
	return suite, errors.Join(errs...)
}

func decodeStrict(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

func isPreset(slug string) bool {
	return slices.ContainsFunc(presetLabels, func(p presetLabel) bool { return p.slug == slug })
}

// message is the case's mail as it arrives: Raw, or Mail built as compose
// builds one (its text part is the HTML's text, so a text-only mail is sent
// as escaped HTML).
func (c evalCase) message() ([]byte, error) {
	switch {
	case (c.Mail == nil) == (c.Raw == ""):
		return nil, errors.New("give one of mail and raw")
	case c.Raw != "":
		if !strings.Contains(c.Raw, "\r\n") {
			return []byte(strings.ReplaceAll(c.Raw, "\n", "\r\n")), nil
		}
		return []byte(c.Raw), nil
	case c.Mail.Text != "" && c.Mail.HTML != "":
		return nil, errors.New("give one of mail.text and mail.html: the text part is the HTML's text")
	}
	m := c.Mail
	date := time.Now()
	if m.Date != "" {
		var err error
		if date, err = time.Parse(time.RFC3339, m.Date); err != nil {
			return nil, err
		}
	}
	body := m.HTML
	if body == "" {
		body = strings.ReplaceAll(html.EscapeString(m.Text), "\n", "<br>")
	}
	o := mail.Outgoing{From: m.From, To: m.To, Cc: m.Cc, Subject: m.Subject, HTML: body}
	for _, a := range m.Attachments {
		o.Attachments = append(o.Attachments, mail.Attachment{Filename: a.Name, ContentType: a.Type, Data: bytes.Repeat([]byte{'x'}, a.Size)})
	}
	raw, _, err := mail.Build(o, date)
	return raw, err
}

// evalWay is one way of judging: setup points a new Service at its judge,
// through the settings as a user would (tests set labelJudge instead).
type evalWay struct {
	name  string
	gen   bool // a generation model answers, 0 or 1: scored at one threshold
	setup func(t *testing.T, s *Service)
}

// evalRun is the cases judged one way, under one preset language and label set.
type evalRun struct {
	way        evalWay
	preset     string
	set        string // "" is the presets as seeded
	cases      []evalCase
	got        map[string]evalGot // by case ID
	calls      int
	took       time.Duration
	refused    int // labels refused
	unanswered int // mails with no valid answer
	either     int
	threshold  float64 // the setting the classifier applied
	err        error   // the run stopped: not scored
}

// evalGot is the judge's answer for a case; a refused label has no p, and an
// unanswered mail none at all.
type evalGot struct {
	p          map[string]float64
	refused    map[string]bool
	unanswered bool
}

// yes is the classifier's rule: at or over the threshold.
func (g evalGot) yes(slug string, threshold float64) bool {
	p, ok := g.p[slug]
	return ok && p >= threshold
}

// evalRecorder passes the classifier's judge calls through, keeping the last answer.
type evalRecorder struct {
	j     ai.Judge
	ans   []ai.Answer
	err   error
	calls int
	took  time.Duration
}

func (r *evalRecorder) Ask(ctx context.Context, state string, qs []ai.Question) ([]ai.Answer, error) {
	start := time.Now()
	r.ans, r.err = r.j.Ask(ctx, state, qs)
	r.calls++
	r.took += time.Since(start)
	return r.ans, r.err
}

// runLabelEval judges the suite each way, under each preset language, with
// the presets as seeded and under each label set that has that language.
// Runs go one at a time, each on a new Service: they share the test keychain.
func runLabelEval(t *testing.T, suite evalSuite, ways []evalWay) []evalRun {
	keyring.MockInit()
	type combo struct {
		run evalRun
		set *evalSet
	}
	var combos []combo
	for _, w := range ways {
		for _, preset := range []string{"zh", "en", "ja"} {
			sets := []*evalSet{nil}
			for i := range suite.sets {
				if suite.sets[i].Labels[preset] != nil {
					sets = append(sets, &suite.sets[i])
				}
			}
			for _, set := range sets {
				r := evalRun{way: w, preset: preset}
				if set != nil {
					r.set = set.name
				}
				for _, c := range suite.cases {
					if c.Set == r.set || c.Set == "" && set != nil && set.Paraphrase {
						r.cases = append(r.cases, c)
					}
				}
				if len(r.cases) > 0 {
					combos = append(combos, combo{r, set})
				}
			}
		}
	}
	runs := make([]evalRun, len(combos))
	for i, c := range combos {
		runs[i] = c.run
		runs[i].err = errors.New("did not finish")
		t.Run(strings.Join([]string{c.run.way.name, c.run.preset, cmp.Or(c.run.set, "presets")}, "_"), func(t *testing.T) {
			runEval(t, &runs[i], c.set)
		})
	}
	return runs
}

// runEval judges r's cases one by one: each is delivered to a test IMAP
// server, synced into the Inbox and classified, as new mail is.
func runEval(t *testing.T, r *evalRun, set *evalSet) {
	s, err := New(bg, Options{DataDir: t.TempDir(), SystemLanguage: evalLangs[r.preset], ImageClient: loopbackImageClient()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	smtpAddr, _ := testmail.StartSMTP(t)
	im := testmail.StartIMAP(t)
	acc, err := s.AddAccount(bg, internalInput(t, im.Addr, smtpAddr, "alice@example.com", true))
	require.NoError(t, err)
	labels, err := s.ListLabels(bg)
	require.NoError(t, err)
	slugs := map[int64]string{}
	for _, l := range labels {
		slugs[l.ID] = l.Slug
		if set == nil {
			continue
		}
		if o, ok := set.Labels[r.preset][l.Slug]; ok { // as the user edits it: the slug stays
			_, err := s.UpdateLabel(bg, l.ID, LabelInput{Name: o.Name, Description: o.Description, Color: l.Color, Icon: l.Icon})
			require.NoError(t, err, "%s: %s", r.set, l.Slug)
		}
	}
	r.way.setup(t, s)
	st, err := s.GetSettings(bg)
	require.NoError(t, err)
	r.threshold = st.JudgeThreshold
	rec := &evalRecorder{}
	pick := s.labelJudge
	s.labelJudge = func(ctx context.Context) (ai.Judge, error) {
		j, err := pick(ctx)
		if err != nil {
			return nil, err
		}
		rec.j = j
		return rec, nil
	}

	r.got = map[string]evalGot{}
	for _, c := range r.cases {
		for _, e := range c.Expect {
			if e == "either" {
				r.either++
			}
		}
		_, err := im.User.Append("INBOX", bytes.NewReader(c.msg), &imap.AppendOptions{})
		require.NoError(t, err)
		require.NoError(t, s.SyncNow(bg, acc.ID))
		rows, err := s.db.Query(bg, "SELECT id FROM messages WHERE labeled_at IS NULL")
		require.NoError(t, err)
		require.Len(t, rows.Values, 1, "%s: synced into the Inbox", c.ID)
		id := store.Int(rows.Values[0][0])
		calls := rec.calls
		n, err := s.classifyBatch(bg)
		if err != nil {
			r.err = fmt.Errorf("%s: %w", c.ID, err)
			return
		}
		require.Equal(t, 1, n, c.ID)
		require.Equal(t, calls+1, rec.calls, "%s: one judge call", c.ID)
		g := evalGot{p: map[string]float64{}, refused: map[string]bool{}, unanswered: rec.err != nil}
		if g.unanswered { // the classifier writes nothing, whatever answers came with the error
			r.unanswered++
			rec.ans = nil
		}
		for _, a := range rec.ans {
			switch {
			case a.Type == ai.Refusal:
				r.refused++
				g.refused[a.Name] = true
			case a.Type == ai.Predicate && a.Probability != nil:
				g.p[a.Name] = *a.Probability
			}
		}
		r.got[c.ID] = g

		// What the recorder saw is what the classifier acted on.
		applied, err := s.MessageLabels(bg, []int64{id})
		require.NoError(t, err)
		var wrote, want []string
		for _, l := range applied[id] {
			if l.Auto {
				wrote = append(wrote, slugs[l.LabelID])
			}
		}
		for _, l := range labels {
			if g.yes(l.Slug, r.threshold) {
				want = append(want, l.Slug)
			}
		}
		slices.Sort(wrote)
		slices.Sort(want)
		if !slices.Equal(wrote, want) {
			t.Errorf("%s: the classifier labeled %v, the recorded answers give %v", c.ID, wrote, want)
		}
	}
	r.calls, r.took, r.err = rec.calls, rec.took, nil
}

type evalTally struct{ tp, fp, fn, tn int }

func (a *evalTally) add(b evalTally) {
	a.tp, a.fp, a.fn, a.tn = a.tp+b.tp, a.fp+b.fp, a.fn+b.fn, a.tn+b.tn
}

// precision and recall are 0 with nothing to divide by.
func (a evalTally) precision() float64 { return ratio(a.tp, a.tp+a.fp) }
func (a evalTally) recall() float64    { return ratio(a.tp, a.tp+a.fn) }
func (a evalTally) f1() float64        { return ratio(2*a.tp, 2*a.tp+a.fp+a.fn) }

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// evalMinCI is the fewest scored cases a line gets an interval at.
const evalMinCI = 100

// evalLine scores a label ("" all of them, micro-averaged) at a threshold,
// over the cases in a mail language ("" every one).
type evalLine struct {
	lang      string
	threshold float64
	label     string
	evalTally
	n  int       // cases scored
	ci []float64 // 95% intervals: precision low, high, recall low, high; nil under evalMinCI cases
}

// thresholds are the cutoffs Settings allows, by 0.05; a generation model's
// answers are 0 or 1, so it has one.
func (r evalRun) thresholds() []float64 {
	if r.way.gen {
		return []float64{0.5}
	}
	var out []float64
	for i := math.Round(MinJudgeThreshold * 20); i <= math.Round(MaxJudgeThreshold*20); i++ {
		out = append(out, i/20)
	}
	return out
}

// score is every label and all of them at each threshold, then all of them
// by mail language.
func (r evalRun) score() []evalLine {
	var lines []evalLine
	for _, thr := range r.thresholds() {
		lines = append(lines, r.line("", thr, ""))
		for _, p := range presetLabels {
			lines = append(lines, r.line("", thr, p.slug))
		}
	}
	for _, lang := range []string{"zh", "en", "ja"} {
		if slices.ContainsFunc(r.cases, func(c evalCase) bool { return c.Lang == lang }) {
			for _, thr := range r.thresholds() {
				lines = append(lines, r.line(lang, thr, ""))
			}
		}
	}
	return lines
}

func (r evalRun) line(lang string, thr float64, label string) evalLine {
	per := r.tallies(lang, thr, label)
	l := evalLine{lang: lang, threshold: thr, label: label, n: len(per)}
	for _, c := range per {
		l.add(c)
	}
	if len(per) >= evalMinCI {
		l.ci = bootstrap(per)
	}
	return l
}

// tallies is each case's score, for the cases in the language with a label
// to score (either is not).
func (r evalRun) tallies(lang string, thr float64, label string) []evalTally {
	var out []evalTally
	for _, c := range r.cases {
		if lang != "" && c.Lang != lang {
			continue
		}
		var t evalTally
		scored := false
		for _, p := range presetLabels {
			e := c.Expect[p.slug]
			if label != "" && p.slug != label || e == "either" {
				continue
			}
			scored = true
			switch yes := r.got[c.ID].yes(p.slug, thr); {
			case e == "yes" && yes:
				t.tp++
			case e == "yes":
				t.fn++
			case yes:
				t.fp++
			default:
				t.tn++
			}
		}
		if scored {
			out = append(out, t)
		}
	}
	return out
}

// bootstrap resamples the cases (a case's labels together) for 95%
// percentile intervals of precision and recall, the same each run.
func bootstrap(per []evalTally) []float64 {
	const n = 1000
	rng := rand.New(rand.NewPCG(1, 2))
	ps, rs := make([]float64, n), make([]float64, n)
	for i := range n {
		var t evalTally
		for range per {
			t.add(per[rng.IntN(len(per))])
		}
		ps[i], rs[i] = t.precision(), t.recall()
	}
	slices.Sort(ps)
	slices.Sort(rs)
	lo, hi := n*25/1000, n*975/1000
	return []float64{ps[lo], ps[hi], rs[lo], rs[hi]}
}

// evalReport is the runs as plain-text tables: case IDs, labels and
// probabilities, never mail content.
func evalReport(runs []evalRun, skipped []string) string {
	var b strings.Builder
	for _, s := range skipped {
		fmt.Fprintf(&b, "skipped %s\n", s)
	}
	for _, r := range runs {
		fmt.Fprintf(&b, "\n== %s · presets %s · %s ==\n", r.way.name, r.preset, cmp.Or(r.set, "as seeded"))
		if r.err != nil {
			fmt.Fprintf(&b, "run failed, not scored: %v\n", r.err)
			continue
		}
		fmt.Fprintf(&b, "%d cases · %d calls in %v · %d labels refused · %d mails unanswered · %d either (not scored)\n",
			len(r.cases), r.calls, r.took.Round(time.Millisecond), r.refused, r.unanswered, r.either)
		if r.way.gen {
			b.WriteString("a generation model answers 0 or 1: one point, at 0.50\n")
		}
		w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "thr\tlang\tlabel\tTP\tFP\tFN\tTN\tprec\trecall\tF1\t95% CI prec\t95% CI recall")
		for _, l := range r.score() {
			ci := fmt.Sprintf("too few cases (%d < %d)\t", l.n, evalMinCI)
			if l.ci != nil {
				ci = fmt.Sprintf("%.2f–%.2f\t%.2f–%.2f", l.ci[0], l.ci[1], l.ci[2], l.ci[3])
			}
			fmt.Fprintf(w, "%.2f\t%s\t%s\t%d\t%d\t%d\t%d\t%.2f\t%.2f\t%.2f\t%s\n", l.threshold, cmp.Or(l.lang, "all"), cmp.Or(l.label, "all"),
				l.tp, l.fp, l.fn, l.tn, l.precision(), l.recall(), l.f1(), ci)
		}
		w.Flush()
		at := r.threshold
		if r.way.gen {
			at = 0.5
		}
		fmt.Fprintf(&b, "wrong at %.2f:\n", at)
		for _, p := range presetLabels {
			var fp, fn []string
			for _, c := range r.cases {
				e, g := c.Expect[p.slug], r.got[c.ID]
				answer := "no answer"
				if v, ok := g.p[p.slug]; ok {
					answer = fmt.Sprintf("%.2f", v)
				} else if g.refused[p.slug] {
					answer = "refused"
				} else if g.unanswered {
					answer = "unanswered"
				}
				switch yes := g.yes(p.slug, at); {
				case e == "no" && yes:
					fp = append(fp, c.ID+" "+answer)
				case e == "yes" && !yes:
					fn = append(fn, c.ID+" "+answer)
				}
			}
			if len(fp)+len(fn) > 0 {
				fmt.Fprintf(&b, "  %s: false yes [%s] missed [%s]\n", p.slug, strings.Join(fp, ", "), strings.Join(fn, ", "))
			}
		}
	}
	return b.String()
}
