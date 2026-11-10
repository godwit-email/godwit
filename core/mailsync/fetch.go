// Package mailsync stores what an account's protocol backend reports (ADR
// 0037): folders, server locations, claims, orphans, merges and threads.
package mailsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// PartFetchBytes is how much of one text part a sync downloads: enough encoded
// bytes for mail.MaxBodyBytes of text even in quoted-printable. Tests lower it.
var PartFetchBytes int64 = 3 * mail.MaxBodyBytes

// BackfillPage is how many messages one page of a sync asks for, which bounds
// its memory. Tests lower it.
var BackfillPage = 1000

// BackfillPages is how many pages a range backfill takes per sync round
// before it yields, so new mail, ops and SyncNow wait at most that long; the
// next round goes on. Tests lower it.
var BackfillPages = 2

// headerFields are the header fields a sync stores of each message.
var headerFields = []string{"MESSAGE-ID", "DATE", "SUBJECT", "FROM", "TO", "CC", "REFERENCES", "IN-REPLY-TO", "REPLY-TO", "LIST-UNSUBSCRIBE", "LIST-UNSUBSCRIBE-POST", "LIST-ID", "AUTO-SUBMITTED", "PRECEDENCE", "AUTHENTICATION-RESULTS", "DKIM-SIGNATURE", "RECEIVED", "MIME-VERSION", "CONTENT-TYPE", "X-GODWIT-RULE"}

// account is a sync's view of the account: limit > 0 keeps the newest limit
// messages per folder, else months > 0 the mail since the cutoff, else all.
type account struct {
	id        int64
	email     string
	limit     int
	months    int
	cursor    string // the backend's account cursor
	noSelf    bool   // a local archive: email is a placeholder, not the user's address
	inboxOnly bool   // InboxOnly
}

// since is the start of a months range: date-only, as IMAP compares
// INTERNALDATE by day; zero for all mail.
func (a *account) since(now time.Time) time.Time {
	if a.months <= 0 {
		return time.Time{}
	}
	y, m, d := now.AddDate(0, -a.months, 0).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// result collects what one sync changed, for the events it publishes.
type result struct {
	received, threads, changed, changedThreads []int64
	orphans                                    []int64 // messages of a stable identity that lost a server location, for the round's end
	expunged                                   []int64 // of them, gone from Trash or Spam: Gmail deleted them for good
	conflicts                                  []string
	// inboxOnly: the account's server has only an inbox (InboxOnly), so a
	// location gone from it only loses its server_ref; originals keeps the
	// originals of its mail, when the caller gave one.
	inboxOnly bool
	originals Originals
	dropped   []string // content hashes of messages deleted for good, for originals.Drop
}

// gone records messages about to be deleted (or moved between threads) as changed.
func (r *result) gone(ids, tids []int64) {
	r.changed = append(r.changed, ids...)
	r.changedThreads = append(r.changedThreads, tids...)
}

// merged repoints thread IDs collected so far from the merged-away threads to target.
func (r *result) merged(target int64, others []int64) {
	for _, l := range [][]int64{r.threads, r.changedThreads} {
		for i, t := range l {
			if slices.Contains(others, t) {
				l[i] = target
			}
		}
	}
	r.changedThreads = append(r.changedThreads, target)
}

// Fetch syncs the folders of the account in only (nil: every folder) through
// recv, then, after a round of every folder, refreshes the
// Authentication-Results of cached messages. more reports that a backfill
// has work left: the caller should sync again soon. originals, at most one,
// keeps the originals of an inbox-only account's mail.
func Fetch(ctx context.Context, db *store.DB, recv backend.Receiver, accountID int64, bus *events.Bus, only []backend.FolderRef, originals ...Originals) (more bool, err error) {
	if more, err = run(ctx, db, recv, accountID, bus, only, 0, originals...); err != nil || only != nil {
		return more, err
	}
	again, err := refreshAuthenticationResults(ctx, db, recv, accountID, bus)
	return more || again, err
}

// Sync runs one sync round of the account through recv and stores the change
// set it reports. Rows committed before an error are still announced.
func Sync(ctx context.Context, db *store.DB, recv backend.Receiver, accountID int64, bus *events.Bus) (more bool, err error) {
	return run(ctx, db, recv, accountID, bus, nil, 0)
}

// run is Sync of the folders in only (nil: all), or with older > 0 one page
// of older mail in only. A round of some folders leaves what needs every
// folder synced (orphans, hidden locations, claims) to the next full round.
func run(ctx context.Context, db *store.DB, recv backend.Receiver, accountID int64, bus *events.Bus, only []backend.FolderRef, older int, originals ...Originals) (more bool, err error) {
	var r result
	complete := false
	defer func() {
		if complete {
			err = errors.Join(err, r.logArrivals(context.WithoutCancel(ctx), db, accountID))
		}
		r.publish(bus, accountID)
		if r.originals != nil && len(r.dropped) > 0 {
			r.originals.Drop(r.dropped)
		}
	}()
	a, err := loadAccount(ctx, db, accountID)
	if err != nil {
		return false, err
	}
	if r.inboxOnly = a.inboxOnly; r.inboxOnly && len(originals) > 0 {
		r.originals = originals[0]
	}
	s := &syncRun{ctx: ctx, db: db, a: a, r: &r, stash: map[int64][]stashed{}, deferred: map[int64]backend.FolderState{}}
	if err := s.loadFolders(ctx); err != nil {
		return false, err
	}
	if older == 0 {
		d, err := recv.Describe(ctx)
		if err != nil {
			return false, err
		}
		s.stable = d.Identity == backend.IdentityStable
		s.allMail = d.Features[backend.FeatureAllMail].Level == backend.LevelSupported
	}
	states := map[backend.FolderRef]backend.FolderState{}
	for ref, f := range s.folders {
		states[ref] = f.state()
	}
	more, err = recv.Sync(ctx, backend.SyncRequest{
		Account: backend.Cursor(a.cursor), Folders: states, Only: only, Older: older,
		Window: backend.Window{Newest: a.limit, Since: a.since(time.Now())}, Pages: BackfillPages, PageSize: BackfillPage,
		HeaderFields: headerFields, TextBytes: PartFetchBytes, TextParts: textRefs, Local: s,
	}, s.commit)
	// A folder the server refused was skipped; the others completed.
	var refused *backend.FolderError
	if err != nil && !errors.As(err, &refused) {
		return more, err // a move's claim still deferred waits for the next round, its cursor too
	}
	if ferr := s.flushAll(ctx); ferr != nil {
		return more, ferr
	}
	if older > 0 {
		return more, err
	}
	complete = true
	if err != nil || only != nil { // a skipped folder may yet hold a membership, a claim or a stale location
		return more, err
	}
	if r.inboxOnly { // what is no longer anywhere on the server stays, unless deleted for good
		purged, _, err := doomed(ctx, db, "account_id = ? AND EXISTS (SELECT 1 FROM ops WHERE message_id = messages.id AND kind = 'purge' AND state = 'done')", accountID)
		if err != nil {
			return more, err
		}
		r.orphans = purged
	}
	if err := dropOrphans(ctx, db, accountID, r.orphans, true, s.stable, s.allMail, &r); err != nil {
		return more, err
	}
	if s.stable {
		if err := reshow(ctx, db, accountID, &r); err != nil {
			return more, err
		}
	}
	return more, unclaimable(ctx, db, accountID, &r)
}

// syncRun stores one Sync's change set; it is the backend's backend.Local.
type syncRun struct {
	ctx     context.Context
	db      *store.DB
	a       *account
	r       *result
	stable  bool // the account's identity is stable (Gmail, JMAP): orphans wait for the round's end (ADR 0034)
	allMail bool // backend.FeatureAllMail (Gmail): a message in no folder synced may still exist
	folders map[backend.FolderRef]*folder
	merged  bool // the round merged the messages sharing a server_id
	// lost is what Lost reported: a message the backend finds again keeps its
	// flags until a snapshot of the folder it is found in.
	lost map[string]bool
	// stash holds a folder's arrivals that may claim a move awaiting its
	// claim, until the folder's arrivals end: a claim needs their count
	// across every page. Its cursor waits with them (deferred).
	stash    map[int64][]stashed
	deferred map[int64]backend.FolderState
}

type stashed struct {
	m backend.MessageAppeared
	p *mail.Parsed
}

func (s *syncRun) commit(p backend.Page) error {
	ctx := s.ctx
	var updated []backend.Folder
	var gone []backend.FolderRef
	for _, c := range p.Changes {
		switch c := c.(type) {
		case backend.FolderUpdated:
			updated = append(updated, c.Folder)
		case backend.FolderGone:
			gone = append(gone, c.Ref)
		}
	}
	if len(updated)+len(gone) > 0 {
		if err := s.applyFolders(ctx, updated, gone); err != nil {
			return err
		}
	}
	var run []backend.MessageAppeared // consecutive appearances in one folder
	flush := func() error {
		if len(run) == 0 {
			return nil
		}
		err := s.appeared(ctx, run)
		run = nil
		return err
	}
	// Consecutive increments (MessageGone, FlagsChanged) in one folder.
	var incFolder backend.FolderRef
	var incRefs []backend.MessageRef
	incFlags := map[backend.MessageRef]backend.Flags{}
	flushInc := func() error {
		if len(incRefs) == 0 {
			return nil
		}
		f, err := s.folder(incFolder)
		if err == nil {
			if err = s.flushStash(ctx, f); err == nil {
				err = applyFlags(ctx, s.db, s.stable, s.a.id, f, incFlags, incRefs, s.r)
			}
		}
		incRefs, incFlags = nil, map[backend.MessageRef]backend.Flags{}
		return err
	}
	increment := func(at backend.Location, fl *backend.Flags) error {
		if len(incRefs) > 0 && incFolder != at.Folder {
			if err := flushInc(); err != nil {
				return err
			}
		}
		incFolder, incRefs = at.Folder, append(incRefs, at.Ref)
		if fl != nil {
			incFlags[at.Ref] = *fl
		} else {
			delete(incFlags, at.Ref)
		}
		return nil
	}
	for _, c := range p.Changes {
		switch c := c.(type) {
		case backend.MessageAppeared:
			if err := flushInc(); err != nil {
				return err
			}
			if len(run) > 0 && run[0].Folder != c.Folder {
				if err := flush(); err != nil {
					return err
				}
			}
			run = append(run, c)
			continue
		case backend.MessageGone:
			if err := flush(); err != nil {
				return err
			}
			if err := increment(c.Location, nil); err != nil {
				return err
			}
			continue
		case backend.FlagsChanged:
			if err := flush(); err != nil {
				return err
			}
			if err := increment(c.Location, &c.Flags); err != nil {
				return err
			}
			continue
		}
		if err := flushInc(); err != nil {
			return err
		}
		if err := flush(); err != nil {
			return err
		}
		switch c := c.(type) {
		case backend.FolderUpdated, backend.FolderGone:
		case backend.FolderReset:
			f, err := s.folder(c.Ref)
			if err != nil {
				return err
			}
			if err := s.flushStash(ctx, f); err != nil {
				return err
			}
			if err := resetFolder(ctx, s.db, s.stable, s.a.id, f, s.r); err != nil {
				return err
			}
		case backend.FolderSnapshot:
			f, err := s.folder(c.Folder)
			if err != nil {
				return err
			}
			if err := s.flushStash(ctx, f); err != nil {
				return err
			}
			if err := applySnapshot(ctx, s.db, s.stable, s.a.id, f, c.Messages, s.r); err != nil {
				return err
			}
		}
	}
	if err := flushInc(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	// The page's cursors go in one transaction, after its changes.
	// shortcut: the changes are not in that transaction (ADR 0037 asks for
	// one per page): storing a message reads what the ones before it wrote
	// (threads, claims, merges), which the store's statement-list
	// transactions cannot. Each change is idempotent, so a crash before the
	// cursors only repeats the page. Put them in one transaction when the
	// store can run reads inside one (a connection-held transaction).
	var stmts []sqlite.Statement
	states := map[*folder]backend.FolderState{}
	for ref, st := range p.Folders {
		f, err := s.folder(ref)
		if err != nil {
			return err
		}
		if len(s.stash[f.id]) > 0 {
			s.deferred[f.id] = st
			continue
		}
		stmts, states[f] = append(stmts, stateStmt(f, st)), st
	}
	if p.Account != "" {
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE accounts SET cursor = ? WHERE id = ?", Args: []any{string(p.Account), s.a.id}})
	}
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		return err
	}
	for f, st := range states {
		f.setState(st)
	}
	if p.Account != "" {
		s.a.cursor = string(p.Account)
	}
	return nil
}

func (s *syncRun) folder(ref backend.FolderRef) (*folder, error) {
	if f := s.folders[ref]; f != nil {
		return f, nil
	}
	return nil, fmt.Errorf("mailsync: folder %q reported before it was listed", ref)
}

func (s *syncRun) writeState(ctx context.Context, f *folder, st backend.FolderState) error {
	if _, err := s.db.Transaction(ctx, []sqlite.Statement{stateStmt(f, st)}); err != nil {
		return err
	}
	f.setState(st)
	return nil
}

// stateStmt stores st as f's sync state.
func stateStmt(f *folder, st backend.FolderState) sqlite.Statement {
	var filled any
	if !st.Filled.IsZero() {
		filled = st.Filled.Unix()
	}
	return sqlite.Statement{SQL: "UPDATE folders SET cursor = ?, synced = ?, filled_since = ? WHERE id = ?",
		Args: []any{nullable(string(st.Cursor)), st.Synced, filled, f.id}}
}

// appeared stores messages reported in one folder: those without content as
// a stored message's location or identity, the others as new messages.
func (s *syncRun) appeared(ctx context.Context, ms []backend.MessageAppeared) error {
	f, err := s.folder(ms[0].Folder)
	if err != nil {
		return err
	}
	for ref, items := range s.stash { // another folder's arrivals ended
		if ref != f.id && len(items) > 0 {
			if err := s.flushStash(ctx, s.byID(ref)); err != nil {
				return err
			}
		}
	}
	var bare []backend.MessageAppeared
	for _, m := range ms {
		if m.Content == nil {
			bare = append(bare, m)
		}
	}
	if len(bare) > 0 {
		if err := s.locate(ctx, f, bare); err != nil {
			return err
		}
	}
	claiming, err := s.db.Query(ctx, "SELECT 1 FROM ops WHERE account_id = ? AND state = 'awaiting_claim' AND dst_folder_id = ? LIMIT 1", s.a.id, f.id)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.Content == nil {
			continue
		}
		p := toParsed(m.Content)
		if len(claiming.Values) > 0 && p.MessageID != "" {
			match, err := s.db.Query(ctx, `SELECT 1 FROM ops o JOIN messages m ON m.id = o.message_id WHERE o.account_id = ? AND o.state = 'awaiting_claim'
				AND o.dst_folder_id = ? AND m.message_id = ? AND m.size = ? LIMIT 1`, s.a.id, f.id, p.MessageID, m.Content.Size)
			if err != nil {
				return err
			}
			if len(match.Values) > 0 {
				s.stash[f.id] = append(s.stash[f.id], stashed{m, p})
				continue
			}
		}
		if err := importMessage(ctx, s.db, s.a, f, m, p, 1, !m.Backfill, s.r); err != nil {
			return err
		}
	}
	return nil
}

func (s *syncRun) byID(id int64) *folder {
	for _, f := range s.folders {
		if f.id == id {
			return f
		}
	}
	return &folder{id: id}
}

// flushStash imports f's stashed arrivals, each knowing how many share its
// Message-ID and size, then writes f's deferred state.
func (s *syncRun) flushStash(ctx context.Context, f *folder) error {
	items := s.stash[f.id]
	delete(s.stash, f.id)
	twins := map[string]int{}
	for _, it := range items {
		twins[twinKey(it.p.MessageID, it.m.Content.Size)]++
	}
	for _, it := range items {
		if err := importMessage(ctx, s.db, s.a, f, it.m, it.p, twins[twinKey(it.p.MessageID, it.m.Content.Size)], !it.m.Backfill, s.r); err != nil {
			return err
		}
	}
	if st, ok := s.deferred[f.id]; ok {
		delete(s.deferred, f.id)
		return s.writeState(ctx, f, st)
	}
	return nil
}

func (s *syncRun) flushAll(ctx context.Context) error {
	for id := range s.stash {
		if err := s.flushStash(ctx, s.byID(id)); err != nil {
			return err
		}
	}
	return nil
}

// KnownIDs is backend.Local's: the ServerIDs of ids the account stores.
func (s *syncRun) KnownIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for page := range slices.Chunk(ids, 500) {
		rows, err := s.db.Query(ctx, "SELECT DISTINCT server_id FROM messages WHERE account_id = ? AND server_id IN ("+marks(len(page))+")",
			append([]any{s.a.id}, anys(page)...)...)
		if err != nil {
			return nil, err
		}
		for _, v := range rows.Values {
			out[store.Str(v[0])] = true
		}
	}
	for _, items := range s.stash { // stored before another folder's report of them
		for _, it := range items {
			if it.m.ServerID != "" && slices.Contains(ids, it.m.ServerID) {
				out[it.m.ServerID] = true
			}
		}
	}
	return out, nil
}

// HeldRefs is backend.Local's: which of refs folder has a location at.
func (s *syncRun) HeldRefs(ctx context.Context, ref backend.FolderRef, refs []backend.MessageRef) (map[backend.MessageRef]bool, error) {
	out := map[backend.MessageRef]bool{}
	f := s.folders[ref]
	if f == nil {
		return out, nil
	}
	for page := range slices.Chunk(refs, 500) {
		rows, err := s.db.Query(ctx, "SELECT server_ref FROM message_locations WHERE folder_id = ? AND server_ref IN ("+marks(len(page))+")",
			append([]any{f.id}, anys(page)...)...)
		if err != nil {
			return nil, err
		}
		for _, v := range rows.Values {
			out[backend.MessageRef(store.Str(v[0]))] = true
		}
	}
	for _, it := range s.stash[f.id] { // stored once the folder's arrivals end
		if slices.Contains(refs, it.m.Ref) {
			out[it.m.Ref] = true
		}
	}
	return out, nil
}

// Held is backend.Local's: every server location in folder, with its message's server_id.
func (s *syncRun) Held(ctx context.Context, ref backend.FolderRef) (map[backend.MessageRef]string, error) {
	out := map[backend.MessageRef]string{}
	f := s.folders[ref]
	if f == nil {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT l.server_ref, m.server_id FROM message_locations l JOIN messages m ON m.id = l.message_id
		WHERE l.folder_id = ? AND l.server_ref IS NOT NULL`, f.id)
	if err != nil {
		return nil, err
	}
	for _, v := range rows.Values {
		out[backend.MessageRef(store.Str(v[0]))] = store.Str(v[1])
	}
	for _, it := range s.stash[f.id] { // stored once the folder's arrivals end
		out[it.m.Ref] = it.m.ServerID
	}
	return out, nil
}

// Lost is backend.Local's: the server_ids of the orphans the round's end
// would delete, but those a pending op still acts on.
func (s *syncRun) Lost(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT server_id FROM messages WHERE account_id = ? AND server_id IS NOT NULL AND `+orphaned+`
		AND NOT EXISTS (SELECT 1 FROM ops WHERE message_id = messages.id AND state = 'pending')`, s.a.id)
	if err != nil {
		return nil, err
	}
	var out []string
	s.lost = map[string]bool{}
	for _, v := range rows.Values {
		out = append(out, store.Str(v[0]))
		s.lost[store.Str(v[0])] = true
	}
	return out, nil
}

// reshow shows a stable-identity message again in every folder the server has
// it in once no move or purge of it waits: a location found while one was
// pending was added hidden, and nothing else shows it after the move succeeds.
func reshow(ctx context.Context, db *store.DB, accountID int64, r *result) error {
	const hidden = `FROM message_locations l JOIN messages ON messages.id = l.message_id
		WHERE messages.account_id = ? AND l.server_ref IS NOT NULL AND NOT l.shown AND ` + notMoving
	rows, err := db.Query(ctx, "SELECT DISTINCT messages.id, messages.thread_id "+hidden, accountID)
	if err != nil || len(rows.Values) == 0 {
		return err
	}
	if _, err := db.Execute(ctx, `UPDATE message_locations SET shown = 1, entered_at = unixepoch() WHERE rowid IN (SELECT l.rowid `+hidden+`)`, accountID); err != nil {
		return err
	}
	var ids, tids []int64
	for _, v := range rows.Values {
		ids, tids = append(ids, store.Int(v[0])), append(tids, store.Int(v[1]))
	}
	r.gone(ids, tids)
	return nil
}

// publish announces what r collected.
func (r *result) publish(bus *events.Bus, accountID int64) {
	if len(r.received) > 0 {
		bus.Publish(events.Event{Type: events.MessageReceived, AccountID: accountID, MessageIDs: r.received, ThreadIDs: r.threads})
	}
	if len(r.changed) > 0 {
		bus.Publish(events.Event{Type: events.MessagesChanged, AccountID: accountID, MessageIDs: slices.Compact(slices.Sorted(slices.Values(r.changed))),
			ThreadIDs: slices.Compact(slices.Sorted(slices.Values(r.changedThreads)))})
	}
	if len(r.conflicts) > 0 {
		bus.Publish(events.Event{Type: events.SyncFailed, AccountID: accountID, Error: strings.Join(r.conflicts, "; ")})
	}
}

func loadAccount(ctx context.Context, db *store.DB, accountID int64) (*account, error) {
	rows, err := db.Query(ctx, "SELECT email, sync_limit, sync_since_months, cursor, "+InboxOnly+" FROM accounts WHERE id = ?", accountID, accountID)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, fmt.Errorf("account %d not found", accountID)
	}
	return &account{id: accountID, email: store.Str(rows.Values[0][0]), limit: int(store.Int(rows.Values[0][1])),
		months: int(store.Int(rows.Values[0][2])), cursor: store.Str(rows.Values[0][3]), inboxOnly: store.Int(rows.Values[0][4]) != 0}, nil
}

// resetFolder drops everything known about the folder's server locations, which
// its backend voided: the locations, and the messages the server then holds
// nowhere (orphan), and pending ops that would act on them become conflicts:
// those taking a message from the folder, and every one of a message held
// nowhere else. The folder's cursor goes with them.
func resetFolder(ctx context.Context, db *store.DB, stable bool, accountID int64, f *folder, r *result) error {
	ids, _, err := doomed(ctx, db, serverIn, f.id)
	if err != nil {
		return err
	}
	stmts := []sqlite.Statement{
		{SQL: `UPDATE ops SET state = 'conflict' WHERE state = 'pending' AND account_id = ?
			AND message_id IN (SELECT id FROM messages WHERE ` + serverIn + `) AND (src_folder_id = ?
				OR NOT EXISTS (SELECT 1 FROM message_locations WHERE message_id = ops.message_id AND folder_id != ? AND server_ref IS NOT NULL))`,
			Args: []any{accountID, f.id, f.id, f.id}},
		{SQL: "DELETE FROM message_locations WHERE folder_id = ? AND server_ref IS NOT NULL", Args: []any{f.id}},
		{SQL: "UPDATE folders SET cursor = NULL, synced = 0, filled_since = NULL WHERE id = ?", Args: []any{f.id}},
	}
	if r.inboxOnly { // the mail stays where it shows, with no server location
		stmts[1] = sqlite.Statement{SQL: "UPDATE message_locations SET server_ref = NULL WHERE folder_id = ?", Args: []any{f.id}}
		stmts = append(stmts, sqlite.Statement{SQL: "DELETE FROM message_locations WHERE folder_id = ? AND server_ref IS NULL AND NOT shown", Args: []any{f.id}})
	}
	if _, err := db.Transaction(ctx, stmts); err != nil {
		return err
	}
	f.setState(backend.FolderState{})
	return r.orphan(ctx, db, stable, accountID, ids)
}

// orphaned is the condition on messages (unaliased) that the server holds the
// message nowhere Godwit knows and no move of it awaits its claim: it is gone.
const orphaned = unlocated + " AND NOT EXISTS (SELECT 1 FROM ops WHERE message_id = messages.id AND state = 'awaiting_claim')"

// orphan deletes those of ids, messages sync just dropped a server location
// of, that are orphaned. With a stable identity they wait for the round's
// end: a folder synced later may hold them still (ADR 0034).
func (r *result) orphan(ctx context.Context, db *store.DB, stable bool, accountID int64, ids []int64) error {
	if r.inboxOnly { // kept locally; run drops those deleted for good
		return nil
	}
	if stable {
		r.orphans = append(r.orphans, ids...)
		return nil
	}
	return dropOrphans(ctx, db, accountID, ids, false, false, false, r)
}

// dropOrphans deletes the orphaned messages of ids (attachments and FTS rows go
// with them), and with stray those of the account without a location left,
// should a round that dropped their last one have stopped before its end.
// Cached attachment files are removed best effort after the commit; one that
// cannot be removed is no longer referenced, so the startup orphan sweep of
// the cache directory (store.CleanOrphans) collects it.
func dropOrphans(ctx context.Context, db *store.DB, accountID int64, ids []int64, stray, stable, allMail bool, r *result) error {
	which := "id IN (" + marks(len(ids)) + ")"
	if stray {
		which = "(" + which + " OR NOT EXISTS (SELECT 1 FROM message_locations WHERE message_id = messages.id))"
	}
	if len(ids) == 0 && !stray {
		return nil
	}
	where, args := "account_id = ? AND "+which+" AND "+orphaned, append([]any{accountID}, anys(ids)...)
	if stable {
		where += ` AND NOT EXISTS (SELECT 1 FROM ops WHERE message_id = messages.id AND state = 'pending')`
	}
	if allMail {
		where += ` AND (server_id IS NULL OR id IN (` + marks(len(r.expunged)) + `)
				OR EXISTS (SELECT 1 FROM folders WHERE account_id = messages.account_id AND role = 'all'))`
		args = append(args, anys(r.expunged)...)
	}
	gone, tids, err := doomed(ctx, db, where, args...)
	if err != nil || len(gone) == 0 {
		return err
	}
	files, err := db.Query(ctx, "SELECT cache_path FROM attachments WHERE cache_path IS NOT NULL AND message_id IN ("+marks(len(gone))+")", anys(gone)...)
	if err != nil {
		return err
	}
	hashes, err := db.Query(ctx, "SELECT content_hash FROM messages WHERE content_hash IS NOT NULL AND id IN ("+marks(len(gone))+")", anys(gone)...)
	if err != nil {
		return err
	}
	stmts := []sqlite.Statement{{SQL: "DELETE FROM messages WHERE id IN (" + marks(len(gone)) + ")", Args: anys(gone)}}
	if _, err := db.Transaction(ctx, append(stmts, tidyThreads(accountID, tids)...)); err != nil {
		return err
	}
	r.gone(gone, tids)
	for _, h := range hashes.Values {
		r.dropped = append(r.dropped, store.Str(h[0]))
	}
	for _, p := range files.Values {
		os.Remove(store.Str(p[0])) // best effort, see above
	}
	return nil
}

// doomed returns the IDs and thread IDs of the messages matching where.
func doomed(ctx context.Context, db *store.DB, where string, args ...any) (ids, tids []int64, err error) {
	rows, err := db.Query(ctx, "SELECT id, thread_id FROM messages WHERE "+where, args...)
	if err != nil {
		return nil, nil, err
	}
	for _, v := range rows.Values {
		ids = append(ids, store.Int(v[0]))
		if t := store.Int(v[1]); t != 0 {
			tids = append(tids, t)
		}
	}
	return ids, tids, nil
}

// twinKey is what a claim tells copies apart by: Message-ID and size.
func twinKey(messageID string, size int64) string {
	return messageID + "\x00" + strconv.FormatInt(size, 10)
}

// textRefs picks the text parts of a structure that a sync downloads: in
// order, until the message used enough for MaxBodyBytes of both text and
// HTML. Each is fetched up to PartFetchBytes; toParsed marks what was left
// out as truncated.
func textRefs(p *backend.Part) []backend.PartRef {
	parts, _ := mail.ParseStructure(p)
	budget := 2 * PartFetchBytes
	var out []backend.PartRef
	for _, tp := range parts {
		if budget <= 0 {
			break
		}
		budget -= partCost(tp)
		out = append(out, backend.PartRef(tp.Part))
	}
	return out
}

// partCost is how many bytes fetching the text part may return.
func partCost(p mail.Attachment) int64 {
	if p.Size > 0 && int64(p.Size) < PartFetchBytes {
		return int64(p.Size)
	}
	return PartFetchBytes
}

// toParsed assembles a message from the fetched header fields and the decoded
// text parts. It reads no ENVELOPE: go-imap fails the whole FETCH on a server's
// malformed one, so one bad message would stop the folder's sync.
func toParsed(c *backend.Content) *mail.Parsed {
	if c.Structure == nil && c.Raw != nil { // a backend with no structure gives the whole message
		if p, err := mail.Parse(c.Raw); err == nil {
			for i := range p.Attachments {
				p.Attachments[i].Data = nil // downloaded on demand, as the backend's other mail
			}
			if p.Date.IsZero() {
				p.Date = c.Date
			}
			return p
		}
	}
	p := &mail.Parsed{}
	if h, err := mail.Parse(c.Header); err == nil {
		p.MessageID, p.Subject, p.Date, p.From, p.To, p.Cc = h.MessageID, h.Subject, h.Date, h.From, h.To, h.Cc
		p.InReplyTo, p.References, p.ReplyTo = h.InReplyTo, h.References, h.ReplyTo
		p.ListUnsubscribe, p.ListUnsubscribePost, p.ListID = h.ListUnsubscribe, h.ListUnsubscribePost, h.ListID
		p.AutoSubmitted, p.Precedence = h.AutoSubmitted, h.Precedence
		p.AuthenticationResults, p.AuthenticationTrace, p.DKIMSigned = h.AuthenticationResults, h.AuthenticationTrace, h.DKIMSigned
		p.RuleSent = h.RuleSent
	}
	if p.Date.IsZero() {
		p.Date = c.Date
	}
	parts, atts := mail.ParseStructure(c.Structure)
	p.Attachments = atts
	for _, tp := range parts {
		raw, fetched := c.Text[backend.PartRef(tp.Part)]
		if cut := int64(tp.Size) > PartFetchBytes; cut || !fetched {
			p.Truncated = true
			if i := bytes.LastIndexByte(raw, '\n'); cut && i >= 0 {
				raw = raw[:i+1] // whole lines decode cleanly in base64 and quoted-printable
			}
		}
		b, err := mail.DecodePart(raw, tp.Encoding, tp.Charset)
		if err != nil {
			b = raw // undecodable: keep the raw text rather than nothing
		}
		dst, html := &p.Text, tp.ContentType == "text/html"
		if html {
			dst = &p.HTML
		}
		b = mail.ValidText(b, tp.Charset, html) // a mislabeled charset must not fail the folder's sync in SQLite
		p.Truncated = appendCapped(dst, b) || p.Truncated
	}
	if p.Text == "" && p.HTML != "" {
		p.Text = mail.HTMLToText(p.HTML)
	}
	return p
}

// appendCapped appends b to *dst, cutting at a rune boundary so *dst stays within
// MaxBodyBytes. It reports whether anything was cut.
func appendCapped(dst *string, b []byte) bool {
	room := mail.MaxBodyBytes - len(*dst)
	if len(b) <= room {
		*dst += string(b)
		return false
	}
	cut := max(room, 0)
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	*dst += string(b[:cut])
	return true
}

// flagString is flags in local storage's spelling: IMAP's, space separated
// and sorted.
func flagString(fl backend.Flags) string {
	out := slices.Clone(fl.Keywords)
	for _, f := range []struct {
		on   bool
		name string
	}{{fl.Seen, `\Seen`}, {fl.Flagged, `\Flagged`}, {fl.Answered, `\Answered`}, {fl.Draft, `\Draft`}} {
		if f.on {
			out = append(out, f.name)
		}
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

func jsonList(l []string) string {
	b, _ := json.Marshal(append([]string{}, l...))
	return string(b)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
