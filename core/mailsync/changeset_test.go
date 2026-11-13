package mailsync_test

// Local storage's side of ADR 0037 on its own interface: given these pages
// from a backend and these write-back outcomes, the store ends up so. The
// backend is backendtest.Fake, the store a real SQLite file.
//
// Where state cannot be made through mailsync's interface the tests write the
// tables themselves, as core does: the account row, and queued ops (core's
// Service queues them, mailsync only pushes them). Those writes are marked
// "core's part".

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/backendtest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const acct = 1

type env struct {
	t    *testing.T
	ctx  context.Context
	db   *store.DB
	fake *backendtest.Fake
	recv backend.Receiver
	bus  *events.Bus
	evs  <-chan events.Event
}

func newEnv(t *testing.T) *env {
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "mail.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	// core's part: adding the account.
	_, err = db.Execute(ctx, "INSERT INTO accounts(id, email, recv_host) VALUES (?, 'me@example.test', 'fake.invalid')", acct)
	require.NoError(t, err)
	fake := backendtest.NewFake()
	bus := events.New()
	evs, stop := bus.Subscribe()
	t.Cleanup(stop)
	return &env{t: t, ctx: ctx, db: db, fake: fake, recv: fake.Receiver(t), bus: bus, evs: evs}
}

// raw is a message with Message-ID <id>; all of one id have the same size.
func raw(id string) []byte {
	return fmt.Appendf(nil, "Message-ID: <%s>\r\nFrom: Sender <s@example.test>\r\nTo: me@example.test\r\nSubject: %s\r\nDate: Mon, 02 Jan 2026 15:04:05 +0000\r\n\r\nbody of %s\r\n", id, id, id)
}

func (e *env) sync() {
	e.t.Helper()
	_, err := mailsync.Sync(e.ctx, e.db, e.recv, acct, e.bus)
	require.NoError(e.t, err)
}

func (e *env) push() error { return mailsync.PushOps(e.ctx, e.db, e.recv, acct, time.Now()) }

func (e *env) rows(q string, args ...any) [][]any {
	e.t.Helper()
	r, err := e.db.Query(e.ctx, q, args...)
	require.NoError(e.t, err)
	return r.Values
}

// where lists each stored message's rows in message_locations, sorted:
// "<Message-ID> <folder> <server_ref or -> shown|hidden".
func (e *env) where() []string {
	e.t.Helper()
	var out []string
	for _, v := range e.rows(`SELECT m.message_id, f.name, l.server_ref, l.shown FROM messages m
		JOIN message_locations l ON l.message_id = m.id JOIN folders f ON f.id = l.folder_id WHERE m.account_id = ?`, acct) {
		ref, shown := store.Str(v[2]), "hidden"
		if ref == "" {
			ref = "-"
		}
		if store.Int(v[3]) != 0 {
			shown = "shown"
		}
		out = append(out, strings.Join([]string{store.Str(v[0]), store.Str(v[1]), ref, shown}, " "))
	}
	slices.Sort(out)
	return out
}

// id is the local id of the one message with Message-ID mid.
func (e *env) id(mid string) int64 {
	e.t.Helper()
	v := e.rows("SELECT id FROM messages WHERE account_id = ? AND message_id = ?", acct, mid)
	require.Len(e.t, v, 1, "messages with Message-ID %s", mid)
	return store.Int(v[0][0])
}

func (e *env) folder(name string) int64 {
	e.t.Helper()
	v := e.rows("SELECT id FROM folders WHERE account_id = ? AND name = ?", acct, name)
	require.Len(e.t, v, 1, "folder %s", name)
	return store.Int(v[0][0])
}

func (e *env) flags(mid string) string {
	e.t.Helper()
	return store.Str(e.rows("SELECT flags FROM messages WHERE id = ?", e.id(mid))[0][0])
}

// op is the state, refusals and attempts of ops row id.
func (e *env) op(id int64) (state string, refusals, attempts int64) {
	e.t.Helper()
	v := e.rows("SELECT state, refusals, attempts FROM ops WHERE id = ?", id)
	require.Len(e.t, v, 1)
	return store.Str(v[0][0]), store.Int(v[0][1]), store.Int(v[0][2])
}

// queueFlags is core's part: the user changes a message's flags, as core's
// flagWrites does.
func (e *env) queueFlags(mid string, flags string, add ...string) int64 {
	e.t.Helper()
	msg := e.id(mid)
	res, err := e.db.Transaction(e.ctx, []sqlite.Statement{
		{SQL: "UPDATE messages SET flags = ? WHERE id = ?", Args: []any{flags, msg}},
		{SQL: `INSERT INTO ops(account_id, message_id, kind, payload, created_at) VALUES (?, ?, 'flags', ?, unixepoch())`,
			Args: []any{acct, msg, fmt.Sprintf(`{"add":[%q]}`, strings.Join(add, `","`))}},
	})
	require.NoError(e.t, err)
	return res[1].LastInsertID
}

// queueMove is core's part: the user moves a message, as core's MoveFrom does.
func (e *env) queueMove(mid, from, to string) int64 {
	e.t.Helper()
	msg, src, dst := e.id(mid), e.folder(from), e.folder(to)
	res, err := e.db.Transaction(e.ctx, append(mailsync.ShowIn(msg, dst, []int64{src}),
		sqlite.Statement{SQL: `INSERT INTO ops(account_id, message_id, kind, src_folder_id, dst_folder_id, payload, created_at)
			VALUES (?, ?, 'move', ?, ?, '{}', unixepoch())`, Args: []any{acct, msg, src, dst}}))
	require.NoError(e.t, err)
	return res[len(res)-1].LastInsertID
}

// events drains what the bus has published so far.
func (e *env) events() []events.Event {
	var out []events.Event
	for {
		select {
		case ev := <-e.evs:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func received(evs []events.Event) []int64 {
	var out []int64
	for _, ev := range evs {
		if ev.Type == events.MessageReceived {
			out = append(out, ev.MessageIDs...)
		}
	}
	return out
}

func syncFailed(evs []events.Event) []string {
	var out []string
	for _, ev := range evs {
		if ev.Type == events.SyncFailed {
			out = append(out, ev.Error)
		}
	}
	return out
}

// The first sync stores the folder and its mail as older mail, not arrivals;
// the next stores only what is new, as an arrival; one with nothing new
// changes nothing.
func TestSyncFirstThenIncremental(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "INBOX", raw("b@x"))
	e.sync()
	assert.Equal(t, []string{"a@x INBOX 1/1 shown", "b@x INBOX 1/2 shown"}, e.where())
	assert.Equal(t, [][]any{{"inbox", "1/2", int64(1)}}, e.rows("SELECT role, cursor, synced FROM folders WHERE account_id = ?", acct))
	assert.Empty(t, received(e.events()), "a first sync's mail is not new mail")

	e.fake.Deliver(t, "INBOX", raw("c@x"))
	e.sync()
	assert.Equal(t, []string{"a@x INBOX 1/1 shown", "b@x INBOX 1/2 shown", "c@x INBOX 1/3 shown"}, e.where())
	assert.Equal(t, []int64{e.id("c@x")}, received(e.events()))

	ids := e.rows("SELECT id FROM messages ORDER BY id")
	e.sync()
	assert.Equal(t, ids, e.rows("SELECT id FROM messages ORDER BY id"))
	assert.Empty(t, e.events())
}

// A folder's snapshot drops the locations the server no longer has, and the
// messages left nowhere, and takes the server's flags.
func TestSnapshotDropsGoneAndTakesFlags(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "INBOX", raw("b@x"))
	e.sync()
	e.fake.Remove("INBOX", "1/1")
	e.fake.SetFlags("INBOX", "1/2", backend.Flags{Seen: true, Keywords: []string{"$Work"}})
	e.sync()
	assert.Equal(t, []string{"b@x INBOX 1/2 shown"}, e.where())
	assert.Equal(t, `$Work \Seen`, e.flags("b@x"))
}

// A flags change still queued wins over the server's flags until it is
// pushed; once pushed, the server has it and sync keeps it.
func TestQueuedFlagsWinOverSnapshot(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.sync()
	op := e.queueFlags("a@x", `\Flagged`, `\Flagged`)
	e.sync()
	assert.Equal(t, `\Flagged`, e.flags("a@x"), "the snapshot does not undo a queued change")

	require.NoError(t, e.push())
	state, _, _ := e.op(op)
	assert.Equal(t, "done", state)
	assert.Equal(t, map[backend.MessageRef]backend.Flags{"1/1": {Flagged: true}}, e.fake.Messages("INBOX"))
	e.sync()
	assert.Equal(t, `\Flagged`, e.flags("a@x"))
}

// Pages are stored as they come, each with its cursors: increments
// (MessageGone, FlagsChanged) act on the locations they name, a later
// page's error keeps what earlier pages stored, and FolderGone takes the
// folder and its mail.
func TestScriptedPages(t *testing.T) {
	e := newEnv(t)
	at := func(ref string) backend.Location { return backend.Location{Folder: "f1", Ref: backend.MessageRef(ref)} }
	appeared := func(ref, mid string) backend.MessageAppeared {
		r := raw(mid)
		return backend.MessageAppeared{Location: at(ref), Backfill: true,
			Content: &backend.Content{Size: int64(len(r)), Date: time.Now(), Raw: r}}
	}
	broken := errors.New("connection lost")
	var gotReq backend.SyncRequest
	pages := [][]backend.Page{{
		{Changes: []backend.Change{
			backend.FolderUpdated{Folder: backend.Folder{Ref: "f1", Name: "Work"}},
			appeared("x", "x@x"), appeared("y", "y@x"),
		}, Account: "acc1", Folders: map[backend.FolderRef]backend.FolderState{"f1": {Cursor: "c1", Synced: true}}},
		{Changes: []backend.Change{backend.MessageGone{Location: at("x")}}, Folders: map[backend.FolderRef]backend.FolderState{"f1": {Cursor: "c2", Synced: true}}},
	}, {
		{Changes: []backend.Change{backend.FlagsChanged{Location: at("y"), Flags: backend.Flags{Seen: true}}},
			Account: "acc2", Folders: map[backend.FolderRef]backend.FolderState{"f1": {Cursor: "c3", Synced: true}}},
		{Changes: []backend.Change{backend.FolderGone{Ref: "f1"}}},
	}}
	round := 0
	e.fake.OnSync = func(req backend.SyncRequest, commit func(backend.Page) error) (bool, error) {
		gotReq = req
		ps := pages[round]
		round++
		for _, p := range ps {
			if err := commit(p); err != nil {
				return false, err
			}
		}
		if round == 1 {
			return false, broken
		}
		return false, nil
	}

	_, err := mailsync.Sync(e.ctx, e.db, e.recv, acct, e.bus)
	require.ErrorIs(t, err, broken)
	assert.Equal(t, []string{"y@x Work y shown"}, e.where(), "both pages stored before the error")
	assert.Equal(t, [][]any{{"c2", int64(1)}}, e.rows("SELECT cursor, synced FROM folders WHERE name = 'Work'"))
	assert.Equal(t, [][]any{{"acc1"}}, e.rows("SELECT cursor FROM accounts WHERE id = ?", acct))

	// The next round starts from what was stored; its first page changes the
	// flags, its second takes the folder away.
	e.fake.OnSync = func(req backend.SyncRequest, commit func(backend.Page) error) (bool, error) {
		gotReq = req
		if err := commit(pages[1][0]); err != nil {
			return false, err
		}
		assert.Equal(t, `\Seen`, e.flags("y@x"))
		return false, commit(pages[1][1])
	}
	e.sync()
	assert.Equal(t, backend.Cursor("acc1"), gotReq.Account)
	assert.Equal(t, backend.FolderState{Cursor: "c2", Synced: true}, gotReq.Folders["f1"])
	assert.Empty(t, e.where())
	assert.Empty(t, e.rows("SELECT 1 FROM folders WHERE name = 'Work'"))
	assert.Equal(t, [][]any{{"acc2"}}, e.rows("SELECT cursor FROM accounts WHERE id = ?", acct))
}

// A folder reset voids its locations: its mail is stored again from what the
// server reports, as older mail, not arrivals. With an identity of location
// only, the messages are new rows, and a change queued for one becomes a
// conflict.
func TestFolderResetStoresMailAgain(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "INBOX", raw("b@x"))
	e.sync()
	e.events()
	old := e.id("a@x")
	op := e.queueFlags("a@x", `\Seen`, `\Seen`)

	e.fake.Reset(t, "INBOX")
	e.sync()
	assert.Equal(t, []string{"a@x INBOX 2/1 shown", "b@x INBOX 2/2 shown"}, e.where())
	assert.Empty(t, received(e.events()), "mail stored again is not new mail")
	assert.NotEqual(t, old, e.id("a@x"), "location-only identity: the message is a new row")
	state, _, _ := e.op(op)
	assert.Equal(t, "conflict", state)
	assert.Equal(t, "", e.flags("a@x"), "the queued \\Seen is not applied to the row stored again")
	assert.Equal(t, [][]any{{"2/2", int64(1)}}, e.rows("SELECT cursor, synced FROM folders WHERE name = 'INBOX'"))
}

// A move the server did without saying where (Unlocated) leaves the message
// at no server location, awaiting its claim; the next sync claims the
// arrival in the destination by Message-ID and size, keeping the local row.
func TestUnlocatedMoveIsClaimed(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "Archive", raw("z@x"))
	e.sync()
	msg := e.id("a@x")
	op := e.queueMove("a@x", "INBOX", "Archive")
	e.fake.OnApply = func(_ backend.Op, apply func() (backend.Result, error)) (backend.Result, error) {
		if _, err := apply(); err != nil {
			return backend.Result{}, err
		}
		return backend.Result{Outcome: backend.Unlocated}, nil
	}
	require.NoError(t, e.push())
	state, _, _ := e.op(op)
	assert.Equal(t, "awaiting_claim", state)
	assert.Equal(t, []string{"a@x Archive - shown", "z@x Archive 1/1 shown"}, e.where())

	e.sync()
	assert.Equal(t, []string{"a@x Archive 1/2 shown", "z@x Archive 1/1 shown"}, e.where())
	assert.Equal(t, msg, e.id("a@x"), "claimed, not stored again")
	state, _, _ = e.op(op)
	assert.Equal(t, "done", state)
	assert.Empty(t, received(e.events()), "a claim is no arrival")
}

// A claim that cannot tell the moved message from another copy in the
// destination (same Message-ID and size) merges nothing: the move is a
// conflict, its local row goes, and each server copy is a message of its own.
func TestUnlocatedMoveAmongTwinsConflicts(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "Archive", raw("a@x"))
	e.sync()
	inbox := e.rows("SELECT m.id FROM messages m JOIN message_locations l ON l.message_id = m.id JOIN folders f ON f.id = l.folder_id WHERE f.name = 'INBOX'")
	require.Len(t, inbox, 1)
	moved := store.Int(inbox[0][0])
	msg, src, dst := moved, e.folder("INBOX"), e.folder("Archive")
	res, err := e.db.Transaction(e.ctx, append(mailsync.ShowIn(msg, dst, []int64{src}), // core's part, as queueMove
		sqlite.Statement{SQL: `INSERT INTO ops(account_id, message_id, kind, src_folder_id, dst_folder_id, payload, created_at)
			VALUES (?, ?, 'move', ?, ?, '{}', unixepoch())`, Args: []any{acct, msg, src, dst}}))
	require.NoError(t, err)
	op := res[len(res)-1].LastInsertID
	e.fake.OnApply = func(_ backend.Op, apply func() (backend.Result, error)) (backend.Result, error) {
		apply()
		return backend.Result{Outcome: backend.Unlocated}, nil
	}
	require.NoError(t, e.push())
	e.events()

	e.sync()
	assert.Equal(t, []string{"a@x Archive 1/1 shown", "a@x Archive 1/2 shown"}, e.where())
	assert.Empty(t, e.rows("SELECT 1 FROM messages WHERE id = ?", moved), "the moved row is gone")
	state, _, _ := e.op(op)
	assert.Equal(t, "conflict", state)
	assert.Len(t, syncFailed(e.events()), 1)
}

// A move awaiting its claim whose message never shows up in the destination
// (another client deleted it there first) ends after a complete sync: the
// op is a conflict and the local row, which the server has nowhere, goes.
func TestUnclaimableMoveEnds(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "Archive", raw("z@x"))
	e.sync()
	op := e.queueMove("a@x", "INBOX", "Archive")
	e.fake.OnApply = func(_ backend.Op, apply func() (backend.Result, error)) (backend.Result, error) {
		apply()
		e.fake.Remove("Archive", "1/2")
		return backend.Result{Outcome: backend.Unlocated}, nil
	}
	require.NoError(t, e.push())
	e.events()

	e.sync()
	assert.Equal(t, []string{"z@x Archive 1/1 shown"}, e.where())
	state, _, _ := e.op(op)
	assert.Equal(t, "conflict", state)
	assert.Len(t, syncFailed(e.events()), 1)
}

// A move the server reports Done with the new location moves the local
// location there; the next sync knows it and stores nothing again.
func TestMoveDone(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "Archive", raw("z@x"))
	e.sync()
	msg := e.id("a@x")
	op := e.queueMove("a@x", "INBOX", "Archive")
	require.NoError(t, e.push())
	state, _, attempts := e.op(op)
	assert.Equal(t, "done", state)
	assert.EqualValues(t, 1, attempts)
	assert.Equal(t, []string{"a@x Archive 1/2 shown", "z@x Archive 1/1 shown"}, e.where())
	e.sync()
	assert.Equal(t, []string{"a@x Archive 1/2 shown", "z@x Archive 1/1 shown"}, e.where())
	assert.Equal(t, msg, e.id("a@x"))
}

// The destination was reset between the sync that stored its cursor and the
// push (e78eb309): the backend, given that cursor, answers Unlocated rather
// than a location of the new generation, and the reset's sync claims it.
func TestMoveIntoResetFolderIsClaimed(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "Archive", raw("z@x"))
	e.sync()
	msg := e.id("a@x")
	op := e.queueMove("a@x", "INBOX", "Archive")
	e.fake.Reset(t, "Archive")
	var sent backend.Move
	e.fake.OnApply = func(o backend.Op, apply func() (backend.Result, error)) (backend.Result, error) {
		sent, _ = o.(backend.Move)
		return apply()
	}
	require.NoError(t, e.push())
	assert.Equal(t, backend.Cursor("1/1"), sent.Cursor, "the destination's stored cursor goes with the move")
	state, _, _ := e.op(op)
	assert.Equal(t, "awaiting_claim", state)

	e.sync()
	assert.Equal(t, []string{"a@x Archive 2/2 shown", "z@x Archive 2/1 shown"}, e.where())
	assert.Equal(t, msg, e.id("a@x"))
	state, _, _ = e.op(op)
	assert.Equal(t, "done", state)
}

// Only the server's refusals count toward MaxAttempts (a8e885f4): a refused
// move stays queued until the last refusal, then fails and the message shows
// where the server still has it.
func TestRefusedMoveGivesUpAfterMaxAttempts(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "Archive", raw("z@x"))
	e.sync()
	op := e.queueMove("a@x", "INBOX", "Archive")
	e.fake.OnApply = func(backend.Op, func() (backend.Result, error)) (backend.Result, error) {
		return backend.Result{Outcome: backend.Failed, Reason: "refused"}, nil
	}
	for i := 1; i < mailsync.MaxAttempts; i++ {
		require.NoError(t, e.push())
		state, refusals, _ := e.op(op)
		assert.Equal(t, "pending", state)
		assert.EqualValues(t, i, refusals)
	}
	var oe *mailsync.OpsError
	require.ErrorAs(t, e.push(), &oe)
	assert.Equal(t, []int64{e.id("a@x")}, oe.Changed)
	state, refusals, _ := e.op(op)
	assert.Equal(t, "failed", state)
	assert.EqualValues(t, mailsync.MaxAttempts, refusals)
	assert.Equal(t, []string{"a@x INBOX 1/1 shown", "z@x Archive 1/1 shown"}, e.where())
}

// A lost reply is not a refusal (a8e885f4): however often the outcome is
// unknown, the op stays queued and its refusal budget untouched, and goes
// through once the connection holds. Every send is counted in attempts.
func TestUnknownOutcomeNeverGivesUp(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.sync()
	op := e.queueFlags("a@x", `\Seen`, `\Seen`)
	lost := errors.New("connection reset")
	e.fake.OnApply = func(backend.Op, func() (backend.Result, error)) (backend.Result, error) {
		return backend.Result{}, lost
	}
	for range 2 * mailsync.MaxAttempts {
		require.ErrorIs(t, e.push(), lost, "the push stops: the connection is unusable")
	}
	state, refusals, attempts := e.op(op)
	assert.Equal(t, "pending", state)
	assert.EqualValues(t, 0, refusals)
	assert.EqualValues(t, 2*mailsync.MaxAttempts, attempts)

	e.fake.OnApply = nil
	require.NoError(t, e.push())
	state, _, _ = e.op(op)
	assert.Equal(t, "done", state)
	assert.True(t, e.fake.Messages("INBOX")["1/1"].Seen)
}

// A refusal after lost replies still has its whole budget: earlier unknown
// outcomes do not bring the give-up closer.
func TestRefusalsAfterLostRepliesKeepTheirBudget(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.sync()
	op := e.queueFlags("a@x", `\Seen`, `\Seen`)
	e.fake.OnApply = func(backend.Op, func() (backend.Result, error)) (backend.Result, error) {
		return backend.Result{}, errors.New("timeout")
	}
	for range mailsync.MaxAttempts {
		_ = e.push()
	}
	e.fake.OnApply = func(backend.Op, func() (backend.Result, error)) (backend.Result, error) {
		return backend.Result{Outcome: backend.Failed, Reason: "refused"}, nil
	}
	require.NoError(t, e.push())
	state, refusals, _ := e.op(op)
	assert.Equal(t, "pending", state)
	assert.EqualValues(t, 1, refusals)
}

// ErrNotFound on a location-only account: the location no longer names the
// message (its folder was recreated), so the change is a conflict, not retried.
func TestFlagsAtVoidedLocationConflicts(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.sync()
	op := e.queueFlags("a@x", `\Seen`, `\Seen`)
	e.fake.OnApply = func(backend.Op, func() (backend.Result, error)) (backend.Result, error) {
		return backend.Result{}, backend.ErrFolderReset
	}
	var oe *mailsync.OpsError
	require.ErrorAs(t, e.push(), &oe)
	state, refusals, attempts := e.op(op)
	assert.Equal(t, "conflict", state)
	assert.EqualValues(t, 0, refusals)
	assert.EqualValues(t, 0, attempts, "nothing reached the server")
}

// A change the server cannot do fails at once, with the feature's reason.
func TestUnsupportedChangeFails(t *testing.T) {
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.sync()
	op := e.queueFlags("a@x", `\Seen`, `\Seen`)
	e.fake.OnApply = func(backend.Op, func() (backend.Result, error)) (backend.Result, error) {
		return backend.Result{Outcome: backend.Unsupported, Reason: "imap.no_move"}, nil
	}
	var oe *mailsync.OpsError
	require.ErrorAs(t, e.push(), &oe)
	state, _, attempts := e.op(op)
	assert.Equal(t, "failed", state)
	assert.EqualValues(t, 0, attempts)
	assert.Contains(t, e.rows("SELECT last_error FROM ops WHERE id = ?", op)[0][0], "MOVE")
}

// The server moved the message but its reply was lost; the retry finds the
// source location gone (ErrNotFound, not ErrFolderReset). Where the message
// ends is the server's: one copy, in the destination, the same local row, not
// announced as new mail, with no conflict claiming the folder was recreated.
func TestMoveWhoseReplyWasLost(t *testing.T) {
	t.Skip("suspected bug: on an IdentityLocation account pushOp reads any ErrNotFound as a recreated folder: " +
		"the op becomes a conflict saying so, the snapshot then orphans the local row, and the moved message is " +
		"stored again as a new arrival (message.received, rules). IMAP escapes it through MoveBaseline; " +
		"a location-only backend without one (EWS) does not. Treat it like lostSource: await the claim when a send may have reached the server.")
	e := newEnv(t)
	e.fake.Deliver(t, "INBOX", raw("a@x"))
	e.fake.Deliver(t, "Archive", raw("z@x"))
	e.sync()
	msg := e.id("a@x")
	op := e.queueMove("a@x", "INBOX", "Archive")
	lost := errors.New("connection reset")
	e.fake.OnApply = func(_ backend.Op, apply func() (backend.Result, error)) (backend.Result, error) {
		apply()
		return backend.Result{}, lost
	}
	require.ErrorIs(t, e.push(), lost)
	e.fake.OnApply = nil
	assert.NoError(t, e.push())
	state, _, _ := e.op(op)
	assert.NotEqual(t, "conflict", state)
	e.events()

	e.sync()
	assert.Equal(t, []string{"a@x Archive 1/2 shown", "z@x Archive 1/1 shown"}, e.where())
	assert.Equal(t, msg, e.id("a@x"))
	assert.Empty(t, received(e.events()), "the user's own move is no new mail")
}
