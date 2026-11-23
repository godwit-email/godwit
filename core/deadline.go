package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/internal/i18n"
)

// Sources of a 期限, highest first: manual > program = wit > ai. A lower one
// never replaces a higher one in effect; the same level replaces.
const (
	DeadlineManual  = "manual"  // the user, in Godwit's interface
	DeadlineProgram = "program" // the user's programs: the Local API's full role and godwit mcp
	DeadlineWit     = "wit"     // Wit
	DeadlineAI      = "ai"      // the mail summary
)

func deadlineRank(source string) int {
	switch source {
	case DeadlineManual:
		return 0
	case DeadlineAI:
		return 2
	}
	return 1
}

// MaxDeadlineQuote caps a 期限's quote, in characters.
const MaxDeadlineQuote = 200

// Deadline is a conversation's 期限 (GLOSSARY; the interface says 回复日期):
// the date the sender wants the user's reply by. Source and Overdue are
// Godwit's on read and ignored on write.
type Deadline struct {
	Date      string // YYYY-MM-DD, a local calendar day; "" on write clears it
	Time      string // HH:MM local, "" for the end of the day
	MessageID int64  // the message from someone else that asks for it; 0 for manual
	Quote     string // the sender's own words, verbatim in that message; "" for manual
	Source    string
	Overdue   bool
}

// Why SetThreadDeadline refuses a write; each says so to the program that tried.
var (
	ErrDeadlineInvalid    = errors.New("a reply date is YYYY-MM-DD, with an optional HH:MM time and a quote of at most 200 characters")
	ErrDeadlineMessage    = errors.New("a reply date needs a message from someone else in this conversation")
	ErrDeadlineNotWaiting = errors.New("the conversation is not waiting on the user's reply to that message: it is replied to or archived since, or needs no reply")
	ErrDeadlineQuote      = errors.New("the quote does not appear word for word in that message")
	ErrDeadlineOutranked  = errors.New("a reply date the user set, or one from a higher source, is in effect; it cannot be replaced or cleared")
)

// SetThreadDeadline sets or (with d.Date "") clears a conversation's 期限, and
// returns the row it replaced (Source "": none) for UndoSetThreadDeadline. The
// caller decides its source: Godwit's interface (RoleUI) writes manual, every
// other caller program. A program's write names a message from someone else
// in the conversation, which is 未回复 and not replied to or archived since
// that message, so the date is in effect once written; its quote, if any, is
// in that message; it is refused while a manual one is in effect.
//
// Not egress (ADR 0003), so no audit row: it writes one local row, which no
// rule reads, no notification or request follows, no IMAP keyword or
// cross-device sync carries, and no events_log row (so no plugin hook) records.
func (s *Service) SetThreadDeadline(ctx context.Context, threadID int64, d Deadline) (RemovedDeadline, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return RemovedDeadline{}, err
	}
	defer end()
	source := DeadlineProgram
	if uiSessionFromContext(ctx) != nil {
		source = DeadlineManual
	}
	before := RemovedDeadline{ThreadID: threadID}
	return before, s.changeDeadline(ctx, source, threadID, d, true, &before)
}

// UndoSetThreadDeadline writes back the row SetThreadDeadline returned, as it
// was (Source "": none), over whatever is there now: 撤销 on the toast after
// the user sets, changes or removes a 回复日期.
func (s *Service) UndoSetThreadDeadline(ctx context.Context, before RemovedDeadline) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	if before.Source != "" && before.Source != DeadlineManual && before.Source != DeadlineProgram && before.Source != DeadlineWit && before.Source != DeadlineAI {
		return ErrDeadlineInvalid
	}
	s.editMu.Lock()
	defer s.editMu.Unlock()
	if before.Source == "" {
		return s.commitDeadline(ctx, sqlite.Statement{SQL: "DELETE FROM thread_deadlines WHERE thread_id = ?", Args: []any{before.ThreadID}})
	}
	// The thread or message may be gone since: the SELECT skips the one, the subquery NULLs the other.
	return s.commitDeadline(ctx, sqlite.Statement{SQL: `INSERT INTO thread_deadlines(thread_id, due_date, due_time, source, message_id, quote, set_at)
		SELECT id, NULLIF(?, ''), ?, ?, (SELECT id FROM messages WHERE id = ?), ?, ? FROM threads WHERE id = ?
		ON CONFLICT(thread_id) DO UPDATE SET due_date = excluded.due_date, due_time = excluded.due_time, source = excluded.source,
		message_id = excluded.message_id, quote = excluded.quote, set_at = excluded.set_at`,
		Args: []any{before.Date, before.Time, before.Source, before.MessageID, before.Quote, before.SetAt, before.ThreadID}})
}

// ReaderDeadline is what the reader's 等你处理 line says of a conversation:
// its dated 期限 in effect (nil for none) and whether it is 未回复.
type ReaderDeadline struct {
	Deadline  *Deadline
	Unreplied bool
}

// ThreadDeadline is the conversation's dated 期限 in effect, in a view or
// not, however long overdue, and its 回复状态: what the reader shows.
func (s *Service) ThreadDeadline(ctx context.Context, threadID int64) (ReaderDeadline, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ReaderDeadline{}, err
	}
	defer end()
	now := s.clk().Now()
	statuses, err := s.replyStatuses(ctx, now.Unix())
	if err != nil {
		return ReaderDeadline{}, err
	}
	got, err := s.deadlinesIn(ctx, now, statuses, []int64{threadID})
	out := ReaderDeadline{Unreplied: statuses[threadID].Kind == ReplyStatusUnreplied}
	if d, ok := got[threadID]; ok && d.Date != "" {
		out.Deadline = &d
	}
	return out, err
}

// setDeadline is the one write path of a 期限, for every source.
func (s *Service) setDeadline(ctx context.Context, source string, threadID int64, d Deadline) error {
	return s.changeDeadline(ctx, source, threadID, d, true, nil)
}

// checkDeadline is whether setDeadline would take d now, writing nothing: a
// Wit call it refuses goes back to the model before the user is asked.
func (s *Service) checkDeadline(ctx context.Context, source string, threadID int64, d Deadline) error {
	return s.changeDeadline(ctx, source, threadID, d, false, nil)
}

// changeDeadline checks d and, if write, writes it; before, if not nil, gets
// the row it replaces.
func (s *Service) changeDeadline(ctx context.Context, source string, threadID int64, d Deadline, write bool, before *RemovedDeadline) error {
	if d.Date != "" {
		if _, err := time.Parse(time.DateOnly, d.Date); err != nil {
			return ErrDeadlineInvalid
		}
	}
	if _, err := time.Parse("15:04", d.Time); d.Time != "" && (err != nil || d.Date == "") || utf8.RuneCountInString(d.Quote) > MaxDeadlineQuote {
		return ErrDeadlineInvalid
	}
	s.editMu.Lock()
	defer s.editMu.Unlock()
	now := s.clk().Now()
	statuses, err := s.replyStatuses(ctx, now.Unix())
	if err != nil {
		return err
	}
	current, err := s.deadlinesIn(ctx, now, statuses, []int64{threadID})
	if err != nil {
		return err
	}
	cur, has := current[threadID]
	if source != DeadlineManual {
		if has && deadlineRank(cur.Source) < deadlineRank(source) {
			return ErrDeadlineOutranked
		}
		if err := s.checkDeadlineMessage(ctx, threadID, statuses[threadID], d); err != nil {
			return err
		}
	}
	if !write {
		return nil
	}
	if before != nil {
		rows, err := s.db.Query(ctx, "SELECT COALESCE(due_date, ''), due_time, COALESCE(message_id, 0), quote, source, set_at FROM thread_deadlines WHERE thread_id = ?", threadID)
		if err != nil {
			return err
		}
		if len(rows.Values) > 0 {
			r := rows.Values[0]
			*before = RemovedDeadline{ThreadID: threadID, Date: store.Str(r[0]), Time: store.Str(r[1]), MessageID: store.Int(r[2]), Quote: store.Str(r[3]), Source: store.Str(r[4]), SetAt: store.Int(r[5])}
		}
	}
	if source == DeadlineManual {
		if d.Date == "" && has && cur.Source != DeadlineManual {
			// Removing another source's date leaves a marker that keeps them out.
			return s.writeDeadline(ctx, threadID, Deadline{Source: DeadlineManual}, now)
		}
		d.MessageID, d.Quote = 0, ""
	}
	if d.Date == "" {
		return s.commitDeadline(ctx, sqlite.Statement{SQL: "DELETE FROM thread_deadlines WHERE thread_id = ?", Args: []any{threadID}})
	}
	d.Source = source
	return s.writeDeadline(ctx, threadID, d, now)
}

// checkDeadlineMessage is whether d's message can carry a 期限 from a source
// other than manual: one from someone else, in the conversation, which waits
// on the user's reply to it (st), with d's quote in its text.
func (s *Service) checkDeadlineMessage(ctx context.Context, threadID int64, st replyStatus, d Deadline) error {
	if d.MessageID == 0 {
		return ErrDeadlineMessage
	}
	m, err := s.GetMessageText(ctx, d.MessageID)
	if err != nil || m.ThreadID != threadID {
		return ErrDeadlineMessage
	}
	roles, err := s.folderRoles(ctx)
	if err != nil {
		return err
	}
	people, err := s.mailPeople(ctx, roles)
	if err != nil {
		return err
	}
	if people.own[ownKey(m.From)] { // the sender addresses of the user's Sent mail are in own too
		return ErrDeadlineMessage
	}
	if st.Kind != ReplyStatusUnreplied || m.Date.Unix() <= st.Settled {
		return ErrDeadlineNotWaiting
	}
	if !strings.Contains(strings.Join(strings.Fields(m.Text), " "), strings.Join(strings.Fields(d.Quote), " ")) {
		return ErrDeadlineQuote
	}
	return nil
}

func (s *Service) writeDeadline(ctx context.Context, threadID int64, d Deadline, now time.Time) error {
	var date, msg any
	if d.Date != "" {
		date = d.Date
	}
	if d.MessageID != 0 {
		msg = d.MessageID
	}
	return s.commitDeadline(ctx, sqlite.Statement{SQL: `INSERT INTO thread_deadlines(thread_id, due_date, due_time, source, message_id, quote, set_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET due_date = excluded.due_date, due_time = excluded.due_time, source = excluded.source,
		message_id = excluded.message_id, quote = excluded.quote, set_at = excluded.set_at`,
		Args: []any{threadID, date, d.Time, d.Source, msg, d.Quote, now.Unix()}})
}

// bumpDeadlineVersion moves the 期限's version, which views' paging cursors carry.
var bumpDeadlineVersion = sqlite.Statement{SQL: `INSERT INTO meta(key, value) VALUES ('reply_dates_version', '1')
	ON CONFLICT(key) DO UPDATE SET value = CAST(value AS INTEGER) + 1`}

// commitDeadline writes st and moves the 期限's version with it: a view's
// paging cursor carries the version, so a page never mixes two orders. The
// views then reread, rows and order changed.
func (s *Service) commitDeadline(ctx context.Context, st sqlite.Statement) error {
	if _, err := s.db.Transaction(ctx, []sqlite.Statement{st, bumpDeadlineVersion}); err != nil {
		return err
	}
	s.publishView()
	return nil
}

// deadlinesIn is the 期限 in effect for each of threadIDs that has one, read
// now with the 回复状态 statuses: what sorting, rows and list_view_threads
// show. A manual entry with no date (the user removed another's) is in the
// result with Date "", so that it keeps lower sources out; callers that show
// dates skip it. A manual entry is in effect until the conversation is
// archived in Godwit after it was set. An ai, program or wit date is in effect while the
// conversation is 未回复 and its message is newer than the latest reply and
// archive (replyStatus.Settled).
func (s *Service) deadlinesIn(ctx context.Context, now time.Time, statuses map[int64]replyStatus, threadIDs []int64) (map[int64]Deadline, error) {
	out := map[int64]Deadline{}
	if len(threadIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT d.thread_id, COALESCE(d.due_date, ''), d.due_time, d.source, COALESCE(d.message_id, 0), d.quote, d.set_at,
		COALESCE(m.thread_id, 0), COALESCE(m.date, 0), COALESCE(a.archived_at, 0) FROM thread_deadlines d LEFT JOIN messages m ON m.id = d.message_id
		LEFT JOIN archive_marks a ON a.thread_id = d.thread_id WHERE d.thread_id IN (SELECT value FROM json_each(?))`, intList(threadIDs))
	if err != nil {
		return nil, err
	}
	for _, r := range rows.Values {
		tid := store.Int(r[0])
		d := Deadline{Date: store.Str(r[1]), Time: store.Str(r[2]), Source: store.Str(r[3]), MessageID: store.Int(r[4]), Quote: store.Str(r[5])}
		if d.Source == DeadlineManual {
			// Archived in Godwit since it was set. shortcut: an archive on another device writes no mark, so it leaves the date; read it from the mail's folders if users miss it.
			if store.Int(r[9]) > store.Int(r[6]) {
				continue
			}
		} else if st := statuses[tid]; store.Int(r[7]) != tid || st.Kind != ReplyStatusUnreplied || store.Int(r[8]) <= st.Settled {
			continue
		}
		if d.Date != "" {
			d.Overdue = !now.Before(deadlineEnd(d, now.Location()))
		}
		out[tid] = d
	}
	return out, nil
}

// RemovedDeadline is one row RemoveAppAndWitDeadlines took away, or the one
// SetThreadDeadline replaced, as stored.
type RemovedDeadline struct {
	ThreadID  int64
	Date      string
	Time      string
	MessageID int64
	Quote     string
	Source    string
	SetAt     int64
}

// CountAppAndWitDeadlines is how many 期限 other apps (program) and Wit have
// written, in effect or not: what RemoveAppAndWitDeadlines would take away.
func (s *Service) CountAppAndWitDeadlines(ctx context.Context) (int, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT count(*) FROM thread_deadlines WHERE source IN (?, ?)", DeadlineProgram, DeadlineWit)
	if err != nil {
		return 0, err
	}
	return int(store.Int(rows.Values[0][0])), nil
}

// RemoveAppAndWitDeadlines deletes every 期限 other apps (program) and Wit
// wrote, in effect or not, and returns them for UndoRemoveAppAndWitDeadlines:
// settings' 全部去掉, for when an injected agent wrote many. Manual and ai
// dates stay.
func (s *Service) RemoveAppAndWitDeadlines(ctx context.Context) ([]RemovedDeadline, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	s.editMu.Lock()
	defer s.editMu.Unlock()
	rows, err := s.db.Query(ctx, `DELETE FROM thread_deadlines WHERE source IN (?, ?)
		RETURNING thread_id, COALESCE(due_date, ''), due_time, COALESCE(message_id, 0), quote, source, set_at`, DeadlineProgram, DeadlineWit)
	if err != nil {
		return nil, err
	}
	var out []RemovedDeadline
	var threads []int64
	for _, r := range rows.Values {
		out = append(out, RemovedDeadline{ThreadID: store.Int(r[0]), Date: store.Str(r[1]), Time: store.Str(r[2]), MessageID: store.Int(r[3]),
			Quote: store.Str(r[4]), Source: store.Str(r[5]), SetAt: store.Int(r[6])})
		threads = append(threads, store.Int(r[0]))
	}
	if len(threads) > 0 {
		if _, err := s.db.Transaction(ctx, []sqlite.Statement{bumpDeadlineVersion}); err != nil {
			return out, err
		}
		s.bus.Publish(events.Event{Type: events.MessagesChanged, ThreadIDs: threads})
	}
	return out, nil
}

// UndoRemoveAppAndWitDeadlines writes back what RemoveAppAndWitDeadlines
// returned: program and wit rows only, and none over a 期限 written since.
func (s *Service) UndoRemoveAppAndWitDeadlines(ctx context.Context, removed []RemovedDeadline) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.editMu.Lock()
	defer s.editMu.Unlock()
	var stmts []sqlite.Statement
	var threads []int64
	for _, d := range removed {
		if d.Source != DeadlineProgram && d.Source != DeadlineWit || d.Date == "" {
			continue
		}
		threads = append(threads, d.ThreadID)
		// The thread or message may be gone since: the SELECT skips the one, the subquery NULLs the other.
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO thread_deadlines(thread_id, due_date, due_time, source, message_id, quote, set_at)
			SELECT id, ?, ?, ?, (SELECT id FROM messages WHERE id = ?), ?, ? FROM threads WHERE id = ? ON CONFLICT(thread_id) DO NOTHING`,
			Args: []any{d.Date, d.Time, d.Source, d.MessageID, d.Quote, d.SetAt, d.ThreadID}})
	}
	if len(stmts) == 0 {
		return nil
	}
	if _, err := s.db.Transaction(ctx, append(stmts, bumpDeadlineVersion)); err != nil {
		return err
	}
	s.bus.Publish(events.Event{Type: events.MessagesChanged, ThreadIDs: threads})
	return nil
}

// deadlineEnd is when d passes: its time, or the end of its day, in loc.
func deadlineEnd(d Deadline, loc *time.Location) time.Time {
	if d.Time != "" {
		t, _ := time.ParseInLocation(time.DateOnly+" 15:04", d.Date+" "+d.Time, loc)
		return t
	}
	t, _ := time.ParseInLocation(time.DateOnly, d.Date, loc)
	return t.AddDate(0, 0, 1)
}

// witDeadlineArgs is a set_reply_deadline call: the model names the message by
// its mail id; Prepare records the message and its conversation.
type witDeadlineArgs struct {
	ID        int    `json:"id"`
	MessageID int64  `json:"message_id"`
	ThreadID  int64  `json:"thread_id"`
	Date      string `json:"date"`
	Time      string `json:"time,omitempty"`
	Quote     string `json:"quote,omitempty"`
}

func (a witDeadlineArgs) deadline() Deadline {
	return Deadline{Date: a.Date, Time: a.Time, MessageID: a.MessageID, Quote: a.Quote}
}

// prepareWitDeadline resolves the mail id and runs setDeadline's checks, so a
// call they refuse never reaches the user; the summary names the
// conversation and the date in the interface's language.
func (s *Service) prepareWitDeadline(ctx context.Context, _ Conversation, args json.RawMessage) (prepared, error) {
	var in witDeadlineArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return prepared{}, err
	}
	var err error
	if in.MessageID, err = s.mailOf(ctx, runOf(ctx), in.ID); err != nil {
		return prepared{}, err
	}
	m, err := s.GetMessageText(ctx, in.MessageID)
	if err != nil {
		return prepared{}, err
	}
	in.ThreadID = m.ThreadID
	if err := s.checkDeadline(ctx, DeadlineWit, in.ThreadID, in.deadline()); err != nil {
		return prepared{}, err
	}
	summary := i18n.Tf("wit.deadline.clear", m.Subject)
	if in.Date != "" {
		day, _ := time.ParseInLocation(time.DateOnly, in.Date, time.Local) // checkDeadline parsed it
		summary = i18n.Tf("wit.deadline.set", m.Subject, strings.TrimSpace(i18n.FullDate(day)+" "+in.Time))
	}
	b, err := json.Marshal(in)
	return prepared{Args: b, Summary: summary, TargetKind: "thread", TargetID: in.ThreadID}, err
}

// applyWitDeadline writes the approved date through setDeadline, which checks
// it again: the conversation may have been replied to since.
func (s *Service) applyWitDeadline(ctx context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
	var in witDeadlineArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return nil, nil, nil, err
	}
	if err := s.setDeadline(ctx, DeadlineWit, in.ThreadID, in.deadline()); err != nil {
		return nil, nil, nil, err
	}
	if in.Date == "" {
		return nil, map[string]any{"cleared": true}, nil, nil
	}
	return nil, map[string]any{"set": strings.TrimSpace(in.Date + " " + in.Time)}, nil, nil
}
