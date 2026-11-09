package core

import (
	"fmt"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A local edit made while sync discovers a remote deletion stays attached to
// its message until the next push explicitly reports that it cannot apply it.
func TestGmailMissingMessageKeepsPendingChangeUntilConflict(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	m := e.im.AddGmail(msg("gone@x"), nil, "INBOX", testmail.GmailAllMail)
	e.sync(t)
	id := e.idOf(t, "gone@x")
	m.Unlabel("INBOX")
	m.Unlabel(testmail.GmailAllMail)
	mailsync.BeforeFlagsWrite = func() {
		mailsync.BeforeFlagsWrite = nil
		require.NoError(t, e.s.SetFlags(bg, []int64{id}, []string{`\Flagged`}, nil))
	}
	t.Cleanup(func() { mailsync.BeforeFlagsWrite = nil })
	e.sync(t)
	exists, err := e.s.MessageExists(bg, id)
	require.NoError(t, err)
	require.True(t, exists, "the pending edit keeps its message")
	pending, err := e.s.PendingOps(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "conflict", pending[0].State)
	e.sync(t)
	pending, err = e.s.PendingOps(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "conflict", pending[0].State)
	assert.NotEmpty(t, pending[0].LastError)
}

func TestArchiveOnRegularIMAPWithAllMailStillMoves(t *testing.T) {
	e := newSyncEnv(t)
	for _, f := range []string{"Work", "All Mail"} {
		require.NoError(t, e.im.User.Create(f, nil))
	}
	e.put(t, "Work", msg("a@x"))
	e.sync(t)
	// Simulate LIST's \\All metadata; that attribute alone does not imply Gmail.
	_, err := e.s.db.Execute(bg, "UPDATE folders SET role = 'all' WHERE id = ?", e.folderID(t, "All Mail"))
	require.NoError(t, err)
	id := e.idOf(t, "a@x")
	require.NoError(t, e.s.MoveToRole(bg, []int64{id}, "archive"))
	e.sync(t)
	assert.Equal(t, []string{"a@x"}, e.onServer(t, "Archive"))
	assert.Empty(t, e.onServer(t, "Work"))
}

func TestGmailUpgradeArchiveBeforeIdentityFetch(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		for _, official := range []bool{false, true} {
			t.Run(fmt.Sprintf("hidden=%v/official=%v", hidden, official), func(t *testing.T) {
				e := newSyncEnv(t, testmail.Gmail())
				require.NoError(t, e.im.User.Create("Work", nil))
				folders := []string{"Work", testmail.GmailAllMail}
				if hidden {
					require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
					folders = []string{"INBOX", "Work"}
				}
				m := e.im.AddGmail(msg("upgrade@x"), nil, folders...)
				e.sync(t)
				rows := e.oldRows(t, e.idOf(t, "upgrade@x"))
				_, err := e.s.db.Execute(bg, "UPDATE accounts SET features = NULL WHERE id = ?", e.acc.ID)
				require.NoError(t, err)
				if official {
					_, err := e.s.db.Execute(bg, "UPDATE accounts SET provider = 'gmail', recv_host = 'imap.gmail.com', recv_port = 993, recv_security = 'tls' WHERE id = ?", e.acc.ID)
					require.NoError(t, err)
					e.im.Stop()
				}
				source := "Work"
				if hidden {
					source = "INBOX"
				}
				require.NoError(t, e.s.MoveToRole(bg, []int64{rows[source]}, "archive"))
				if official {
					_, err := e.s.db.Execute(bg, "UPDATE accounts SET recv_host = ?, recv_port = ?, recv_security = ? WHERE id = ?", e.acc.Receiving.Host, e.acc.Receiving.Port, string(e.acc.Receiving.Security), e.acc.ID)
					require.NoError(t, err)
					e.im.Restart()
				}
				e.sync(t)
				want := []string{"Work"}
				if !hidden {
					want = append(want, testmail.GmailAllMail)
				}
				assert.Equal(t, want, m.Folders())
			})
		}
	}
}

func TestGmailUpgradeArchiveUndoResolvesMergedID(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
	require.NoError(t, e.im.User.Create("Work", nil))
	m := e.im.AddGmail(msg("upgrade@x"), nil, "INBOX", "Work")
	e.sync(t)
	rows := e.oldRows(t, e.idOf(t, "upgrade@x"))
	u, err := e.s.ArchiveMessages(bg, []int64{rows["INBOX"]}, 0)
	require.NoError(t, err)
	e.sync(t)
	assert.Equal(t, []string{"Work"}, m.Folders())
	e.reopen(t)
	require.NoError(t, e.s.UndoArchiveThreads(bg, u))
	e.sync(t)
	assert.Equal(t, []string{"INBOX", "Work"}, m.Folders(), "the undo token survives the upgrade merge")
}

func TestGmailUnknownLegacyArchiveWaitsForCapabilityProbe(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail())
	require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
	require.NoError(t, e.im.User.Create("Work", nil))
	m := e.im.AddGmail(msg("legacy@x"), nil, "INBOX", "Work")
	e.sync(t)
	rows := e.oldRows(t, e.idOf(t, "legacy@x"))
	_, err := e.s.db.Execute(bg, "UPDATE accounts SET features = NULL WHERE id = ?", e.acc.ID)
	require.NoError(t, err)
	e.im.Stop()
	_, err = e.s.ArchiveMessages(bg, []int64{rows["INBOX"]}, 0)
	require.Error(t, err, "unknown legacy semantics cannot be guessed while offline")
	assert.Equal(t, []int64{rows["INBOX"]}, e.listed(t, "INBOX"))
	pending, err := e.s.PendingOps(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Empty(t, pending)
	e.im.Restart()
	_, err = e.s.ArchiveMessages(bg, []int64{rows["INBOX"]}, 0)
	require.NoError(t, err, "a capability probe is sufficient; no new message sync is needed")
	e.sync(t)
	assert.Equal(t, []string{"Work"}, m.Folders())
}

func TestGmailArchiveEntryPointsPreserveLabels(t *testing.T) {
	for _, entry := range []string{"role", "messages", "threads"} {
		for _, inbox := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/inbox=%v", entry, inbox), func(t *testing.T) {
				e := newSyncEnv(t, testmail.Gmail())
				require.NoError(t, e.im.User.Create("Work", nil))
				folders := []string{"Work", testmail.GmailAllMail}
				if inbox {
					folders = append(folders, "INBOX")
				}
				m := e.im.AddGmail(msg("a@x"), nil, folders...)
				e.sync(t)
				id := e.idOf(t, "a@x")
				switch entry {
				case "role":
					require.NoError(t, e.s.MoveToRole(bg, []int64{id}, "archive"))
				case "messages":
					u, err := e.s.ArchiveMessages(bg, []int64{id}, e.folderID(t, testmail.GmailAllMail))
					require.NoError(t, err)
					if !inbox {
						assert.Empty(t, u.Moved)
					}
				case "threads":
					_, err := e.s.ArchiveThreads(bg, []int64{e.threadOf(t, "a@x")})
					require.NoError(t, err)
				}
				assert.Equal(t, []int64{id}, e.listed(t, "Work"))
				assert.Empty(t, e.listed(t, "INBOX"))
				e.sync(t)
				assert.Equal(t, []string{"Work", testmail.GmailAllMail}, m.Folders())
				assert.Equal(t, []int64{id}, e.listed(t, "Work"))
			})
		}
	}
}

func TestGmailHiddenAllMailArchiveEntryPoints(t *testing.T) {
	for _, noMove := range []bool{false, true} {
		for _, entry := range []string{"role", "messages", "threads", "sender"} {
			t.Run(fmt.Sprintf("%s/noMove=%v", entry, noMove), func(t *testing.T) {
				opts := []testmail.Option{testmail.Gmail()}
				if noMove {
					opts = append(opts, testmail.WithoutMove(), testmail.WithoutUIDPlus())
				}
				e := newSyncEnv(t, opts...)
				require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
				require.NoError(t, e.im.User.Create("Work", nil))
				m := e.im.AddGmail(msg("a@x"), nil, "INBOX", "Work")
				e.sync(t)
				id := e.idOf(t, "a@x")
				switch entry {
				case "role":
					require.NoError(t, e.s.MoveToRole(bg, []int64{id}, "archive"))
				case "messages":
					_, err := e.s.ArchiveMessages(bg, []int64{id}, 0)
					require.NoError(t, err)
				case "threads":
					_, err := e.s.ArchiveThreads(bg, []int64{e.threadOf(t, "a@x")})
					require.NoError(t, err)
				case "sender":
					got, err := e.s.GetMessage(bg, id, false)
					require.NoError(t, err)
					u, err := e.s.ArchiveSender(bg, got.From)
					require.NoError(t, err)
					require.Len(t, u.Moved, 1)
				}
				assert.Empty(t, e.listed(t, "INBOX"))
				assert.Equal(t, []int64{id}, e.listed(t, "Work"))
				e.sync(t)
				assert.Equal(t, []string{"Work"}, m.Folders())
				assert.Equal(t, []int64{id}, e.listed(t, "Work"))
				assert.Equal(t, []string{"done"}, e.opStates(t))
				assert.Zero(t, e.im.MovesAndCopies())
			})
		}
	}
}

func TestGmailHiddenAllMailRuleArchive(t *testing.T) {
	for _, inbox := range []bool{false, true} {
		t.Run(fmt.Sprint(inbox), func(t *testing.T) {
			e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
			require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
			require.NoError(t, e.im.User.Create("Work", nil))
			e.sync(t)
			e.saveRule(t, RuleDoc{Version: RuleVersion, Name: "archive", Trigger: TriggerReceive, Match: MatchAll, Actions: []RuleAction{{Kind: ActionArchive}}}, true)
			folders := []string{"Work"}
			if inbox {
				folders = append(folders, "INBOX")
			}
			m := e.im.AddGmail(msg("a@x"), nil, folders...)
			e.sync(t)
			e.waitRuns(t, 1)
			e.sync(t)
			assert.Equal(t, []string{"archive done"}, e.results(t, e.runOf(t, "a@x")))
			assert.Equal(t, []string{"Work"}, m.Folders())
			if inbox {
				_, err := e.s.UndoRuleRun(bg, e.runOf(t, "a@x"))
				require.NoError(t, err)
				e.sync(t)
				assert.Equal(t, []string{"INBOX", "Work"}, m.Folders())
			}
		})
	}
}

func TestGmailHiddenOnlyInboxArchiveUndoSurvivesRestart(t *testing.T) {
	clock := useTestClock(t)
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
	m := e.im.AddGmail(msg("only@x"), nil, "INBOX")
	e.sync(t)
	id := e.idOf(t, "only@x")
	u, err := e.s.ArchiveMessages(bg, []int64{id}, 0)
	require.NoError(t, err)
	assert.Empty(t, e.listed(t, "INBOX"))
	e.sync(t)
	assert.Equal(t, []string{"INBOX"}, m.Folders(), "last addressable location remains during undo window")
	e.reopen(t)
	clock.Advance(10*time.Second - time.Millisecond)
	e.sync(t)
	assert.Equal(t, []string{"INBOX"}, m.Folders(), "restart cannot shorten the durable deadline")
	require.NoError(t, e.s.UndoArchiveThreads(bg, u))
	e.sync(t)
	clock.Advance(10 * time.Second)
	e.sync(t)
	assert.Equal(t, []string{"INBOX"}, m.Folders())
	assert.Equal(t, []int64{id}, e.listed(t, "INBOX"))
	assert.Zero(t, e.im.GmailLabelStores(), "undo cancels an unsent archive")
}

func TestGmailHiddenOnlyInboxArchiveRetainsIdentityAfterWindow(t *testing.T) {
	clock := useTestClock(t)
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
	m := e.im.AddGmail(msg("only@x"), nil, "INBOX")
	e.sync(t)
	id := e.idOf(t, "only@x")
	require.NoError(t, e.s.SetFlags(bg, []int64{id}, []string{`\Flagged`}, nil))
	e.sync(t)
	u, err := e.s.ArchiveMessages(bg, []int64{id}, 0)
	require.NoError(t, err)
	clock.Advance(10 * time.Second)
	e.sync(t)
	assert.Empty(t, m.Folders())
	e.reopen(t)
	e.sync(t)
	exists, err := e.s.MessageExists(bg, id)
	require.NoError(t, err)
	require.True(t, exists, "hidden All Mail means a complete visible scan cannot prove permanent deletion")
	ch, unsub := e.s.Subscribe()
	defer unsub()
	require.NoError(t, e.s.UndoArchiveThreads(bg, u))
	e.sync(t)
	failures := ofType(collect(ch), events.SyncFailed)
	require.NotEmpty(t, failures, "late undo explicitly reports the blocked intent")
	assert.Contains(t, failures[0].Error, "visible IMAP")
	pending, err := e.s.PendingOps(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "pending", pending[0].State)
	assert.Contains(t, pending[0].LastError, "visible IMAP")
	assert.Empty(t, m.Folders(), "no invalid UID or replacement APPEND is used")
	// Another client exposes All Mail again; fetch must run despite the blocked push.
	require.NoError(t, e.im.User.Create(testmail.GmailAllMail, nil))
	m.Label(testmail.GmailAllMail)
	e.sync(t)
	assert.Equal(t, []string{"INBOX", testmail.GmailAllMail}, m.Folders())
	assert.Equal(t, []int64{id}, e.listed(t, "INBOX"))
	assert.Equal(t, []string{`only@x \Flagged`}, e.onServer(t, "INBOX"))
	pending, err = e.s.PendingOps(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func TestGmailHiddenOnlyInboxArchiveOtherEntryPointsUndo(t *testing.T) {
	for _, entry := range []string{"threads", "sender", "rule"} {
		t.Run(entry, func(t *testing.T) {
			clock := useTestClock(t)
			e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
			require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
			e.sync(t)
			if entry == "rule" {
				e.saveRule(t, RuleDoc{Version: RuleVersion, Name: "archive", Trigger: TriggerReceive, Match: MatchAll, Actions: []RuleAction{{Kind: ActionArchive}}}, true)
			}
			m := e.im.AddGmail(msg("only@x"), nil, "INBOX")
			e.sync(t)
			id := e.idOf(t, "only@x")
			var undo func() error
			switch entry {
			case "threads":
				u, err := e.s.ArchiveThreads(bg, []int64{e.threadOf(t, "only@x")})
				require.NoError(t, err)
				undo = func() error { return e.s.UndoArchiveThreads(bg, u) }
			case "sender":
				got, err := e.s.GetMessage(bg, id, false)
				require.NoError(t, err)
				u, err := e.s.ArchiveSender(bg, got.From)
				require.NoError(t, err)
				undo = func() error { return e.s.UndoArchive(bg, u) }
			case "rule":
				e.waitRuns(t, 1)
				run := e.runOf(t, "only@x")
				undo = func() error { kept, err := e.s.UndoRuleRun(bg, run); assert.Zero(t, kept); return err }
			}
			e.sync(t)
			assert.Empty(t, e.listed(t, "INBOX"))
			e.reopen(t)
			require.NoError(t, undo())
			clock.Advance(10 * time.Second)
			e.sync(t)
			assert.Equal(t, []string{"INBOX"}, m.Folders())
			assert.Equal(t, []int64{id}, e.listed(t, "INBOX"))
			assert.Zero(t, e.im.GmailLabelStores())
		})
	}
}

func TestGmailHiddenOnlyInboxRuleLateUndoStaysPending(t *testing.T) {
	clock := useTestClock(t)
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
	e.sync(t)
	e.saveRule(t, RuleDoc{Version: RuleVersion, Name: "archive", Trigger: TriggerReceive, Match: MatchAll, Actions: []RuleAction{{Kind: ActionArchive}}}, true)
	m := e.im.AddGmail(msg("only@x"), nil, "INBOX")
	e.sync(t)
	e.waitRuns(t, 1)
	run := e.runOf(t, "only@x")
	clock.Advance(10 * time.Second)
	e.sync(t)
	assert.Empty(t, m.Folders())
	e.reopen(t)
	ch, unsub := e.s.Subscribe()
	defer unsub()
	kept, err := e.s.UndoRuleRun(bg, run)
	require.NoError(t, err)
	assert.Zero(t, kept)
	e.sync(t)
	failures := ofType(collect(ch), events.SyncFailed)
	require.NotEmpty(t, failures)
	assert.Contains(t, failures[0].Error, "visible IMAP")
	pending, err := e.s.PendingOps(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "pending", pending[0].State)
	assert.Contains(t, pending[0].LastError, "visible IMAP")
}

func TestGmailHiddenOnlyInboxArchiveWorkerWakesAtDeadline(t *testing.T) {
	e, _ := newWorkerEnv(t, testmail.Gmail(), testmail.WithoutMove())
	require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
	m := e.im.AddGmail(msg("only@x"), nil, "INBOX")
	e.sync(t)
	_, err := e.s.ArchiveMessages(bg, []int64{e.idOf(t, "only@x")}, 0)
	require.NoError(t, err)
	e.sync(t)
	assert.Equal(t, []string{"INBOX"}, m.Folders())
	e.clock.Advance(10 * time.Second)
	require.Eventually(t, func() bool { return len(m.Folders()) == 0 }, 5*time.Second, 10*time.Millisecond, "the durable deadline wakes the existing worker")
}

func TestGmailHiddenAllMailArchiveUndoKeepsLabels(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
	require.NoError(t, e.im.User.Create("Work", nil))
	m := e.im.AddGmail(msg("a@x"), nil, "INBOX", "Work")
	e.sync(t)
	id := e.idOf(t, "a@x")
	u, err := e.s.ArchiveMessages(bg, []int64{id}, 0)
	require.NoError(t, err)
	e.sync(t)
	require.NoError(t, e.s.UndoArchiveThreads(bg, u))
	assert.Equal(t, []int64{id}, e.listed(t, "INBOX"))
	e.sync(t)
	assert.Equal(t, []string{"INBOX", "Work"}, m.Folders())
	assert.Equal(t, []string{"done", "done"}, e.opStates(t))
}

func TestGmailLabelMoveLostResponseReconciles(t *testing.T) {
	for _, drop := range []int{1, 2} {
		t.Run(fmt.Sprint(drop), func(t *testing.T) {
			e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove(), testmail.WithoutUIDPlus())
			for _, f := range []string{"Work", "Done", "External"} {
				require.NoError(t, e.im.User.Create(f, nil))
			}
			m := e.im.AddGmail(msg("a@x"), nil, "INBOX", "Work", testmail.GmailAllMail)
			twin := e.im.AddGmail(msg("a@x"), nil, "Done")
			e.sync(t)
			id := e.listed(t, "Work")[0]
			require.NoError(t, e.s.MoveFrom(bg, []int64{id}, e.folderID(t, "Work"), e.folderID(t, "Done")))
			require.NoError(t, e.s.SetFlags(bg, []int64{id}, []string{`\Flagged`}, nil))
			e.im.DropAfterGmailLabelStore(drop)
			require.Error(t, e.s.SyncNow(bg, e.acc.ID))
			pending, err := e.s.PendingOps(bg, e.acc.ID)
			require.NoError(t, err)
			require.Len(t, pending, 2)
			assert.Equal(t, "pending", pending[0].State)
			m.Label("External")
			e.reopen(t)
			if drop == 1 {
				e.im.RejectNextGmailSelect()
				e.sync(t)
				pending, err := e.s.PendingOps(bg, e.acc.ID)
				require.NoError(t, err)
				require.Len(t, pending, 2)
				assert.Equal(t, "pending", pending[0].State, "SELECT NO does not prove source membership is absent")
			}
			e.sync(t)
			e.sync(t)
			assert.Equal(t, []string{"done", "done"}, e.opStates(t))
			assert.EqualValues(t, 2, e.im.GmailLabelStores(), "only the missing delta runs after reconnect")
			assert.Zero(t, e.im.MovesAndCopies())
			assert.Equal(t, []string{"Done", "External", "INBOX", testmail.GmailAllMail}, m.Folders())
			assert.Equal(t, []string{"Done"}, twin.Folders())
			assert.Equal(t, []string{`a@x \Flagged`}, e.onServer(t, "External"))
			assert.Len(t, e.listed(t, "Done"), 2, "same Message-ID is not Gmail identity")
		})
	}
}

func TestGmailWithoutMoveSpecialTransitions(t *testing.T) {
	for _, role := range []string{"trash", "junk"} {
		t.Run(role, func(t *testing.T) {
			e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
			require.NoError(t, e.im.User.Create("Work", nil))
			m := e.im.AddGmail(msg("a@x"), nil, "INBOX", "Work", testmail.GmailAllMail)
			e.sync(t)
			id := e.idOf(t, "a@x")
			require.NoError(t, e.s.MoveToRole(bg, []int64{id}, role))
			require.NoError(t, e.s.SetFlags(bg, []int64{id}, []string{`\Flagged`}, nil))
			e.sync(t)
			folder := testmail.GmailTrash
			if role == "junk" {
				folder = testmail.GmailSpam
			}
			assert.Equal(t, []string{folder}, m.Folders())
			assert.Equal(t, []string{`a@x \Flagged`}, e.onServer(t, folder))
			assert.Equal(t, []string{"done", "done"}, e.opStates(t))
			assert.Zero(t, e.im.MovesAndCopies())
		})
	}
}

func TestGmailWithoutMoveRescuesInterruptedPurge(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	e.im.AddGmail(msg("a@x"), nil, testmail.GmailTrash)
	e.sync(t)
	require.NoError(t, e.s.SetPurge(bg, e.acc.ID, "trash", 1))
	e.age(t, 2)
	e.im.DropOnNextUIDExpunge()
	require.Error(t, e.s.SyncNow(bg, e.acc.ID))
	require.Equal(t, []string{`a@x \Deleted`}, e.onServer(t, testmail.GmailTrash))
	require.NoError(t, e.s.MoveToRole(bg, []int64{e.idOf(t, "a@x")}, "inbox"))
	e.sync(t)
	assert.Equal(t, []string{"a@x"}, e.onServer(t, "INBOX"))
	assert.Empty(t, e.onServer(t, testmail.GmailTrash))
}

func TestGmailUncertainLabelWriteSurvivesRetryBudget(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	for _, f := range []string{"Work", "Done"} {
		require.NoError(t, e.im.User.Create(f, nil))
	}
	m := e.im.AddGmail(msg("a@x"), nil, "Work", testmail.GmailAllMail)
	e.sync(t)
	id := e.idOf(t, "a@x")
	require.NoError(t, e.s.MoveFrom(bg, []int64{id}, e.folderID(t, "Work"), e.folderID(t, "Done")))
	for range mailsync.MaxAttempts + 1 {
		e.im.DropAfterGmailLabelStore(1)
		require.Error(t, e.s.SyncNow(bg, e.acc.ID))
		pending, err := e.s.PendingOps(bg, e.acc.ID)
		require.NoError(t, err)
		require.Len(t, pending, 1)
		assert.Equal(t, "pending", pending[0].State)
		m.Unlabel("Done") // another client changes membership before reconnect
	}
	e.reopen(t)
	e.im.RejectNextGmailSearch()
	e.sync(t)
	pending, err := e.s.PendingOps(bg, e.acc.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, "pending", pending[0].State, "a failed identity check cannot resolve an uncertain write")
	e.sync(t)
	assert.Equal(t, []string{"done"}, e.opStates(t))
	assert.Equal(t, []string{"Done", testmail.GmailAllMail}, m.Folders())
}

func TestGmailLabelMoveAfterFailedMoveUsesRebasedSource(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	for _, f := range []string{"Work", "Done", "Later"} {
		require.NoError(t, e.im.User.Create(f, nil))
	}
	m := e.im.AddGmail(msg("a@x"), nil, "Work", testmail.GmailAllMail)
	e.sync(t)
	id := e.idOf(t, "a@x")
	require.NoError(t, e.s.MoveFrom(bg, []int64{id}, e.folderID(t, "Work"), e.folderID(t, "Done")))
	require.NoError(t, e.s.MoveFrom(bg, []int64{id}, e.folderID(t, "Done"), e.folderID(t, "Later")))
	require.NoError(t, e.im.User.Delete("Done"))
	for range mailsync.MaxAttempts {
		e.sync(t)
	}
	assert.Equal(t, []string{"failed", "done"}, e.opStates(t))
	assert.Equal(t, []string{"Later", testmail.GmailAllMail}, m.Folders())
	assert.Equal(t, []int64{id}, e.listed(t, "Later"))
}

func TestGmailLabelMoveMissingSourceCompletedTarget(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	for _, f := range []string{"Work", "Done"} {
		require.NoError(t, e.im.User.Create(f, nil))
	}
	m := e.im.AddGmail(msg("a@x"), nil, "INBOX", "Work", testmail.GmailAllMail)
	e.sync(t)
	id := e.idOf(t, "a@x")
	require.NoError(t, e.s.MoveFrom(bg, []int64{id}, e.folderID(t, "Work"), e.folderID(t, "Done")))
	m.Label("Done")
	m.Unlabel("Work")
	e.sync(t)
	assert.Equal(t, []string{"done"}, e.opStates(t))
	assert.Zero(t, e.im.GmailLabelStores())
	assert.Equal(t, []int64{id}, e.listed(t, "Done"))
	assert.Equal(t, []string{"Done", "INBOX", testmail.GmailAllMail}, m.Folders())
}

func TestGmailHiddenArchiveLostResponse(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	require.NoError(t, e.im.User.Delete(testmail.GmailAllMail))
	for _, f := range []string{"Work", "External"} {
		require.NoError(t, e.im.User.Create(f, nil))
	}
	m := e.im.AddGmail(msg("a@x"), nil, "INBOX", "Work")
	e.sync(t)
	id := e.idOf(t, "a@x")
	require.NoError(t, e.s.MoveToRole(bg, []int64{id}, "archive"))
	e.im.DropAfterGmailLabelStore(1)
	require.Error(t, e.s.SyncNow(bg, e.acc.ID))
	m.Label("External")
	e.reopen(t)
	e.sync(t)
	assert.Equal(t, []string{"done"}, e.opStates(t))
	assert.EqualValues(t, 1, e.im.GmailLabelStores())
	assert.Equal(t, []string{"External", "Work"}, m.Folders())
	assert.Equal(t, []int64{id}, e.listed(t, "Work"))
	assert.Empty(t, e.listed(t, "INBOX"))
}

func TestGmailWithoutMoveFromAllMailPreservesAllMail(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	require.NoError(t, e.im.User.Create("Work", nil))
	m := e.im.AddGmail(msg("a@x"), nil, testmail.GmailAllMail)
	e.sync(t)
	id := e.idOf(t, "a@x")
	require.NoError(t, e.s.MoveFrom(bg, []int64{id}, e.folderID(t, testmail.GmailAllMail), e.folderID(t, "Work")))
	e.sync(t)
	assert.Equal(t, []string{"Work", testmail.GmailAllMail}, m.Folders())
	assert.Equal(t, []int64{id}, e.listed(t, "Work"))
	assert.Equal(t, []int64{id}, e.listed(t, testmail.GmailAllMail))
	assert.EqualValues(t, 1, e.im.GmailLabelStores())
}

// Gmail's identity extension does not promise MOVE. It must never fall back
// to COPY/Deleted/EXPUNGE, whose Gmail semantics depend on account settings.
func TestGmailWithoutMoveArchivesWithLabels(t *testing.T) {
	e := newSyncEnv(t, testmail.Gmail(), testmail.WithoutMove())
	m := e.im.AddGmail(msg("stay@x"), nil, "INBOX", testmail.GmailAllMail)
	e.sync(t)
	id := e.idOf(t, "stay@x")
	require.NoError(t, e.s.MoveToRole(bg, []int64{id}, "archive"))
	e.sync(t)
	pending, err := e.s.PendingOps(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Empty(t, pending)
	assert.Equal(t, []string{testmail.GmailAllMail}, m.Folders())
	assert.Zero(t, e.im.MovesAndCopies())
	assert.EqualValues(t, 1, e.im.GmailLabelStores())
	assert.Empty(t, e.listed(t, "INBOX"))
	assert.Equal(t, []int64{id}, e.listed(t, testmail.GmailAllMail))
}
