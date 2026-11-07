package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
)

// SavedViewVersion is the document version SaveView writes.
const SavedViewVersion = 1

// Fields only views use: they read a conversation's state now, where
// a rule runs once on an event.
const (
	FieldReply = "reply" // is ViewReplyUnreplied, or older_than a number of whole days
	FieldRead  = "read"  // is ViewReadUnread or ViewReadRead

	// FieldDate is, or isn't (OpNotContains), one of the ViewDate values or a
	// number of days, today included, in local calendar days.
	FieldDate    = "date"
	FieldStarred = "starred" // is RuleYes or RuleNo
	FieldWrote   = "wrote"   // is RuleYes: a sender the user has written to, or their own mail; RuleNo: only others

	ViewDateToday     = "today"
	ViewDateYesterday = "yesterday"
	ViewDateThisWeek  = "this_week"
	ViewDateLastWeek  = "last_week"
	ViewDateThisMonth = "this_month"
	ViewDateLastMonth = "last_month"

	ViewReplyUnreplied = "unreplied"
	ViewReadUnread     = "unread"
	ViewReadRead       = "read"

	// Exceptions: a conversation added to or removed from a view by hand.
	ViewExceptionAdded   = "added"
	ViewExceptionRemoved = "removed"

	// ViewGroupOther is the ID of a view's 其他: its conversations in no group.
	ViewGroupOther = "other"
)

// View limits: the document stays a list of choices, not a language.
const (
	MaxViewGroups    = 20
	MaxViewWaitDays  = 29 // 回复状态 reads replyStatusDays days back
	MaxViewDateDays  = 3650
	maxViewIconBytes = 64
)

// SavedViewDoc is what a view shows: conversations of Account (0:
// every account) meeting Match of Conditions, placed in the first of Groups they
// meet, the rest in 其他. As in a rule, no conditions is met by all.
type SavedViewDoc struct {
	Version    int             `json:"version"`
	Account    int64           `json:"account,omitempty"`
	Match      string          `json:"match"`
	Conditions []RuleCondition `json:"conditions"`
	Groups     []ViewGroup     `json:"groups"`
}

// ViewGroup is a named group of conditions; ID stays when it is renamed or moved.
type ViewGroup struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Match      string          `json:"match"`
	Conditions []RuleCondition `json:"conditions"`
}

// SavedView is a stored view. Unread counts its conversations with
// unread mail.
type SavedView struct {
	ID       int64
	Name     string
	Icon     string
	Doc      SavedViewDoc
	Revision int64
	Unread   int
}

// ViewRow is a conversation in a view: Status is ReplyStatusUnreplied,
// ReplyStatusWaiting or none, WaitingDays since the user's last mail, NewReply
// that it is newer than its last archive, and Manual that it was added by hand.
type ViewRow struct {
	ThreadSummary
	Status      string
	NewReply    bool
	WaitingDays int
	Manual      bool
}

// ViewGroupPage is one page of a group or of 其他; Total counts all of it.
type ViewGroupPage struct {
	ID         string
	Name       string
	Total      int
	Items      []ViewRow
	NextCursor string
}

// ViewContents is a view's non-empty groups, in order, then 其他.
type ViewContents struct {
	Groups []ViewGroupPage
}

// ViewException is a conversation added to or removed from a view by hand.
type ViewException struct {
	ThreadID      int64
	Kind          string
	Subject, From string
}

// ViewReason says why a conversation is in a view: the group it is
// in ("" for none, or 其他), the condition it met, and whether it was added by
// hand. Condition is the zero value for a hand-added one.
type ViewReason struct {
	Member    bool
	GroupID   string
	GroupName string
	Condition RuleCondition
	Manual    bool
	Status    string
	NewReply  bool
	Days      int
}

// ErrInvalidView is a document SaveView refuses.
var ErrInvalidView = errors.New("invalid view")

func invalidView(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidView, fmt.Sprintf(format, args...))
}

// validSavedView accepts what the editor can produce.
func (s *Service) validSavedView(ctx context.Context, name, icon string, d SavedViewDoc) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > MaxRuleName {
		return invalidView("name")
	}
	if icon == "" || len(icon) > maxViewIconBytes {
		return invalidView("icon")
	}
	return s.validViewDoc(ctx, d)
}

func (s *Service) validViewDoc(ctx context.Context, d SavedViewDoc) error {
	if len(d.Groups) > MaxViewGroups {
		return invalidView("more than %d groups", MaxViewGroups)
	}
	if d.Account != 0 {
		if _, err := s.account(ctx, d.Account); err != nil {
			return invalidView("account %d", d.Account)
		}
	}
	if err := s.validViewConditions(ctx, d.Match, d.Conditions); err != nil {
		return err
	}
	seen := map[string]bool{ViewGroupOther: true}
	for _, g := range d.Groups {
		if g.ID == "" || seen[g.ID] {
			return invalidView("group id %q", g.ID)
		}
		seen[g.ID] = true
		if strings.TrimSpace(g.Name) == "" || utf8.RuneCountInString(g.Name) > MaxRuleName {
			return invalidView("group name")
		}
		if err := s.validViewConditions(ctx, g.Match, g.Conditions); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) validViewConditions(ctx context.Context, match string, conds []RuleCondition) error {
	if len(conds) > 0 && match != MatchAll && match != MatchAny {
		return invalidView("match %q", match)
	}
	if err := validGroups(RuleDoc{Conditions: conds}); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidView, err)
	}
	rows := RuleDoc{Conditions: conds}.Rows()
	if len(rows) > MaxRuleConditions {
		return invalidView("more than %d conditions", MaxRuleConditions)
	}
	for _, c := range rows {
		switch c.Field {
		case FieldReply:
			switch c.Op {
			case OpIs:
				if c.Value != ViewReplyUnreplied {
					return invalidView("reply %q", c.Value)
				}
			case OpOlderThan:
				if n, err := strconv.Atoi(c.Value); err != nil || n < 1 || n > MaxViewWaitDays {
					return invalidView("reply days %q", c.Value)
				}
			default:
				return invalidView("reply op %q", c.Op)
			}
		case FieldRead:
			if c.Op != OpIs || c.Value != ViewReadUnread && c.Value != ViewReadRead {
				return invalidView("read %s %q", c.Op, c.Value)
			}
		case FieldDate:
			if c.Op != OpIs && c.Op != OpNotContains {
				return invalidView("date op %q", c.Op)
			}
			if n, err := strconv.Atoi(c.Value); err == nil {
				if n < 1 || n > MaxViewDateDays {
					return invalidView("date days %q", c.Value)
				}
			} else if !slices.Contains(viewDates, c.Value) {
				return invalidView("date %q", c.Value)
			}
		case FieldStarred, FieldWrote:
			if c.Op != OpIs || c.Value != RuleYes && c.Value != RuleNo {
				return invalidView("%s %s %q", c.Field, c.Op, c.Value)
			}
		case FieldAny:
			if c.Op != OpContains && c.Op != OpNotContains || strings.TrimSpace(c.Value) == "" || utf8.RuneCountInString(c.Value) > MaxRuleValue {
				return invalidView("any %s %q", c.Op, c.Value)
			}
		case FieldAge:
			return invalidView("field %q", c.Field)
		default:
			if err := s.validCondition(ctx, c); err != nil {
				return fmt.Errorf("%w: %v", ErrInvalidView, err)
			}
		}
	}
	return nil
}

// viewEval is what compiling and placing share: the state read once per call.
type viewEval struct {
	now      int64
	statuses map[int64]replyStatus
	notIn    string // the condition on messages m that they count in a conversation
	unread   string // the condition on messages m that they are unread: your own sent mail never is
	wrote    string // the condition on messages m that they are the user's, or from who they wrote to
	wroteTo  string // its argument
	today    time.Time
}

func (s *Service) newViewEval(ctx context.Context) (*viewEval, error) {
	now := s.clk().Now().Unix()
	statuses, err := s.replyStatuses(ctx, now)
	if err != nil {
		return nil, err
	}
	roles, err := s.folderRoles(ctx)
	if err != nil {
		return nil, err
	}
	var out, sent []string
	for id, r := range roles {
		switch r {
		case "trash", "junk", "drafts":
			out = append(out, strconv.FormatInt(id, 10))
		case "sent":
			sent = append(sent, strconv.FormatInt(id, 10))
		}
	}
	slices.Sort(out)
	inSent := strings.Join(sent, ",")
	unread := `instr(COALESCE(m.flags, ''), '\Seen') = 0 AND m.folder_id NOT IN (` + inSent + `) AND NOT (COALESCE(m.message_id, '') != '' AND EXISTS (SELECT 1 FROM messages sc
		WHERE sc.thread_id = m.thread_id AND sc.message_id = m.message_id AND sc.folder_id IN (` + inSent + `)))`
	notIn := notPurgedCond
	if len(out) > 0 {
		notIn += " AND m.folder_id NOT IN (" + strings.Join(out, ",") + ")"
	}
	people, err := s.mailPeople(ctx, roles)
	if err != nil {
		return nil, err
	}
	addrs := make([]string, 0, len(people.known)+len(people.own))
	for a := range people.known {
		addrs = append(addrs, a)
	}
	for a := range people.own {
		addrs = append(addrs, a)
	}
	list, err := json.Marshal(addrs)
	if err != nil {
		return nil, err
	}
	const bare = `CASE WHEN instr(m.from_addr, '<') > 0 AND instr(m.from_addr, '>') > instr(m.from_addr, '<')
		THEN substr(m.from_addr, instr(m.from_addr, '<') + 1, instr(m.from_addr, '>') - instr(m.from_addr, '<') - 1) ELSE trim(m.from_addr) END`
	wrote := "(lower(" + bare + ") IN (SELECT value FROM json_each(?)) OR m.folder_id IN (" + inSent + "))"
	return &viewEval{now: now, statuses: statuses, notIn: notIn, unread: unread, wrote: wrote, wroteTo: string(list), today: s.clk().Now()}, nil
}

// inThread is the condition on threads t that a message of it meets cond,
// holding on messages m.
func (e *viewEval) inThread(cond string) string {
	return "EXISTS (SELECT 1 FROM messages m WHERE m.thread_id = t.id AND " + e.notIn + " AND " + cond + ")"
}

// onMessage is the condition on messages lm that a copy of it meets cond.
func (e *viewEval) onMessage(cond string) string {
	return "EXISTS (SELECT 1 FROM messages m WHERE (m.id = lm.id OR (COALESCE(lm.message_id, '') != '' AND m.thread_id = lm.thread_id AND m.message_id = lm.message_id)) AND " +
		e.notIn + " AND " + cond + ")"
}

// positive is c without its negation: not_contains holds when no message of a
// conversation meets this, not when one message doesn't.
func positive(c RuleCondition) RuleCondition {
	if c.Op == OpNotContains {
		if c.Field == FieldLabel || c.Field == FieldFolder {
			c.Op = OpIs
		} else {
			c.Op = OpContains
		}
	}
	return c
}

// stateSQL is a state condition on threads t.
func (e *viewEval) stateSQL(c RuleCondition) (string, []any) {
	if c.Field == FieldRead {
		unread := e.inThread(e.unread)
		if c.Value == ViewReadRead {
			return "NOT " + unread, nil
		}
		return unread, nil
	}
	var ids []int64
	days, _ := strconv.Atoi(c.Value)
	for id, st := range e.statuses {
		switch {
		case c.Op == OpIs && st.Kind == ReplyStatusUnreplied,
			c.Op == OpOlderThan && st.Kind == ReplyStatusWaiting && st.Since <= e.now-int64(days)*86400:
			ids = append(ids, id)
		}
	}
	return "t.id IN (SELECT value FROM json_each(?))", []any{intList(ids)}
}

// conditionsSQL is match of conds as a condition on threads t. Their positive
// conditions hold on one message, in their groups too: spread over two, (A or
// B) and C doesn't hold.
func (e *viewEval) conditionsSQL(match string, conds []RuleCondition) (string, []any) {
	if len(conds) == 0 {
		return "1", nil
	}
	sql, args, perMessage := e.treeSQL(match, conds)
	if perMessage {
		sql = "EXISTS (SELECT 1 FROM messages lm WHERE lm.thread_id = t.id AND " + sql + ")"
	}
	return sql, args
}

// treeSQL is match of conds, on messages lm if perMessage, else on threads t alone.
func (e *viewEval) treeSQL(match string, conds []RuleCondition) (string, []any, bool) {
	var parts []string
	var args []any
	perMessage := false
	for _, c := range conds {
		var sql string
		var a []any
		switch {
		case c.Group != nil:
			var on bool
			sql, a, on = e.treeSQL(c.Match, c.Group)
			perMessage = perMessage || on
		case c.Field == FieldReply || c.Field == FieldRead:
			sql, a = e.stateSQL(c)
		case c.Field == FieldDate, c.Field == FieldStarred, c.Field == FieldWrote:
			var yes bool
			sql, a, yes = e.fieldSQL(c)
			if yes {
				sql, perMessage = e.onMessage(sql), true
			} else {
				sql = "NOT " + e.inThread(sql)
			}
		case c.Op == OpNotContains:
			sql, a = positive(c).sql(e.now)
			sql = "NOT " + e.inThread(sql)
		default:
			sql, a = c.sql(e.now)
			sql, perMessage = e.onMessage(sql), true
		}
		parts, args = append(parts, sql), append(args, a...)
	}
	join := " AND "
	if match == MatchAny {
		join = " OR "
	}
	return "(" + strings.Join(parts, join) + ")", args, perMessage
}

// viewDates are the named values of FieldDate.
var viewDates = []string{ViewDateToday, ViewDateYesterday, ViewDateThisWeek, ViewDateLastWeek, ViewDateThisMonth, ViewDateLastMonth}

// viewDateRange is when value of FieldDate starts and ends, in now's local days;
// a week starts on Monday, and a number of days has today as its last.
func viewDateRange(now time.Time, value string) (time.Time, time.Time) {
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	monday := today.AddDate(0, 0, -(int(now.Weekday())+6)%7)
	month := time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
	switch value {
	case ViewDateToday:
		return today, today.AddDate(0, 0, 1)
	case ViewDateYesterday:
		return today.AddDate(0, 0, -1), today
	case ViewDateThisWeek:
		return monday, monday.AddDate(0, 0, 7)
	case ViewDateLastWeek:
		return monday.AddDate(0, 0, -7), monday
	case ViewDateThisMonth:
		return month, month.AddDate(0, 1, 0)
	case ViewDateLastMonth:
		return month.AddDate(0, -1, 0), month
	}
	n, _ := strconv.Atoi(value)
	return today.AddDate(0, 0, 1-n), today.AddDate(0, 0, 1)
}

// fieldSQL is c's positive form on messages m, and whether c asks for it or for its absence.
func (e *viewEval) fieldSQL(c RuleCondition) (string, []any, bool) {
	switch c.Field {
	case FieldDate:
		start, end := viewDateRange(e.today, c.Value)
		return "COALESCE(m.date, 0) >= ? AND COALESCE(m.date, 0) < ?", []any{start.Unix(), end.Unix()}, c.Op != OpNotContains
	case FieldStarred:
		return `instr(COALESCE(m.flags, ''), '\Flagged') > 0`, nil, c.Value == RuleYes
	}
	return e.wrote, []any{e.wroteTo}, c.Value == RuleYes
}

// placedThread is a conversation in a view: group is its index in the
// document's groups, -1 for 其他.
type placedThread struct {
	id, date int64
	group    int
	unread   bool
	manual   bool
}

// placed lists the conversations of d, newest first, each in the first group
// it meets: membership and placement come before any paging, so a group is
// never empty because its rows are on a later page.
// shortcut: reads every member on each call; page in SQL by group if large views get slow.
func (s *Service) placed(ctx context.Context, e *viewEval, viewID int64, d SavedViewDoc) ([]placedThread, error) {
	exceptions := map[string][]int64{}
	if viewID != 0 {
		rows, err := s.db.Query(ctx, "SELECT thread_id, kind FROM saved_view_exceptions WHERE view_id = ?", viewID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows.Values {
			exceptions[store.Str(r[1])] = append(exceptions[store.Str(r[1])], store.Int(r[0]))
		}
	}
	addedSQL := "t.id IN (SELECT value FROM json_each(?))"
	addedArg := intList(exceptions[ViewExceptionAdded])
	groupCase := "-1"
	var args []any
	if len(d.Groups) > 0 {
		var whens []string
		for i, g := range d.Groups {
			sql, a := e.conditionsSQL(g.Match, g.Conditions)
			whens = append(whens, "WHEN "+sql+" THEN "+strconv.Itoa(i))
			args = append(args, a...)
		}
		groupCase = "CASE " + strings.Join(whens, " ") + " ELSE -1 END"
	}
	cols := []string{"t.id", "COALESCE(t.last_date, 0)", groupCase + " AS g", e.inThread(e.unread), addedSQL}
	args = append(args, addedArg)
	where := e.inThread("1")
	if needsStatus(d.Match, d.Conditions) {
		// Only conversations with a 回复状态 can meet them, so all history isn't walked.
		ids := slices.Clone(exceptions[ViewExceptionAdded])
		for id := range e.statuses {
			ids = append(ids, id)
		}
		where += " AND t.id IN (SELECT value FROM json_each(?))"
		args = append(args, intList(ids))
	}
	if d.Account != 0 {
		where += " AND t.account_id = ?"
		args = append(args, d.Account)
	}
	sql, a := e.conditionsSQL(d.Match, d.Conditions)
	where += " AND (" + sql + " OR " + addedSQL + ")"
	args = append(append(args, a...), addedArg)
	where += " AND t.id NOT IN (SELECT value FROM json_each(?))"
	args = append(args, intList(exceptions[ViewExceptionRemoved]))
	rows, err := s.db.Query(ctx, "SELECT "+strings.Join(cols, ", ")+" FROM threads t WHERE "+where+" ORDER BY t.last_date DESC, t.id DESC", args...)
	if err != nil {
		return nil, err
	}
	out := make([]placedThread, len(rows.Values))
	for i, r := range rows.Values {
		out[i] = placedThread{id: store.Int(r[0]), date: store.Int(r[1]), group: int(store.Int(r[2])), unread: store.Int(r[3]) != 0, manual: store.Int(r[4]) != 0}
	}
	return out, nil
}

// needsStatus is whether only a conversation with a 回复状态 can meet match of conds.
func needsStatus(match string, conds []RuleCondition) bool {
	reply := func(c RuleCondition) bool {
		if c.Group != nil {
			return needsStatus(c.Match, c.Group)
		}
		return c.Field == FieldReply
	}
	if match == MatchAny {
		return len(conds) > 0 && !slices.ContainsFunc(conds, func(c RuleCondition) bool { return !reply(c) })
	}
	return slices.ContainsFunc(conds, reply)
}

// usesWaiting is whether conds ask how long the user has waited: only then does
// a row say so, or every mail just sent would.
func usesWaiting(conds []RuleCondition) bool {
	return slices.ContainsFunc(RuleDoc{Conditions: conds}.Rows(), func(c RuleCondition) bool { return c.Field == FieldReply && c.Op == OpOlderThan })
}

// row completes a placed conversation's status, in a view or group whose
// conditions may ask how long it has waited.
func (e *viewEval) row(p placedThread, waiting bool) ViewRow {
	r := ViewRow{ThreadSummary: ThreadSummary{ID: p.id}, Manual: p.manual}
	st, ok := e.statuses[p.id]
	switch {
	case !ok:
	case st.Kind == ReplyStatusUnreplied:
		r.Status, r.NewReply = st.Kind, st.NewReply
	case waiting:
		r.Status, r.WaitingDays = st.Kind, int((e.now-st.Since)/86400)
	}
	return r
}

// groupPage is the page of group after cursor; rev ties a cursor to the
// document it was read from, so one from before an edit starts over.
func (s *Service) groupPage(ctx context.Context, e *viewEval, d SavedViewDoc, all []placedThread, index int, rev int64, cursor string, limit int) (ViewGroupPage, error) {
	page := ViewGroupPage{ID: ViewGroupOther}
	waiting := usesWaiting(d.Conditions)
	if index >= 0 {
		g := d.Groups[index]
		page.ID, page.Name = g.ID, g.Name
		waiting = waiting || usesWaiting(g.Conditions)
	}
	var rows []placedThread
	for _, p := range all {
		if p.group == index {
			rows = append(rows, p)
		}
	}
	page.Total = len(rows)
	if c, ok := strings.CutPrefix(cursor, strconv.FormatInt(rev, 10)+"."); ok {
		date, id, err := decodeCursor(c)
		if err != nil {
			return ViewGroupPage{}, err
		}
		rows = slices.DeleteFunc(rows, func(p placedThread) bool { return p.date > date || p.date == date && p.id >= id })
	}
	limit = clampLimit(limit)
	if len(rows) > limit {
		last := rows[limit-1]
		page.NextCursor = strconv.FormatInt(rev, 10) + "." + encodeCursor(last.date, last.id)
		rows = rows[:limit]
	}
	items := make([]ThreadSummary, len(rows))
	for i, p := range rows {
		items[i] = ThreadSummary{ID: p.id, LastDate: time.Unix(p.date, 0)}
	}
	if err := s.fillThreads(ctx, items, e.notIn); err != nil {
		return ViewGroupPage{}, err
	}
	if err := s.fillRecipients(ctx, e, items); err != nil {
		return ViewGroupPage{}, err
	}
	subjects, err := s.threadSubjects(ctx, items)
	if err != nil {
		return ViewGroupPage{}, err
	}
	page.Items = make([]ViewRow, len(rows))
	for i, p := range rows {
		r := e.row(p, waiting)
		r.ThreadSummary = items[i]
		r.Subject = subjects[p.id]
		page.Items[i] = r
	}
	return page, nil
}

// fillRecipients names a conversation of only the user's mail by who it went
// to: a row saying "Me" would tell nothing, and 待跟进 is full of them.
func (s *Service) fillRecipients(ctx context.Context, e *viewEval, items []ThreadSummary) error {
	if len(items) == 0 {
		return nil
	}
	roles, err := s.folderRoles(ctx)
	if err != nil {
		return err
	}
	people, err := s.mailPeople(ctx, roles)
	if err != nil {
		return err
	}
	args := make([]any, len(items))
	for i, t := range items {
		args[i] = t.ID
	}
	rows, err := s.db.Query(ctx, "SELECT m.thread_id, m.id, COALESCE(m.message_id, ''), m.from_addr, m.to_addrs, m.folder_id FROM messages m WHERE m.thread_id IN ("+
		placeholders(len(args))+") AND "+e.notIn+" ORDER BY m.date, m.id", args...)
	if err != nil {
		return err
	}
	mine := map[int64]map[string]bool{} // by thread, then logical message: any copy shows it is the user's
	first := map[int64][]string{}
	for _, r := range rows.Values {
		tid, key := store.Int(r[0]), cmp.Or(store.Str(r[2]), "\x00"+store.Str(r[1]))
		if mine[tid] == nil {
			mine[tid] = map[string]bool{}
			first[tid] = jsonList(store.Str(r[4]))
		}
		mine[tid][key] = mine[tid][key] || people.own[ownKey(store.Str(r[3]))] || roles[store.Int(r[5])] == "sent"
	}
	for i, t := range items {
		if slices.Contains(slices.Collect(maps.Values(mine[t.ID])), false) || len(first[t.ID]) == 0 {
			continue
		}
		items[i].Participants = nil
		for _, to := range first[t.ID] {
			items[i].Participants = append(items[i].Participants, displayName(to))
		}
	}
	return nil
}

func (s *Service) threadSubjects(ctx context.Context, items []ThreadSummary) (map[int64]string, error) {
	out := make(map[int64]string, len(items))
	if len(items) == 0 {
		return out, nil
	}
	args := make([]any, len(items))
	for i, t := range items {
		args[i] = t.ID
	}
	rows, err := s.db.Query(ctx, "SELECT id, subject FROM threads WHERE id IN ("+placeholders(len(args))+")", args...)
	if err != nil {
		return nil, err
	}
	for _, r := range rows.Values {
		out[store.Int(r[0])] = store.Str(r[1])
	}
	return out, nil
}

// contents is d's groups, each with a first page, empty ones left out.
func (s *Service) contents(ctx context.Context, viewID, rev int64, d SavedViewDoc, limit int) (ViewContents, error) {
	e, err := s.newViewEval(ctx)
	if err != nil {
		return ViewContents{}, err
	}
	all, err := s.placed(ctx, e, viewID, d)
	if err != nil {
		return ViewContents{}, err
	}
	var out ViewContents
	for i := 0; i <= len(d.Groups); i++ {
		index := i
		if i == len(d.Groups) {
			index = -1 // 其他 comes last
		}
		g, err := s.groupPage(ctx, e, d, all, index, rev, "", limit)
		if err != nil {
			return ViewContents{}, err
		}
		if g.Total > 0 {
			out.Groups = append(out.Groups, g)
		}
	}
	return out, nil
}

// ViewContents lists a view: its non-empty groups, in order, then
// 其他, each with its total and a first page of limit conversations.
func (s *Service) ViewContents(ctx context.Context, id int64, limit int) (ViewContents, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ViewContents{}, err
	}
	defer end()
	b, err := s.savedView(ctx, id)
	if err != nil {
		return ViewContents{}, err
	}
	return s.contents(ctx, b.ID, b.Revision, b.Doc, limit)
}

// ViewGroupPage is the next page of one group, or of 其他 (ViewGroupOther).
// A cursor from before an edit to the view gives the first page.
func (s *Service) ViewGroupPage(ctx context.Context, id int64, groupID, cursor string, limit int) (ViewGroupPage, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ViewGroupPage{}, err
	}
	defer end()
	b, err := s.savedView(ctx, id)
	if err != nil {
		return ViewGroupPage{}, err
	}
	index := -1
	if groupID != ViewGroupOther {
		index = slices.IndexFunc(b.Doc.Groups, func(g ViewGroup) bool { return g.ID == groupID })
		if index < 0 {
			return ViewGroupPage{ID: groupID}, nil // an edit removed it
		}
	}
	e, err := s.newViewEval(ctx)
	if err != nil {
		return ViewGroupPage{}, err
	}
	all, err := s.placed(ctx, e, b.ID, b.Doc)
	if err != nil {
		return ViewGroupPage{}, err
	}
	return s.groupPage(ctx, e, b.Doc, all, index, b.Revision, cursor, limit)
}

// PreviewView lists a document as ViewContents would once saved:
// with viewID (0: a new one), that view's exceptions apply.
func (s *Service) PreviewView(ctx context.Context, d SavedViewDoc, viewID int64, limit int) (ViewContents, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ViewContents{}, err
	}
	defer end()
	if err := s.validViewDoc(ctx, d); err != nil {
		return ViewContents{}, err
	}
	return s.contents(ctx, viewID, 0, d, limit)
}

// SavedViews returns the views in sidebar order, with their
// unread counts.
func (s *Service) SavedViews(ctx context.Context) ([]SavedView, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	out, err := s.savedViews(ctx, "")
	if err != nil || len(out) == 0 {
		return out, err
	}
	e, err := s.newViewEval(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		all, err := s.placed(ctx, e, out[i].ID, out[i].Doc)
		if err != nil {
			return nil, err
		}
		for _, p := range all {
			if p.unread {
				out[i].Unread++
			}
		}
	}
	return out, nil
}

func (s *Service) savedViews(ctx context.Context, where string, args ...any) ([]SavedView, error) {
	rows, err := s.db.Query(ctx, "SELECT id, name, icon, doc, revision FROM saved_views "+where+" ORDER BY position, id", args...)
	if err != nil {
		return nil, err
	}
	out := make([]SavedView, 0, len(rows.Values))
	for _, r := range rows.Values {
		b := SavedView{ID: store.Int(r[0]), Name: store.Str(r[1]), Icon: store.Str(r[2]), Revision: store.Int(r[4])}
		if err := json.Unmarshal([]byte(store.Str(r[3])), &b.Doc); err != nil {
			return nil, fmt.Errorf("view %d: %w", b.ID, err)
		}
		out = append(out, b)
	}
	return out, nil
}

func (s *Service) savedView(ctx context.Context, id int64) (SavedView, error) {
	out, err := s.savedViews(ctx, "WHERE id = ?", id)
	if err != nil {
		return SavedView{}, err
	}
	if len(out) == 0 {
		return SavedView{}, fmt.Errorf("view %d not found", id)
	}
	return out[0], nil
}

// SaveView stores a view (ID 0: a new one, last in the sidebar) and
// returns its id.
func (s *Service) SaveView(ctx context.Context, b SavedView) (int64, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	if err := s.validSavedView(ctx, b.Name, b.Icon, b.Doc); err != nil {
		return 0, err
	}
	b.Doc.Version = SavedViewVersion
	doc, err := json.Marshal(b.Doc)
	if err != nil {
		return 0, err
	}
	id := b.ID
	if id == 0 {
		res, err := s.db.Execute(ctx, `INSERT INTO saved_views(name, icon, doc, position)
			VALUES (?, ?, ?, (SELECT COALESCE(max(position), 0) + 1 FROM saved_views))`, b.Name, b.Icon, string(doc))
		if err != nil {
			return 0, err
		}
		id = res.LastInsertID
	} else {
		res, err := s.db.Execute(ctx, `UPDATE saved_views SET name = ?, icon = ?, doc = ?, revision = revision + 1 WHERE id = ?`, b.Name, b.Icon, string(doc), id)
		if err != nil {
			return 0, err
		}
		if res.Changes == 0 {
			return 0, fmt.Errorf("view %d not found", id)
		}
	}
	s.publishView()
	return id, nil
}

// DeleteView removes the view; its mail and rules stay as they are.
func (s *Service) DeleteView(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	if _, err := s.db.Execute(ctx, "DELETE FROM saved_views WHERE id = ?", id); err != nil {
		return err
	}
	s.publishView()
	return nil
}

// ReorderViews sets the sidebar order.
func (s *Service) ReorderViews(ctx context.Context, ids []int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	stmts := make([]sqlite.Statement, len(ids))
	for i, id := range ids {
		stmts[i] = sqlite.Statement{SQL: "UPDATE saved_views SET position = ? WHERE id = ?", Args: []any{i, id}}
	}
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		return err
	}
	s.publishView()
	return nil
}

func (s *Service) publishView() { s.bus.Publish(events.Event{Type: events.LabelsChanged}) }

// SetViewException adds a conversation to a view or removes it by
// hand; kind "" takes that back.
func (s *Service) SetViewException(ctx context.Context, id, threadID int64, kind string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	if kind == "" {
		_, err = s.db.Execute(ctx, "DELETE FROM saved_view_exceptions WHERE view_id = ? AND thread_id = ?", id, threadID)
	} else {
		if kind != ViewExceptionAdded && kind != ViewExceptionRemoved {
			return fmt.Errorf("unknown exception %q", kind)
		}
		_, err = s.db.Execute(ctx, `INSERT INTO saved_view_exceptions(view_id, thread_id, kind) VALUES (?, ?, ?)
			ON CONFLICT(view_id, thread_id) DO UPDATE SET kind = excluded.kind`, id, threadID, kind)
	}
	if err != nil {
		return err
	}
	s.publishView()
	return nil
}

// ViewExceptions lists a view's exceptions, to see and undo them.
func (s *Service) ViewExceptions(ctx context.Context, id int64) ([]ViewException, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	rows, err := s.db.Query(ctx, `SELECT x.thread_id, x.kind, t.subject,
		(SELECT m.from_addr FROM messages m WHERE m.thread_id = x.thread_id ORDER BY m.date DESC, m.id DESC LIMIT 1)
		FROM saved_view_exceptions x JOIN threads t ON t.id = x.thread_id WHERE x.view_id = ? ORDER BY x.thread_id`, id)
	if err != nil {
		return nil, err
	}
	out := make([]ViewException, len(rows.Values))
	for i, r := range rows.Values {
		out[i] = ViewException{ThreadID: store.Int(r[0]), Kind: store.Str(r[1]), Subject: store.Str(r[2]), From: displayName(store.Str(r[3]))}
	}
	return out, nil
}

// ViewWhy says why a conversation is in a view, for the reader.
func (s *Service) ViewWhy(ctx context.Context, id, threadID int64) (ViewReason, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ViewReason{}, err
	}
	defer end()
	b, err := s.savedView(ctx, id)
	if err != nil {
		return ViewReason{}, err
	}
	e, err := s.newViewEval(ctx)
	if err != nil {
		return ViewReason{}, err
	}
	all, err := s.placed(ctx, e, b.ID, b.Doc)
	if err != nil {
		return ViewReason{}, err
	}
	at := slices.IndexFunc(all, func(p placedThread) bool { return p.id == threadID })
	if at < 0 {
		return ViewReason{}, nil
	}
	p := all[at]
	why := ViewReason{Member: true, Manual: p.manual, GroupID: ViewGroupOther}
	conds, match := b.Doc.Conditions, b.Doc.Match
	waiting := usesWaiting(conds)
	if p.group >= 0 {
		g := b.Doc.Groups[p.group]
		why.GroupID, why.GroupName = g.ID, g.Name
		conds, match = g.Conditions, g.Match
		waiting = waiting || usesWaiting(conds)
	}
	r := e.row(p, waiting)
	why.Status, why.NewReply, why.Days = r.Status, r.NewReply, r.WaitingDays
	for _, c := range (RuleDoc{Conditions: conds}).Rows() {
		sql, args := e.conditionsSQL(cmp.Or(match, MatchAll), []RuleCondition{c})
		rows, err := s.db.Query(ctx, "SELECT 1 FROM threads t WHERE t.id = ? AND "+sql, append([]any{threadID}, args...)...)
		if err != nil {
			return ViewReason{}, err
		}
		if len(rows.Values) > 0 {
			why.Condition = c
			break
		}
	}
	return why, nil
}

// MarkViewRead marks the view's conversations read and returns how
// many messages that changed.
func (s *Service) MarkViewRead(ctx context.Context, id int64) (int, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return 0, err
	}
	defer end()
	b, err := s.savedView(ctx, id)
	if err != nil {
		return 0, err
	}
	e, err := s.newViewEval(ctx)
	if err != nil {
		return 0, err
	}
	all, err := s.placed(ctx, e, b.ID, b.Doc)
	if err != nil {
		return 0, err
	}
	var threads []any
	for _, p := range all {
		if p.unread {
			threads = append(threads, p.id)
		}
	}
	var ids []int64
	for c := range slices.Chunk(threads, 200) {
		rows, err := s.db.Query(ctx, "SELECT m.id FROM messages m WHERE m.thread_id IN ("+placeholders(len(c))+") AND "+e.notIn+" AND "+e.unread, c...)
		if err != nil {
			return 0, err
		}
		for _, r := range rows.Values {
			ids = append(ids, store.Int(r[0]))
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return len(ids), s.setFlagsByAccount(ctx, ids, []string{`\Seen`}, nil)
}

// ArchiveUndo is what UndoArchiveThreads puts back.
type ArchiveUndo struct {
	Moved []ArchivedMessage
	Marks []ArchiveMarkBefore
}

// ArchiveMarkBefore is a conversation's archive mark before an archive (Had false: none).
type ArchiveMarkBefore struct {
	ThreadID int64
	Had      bool
	MID      string
	Date     int64
}

// newestCounted is each conversation's newest counted message: its mark, and
// the row ids of its copies.
func (s *Service) newestCounted(ctx context.Context, threads []any) (map[int64]archiveMark, map[int64][]int64, error) {
	roles, err := s.folderRoles(ctx)
	if err != nil {
		return nil, nil, err
	}
	marks := map[int64]archiveMark{}
	copies := map[int64][]int64{}
	for c := range slices.Chunk(threads, 200) {
		rows, err := s.db.Query(ctx, "SELECT m.thread_id, m.id, COALESCE(m.message_id, ''), COALESCE(m.date, 0), m.folder_id FROM messages m WHERE m.thread_id IN ("+placeholders(len(c))+") AND "+notPurgedCond+" ORDER BY m.date, m.id", c...)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows.Values {
			if role := roles[store.Int(r[4])]; role == "trash" || role == "junk" || role == "drafts" {
				continue
			}
			tid, id, mid, date := store.Int(r[0]), store.Int(r[1]), store.Str(r[2]), store.Int(r[3])
			if m := marks[tid]; mid != "" && m.mid == mid {
				copies[tid] = append(copies[tid], id)
				continue
			}
			marks[tid], copies[tid] = archiveMark{has: true, mid: mid, date: date}, []int64{id} // rows come oldest first
		}
	}
	return marks, copies, nil
}

// markArchived records the marks and returns the ones they replace.
func (s *Service) markArchived(ctx context.Context, marks map[int64]archiveMark) ([]ArchiveMarkBefore, error) {
	if len(marks) == 0 {
		return nil, nil
	}
	had, err := s.archiveMarks(ctx)
	if err != nil {
		return nil, err
	}
	var stmts []sqlite.Statement
	var before []ArchiveMarkBefore
	for tid, m := range marks {
		h := had[tid]
		before = append(before, ArchiveMarkBefore{ThreadID: tid, Had: h.has, MID: h.mid, Date: h.date})
		stmts = append(stmts, sqlite.Statement{SQL: `INSERT INTO archive_marks(thread_id, message_id_hdr, date) VALUES (?, ?, ?)
			ON CONFLICT(thread_id) DO UPDATE SET message_id_hdr = excluded.message_id_hdr, date = excluded.date`, Args: []any{tid, m.mid, m.date}})
	}
	_, err = s.db.Transaction(ctx, stmts)
	return before, err
}

// ArchiveMessages moves the messages to folderID, the archive folder of their
// account, and marks the conversations whose newest message is among them, so
// that 回复状态 leaves them until newer mail comes.
func (s *Service) ArchiveMessages(ctx context.Context, ids []int64, folderID int64) (ArchiveUndo, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ArchiveUndo{}, err
	}
	defer end()
	s.editMu.Lock()
	defer s.editMu.Unlock()
	acc, ts, err := s.targets(ctx, ids)
	if err != nil {
		return ArchiveUndo{}, err
	}
	rows, err := s.db.Query(ctx, "SELECT 1 FROM folders WHERE id = ? AND account_id = ?", folderID, acc)
	if err != nil {
		return ArchiveUndo{}, err
	}
	if len(ts) > 0 && len(rows.Values) == 0 {
		return ArchiveUndo{}, errors.New("the folder belongs to another account")
	}
	var undo ArchiveUndo
	threads := map[int64]bool{}
	inIDs := map[int64]bool{}
	for _, t := range ts {
		inIDs[t.id] = true
		if t.thread != 0 {
			threads[t.thread] = true
		}
		if t.folder != folderID {
			undo.Moved = append(undo.Moved, ArchivedMessage{ID: t.id, From: t.folder, To: folderID})
		}
	}
	var list []any
	for t := range threads {
		list = append(list, t)
	}
	marks, copies, err := s.newestCounted(ctx, list)
	if err != nil {
		return ArchiveUndo{}, err
	}
	for t, c := range copies {
		if !slices.ContainsFunc(c, func(id int64) bool { return inIDs[id] }) {
			delete(marks, t) // an older message of it: its newest still counts
		}
	}
	if undo.Marks, err = s.markArchived(ctx, marks); err != nil {
		return ArchiveUndo{}, err
	}
	if err := s.move(ctx, acc, ts, folderID); err != nil {
		return ArchiveUndo{}, err
	}
	return undo, nil
}

// ArchiveThreads archives whole conversations, as a view does: copies
// in the inbox move to their account's archive, Sent copies never, and each
// conversation is marked, so one of only sent mail just records it. An account
// with mail to move and no archive folder is ErrNoRoleFolder, and nothing of it
// is recorded.
func (s *Service) ArchiveThreads(ctx context.Context, threadIDs []int64) (ArchiveUndo, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ArchiveUndo{}, err
	}
	defer end()
	s.editMu.Lock()
	defer s.editMu.Unlock()
	roles, err := s.folderRoles(ctx)
	if err != nil {
		return ArchiveUndo{}, err
	}
	var list []any
	for _, t := range threadIDs {
		list = append(list, t)
	}
	byAccount := map[int64][]int64{}
	for c := range slices.Chunk(list, 200) {
		rows, err := s.db.Query(ctx, "SELECT m.id, m.account_id, m.folder_id FROM messages m WHERE m.thread_id IN ("+placeholders(len(c))+") AND "+notPurgedCond, c...)
		if err != nil {
			return ArchiveUndo{}, err
		}
		for _, r := range rows.Values {
			if roles[store.Int(r[2])] == "inbox" {
				byAccount[store.Int(r[1])] = append(byAccount[store.Int(r[1])], store.Int(r[0]))
			}
		}
	}
	folders := map[int64]int64{}
	for acc := range byAccount {
		if folders[acc], err = s.roleFolder(ctx, acc, "archive"); err != nil {
			return ArchiveUndo{}, err
		}
	}
	marks, _, err := s.newestCounted(ctx, list)
	if err != nil {
		return ArchiveUndo{}, err
	}
	var undo ArchiveUndo
	if undo.Marks, err = s.markArchived(ctx, marks); err != nil {
		return ArchiveUndo{}, err
	}
	for acc, ids := range byAccount {
		a, ts, err := s.targets(ctx, ids)
		if err != nil {
			return ArchiveUndo{}, err
		}
		for _, t := range ts {
			undo.Moved = append(undo.Moved, ArchivedMessage{ID: t.id, From: t.folder, To: folders[acc]})
		}
		if err := s.move(ctx, a, ts, folders[acc]); err != nil {
			return ArchiveUndo{}, err
		}
	}
	if len(byAccount) == 0 {
		s.bus.Publish(events.Event{Type: events.MessagesChanged, ThreadIDs: threadIDs})
	}
	return undo, nil
}

// UndoArchiveThreads puts back what an archive moved and marked.
func (s *Service) UndoArchiveThreads(ctx context.Context, u ArchiveUndo) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	var stmts []sqlite.Statement
	var threads []int64
	for _, m := range u.Marks {
		threads = append(threads, m.ThreadID)
		if m.Had {
			stmts = append(stmts, sqlite.Statement{SQL: "UPDATE archive_marks SET message_id_hdr = ?, date = ? WHERE thread_id = ?", Args: []any{m.MID, m.Date, m.ThreadID}})
		} else {
			stmts = append(stmts, sqlite.Statement{SQL: "DELETE FROM archive_marks WHERE thread_id = ?", Args: []any{m.ThreadID}})
		}
	}
	if len(stmts) > 0 {
		if _, err := s.db.Transaction(ctx, stmts); err != nil {
			return err
		}
	}
	if err := s.UndoArchive(ctx, SenderArchive{Moved: u.Moved}); err != nil {
		return err
	}
	if len(u.Moved) == 0 {
		s.bus.Publish(events.Event{Type: events.MessagesChanged, ThreadIDs: threads})
	}
	return nil
}

// ViewTemplate is a view to start from.
type ViewTemplate struct {
	ID, Name, Icon string
	Doc            SavedViewDoc
}

func viewText(def string, byLang map[string]string) plugins.Text {
	return plugins.Text{Default: def, ByLang: byLang}
}

var (
	textToReply = viewText("To reply", map[string]string{"zh-Hans": "待回复", "zh-Hant": "待回覆", "ja": "返信待ち", "ko": "답장 필요",
		"de": "Zu beantworten", "fr": "À répondre", "es": "Por responder", "pt": "Por responder", "it": "Da rispondere", "ru": "Нужен ответ", "nl": "Te beantwoorden", "tr": "Yanıtlanacak", "vi": "Cần trả lời"})
	textFollowUp = viewText("To follow up", map[string]string{"zh-Hans": "待跟进", "zh-Hant": "待跟進", "ja": "フォローアップ", "ko": "후속 조치",
		"de": "Nachfassen", "fr": "À relancer", "es": "Por seguir", "pt": "Por acompanhar", "it": "Da sollecitare", "ru": "Напомнить", "nl": "Op te volgen", "tr": "Takip edilecek", "vi": "Cần theo dõi"})
	textToHandle = viewText("To handle", map[string]string{"zh-Hans": "待处理", "zh-Hant": "待處理", "ja": "未処理", "ko": "처리 필요",
		"de": "Zu erledigen", "fr": "À traiter", "es": "Por gestionar", "pt": "Por tratar", "it": "Da gestire", "ru": "Нужно обработать", "nl": "Te behandelen", "tr": "İşlenecek", "vi": "Cần xử lý"})
	textUnread = viewText("Unread", map[string]string{"zh-Hans": "未读", "zh-Hant": "未讀", "ja": "未読", "ko": "읽지 않음",
		"de": "Ungelesen", "fr": "Non lus", "es": "No leídos", "pt": "Não lidas", "it": "Non letti", "ru": "Непрочитанные", "nl": "Ongelezen", "tr": "Okunmamış", "vi": "Chưa đọc"})
	textClient = viewText("Client", map[string]string{"zh-Hans": "客户", "zh-Hant": "客戶", "ja": "クライアント", "ko": "고객",
		"de": "Kunde", "fr": "Client", "es": "Cliente", "pt": "Cliente", "it": "Cliente", "ru": "Клиент", "nl": "Klant", "tr": "Müşteri", "vi": "Khách hàng"})
	textInvoices = viewText("Invoices", map[string]string{"zh-Hans": "票据", "zh-Hant": "票據", "ja": "請求書", "ko": "청구서",
		"de": "Rechnungen", "fr": "Factures", "es": "Facturas", "pt": "Faturas", "it": "Fatture", "ru": "Счета", "nl": "Facturen", "tr": "Faturalar", "vi": "Hóa đơn"})
	textInvoiceWord = viewText("receipt", map[string]string{"zh-Hans": "发票", "zh-Hant": "發票", "ja": "請求書", "ko": "청구서",
		"de": "Rechnung", "fr": "facture", "es": "factura", "pt": "fatura", "it": "fattura", "ru": "счёт", "nl": "factuur", "tr": "fatura", "vi": "hóa đơn"})
	textNewsletters = viewText("Newsletters", map[string]string{"zh-Hans": "订阅", "zh-Hant": "訂閱", "ja": "ニュースレター", "ko": "뉴스레터",
		"de": "Newsletter", "fr": "Newsletters", "es": "Boletines", "pt": "Newsletters", "it": "Newsletter", "ru": "Рассылки", "nl": "Nieuwsbrieven", "tr": "Bültenler", "vi": "Bản tin"})
)

// toHandleLeavesOut are the labels 待处理 leaves out: nobody replies to them.
var toHandleLeavesOut = []string{"otp", "notice", "newsletter"}

// toHandleDoc is 待处理, in lang, leaving out those of toHandleLeavesOut that exist: a view like any other.
func (s *Service) toHandleDoc(ctx context.Context, lang string) (SavedViewDoc, error) {
	rows, err := s.db.Query(ctx, "SELECT slug FROM labels")
	if err != nil {
		return SavedViewDoc{}, err
	}
	var out []RuleCondition
	for _, slug := range toHandleLeavesOut {
		if slices.ContainsFunc(rows.Values, func(r []any) bool { return store.Str(r[0]) == slug }) {
			out = append(out, RuleCondition{Field: FieldLabel, Op: OpNotContains, Value: slug})
		}
	}
	unreplied := RuleCondition{Field: FieldReply, Op: OpIs, Value: ViewReplyUnreplied}
	waiting := RuleCondition{Field: FieldReply, Op: OpOlderThan, Value: "3"}
	d := SavedViewDoc{Version: SavedViewVersion, Match: MatchAny, Conditions: []RuleCondition{unreplied, waiting}, Groups: []ViewGroup{
		{ID: "reply", Name: textToReply.For(lang), Match: MatchAll, Conditions: []RuleCondition{unreplied}},
		{ID: "wait", Name: textFollowUp.For(lang), Match: MatchAll, Conditions: []RuleCondition{waiting}},
	}}
	if len(out) > 0 {
		d.Match, d.Conditions = MatchAll, []RuleCondition{{Match: MatchAny, Group: d.Conditions}, {Match: MatchAll, Group: out}}
	}
	return d, nil
}

// seedSavedViews writes 待处理 once, in the UI language (the default one when
// none is known yet). From then on it is the user's: one they deleted stays gone.
func (s *Service) seedSavedViews(ctx context.Context) error {
	rows, err := s.db.Query(ctx, "SELECT 1 FROM meta WHERE key = 'saved_views_seeded'")
	if err != nil || len(rows.Values) > 0 {
		return err
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	lang := cmp.Or(st.UILanguage, s.opts.SystemLanguage)
	toHandle, err := s.toHandleDoc(ctx, lang)
	if err != nil {
		return err
	}
	doc, err := json.Marshal(toHandle)
	if err != nil {
		return err
	}
	_, err = s.db.Transaction(ctx, []sqlite.Statement{
		{SQL: "INSERT INTO saved_views(name, icon, doc, position) VALUES (?, 'chat-3', ?, 0)", Args: []any{textToHandle.For(lang), string(doc)}},
		{SQL: "INSERT INTO meta(key, value) VALUES ('saved_views_seeded', '1')"}})
	return err
}

// ViewTemplates are the views to start from, in the UI language.
func (s *Service) ViewTemplates(ctx context.Context) ([]ViewTemplate, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	lang := cmp.Or(st.UILanguage, s.opts.SystemLanguage)
	reply := ViewGroup{ID: "reply", Name: textToReply.For(lang), Match: MatchAll, Conditions: []RuleCondition{{Field: FieldReply, Op: OpIs, Value: ViewReplyUnreplied}}}
	out := []ViewTemplate{
		{ID: "client", Name: textClient.For(lang), Icon: "team", Doc: SavedViewDoc{Match: MatchAll,
			Conditions: []RuleCondition{{Field: FieldFrom, Op: OpContains}}, Groups: []ViewGroup{reply}}},
		{ID: "invoices", Name: textInvoices.For(lang), Icon: "bill", Doc: SavedViewDoc{Match: MatchAny,
			Conditions: []RuleCondition{{Field: FieldSubject, Op: OpContains, Value: "invoice"}, {Field: FieldSubject, Op: OpContains, Value: textInvoiceWord.For(lang)}},
			Groups:     []ViewGroup{{ID: "open", Name: textToHandle.For(lang), Match: MatchAll, Conditions: []RuleCondition{{Field: FieldFolder, Op: OpIs, Value: "inbox"}}}}}},
	}
	if s.labelExists(ctx, "newsletter") == nil { // the user may have deleted it
		out = append(out, ViewTemplate{ID: "newsletters", Name: textNewsletters.For(lang), Icon: "newspaper", Doc: SavedViewDoc{Match: MatchAll,
			Conditions: []RuleCondition{{Field: FieldLabel, Op: OpIs, Value: "newsletter"}},
			Groups:     []ViewGroup{{ID: "unread", Name: textUnread.For(lang), Match: MatchAll, Conditions: []RuleCondition{{Field: FieldRead, Op: OpIs, Value: ViewReadUnread}}}}}})
	}
	toHandle, err := s.toHandleDoc(ctx, lang)
	if err != nil {
		return nil, err
	}
	return append(out, ViewTemplate{ID: "to_handle", Name: textToHandle.For(lang), Icon: "chat-3", Doc: toHandle}), nil
}

// setFlagsByAccount sets flags on messages of any accounts.
func (s *Service) setFlagsByAccount(ctx context.Context, ids []int64, add, remove []string) error {
	rows, err := s.db.Query(ctx, "SELECT DISTINCT account_id FROM messages WHERE id IN (SELECT value FROM json_each(?))", intList(ids))
	if err != nil {
		return err
	}
	for _, r := range rows.Values {
		acc := store.Int(r[0])
		of, err := s.db.Query(ctx, "SELECT id FROM messages WHERE account_id = ? AND id IN (SELECT value FROM json_each(?))", acc, intList(ids))
		if err != nil {
			return err
		}
		var mine []int64
		for _, v := range of.Values {
			mine = append(mine, store.Int(v[0]))
		}
		if err := s.SetFlags(ctx, mine, add, remove); err != nil {
			return err
		}
	}
	return nil
}

// intList is ids for json_each: never "null", whose one NULL row makes every
// NOT IN false.
func intList(ids []int64) string {
	if len(ids) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(ids)
	return string(b)
}
