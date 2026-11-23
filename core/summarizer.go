package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
)

// 收到就总结 (Settings.SummaryMode SummaryArrive) summarizes a conversation
// whose 回复状态 is 未回复 once its newest message arrives, through the
// request opening it makes, so the reader then reads the cache and the
// summary's reply date sorts 待处理. Once per newest message: summary_checks
// records each, and a message is never asked about again, whatever the
// answer. Only mail that arrives after the level was chosen, and only while
// the model service is the one it was chosen with; older mail when the user
// asks (SummarizeUnreplied).

// 邮件总结's levels (Settings.SummaryMode).
const (
	SummaryOff    = "off"
	SummaryOpen   = "open"
	SummaryArrive = "arrive"
)

// SummaryOnArrival lets SummaryArrive be chosen. Off until the reply-date
// evaluation (#845) passes on real mail; GODWIT_SUMMARY_ON_ARRIVAL=1 turns
// it on for a maintainer trying it.
var SummaryOnArrival = os.Getenv("GODWIT_SUMMARY_ON_ARRIVAL") == "1"

// summaryRank orders the levels by what they read; -1 for none of them.
func summaryRank(level string) int {
	switch level {
	case "", SummaryOff:
		return 0
	case SummaryOpen:
		return 1
	case SummaryArrive:
		if SummaryOnArrival {
			return 2
		}
	}
	return -1
}

// summary_checks outcomes.
const (
	summaryQueued  = "queued"  // received while 收到就总结 is chosen, or asked for by SummarizeUnreplied; not done yet
	summaryDone    = "done"    // summarized
	summaryBlocked = "blocked" // 脱敏 blocked it (拦截)
	summaryFailed  = "failed"  // the service refused it or gave no usable answer
)

const summaryBatch = 10

// summaryAttempts is how many times a message is tried when it fails in a
// way that may pass; then it is failed.
const summaryAttempts = 3

// summaryCursor is the summarizer's event_cursors row: queueArrivals has
// read events_log up to it, so cleanEvents keeps what comes after.
const summaryCursor = "summary"

// summaryPoll is how long the idle summarizer waits for a wake-up: every
// sync round, and every change that can give it work, wakes it, and a look
// reads 回复状态 over 30 days of mail, so it does not poll often.
const summaryPoll = time.Hour

// summaryArriveKey holds what 收到就总结 was chosen with (arrivalChoice), a
// settings row beside Settings so a client's stale Settings cannot move it.
const summaryArriveKey = "summary.arrive"

// arrivalChoice is what 收到就总结 was chosen with: mail events_log records
// as received after seq After is new (queueArrivals queues it), and Service
// (by modelService; Name for the user) is the model service it may go to, at
// Host, the endpoint's host: the user agreed to mail going there (ADR 0003),
// so another host needs their say again, a new name does not.
type arrivalChoice struct {
	After    int64
	Service  string
	Name     string
	Host     string
	OnDevice bool
}

// SummaryArrival is 收到就总结's state for the settings page: Service is the
// model service it was chosen with ("" never chosen), Current the one
// summaries use now with its host, "name (host)" ("" none). Paused: they differ, and nothing is summarized
// on arrival until ConfirmSummaryService.
type SummaryArrival struct {
	Service, Current string
	Paused           bool
}

// summaryModel is the model a summary runs on under settings st, as RunSkill resolves it.
func (s *Service) summaryModel(ctx context.Context, st Settings) (ai.ResolvedModel, string, error) {
	sk, err := s.skill(ctx, "summarize")
	if err != nil {
		return ai.ResolvedModel{}, "", err
	}
	if sk.Model != "" {
		return s.resolveRef(ctx, sk.Model)
	}
	return s.resolveTaskIn(ctx, st, sk.Tier == "small")
}

// currentChoice is an arrivalChoice for the summary model under settings
// st, after seq after.
func (s *Service) currentChoice(ctx context.Context, st Settings, after int64) (arrivalChoice, error) {
	m, ref, err := s.summaryModel(ctx, st)
	if err != nil {
		return arrivalChoice{}, err
	}
	service, _, _ := strings.Cut(ref, "/")
	if i := strings.LastIndex(ref, "/"); service == "plugin" && i > 0 {
		service = ref[:i] // plugin/<name>
	}
	c := arrivalChoice{After: after, Service: service, Name: strings.TrimPrefix(service, "plugin/"), OnDevice: m.OnDevice}
	if u, err := url.Parse(m.BaseURL); err == nil {
		c.Host = strings.ToLower(u.Host)
	}
	if m.ServiceID != 0 {
		c.Service = strconv.FormatInt(m.ServiceID, 10) // a renamed slug is the same service
		rows, err := s.db.Query(ctx, "SELECT name FROM model_services WHERE id = ?", m.ServiceID)
		if err != nil {
			return c, err
		}
		if len(rows.Values) > 0 {
			c.Name = store.Str(rows.Values[0][0])
		}
	}
	return c, nil
}

func (c arrivalChoice) same(o arrivalChoice) bool {
	return c.Service == o.Service && c.Host == o.Host && c.OnDevice == o.OnDevice
}

// arrivalChoiceNow is a choice of 收到就总结 made now with settings st, to
// store with them.
func (s *Service) arrivalChoiceNow(ctx context.Context, st Settings) (sqlite.Statement, error) {
	rows, err := s.db.Query(ctx, "SELECT COALESCE(MAX(seq), 0) FROM events_log")
	if err != nil {
		return sqlite.Statement{}, err
	}
	c, err := s.currentChoice(ctx, st, store.Int(rows.Values[0][0]))
	if err != nil {
		return sqlite.Statement{}, err
	}
	return choiceWrite(c)
}

func choiceWrite(c arrivalChoice) (sqlite.Statement, error) {
	b, err := json.Marshal(c)
	return sqlite.Statement{SQL: `INSERT INTO settings(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, Args: []any{summaryArriveKey, string(b)}}, err
}

// storedChoice is the choice of 收到就总结 last made; ok false if none was.
func (s *Service) storedChoice(ctx context.Context) (c arrivalChoice, ok bool, err error) {
	rows, err := s.db.Query(ctx, "SELECT value FROM settings WHERE key = ?", summaryArriveKey)
	if err != nil || len(rows.Values) == 0 {
		return c, false, err
	}
	return c, true, json.Unmarshal([]byte(store.Str(rows.Values[0][0])), &c)
}

// arrivalOn is the choice of 收到就总结 in effect: chosen now, with the
// model service it was chosen with. ok false: nothing is summarized on arrival.
func (s *Service) arrivalOn(ctx context.Context) (arrivalChoice, bool, error) {
	st, err := s.loadSettings(ctx)
	if err != nil || st.SummaryMode != SummaryArrive || !SummaryOnArrival {
		return arrivalChoice{}, false, err
	}
	c, ok, err := s.storedChoice(ctx)
	if err != nil || !ok {
		return c, false, err
	}
	now, err := s.currentChoice(ctx, st, c.After)
	if noModel(err) {
		return c, false, nil
	}
	return c, err == nil && now.same(c), err
}

// SummaryArrivalState is 收到就总结's state for the settings page.
func (s *Service) SummaryArrivalState(ctx context.Context) (SummaryArrival, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return SummaryArrival{}, err
	}
	defer end()
	c, chosen, err := s.storedChoice(ctx)
	if err != nil {
		return SummaryArrival{}, err
	}
	var out SummaryArrival
	if chosen {
		out.Service = c.Name
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return SummaryArrival{}, err
	}
	now, err := s.currentChoice(ctx, st, c.After)
	if noModel(err) {
		return out, nil
	}
	out.Current, out.Paused = now.Name, chosen && !now.same(c)
	if now.Host != "" { // what the user agrees to names where mail goes (ADR 0003), not just a name they can edit
		out.Current += " (" + now.Host + ")"
	}
	return out, err
}

// noModel is whether err says no model is set for the task, or the one set is gone.
func noModel(err error) bool {
	return errors.Is(err, ErrAINotConfigured) || errors.Is(err, ErrModelServiceNotFound) || errors.Is(err, ErrModelNotFound)
}

// ConfirmSummaryService lets 收到就总结 go on with the model service
// summaries use now, after it paused for a change of service.
func (s *Service) ConfirmSummaryService(ctx context.Context) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	c, ok, err := s.storedChoice(ctx)
	if err != nil || !ok {
		return err
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	now, err := s.currentChoice(ctx, st, c.After)
	if err != nil {
		return err
	}
	stmt, err := choiceWrite(now)
	if err != nil {
		return err
	}
	if err := s.exec(ctx, stmt); err != nil {
		return err
	}
	s.wakeSummarizer()
	return nil
}

// unreplied is the newest message of each conversation whose 回复状态 is 未回复 now.
func (s *Service) unreplied(ctx context.Context) ([]int64, error) {
	statuses, err := s.replyStatuses(ctx, s.clk().Now().Unix())
	var ids []int64
	for _, st := range statuses {
		if st.Kind == ReplyStatusUnreplied {
			ids = append(ids, st.Newest)
		}
	}
	return ids, err
}

// unrepliedNewest is unreplied without the messages summary_checks has.
func (s *Service) unrepliedNewest(ctx context.Context) ([]int64, error) {
	ids, err := s.unreplied(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, "SELECT value FROM json_each(?) WHERE value NOT IN (SELECT message_id FROM summary_checks)", intList(ids))
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(rows.Values))
	for i, r := range rows.Values {
		out[i] = store.Int(r[0])
	}
	return out, nil
}

// CountUnrepliedToSummarize counts the conversations SummarizeUnreplied
// would send: those whose 回复状态 is 未回复 (so active in the last 30
// days) and whose newest message was never summarized on arrival.
func (s *Service) CountUnrepliedToSummarize(ctx context.Context) (int, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	ids, err := s.unrepliedNewest(ctx)
	return len(ids), err
}

// SummarizeUnreplied queues the conversations CountUnrepliedToSummarize
// counts for 收到就总结, which summarizes them as it does new mail, and
// returns how many. It needs 收到就总结 chosen.
func (s *Service) SummarizeUnreplied(ctx context.Context) (int, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	st, err := s.loadSettings(ctx)
	if err != nil {
		return 0, err
	}
	if st.SummaryMode != SummaryArrive {
		return 0, errors.New("summarize on arrival is off")
	}
	ids, err := s.unrepliedNewest(ctx)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Execute(ctx, `INSERT INTO summary_checks(message_id, at, outcome) SELECT value, ?, ? FROM json_each(?) WHERE true
		ON CONFLICT(message_id) DO NOTHING`, s.clk().Now().Unix(), summaryQueued, intList(ids)); err != nil {
		return 0, err
	}
	s.wakeSummarizer()
	return len(ids), nil
}

// startSummarizer runs 收到就总结 until Close.
func (s *Service) startSummarizer() {
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	go func() {
		defer end()
		s.runSummarizer(ctx)
	}()
}

// wakeSummarizer asks 收到就总结 to look for work now; a signal sent while
// it is busy is kept.
func (s *Service) wakeSummarizer() {
	select {
	case s.summaryWake <- struct{}{}:
	default:
	}
}

// runSummarizer summarizes batch after batch, as runLabeler classifies.
func (s *Service) runSummarizer(ctx context.Context) {
	failures := 0
	for {
		n, err := s.summarizeBatch(ctx)
		ok := ctx.Err() == nil
		switch {
		case err == nil && n > 0:
			failures = 0
		case err == nil:
			failures = 0
			ok = s.hookWait(ctx, summaryPoll, s.summaryWake)
		default:
			failures++
			ok = s.hookWait(ctx, hookBackoff(failures), nil)
		}
		if !ok {
			return
		}
	}
}

// summaryTarget is a conversation to summarize, as the reader's
// summaryTarget asks for it, and the newest message it is for.
type summaryTarget struct {
	newest int64
	target Target
}

// queueArrivals queues the mail received since 收到就总结 was chosen, while
// it is (paused or not), and moves summaryCursor past what it read: the
// queue outlives events_log, which cleanEvents empties after a week.
func (s *Service) queueArrivals(ctx context.Context) error {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	c, chosen, err := s.storedChoice(ctx)
	if err != nil {
		return err
	}
	var stmts []sqlite.Statement
	if chosen && st.SummaryMode == SummaryArrive && SummaryOnArrival {
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO summary_checks(message_id, at, outcome)
			SELECT e.message_id, ?, ? FROM events_log e JOIN messages m ON m.id = e.message_id WHERE e.type = 'message.received'
			AND e.seq > max(?, COALESCE((SELECT seq FROM event_cursors WHERE consumer = ?), 0)) ON CONFLICT(message_id) DO NOTHING`,
			Args: []any{s.clk().Now().Unix(), summaryQueued, c.After, summaryCursor}})
	}
	stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO event_cursors(consumer, seq) SELECT ?, COALESCE(MAX(seq), 0) FROM events_log
		WHERE true ON CONFLICT(consumer) DO UPDATE SET seq = excluded.seq`, Args: []any{summaryCursor}})
	_, err = s.db.Transaction(ctx, stmts)
	return err
}

// summaryTargets are up to summaryBatch conversations to summarize: 未回复,
// with a newest message queued, the least tried first.
func (s *Service) summaryTargets(ctx context.Context) ([]summaryTarget, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := s.unreplied(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT m.id, m.thread_id FROM messages m JOIN summary_checks c ON c.message_id = m.id
		WHERE m.id IN (SELECT value FROM json_each(?)) AND c.outcome = ?
		ORDER BY c.attempts, m.date DESC, m.id DESC LIMIT ?`, intList(ids), summaryQueued, summaryBatch)
	if err != nil {
		return nil, err
	}
	var out []summaryTarget
	for _, r := range rows.Values {
		id, tid := store.Int(r[0]), store.Int(r[1])
		n, err := s.db.Query(ctx, "SELECT count(*) FROM messages m WHERE m.thread_id = ? AND "+notPurgedCond, tid)
		if err != nil {
			return nil, err
		}
		// As the reader's summaryTarget: the thread when it lists the thread's
		// messages (GetThread's) and they are more than one, else the message.
		t := Target{Kind: "message", ID: id}
		if st.ThreadView && store.Int(n.Values[0][0]) > 1 {
			t = Target{Kind: "thread", ID: tid}
		}
		out = append(out, summaryTarget{newest: id, target: t})
	}
	return out, nil
}

// summarizeBatch summarizes up to summaryBatch conversations and returns
// how many it finished.
func (s *Service) summarizeBatch(ctx context.Context) (int, error) {
	if err := s.queueArrivals(ctx); err != nil {
		return 0, err
	}
	_, ok, err := s.arrivalOn(ctx)
	if err != nil || !ok {
		return 0, err
	}
	targets, err := s.summaryTargets(ctx)
	if err != nil {
		return 0, err
	}
	done := 0
	for i, tg := range targets {
		if i > 0 { // the level or the model service may change mid-batch
			if _, ok, err := s.arrivalOn(ctx); err != nil || !ok {
				return done, err
			}
		}
		outcome, err := s.summarizeOne(ctx, tg.target)
		if err != nil && ctx.Err() == nil {
			// It may pass: the rest go first next time, and the last try fails it.
			_, rerr := s.db.Execute(ctx, `UPDATE summary_checks SET at = ?, attempts = attempts + 1,
				outcome = CASE WHEN attempts + 1 >= ? THEN ? ELSE outcome END WHERE message_id = ?`, s.clk().Now().Unix(), summaryAttempts, summaryFailed, tg.newest)
			return done, errors.Join(err, rerr)
		}
		if err != nil {
			return done, err
		}
		if _, err := s.db.Execute(ctx, `INSERT INTO summary_checks(message_id, at, outcome) SELECT id, ?, ? FROM messages WHERE id = ?
			ON CONFLICT(message_id) DO UPDATE SET at = excluded.at, outcome = excluded.outcome`, s.clk().Now().Unix(), outcome, tg.newest); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// summarizeOne summarizes t as the reader does, which writes the summary's
// reply date, and says how it went; an error is one that may pass (the
// service unreachable, a rate limit), which summarizeBatch tries again.
func (s *Service) summarizeOne(ctx context.Context, t Target) (string, error) {
	ch, err := s.RunSkill(ctx, "summarize", t)
	if err != nil {
		return "", err
	}
	var last ai.Event
	for ev := range ch {
		last = ev
	}
	switch {
	case last.Kind == "done":
		return summaryDone, nil
	case last.Err == nil:
		return "", errors.New("summary ended without an answer")
	case MaskBlocked(last.Err) != nil:
		return summaryBlocked, nil
	case last.Err.Code == ai.CodeIncomplete || last.Err.Code == ai.CodeBadResponse:
		return summaryFailed, nil
	}
	return "", last.Err
}
