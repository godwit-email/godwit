package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// The classifier puts labels on Inbox mail: labeled_at NULL (new mail, while
// Settings.AutoLabel is on) or 0 (a rescan's target, always). One judge call
// per message, a yes/no question per label the user has not set by hand.

const (
	labelBatch     = 20
	labelBodyRunes = 4000
	inboxCond      = "m.folder_id IN (SELECT id FROM folders WHERE role = 'inbox')"
)

var errNoLabelModel = errors.New("no model to classify mail with")

// defaultLabelJudge is the judge the settings name; none configured is errNoLabelModel.
func (s *Service) defaultLabelJudge(ctx context.Context) (ai.Judge, error) {
	j, _, _, err := s.resolveJudge(ctx)
	if errors.Is(err, ErrAINotConfigured) {
		return nil, errNoLabelModel
	}
	return j, err
}

// startLabeler runs the classifier until Close.
func (s *Service) startLabeler() {
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	go func() {
		defer end()
		s.runLabeler(ctx)
	}()
}

// wakeLabeler asks the classifier to look for work now; a signal sent while it
// is busy is kept.
func (s *Service) wakeLabeler() {
	select {
	case s.labelWake <- struct{}{}:
	default:
	}
}

// runLabeler classifies batch after batch. Idle, it waits hookPoll or a wake-up;
// after a failure it waits hookBackoff, which a wake-up does not cut short.
func (s *Service) runLabeler(ctx context.Context) {
	failures := 0
	for {
		n, err := s.classifyBatch(ctx)
		ok := ctx.Err() == nil
		switch {
		case err == nil && n > 0:
			failures = 0
		case err == nil || errors.Is(err, errNoLabelModel):
			failures = 0
			ok = s.hookWait(ctx, hookPoll, s.labelWake)
		default:
			failures++
			ok = s.hookWait(ctx, hookBackoff(failures), nil)
		}
		if !ok {
			return
		}
	}
}

// classifyBatch classifies up to labelBatch waiting messages, rescan targets
// first, and returns how many it finished.
func (s *Service) classifyBatch(ctx context.Context) (int, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return 0, err
	}
	// New mail waits for the presets (a start with no language known seeds
	// none yet), to be classified against them; a rescan, asked for, runs
	// against the labels there are.
	seeded, err := s.db.Query(ctx, "SELECT 1 FROM settings WHERE key = 'labels_seeded'")
	if err != nil {
		return 0, err
	}
	newMail := st.AutoLabel && len(seeded.Values) > 0
	rows, err := s.db.Query(ctx, `SELECT m.id, m.labeled_at FROM messages m
		WHERE (m.labeled_at = 0 OR (? AND m.labeled_at IS NULL)) AND `+inboxCond+`
		ORDER BY m.labeled_at IS NULL, m.date DESC, m.id DESC LIMIT ?`, b2i(newMail), labelBatch)
	if err != nil {
		return 0, err
	}
	var judge ai.Judge
	done := 0
	defer func() {
		if done > 0 {
			s.bus.Publish(events.Event{Type: events.LabelsChanged})
		}
	}()
	for _, r := range rows.Values {
		id, at := store.Int(r[0]), r[1]
		if at == nil { // new mail: only while auto-labeling is on, which may change mid-batch
			if st, err := s.loadSettings(ctx); err != nil || !st.AutoLabel {
				return done, err
			}
		}
		labels, qs, newest, err := s.labelQuestions(ctx, id)
		if err != nil {
			return done, err
		}
		yes := map[string]bool{}
		answered := true
		if len(qs) > 0 {
			if judge == nil {
				if judge, err = s.labelJudge(ctx); err != nil {
					return done, err
				}
			}
			m, err := s.mailForModel(ctx, id)
			if errors.Is(err, errMailNotFound) { // deleted since the query
				done++
				continue
			}
			if err != nil {
				return done, err
			}
			answers, err := judge.Ask(ctx, labelState(m), qs)
			if err != nil {
				if !unanswerable(err) {
					return done, err
				}
				answered = false // no answer to go by: the labels it has stay
			}
			for _, a := range answers {
				yes[a.Name] = a.Type == ai.Predicate && a.Probability != nil && *a.Probability >= st.JudgeThreshold // a refusal is no
			}
		}
		if _, err := s.db.Transaction(ctx, labelWrites(id, at, newest, labels, yes, answered)); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// unanswerable reports an error that would repeat for this message alone: no
// valid answer, or the service or judge plugin rejecting the request (a
// content filter). The message is done with its labels as they are, so one
// message does not stop the rest. A plugin judge that is unavailable or
// failed (ai.ErrJudgeUnavailable, ai.ErrJudgePlugin) stops the round.
func unanswerable(err error) bool {
	var he *ai.HTTPError
	return errors.Is(err, ai.ErrJudgeAnswer) || errors.Is(err, errJudgeInput) || errors.As(err, &he) && he.Status == 400
}

// labelQuestions returns the labels to ask about for a message, those it has
// no hand-set row for, their questions (named by slug), and the newest label's
// id then, so a label added while the model answers is noticed.
func (s *Service) labelQuestions(ctx context.Context, id int64) ([]Label, []ai.Question, int64, error) {
	top, err := s.db.Query(ctx, "SELECT COALESCE(MAX(id), 0) FROM labels")
	if err != nil {
		return nil, nil, 0, err
	}
	newest := store.Int(top.Values[0][0])
	rows, err := s.db.Query(ctx, `SELECT id, slug, name, description FROM labels l
		WHERE NOT EXISTS (SELECT 1 FROM message_labels ml WHERE ml.message_id = ? AND ml.label_id = l.id AND ml.source = 'manual')
		ORDER BY position, id`, id)
	if err != nil {
		return nil, nil, 0, err
	}
	labels := make([]Label, 0, len(rows.Values))
	qs := make([]ai.Question, 0, len(rows.Values))
	for _, r := range rows.Values {
		l := Label{ID: store.Int(r[0]), Slug: store.Str(r[1]), Name: store.Str(r[2]), Description: store.Str(r[3])}
		labels = append(labels, l)
		qs = append(qs, ai.Question{Name: l.Slug, Type: ai.Predicate,
			Instructions: strings.TrimSpace(fmt.Sprintf("Answer yes if this email belongs to the user's label %q. %s", l.Name, l.Description))})
	}
	return labels, qs, newest, nil
}

// labelState is the message for the judge: headers and the start of its text,
// as untrusted mail.
func labelState(m Message) string {
	body := m.Text
	if strings.TrimSpace(body) == "" {
		body = gomail.HTMLToText(m.HTML)
	}
	suspect := ai.InjectionQuote(mailHeader(m.MessageSummary)+"\n"+body) != "" // before cuts, as in mailContext
	body = cleanMailURLs(body, true)
	if r := []rune(body); len(r) > labelBodyRunes {
		body = string(r[:labelBodyRunes])
	}
	text := mailHeader(m.MessageSummary) + "\n" + body
	return ai.Untrusted(fmt.Sprintf("message:%d", m.ID), text, suspect)
}

// labelWrites applies the answers to a message read with labeled_at at. Every
// statement checks the message is still that one in the Inbox (not stopped,
// moved or rescanned meanwhile), that no label was added and no label asked
// about was edited meanwhile (an answer to its old name or description); otherwise nothing is
// written and the message is asked again. Hand-set rows are left alone; with
// answered false no label is touched.
func labelWrites(id int64, at any, newest int64, labels []Label, yes map[string]bool, answered bool) []sqlite.Statement {
	guard := "EXISTS (SELECT 1 FROM messages m WHERE m.id = ? AND m.labeled_at IS ? AND " + inboxCond + ")" +
		" AND NOT EXISTS (SELECT 1 FROM labels WHERE id > ?)" // a label added meanwhile was never asked about
	guardArgs := []any{id, at, newest}
	for _, l := range labels { // a deleted label is no reason to ask again
		guard += " AND NOT EXISTS (SELECT 1 FROM labels WHERE id = ? AND (name IS NOT ? OR description IS NOT ?))"
		guardArgs = append(guardArgs, l.ID, l.Name, l.Description)
	}
	var stmts []sqlite.Statement
	if answered { // unanswered: only the guarded labeled_at update, so an edited label is still asked again
		for _, l := range labels {
			if yes[l.Slug] {
				stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO message_labels(message_id, label_id, source, active)
					SELECT ?, ?, 'auto', 1 WHERE ` + guard + ` AND EXISTS (SELECT 1 FROM labels WHERE id = ?)
					ON CONFLICT(message_id, label_id) DO UPDATE SET active = 1 WHERE source = 'auto'`,
					Args: append(append([]any{id, l.ID}, guardArgs...), l.ID)})
			} else {
				stmts = append(stmts, sqlite.Statement{SQL: `DELETE FROM message_labels
					WHERE message_id = ? AND label_id = ? AND source = 'auto' AND ` + guard, Args: append([]any{id, l.ID}, guardArgs...)})
			}
		}
	}
	return append(stmts, sqlite.Statement{SQL: `UPDATE messages SET labeled_at = unixepoch() WHERE id = ? AND ` + guard,
		Args: append([]any{id}, guardArgs...)})
}

// RescanLabels makes the Inbox mail of the last days (0: all of it) the
// classifier's targets again: all of it, or only mail with no label on. Model
// labels are replaced only once the judge answers for that message, so a judge
// failing or a stop midway loses none; hand-set labels stay. It returns how many.
func (s *Service) RescanLabels(ctx context.Context, days int, all bool) (int, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	if _, err := s.labelJudge(ctx); err != nil { // nothing would classify the targets
		if errors.Is(err, errNoLabelModel) {
			err = ErrAINotConfigured
		}
		return 0, err
	}
	since := int64(0)
	if days > 0 {
		since = time.Now().AddDate(0, 0, -days).Unix()
	}
	target := `m.date >= ? AND ` + inboxCond
	if !all {
		target += " AND NOT EXISTS (SELECT 1 FROM message_labels ml WHERE ml.message_id = m.id AND ml.active = 1)"
	}
	res, err := s.db.Execute(ctx, "UPDATE messages SET labeled_at = 0 WHERE id IN (SELECT m.id FROM messages m WHERE "+target+")", since)
	if err != nil {
		return 0, err
	}
	s.bus.Publish(events.Event{Type: events.LabelsChanged})
	s.wakeLabeler()
	return int(res.Changes), nil
}

// LabelModelReady reports whether a model can classify mail.
func (s *Service) LabelModelReady(ctx context.Context) bool {
	_, err := s.labelJudge(ctx)
	return err == nil
}

// StopRescan gives up on the rescan targets not classified yet.
func (s *Service) StopRescan(ctx context.Context) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	_, err = s.db.Execute(ctx, "UPDATE messages SET labeled_at = unixepoch() WHERE labeled_at = 0")
	return err
}

// PendingLabels counts the rescan targets not classified yet.
func (s *Service) PendingLabels(ctx context.Context) (int, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT count(*) FROM messages m WHERE m.labeled_at = 0 AND "+inboxCond)
	if err != nil {
		return 0, err
	}
	return int(store.Int(rows.Values[0][0])), nil
}
