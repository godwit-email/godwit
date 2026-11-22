package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
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

// SetThreadDeadline sets or (with d.Date "") clears a conversation's 期限. The
// caller decides its source: Godwit's interface (RoleUI) writes manual, every
// other caller program. A program's write names a message from someone else
// in the conversation, which is 未回复 and not replied to or archived since
// that message, so the date is in effect once written; its quote, if any, is
// in that message; it is refused while a manual one is in effect.
//
// Not egress (ADR 0003), so no audit row: it writes one local row, which no
// rule reads, no notification or request follows, no IMAP keyword or
// cross-device sync carries, and no events_log row (so no plugin hook) records.
func (s *Service) SetThreadDeadline(ctx context.Context, threadID int64, d Deadline) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	source := DeadlineProgram
	if uiSessionFromContext(ctx) != nil {
		source = DeadlineManual
	}
	return s.setDeadline(ctx, source, threadID, d)
}

// setDeadline is the one write path of a 期限, for every source.
func (s *Service) setDeadline(ctx context.Context, source string, threadID int64, d Deadline) error {
	return s.changeDeadline(ctx, source, threadID, d, true)
}

// checkDeadline is whether setDeadline would take d now, writing nothing: a
// Wit call it refuses goes back to the model before the user is asked.
func (s *Service) checkDeadline(ctx context.Context, source string, threadID int64, d Deadline) error {
	return s.changeDeadline(ctx, source, threadID, d, false)
}

func (s *Service) changeDeadline(ctx context.Context, source string, threadID int64, d Deadline, write bool) error {
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
	if source == DeadlineManual {
		if d.Date == "" && has && cur.Source != DeadlineManual {
			// Removing another source's date leaves a marker that keeps them out.
			return s.writeDeadline(ctx, threadID, Deadline{Source: DeadlineManual}, now)
		}
		d.MessageID, d.Quote = 0, ""
	}
	if d.Date == "" {
		_, err := s.db.Execute(ctx, "DELETE FROM thread_deadlines WHERE thread_id = ?", threadID)
		return err
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
	_, err := s.db.Execute(ctx, `INSERT INTO thread_deadlines(thread_id, due_date, due_time, source, message_id, quote, set_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(thread_id) DO UPDATE SET due_date = excluded.due_date, due_time = excluded.due_time, source = excluded.source,
		message_id = excluded.message_id, quote = excluded.quote, set_at = excluded.set_at`,
		threadID, date, d.Time, d.Source, msg, d.Quote, now.Unix())
	return err
}

// deadlinesIn is the 期限 in effect for each of threadIDs that has one, read
// now with the 回复状态 statuses: what sorting, rows and list_view_threads
// show. A manual entry with no date (the user removed another's) is in the
// result with Date "", so that it keeps lower sources out; callers that show
// dates skip it. An ai, program or wit date is in effect while the
// conversation is 未回复 and its message is newer than the latest reply and
// archive (replyStatus.Settled).
func (s *Service) deadlinesIn(ctx context.Context, now time.Time, statuses map[int64]replyStatus, threadIDs []int64) (map[int64]Deadline, error) {
	out := map[int64]Deadline{}
	if len(threadIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT d.thread_id, COALESCE(d.due_date, ''), d.due_time, d.source, COALESCE(d.message_id, 0), d.quote, d.set_at,
		COALESCE(m.thread_id, 0), COALESCE(m.date, 0) FROM thread_deadlines d LEFT JOIN messages m ON m.id = d.message_id
		WHERE d.thread_id IN (SELECT value FROM json_each(?))`, intList(threadIDs))
	if err != nil {
		return nil, err
	}
	for _, r := range rows.Values {
		tid := store.Int(r[0])
		d := Deadline{Date: store.Str(r[1]), Time: store.Str(r[2]), Source: store.Str(r[3]), MessageID: store.Int(r[4]), Quote: store.Str(r[5])}
		if d.Source == DeadlineManual {
			if !manualDeadlineInEffect(tid, store.Int(r[6])) {
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

// manualDeadlineInEffect is whether a manual 期限 set at setAt is in effect.
// shortcut: always, until #848 ends it at the conversation's next archive (archive_marks.archived_at).
func manualDeadlineInEffect(threadID, setAt int64) bool { return true }

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
