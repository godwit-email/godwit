package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
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

// deadlineRank is source's place in that order, lower first; -1 for no
// source.
func deadlineRank(source string) int {
	switch source {
	case DeadlineManual:
		return 0
	case DeadlineProgram, DeadlineWit:
		return 1
	case DeadlineAI:
		return 2
	}
	return -1
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
	ErrDeadlineInvalid    = errors.New("a reply date is YYYY-MM-DD, with an optional HH:MM time and a quote of at most " + strconv.Itoa(MaxDeadlineQuote) + " characters")
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
	s.editMu.Lock()
	defer s.editMu.Unlock()
	before := RemovedDeadline{ThreadID: threadID}
	stmts, err := s.deadlineWrites(ctx, source, threadID, d, &before)
	if err != nil {
		return before, err
	}
	return before, s.commitDeadline(ctx, stmts...)
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
	if before.Source != "" && deadlineRank(before.Source) < 0 {
		return ErrDeadlineInvalid
	}
	s.editMu.Lock()
	defer s.editMu.Unlock()
	if before.Source == "" {
		return s.commitDeadline(ctx, sqlite.Statement{SQL: "DELETE FROM thread_deadlines WHERE thread_id = ?", Args: []any{before.ThreadID}})
	}
	return s.commitDeadline(ctx, deadlineRowWrite(before, true, false))
}

// ReaderDeadline is what the reader's 等你处理 line, or its 待跟进 band,
// says of a conversation: its dated 期限 in effect (nil for none), whether it
// is 未回复, or 等待回复 and for how many days.
type ReaderDeadline struct {
	Deadline    *Deadline
	Unreplied   bool
	Waiting     bool
	WaitingDays int
}

// ThreadDeadline is the conversation's dated 期限 in effect, in a view or
// not, however long overdue, and its 回复状态: what the reader shows.
func (s *Service) ThreadDeadline(ctx context.Context, threadID int64) (ReaderDeadline, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ReaderDeadline{}, err
	}
	defer end()
	now := s.userNow(ctx)
	statuses, err := s.replyStatuses(ctx, now.Unix())
	if err != nil {
		return ReaderDeadline{}, err
	}
	got, err := s.deadlinesIn(ctx, now, statuses, []int64{threadID})
	st := statuses[threadID]
	out := ReaderDeadline{Unreplied: st.Kind == ReplyStatusUnreplied, Waiting: st.Kind == ReplyStatusWaiting}
	if out.Waiting {
		out.WaitingDays = int((now.Unix() - st.Since) / 86400) // as a view's row counts them
	}
	if d, ok := got[threadID]; ok && d.Date != "" {
		out.Deadline = &d
	}
	return out, err
}

// setDeadline checks and writes a 期限 from source.
func (s *Service) setDeadline(ctx context.Context, source string, threadID int64, d Deadline) error {
	s.editMu.Lock()
	defer s.editMu.Unlock()
	stmts, err := s.deadlineWrites(ctx, source, threadID, d, nil)
	if err != nil {
		return err
	}
	return s.commitDeadline(ctx, stmts...)
}

// checkDeadline is whether setDeadline would take d now, writing nothing: a
// Wit call it refuses goes back to the model before the user is asked.
func (s *Service) checkDeadline(ctx context.Context, source string, threadID int64, d Deadline) error {
	s.editMu.Lock()
	defer s.editMu.Unlock()
	_, err := s.deadlineWrites(ctx, source, threadID, d, nil)
	return err
}

// deadlineWrites is the one write path of a 期限, for every source: it checks
// d and returns the statements that write it, for commitDeadline or an
// approval; before, if not nil, gets the row they replace. The caller holds
// editMu.
func (s *Service) deadlineWrites(ctx context.Context, source string, threadID int64, d Deadline, before *RemovedDeadline) ([]sqlite.Statement, error) {
	var err error
	if d.MessageID, err = s.resolveID(ctx, d.MessageID); err != nil { // a row merged away: checked and written as the one it went into
		return nil, err
	}
	if d.Date != "" {
		if _, err := time.Parse(time.DateOnly, d.Date); err != nil {
			return nil, ErrDeadlineInvalid
		}
	}
	if _, err := time.Parse("15:04", d.Time); (d.Time != "" && (err != nil || d.Date == "")) || utf8.RuneCountInString(d.Quote) > MaxDeadlineQuote {
		return nil, ErrDeadlineInvalid
	}
	now := s.userNow(ctx)
	statuses, err := s.replyStatuses(ctx, now.Unix())
	if err != nil {
		return nil, err
	}
	current, err := s.deadlinesIn(ctx, now, statuses, []int64{threadID})
	if err != nil {
		return nil, err
	}
	cur, has := current[threadID]
	if source != DeadlineManual {
		if has && deadlineRank(cur.Source) < deadlineRank(source) {
			return nil, ErrDeadlineOutranked
		}
		if err := s.checkDeadlineMessage(ctx, threadID, statuses[threadID], d); err != nil {
			return nil, err
		}
	}
	if before != nil {
		rows, err := s.db.Query(ctx, "SELECT "+deadlineRowCols+" FROM thread_deadlines WHERE thread_id = ?", threadID)
		if err != nil {
			return nil, err
		}
		if len(rows.Values) > 0 {
			*before = deadlineRow(rows.Values[0])
		}
	}
	if source == DeadlineManual {
		if d.Date == "" && has && cur.Source != DeadlineManual {
			// Removing another source's date leaves a marker that keeps them out.
			return []sqlite.Statement{deadlineRowWrite(RemovedDeadline{ThreadID: threadID, Source: DeadlineManual, SetAt: now.Unix()}, true, true)}, nil
		}
		d.MessageID, d.Quote = 0, ""
	}
	if d.Date == "" {
		return []sqlite.Statement{{SQL: "DELETE FROM thread_deadlines WHERE thread_id = ?", Args: []any{threadID}}}, nil
	}
	return []sqlite.Statement{deadlineRowWrite(RemovedDeadline{ThreadID: threadID, Date: d.Date, Time: d.Time, MessageID: d.MessageID,
		Quote: d.Quote, Source: source, SetAt: now.Unix()}, true, true)}, nil
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
	if !quoteIn(m.Text, d.Quote, d.MessageID) {
		return ErrDeadlineQuote
	}
	return nil
}

// quoteIn is whether quote is in text of message id, spaces aside, as written
// or with both links as a model gets them (linkRefs: tracking parameters
// dropped, long links as references), so a summary quoting what it was given
// checks against the stored message too.
func quoteIn(text, quote string, id int64) bool {
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	return strings.Contains(flat(text), flat(quote)) || strings.Contains(flat(linkRefs(text, id)), flat(linkRefs(quote, id)))
}

// deadlineRowWrite writes r as its thread's row, over the one there if
// replace, else only where there is none. A fresh row takes the version the
// transaction's bumpDeadlineVersion, run before it, moved to; any other keeps
// r.SetVersion. The thread or message may be gone since r was read: the
// SELECT skips the one, the subquery NULLs the other.
func deadlineRowWrite(r RemovedDeadline, replace, fresh bool) sqlite.Statement {
	conflict := "DO NOTHING"
	if replace {
		conflict = `DO UPDATE SET due_date = excluded.due_date, due_time = excluded.due_time, source = excluded.source,
		message_id = excluded.message_id, quote = excluded.quote, set_at = excluded.set_at, set_version = excluded.set_version`
	}
	version, args := "?", []any{r.Date, r.Time, r.Source, r.MessageID, r.Quote, r.SetAt, r.SetVersion, r.ThreadID}
	if fresh {
		version, args = deadlineVersionSQL, slices.Delete(args, 6, 7)
	}
	return sqlite.Statement{SQL: `INSERT INTO thread_deadlines(thread_id, due_date, due_time, source, message_id, quote, set_at, set_version)
		SELECT id, NULLIF(?, ''), ?, ?, (SELECT id FROM messages WHERE id = ?), ?, ?, ` + version + ` FROM threads WHERE id = ?
		ON CONFLICT(thread_id) ` + conflict,
		Args: args}
}

// deadlineRowCols are the columns deadlineRow reads.
const deadlineRowCols = "thread_id, COALESCE(due_date, ''), due_time, COALESCE(message_id, 0), quote, source, set_at, set_version"

func deadlineRow(r []any) RemovedDeadline {
	return RemovedDeadline{ThreadID: store.Int(r[0]), Date: store.Str(r[1]), Time: store.Str(r[2]), MessageID: store.Int(r[3]),
		Quote: store.Str(r[4]), Source: store.Str(r[5]), SetAt: store.Int(r[6]), SetVersion: store.Int(r[7])}
}

// bumpDeadlineVersion moves the 期限's version, which views' paging cursors
// carry and deadlineVersionSQL reads.
var bumpDeadlineVersion = sqlite.Statement{SQL: `INSERT INTO meta(key, value) VALUES ('reply_dates_version', '1')
	ON CONFLICT(key) DO UPDATE SET value = CAST(value AS INTEGER) + 1`}

// deadlineVersionSQL is the 期限's version now: a 期限 write or an archive
// records the one its bumpDeadlineVersion moved to, which orders the two
// within a second.
const deadlineVersionSQL = "(SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'reply_dates_version')"

// commitDeadline moves the 期限's version and writes stmts with it: a view's
// paging cursor carries the version, so a page never mixes two orders. The
// views then reread, rows and order changed.
func (s *Service) commitDeadline(ctx context.Context, stmts ...sqlite.Statement) error {
	if _, err := s.db.Transaction(ctx, append([]sqlite.Statement{bumpDeadlineVersion}, stmts...)); err != nil {
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
		COALESCE(m.thread_id, 0), COALESCE(m.date, 0), COALESCE(a.archived_at, 0), d.set_version, COALESCE(a.archived_version, 0)
		FROM thread_deadlines d LEFT JOIN messages m ON m.id = d.message_id
		LEFT JOIN archive_marks a ON a.thread_id = d.thread_id WHERE d.thread_id IN (SELECT value FROM json_each(?))`, intList(threadIDs))
	if err != nil {
		return nil, err
	}
	for _, r := range rows.Values {
		tid := store.Int(r[0])
		d := Deadline{Date: store.Str(r[1]), Time: store.Str(r[2]), Source: store.Str(r[3]), MessageID: store.Int(r[4]), Quote: store.Str(r[5])}
		if d.Source == DeadlineManual {
			if manualEnded(store.Int(r[6]), store.Int(r[10]), archiveMark{at: store.Int(r[9]), ver: store.Int(r[11])}) {
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

// manualEnded is whether a manual 期限 set at setAt with version setVer has
// ended: the conversation was archived in Godwit since (mark), by version, or
// by time for rows from before versions were kept.
// shortcut: an archive on another device writes no mark, so it leaves the date; read it from the mail's folders if users miss it.
func manualEnded(setAt, setVer int64, mark archiveMark) bool {
	if setVer != 0 && mark.ver != 0 {
		return mark.ver > setVer
	}
	return mark.at > setAt
}

// mergedReplyDate is, of threads about to merge into one conversation, the
// one whose 期限 row the merged conversation keeps (0: none has one): one
// in effect once their mail, replies and archives count as one
// conversation's, as deadlinesIn has it, then by source (deadlineRank), then
// on the newest mail (the newer request holds), then the earliest date.
func (s *Service) mergedReplyDate(ctx context.Context, threads []int64) (int64, error) {
	roles, err := s.folderRoles(ctx)
	if err != nil {
		return 0, err
	}
	people, err := s.mailPeople(ctx, roles)
	if err != nil {
		return 0, err
	}
	mails, err := s.logicalMails(ctx, roles, "m.thread_id IN (SELECT value FROM json_each(?))", intList(threads))
	if err != nil {
		return 0, err
	}
	marks, err := s.archiveMarks(ctx)
	if err != nil {
		return 0, err
	}
	replied, err := s.repliedAt(ctx)
	if err != nil {
		return 0, err
	}
	// The merge keeps the newest mail's mark with the latest archive of all.
	msgs, mark, repliedAt := map[string]*logicalMail{}, archiveMark{}, int64(0)
	for _, t := range threads {
		maps.Copy(msgs, mails[t])
		if m := marks[t]; m.has {
			mark = archiveMark{has: true, date: max(mark.date, m.date), at: max(mark.at, m.at), ver: max(mark.ver, m.ver)}
		}
		repliedAt = max(repliedAt, replied[t])
	}
	settledAt := settled(msgs, people, mark, repliedAt)
	rows, err := s.db.Query(ctx, `SELECT d.thread_id, COALESCE(d.due_date, ''), d.due_time, d.source, d.set_at, d.set_version,
		COALESCE(m.thread_id, 0), COALESCE(m.date, 0) FROM thread_deadlines d LEFT JOIN messages m ON m.id = d.message_id
		WHERE d.thread_id IN (SELECT value FROM json_each(?))`, intList(threads))
	if err != nil || len(rows.Values) == 0 {
		return 0, err
	}
	type row struct {
		thread, mailDate int64
		effective        bool
		rank             int
		date, time       string
	}
	var keep []row
	for _, r := range rows.Values {
		d := row{thread: store.Int(r[0]), date: store.Str(r[1]), time: store.Str(r[2]), rank: deadlineRank(store.Str(r[3])), mailDate: store.Int(r[7])}
		if d.rank == 0 {
			d.effective = !manualEnded(store.Int(r[4]), store.Int(r[5]), mark)
		} else {
			d.effective = slices.Contains(threads, store.Int(r[6])) && d.mailDate > settledAt
		}
		keep = append(keep, d)
	}
	last := func(b bool) int { // true sorts last: not in effect, no date, no time
		if b {
			return 1
		}
		return 0
	}
	return slices.MinFunc(keep, func(a, b row) int {
		return cmp.Or(cmp.Compare(last(!a.effective), last(!b.effective)), cmp.Compare(a.rank, b.rank), cmp.Compare(b.mailDate, a.mailDate),
			cmp.Compare(last(a.date == ""), last(b.date == "")), cmp.Compare(a.date, b.date),
			cmp.Compare(last(a.time == ""), last(b.time == "")), cmp.Compare(a.time, b.time), cmp.Compare(a.thread, b.thread))
	}).thread, nil
}

// RemovedDeadline is one row RemoveAppAndWitDeadlines took away, or the one
// SetThreadDeadline replaced, as stored.
type RemovedDeadline struct {
	ThreadID   int64
	Date       string
	Time       string
	MessageID  int64
	Quote      string
	Source     string
	SetAt      int64
	SetVersion int64
}

// CountAppAndWitDeadlines is how many 期限 other apps (program) and Wit have
// written that are in effect, as the user sees them; RemoveAppAndWitDeadlines
// also takes away those no longer in effect.
func (s *Service) CountAppAndWitDeadlines(ctx context.Context) (int, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT thread_id FROM thread_deadlines WHERE source IN (?, ?)", DeadlineProgram, DeadlineWit)
	if err != nil || len(rows.Values) == 0 {
		return 0, err
	}
	ids := make([]int64, len(rows.Values))
	for i, r := range rows.Values {
		ids[i] = store.Int(r[0])
	}
	now := s.userNow(ctx)
	statuses, err := s.replyStatuses(ctx, now.Unix())
	if err != nil {
		return 0, err
	}
	got, err := s.deadlinesIn(ctx, now, statuses, ids)
	return len(got), err // only program and wit rows, each with a date
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
	// Read, then delete those threads' rows with the version bump in one
	// transaction (Transaction returns no rows for a DELETE … RETURNING);
	// every 期限 write but a thread merge holds editMu too.
	rows, err := s.db.Query(ctx, "SELECT "+deadlineRowCols+" FROM thread_deadlines WHERE source IN (?, ?)", DeadlineProgram, DeadlineWit)
	if err != nil {
		return nil, err
	}
	var out []RemovedDeadline
	var threads []int64
	for _, r := range rows.Values {
		out = append(out, deadlineRow(r))
		threads = append(threads, store.Int(r[0]))
	}
	if len(threads) == 0 {
		return nil, nil
	}
	if _, err := s.db.Transaction(ctx, []sqlite.Statement{{SQL: "DELETE FROM thread_deadlines WHERE source IN (?, ?) AND thread_id IN (SELECT value FROM json_each(?))",
		Args: []any{DeadlineProgram, DeadlineWit, intList(threads)}}, bumpDeadlineVersion}); err != nil {
		return nil, err
	}
	s.bus.Publish(events.Event{Type: events.MessagesChanged, ThreadIDs: threads})
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
		stmts = append(stmts, deadlineRowWrite(d, false, false))
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
		day, _ := time.ParseInLocation(time.DateOnly, in.Date, s.userNow(ctx).Location()) // checkDeadline parsed it
		summary = i18n.Tf("wit.deadline.set", m.Subject, strings.TrimSpace(i18n.FullDate(day)+" "+in.Time))
	}
	b, err := json.Marshal(in)
	return prepared{Args: b, Summary: summary, TargetKind: "thread", TargetID: in.ThreadID}, err
}

// applyWitDeadline checks the approved date again (the conversation may have
// been replied to since) and returns its writes, committed with the approval
// (ADR 0004).
func (s *Service) applyWitDeadline(ctx context.Context, a Approval) ([]sqlite.Statement, any, any, error) {
	var in witDeadlineArgs
	if err := json.Unmarshal(a.Args, &in); err != nil {
		return nil, nil, nil, err
	}
	s.editMu.Lock()
	defer s.editMu.Unlock()
	var before RemovedDeadline
	stmts, err := s.deadlineWrites(ctx, DeadlineWit, in.ThreadID, in.deadline(), &before)
	if err != nil {
		return nil, nil, nil, err
	}
	// The commit comes after editMu is released: a row written meanwhile (the
	// user's own date) fails it, as a draft's changed revision does.
	var was any
	if before.Source != "" {
		was = before.Source + " " + strconv.FormatInt(before.SetAt, 10)
	}
	guard := sqlite.Statement{SQL: "UPDATE approvals SET state = NULL WHERE id = ? AND (SELECT source || ' ' || set_at FROM thread_deadlines WHERE thread_id = ?) IS NOT ?",
		Args: []any{a.ID, in.ThreadID, was}}
	stmts = append([]sqlite.Statement{guard, bumpDeadlineVersion}, stmts...)
	if in.Date == "" {
		return stmts, map[string]any{"cleared": true}, nil, nil
	}
	return stmts, map[string]any{"set": strings.TrimSpace(in.Date + " " + in.Time)}, nil, nil
}
