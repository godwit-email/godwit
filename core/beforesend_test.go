package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sendPerms = `{"mail":["read"]}`

// checker counts its beforeSend calls and logs each subject it saw in
// ctx.storage, then returns what result(draft) gives.
func checker(result string) string {
	return `export default function (ctx: any) {
  ctx.on("message.beforeSend", async (d: any) => {
    await ctx.storage.set("calls", String(Number((await ctx.storage.get("calls")) ?? 0) + 1))
    const seen = JSON.parse((await ctx.storage.get("seen")) ?? "[]")
    seen.push(d.subject)
    await ctx.storage.set("seen", JSON.stringify(seen))
    return (` + result + `)(d)
  })
}`
}

func calls(t *testing.T, s *Service, name string) int {
	t.Helper()
	rows, err := s.db.Query(t.Context(), "SELECT value FROM plugin_kv WHERE plugin = ? AND key = 'calls'", name)
	require.NoError(t, err)
	if len(rows.Values) == 0 {
		return 0
	}
	var n int
	fmt.Sscan(store.Str(rows.Values[0][0]), &n)
	return n
}

func seen(t *testing.T, s *Service, name string) string {
	t.Helper()
	rows, err := s.db.Query(t.Context(), "SELECT value FROM plugin_kv WHERE plugin = ? AND key = 'seen'", name)
	require.NoError(t, err)
	if len(rows.Values) == 0 {
		return ""
	}
	return store.Str(rows.Values[0][0])
}

// sendDraft saves a sendable draft in account 1.
func sendDraft(t *testing.T, s *Service, subject string) Draft {
	t.Helper()
	d, err := s.SaveDraft(t.Context(), Draft{AccountID: 1, Mode: "new", To: []string{"bob@x.example"}, Subject: subject, HTML: "<p>hi</p>"})
	require.NoError(t, err)
	return d
}

func outboxCount(t *testing.T, s *Service) int {
	t.Helper()
	rows, err := s.db.Query(t.Context(), "SELECT count(*) FROM outbox")
	require.NoError(t, err)
	return int(store.Int(rows.Values[0][0]))
}

func pluginID(t *testing.T, s *Service, name string) string {
	t.Helper()
	hash, ok := s.RunningPluginForTest(name)
	require.True(t, ok)
	return name + "@" + hash
}

func TestBeforeSendWarnThenAcknowledge(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "warner", sendPerms, checker(`d => ({ warn: "no attachment: " + d.subject })`))
	startTestPlugin(t, s, "auditor", sendPerms, checker(`d => undefined`))
	d := sendDraft(t, s, "report")

	err := s.Send(t.Context(), d.ID)
	var we *SendWarningsError
	require.ErrorAs(t, err, &we)
	c := we.Check
	assert.Equal(t, d.ID, c.DraftID)
	assert.Equal(t, d.Revision, c.Revision)
	assert.Equal(t, []string{pluginID(t, s, "auditor"), pluginID(t, s, "warner")}, c.Plugins, "sorted by name")
	assert.Equal(t, []SendWarning{{Plugin: "warner", Code: "plugin_warning", Message: "no attachment: report"}}, c.Warnings)
	assert.Len(t, c.Digest, 64)
	assert.Zero(t, outboxCount(t, s))
	assert.Equal(t, 1, calls(t, s, "warner"))

	require.NoError(t, s.SendAcknowledged(t.Context(), d.ID, c.Digest))
	assert.Equal(t, 1, outboxCount(t, s))
	assert.Equal(t, 1, calls(t, s, "warner"), "an acknowledgement does not run the checks again")
	assert.Equal(t, 1, calls(t, s, "auditor"))
	_, err = s.GetDraft(t.Context(), d.ID)
	assert.Error(t, err, "the draft is gone once queued")
}

func TestBeforeSendBlock(t *testing.T) {
	s := newMailContextService(t)
	long := strings.Repeat("x", 600)
	startTestPlugin(t, s, "a-blocker", sendPerms, checker(`d => ({ block: "secret\u0007\u0007leak\n`+long+`" })`))
	startTestPlugin(t, s, "b-later", sendPerms, checker(`d => undefined`))
	d := sendDraft(t, s, "report")

	err := s.Send(t.Context(), d.ID)
	var be *SendBlockedError
	require.ErrorAs(t, err, &be)
	assert.Equal(t, "a-blocker", be.Plugin)
	assert.True(t, strings.HasPrefix(be.Message, "secret leak xxx"), "a run of control characters is one space: %q", be.Message)
	assert.Equal(t, 500, utf8.RuneCountInString(be.Message))
	assert.Zero(t, calls(t, s, "b-later"), "a block stops the checks")
	assert.Zero(t, outboxCount(t, s))
	_, err = s.GetDraft(t.Context(), d.ID)
	require.NoError(t, err)

	// A block can never be acknowledged: there is no check to bind to.
	err = s.SendAcknowledged(t.Context(), d.ID, strings.Repeat("0", 64))
	require.ErrorAs(t, err, &be)
	assert.Zero(t, outboxCount(t, s))
}

// timeoutClock fires the 5 s beforeSend limit at once and never fires anything else.
type timeoutClock struct{}

func (timeoutClock) AfterFunc(d time.Duration, f func()) func() bool {
	if d == 5*time.Second {
		go f()
	}
	return func() bool { return false }
}

func TestBeforeSendTimeoutIsWarning(t *testing.T) {
	testPluginClock = timeoutClock{}
	t.Cleanup(func() { testPluginClock = nil })
	s := newMailContextService(t)
	startTestPlugin(t, s, "slow", sendPerms, `export default function (ctx: any) {
  ctx.on("message.beforeSend", () => new Promise(() => {}))
}`)
	d := sendDraft(t, s, "report")

	err := s.Send(t.Context(), d.ID)
	var we *SendWarningsError
	require.ErrorAs(t, err, &we)
	assert.Equal(t, []SendWarning{{Plugin: "slow", Code: "no_response", Message: "plugin slow did not respond"}}, we.Check.Warnings)
	rows, err := s.db.Query(t.Context(), "SELECT failures FROM plugins WHERE name = 'slow'")
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows.Values[0][0], "a timeout counts as a failure")
	assert.Zero(t, outboxCount(t, s))

	require.NoError(t, s.SendAcknowledged(t.Context(), d.ID, we.Check.Digest))
	assert.Equal(t, 1, outboxCount(t, s))
}

func TestAcknowledgeAfterEditRechecks(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "warner", sendPerms, checker(`d => ({ warn: d.subject })`))
	d := sendDraft(t, s, "first")
	var we *SendWarningsError
	require.ErrorAs(t, s.Send(t.Context(), d.ID), &we)
	old := we.Check

	d.Subject = "second"
	_, err := s.SaveDraft(t.Context(), d)
	require.NoError(t, err)
	require.ErrorAs(t, s.SendAcknowledged(t.Context(), d.ID, old.Digest), &we, "the edit made the old check stale")
	assert.Equal(t, []SendWarning{{Plugin: "warner", Code: "plugin_warning", Message: "second"}}, we.Check.Warnings)
	assert.NotEqual(t, old.Digest, we.Check.Digest)
	assert.Equal(t, 2, calls(t, s, "warner"))
	assert.Zero(t, outboxCount(t, s))

	require.NoError(t, s.SendAcknowledged(t.Context(), d.ID, we.Check.Digest))
	assert.Equal(t, 1, outboxCount(t, s))
	assert.Equal(t, 2, calls(t, s, "warner"))
}

func TestAcknowledgeAfterPluginUpdateRechecks(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "warner", sendPerms, checker(`d => ({ warn: "v1" })`))
	d := sendDraft(t, s, "report")
	var we *SendWarningsError
	require.ErrorAs(t, s.Send(t.Context(), d.ID), &we)
	old := we.Check

	startTestPlugin(t, s, "warner", sendPerms, checker(`d => ({ warn: "v2" })`))
	require.NotEqual(t, old.Plugins, []string{pluginID(t, s, "warner")})
	require.ErrorAs(t, s.SendAcknowledged(t.Context(), d.ID, old.Digest), &we, "new code checks again")
	assert.Equal(t, []SendWarning{{Plugin: "warner", Code: "plugin_warning", Message: "v2"}}, we.Check.Warnings)
	assert.Equal(t, 2, calls(t, s, "warner"))
	assert.Zero(t, outboxCount(t, s))
}

func TestBeforeSendCannotWrite(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Hi", "Bob <bob@x>", "hello", "", 1_700_000_000)
	startTestPlugin(t, s, "writer", `{"mail":["read","write"]}`, fmt.Sprintf(`export default function (ctx: any) {
  ctx.on("message.beforeSend", async () => {
    try { await ctx.mail.setFlags([%d], ["\\Seen"], []) } catch (e: any) { return { warn: e.message } }
    return undefined
  })
}`, id))
	d := sendDraft(t, s, "report")

	done := make(chan error, 1)
	go func() { done <- s.Send(t.Context(), d.ID) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Send deadlocked")
	}
	var we *SendWarningsError
	require.ErrorAs(t, err, &we)
	require.Len(t, we.Check.Warnings, 1)
	assert.Contains(t, we.Check.Warnings[0].Message, "permission denied: mail:write")
	rows, err := s.db.Query(t.Context(), "SELECT flags, (SELECT COUNT(*) FROM ops) FROM messages WHERE id = ?", id)
	require.NoError(t, err)
	assert.Equal(t, []any{"", int64(0)}, rows.Values[0], "nothing written")
}

func TestEditDuringCheck(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "checker", sendPerms, checker(`d => undefined`))
	d := sendDraft(t, s, "old")

	// The user saves while the check runs (outside the edit lock, so the
	// save does not wait); the new version is checked and sent, never the old.
	var once sync.Once
	duringSendCheck = func() {
		once.Do(func() {
			nd := d
			nd.Subject = "new"
			_, err := s.SaveDraft(t.Context(), nd)
			assert.NoError(t, err)
		})
	}
	t.Cleanup(func() { duringSendCheck = nil })
	require.NoError(t, s.Send(t.Context(), d.ID))
	assert.Equal(t, `["old","new"]`, seen(t, s, "checker"))
	out, err := s.ListOutbox(t.Context(), 1)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "new", out[0].Subject)

	// Edited during every check: two changes in a row give up.
	d = sendDraft(t, s, "busy")
	duringSendCheck = func() {
		cur, err := s.GetDraft(t.Context(), d.ID)
		require.NoError(t, err)
		cur.Subject += "!"
		_, err = s.SaveDraft(t.Context(), cur)
		assert.NoError(t, err)
	}
	assert.ErrorIs(t, s.Send(t.Context(), d.ID), ErrStaleDraft)
	assert.Equal(t, 1, outboxCount(t, s))
}

func TestBeforeSendNoPlugins(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "quiet", sendPerms, stashCtx) // no beforeSend handler
	startTestPlugin(t, s, "off", sendPerms, checker(`d => ({ block: "no" })`))
	require.NoError(t, s.SetPluginEnabled(t.Context(), "off", false))
	d := sendDraft(t, s, "report")
	require.NoError(t, s.Send(t.Context(), d.ID))
	assert.Equal(t, 1, outboxCount(t, s))
	assert.Zero(t, calls(t, s, "off"))

	// An unknown digest is a plain Send.
	d = sendDraft(t, s, "again")
	require.NoError(t, s.SendAcknowledged(t.Context(), d.ID, "nope"))
	assert.Equal(t, 2, outboxCount(t, s))
}

// Fix round 1

func TestBeforeSendFreshDraftPerChecker(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "a-mutator", sendPerms, checker(`d => { d.subject = "evil"; d.html = ""; d.to.push("evil@x.example"); d.to[0] = "x@x.example" }`))
	startTestPlugin(t, s, "b-viewer", sendPerms, checker(`d => ({ warn: JSON.stringify([d.subject, d.to, d.html]) })`))
	d := sendDraft(t, s, "report")
	var we *SendWarningsError
	require.ErrorAs(t, s.Send(t.Context(), d.ID), &we)
	require.Len(t, we.Check.Warnings, 1)
	assert.Equal(t, `["report",["bob@x.example"],"<p>hi</p>"]`, we.Check.Warnings[0].Message)
}

func TestEmptyBlockAndWarnCount(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "warner", sendPerms, checker(`d => ({ warn: "" })`))
	d := sendDraft(t, s, "report")
	var we *SendWarningsError
	require.ErrorAs(t, s.Send(t.Context(), d.ID), &we)
	require.Len(t, we.Check.Warnings, 1)
	assert.Equal(t, "plugin_warning", we.Check.Warnings[0].Code)
	assert.NotEmpty(t, we.Check.Warnings[0].Message)

	startTestPlugin(t, s, "blocker", sendPerms, checker(`d => ({ block: "" })`))
	var be *SendBlockedError
	require.ErrorAs(t, s.Send(t.Context(), d.ID), &be)
	assert.Equal(t, "blocker", be.Plugin)
	assert.NotEmpty(t, be.Message)
	assert.Zero(t, outboxCount(t, s))
}

func TestSendCheckCopies(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "warner", sendPerms, checker(`d => ({ warn: "w" })`))
	d := sendDraft(t, s, "report")
	var we *SendWarningsError
	require.ErrorAs(t, s.Send(t.Context(), d.ID), &we)
	digest := we.Check.Digest
	we.Check.Plugins[0] = "someone@else"
	we.Check.Warnings[0].Message = "changed"
	require.NoError(t, s.SendAcknowledged(t.Context(), d.ID, digest))
	assert.Equal(t, 1, calls(t, s, "warner"), "the stored check kept its own binding")
	assert.Equal(t, 1, outboxCount(t, s))
}

func TestSendCheckExpiry(t *testing.T) {
	s := newMailContextService(t)
	start := time.Unix(1_700_000_000, 0)
	var mu sync.Mutex
	now := start
	s.sendNow = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	set := func(t time.Time) { mu.Lock(); now = t; mu.Unlock() }
	startTestPlugin(t, s, "warner", sendPerms, checker(`d => ({ warn: "w" })`))
	d := sendDraft(t, s, "report")
	var we *SendWarningsError
	require.ErrorAs(t, s.Send(t.Context(), d.ID), &we)

	set(start.Add(10*time.Minute - time.Nanosecond))
	_, ok := s.sendCheck(we.Check.Digest)
	assert.True(t, ok, "still valid just before 10 minutes")
	set(start.Add(10 * time.Minute))
	require.ErrorAs(t, s.SendAcknowledged(t.Context(), d.ID, we.Check.Digest), &we, "expired: checked again")
	assert.Equal(t, 2, calls(t, s, "warner"))
	assert.Zero(t, outboxCount(t, s))
}

// The plugin list is compared and the draft queued while plugin changes
// wait: a plugin enabled before the comparison re-checks (and blocks), one
// enabled after it waits until the draft is queued.
func TestPluginEnabledDuringSend(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "a-checker", sendPerms, checker(`d => undefined`))
	startTestPlugin(t, s, "b-blocker", sendPerms, checker(`d => ({ block: "no" })`))
	require.NoError(t, s.SetPluginEnabled(t.Context(), "b-blocker", false))
	d := sendDraft(t, s, "one")
	var once sync.Once
	duringSendCheck = func() {
		once.Do(func() { assert.NoError(t, s.SetPluginEnabled(t.Context(), "b-blocker", true)) })
	}
	t.Cleanup(func() { duringSendCheck = nil })
	var be *SendBlockedError
	require.ErrorAs(t, s.Send(t.Context(), d.ID), &be, "re-checked with the new plugin")
	assert.Equal(t, "b-blocker", be.Plugin)
	duringSendCheck = nil

	require.NoError(t, s.SetPluginEnabled(t.Context(), "b-blocker", false))
	enabled := make(chan error, 1)
	afterDraftLoad = func() {
		afterDraftLoad = nil
		go func() { enabled <- s.SetPluginEnabled(context.Background(), "b-blocker", true) }()
		select {
		case err := <-enabled:
			t.Error("a plugin was enabled between the plugin-list check and the enqueue")
			enabled <- err
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Cleanup(func() { afterDraftLoad = nil })
	require.NoError(t, s.Send(t.Context(), d.ID))
	require.NoError(t, <-enabled)
	assert.Equal(t, 1, outboxCount(t, s))
}

func TestCloseDuringBeforeSend(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "slow", sendPerms, `export default function (ctx: any) {
  ctx.on("message.beforeSend", () => new Promise(() => {}))
}`)
	d := sendDraft(t, s, "report")
	started := make(chan struct{})
	duringSendCheck = func() { close(started) }
	t.Cleanup(func() { duringSendCheck = nil })
	sent := make(chan error, 1)
	go func() { sent <- s.Send(context.Background(), d.ID) }()
	<-started
	time.Sleep(50 * time.Millisecond) // the check is running
	require.NoError(t, s.Close())
	select {
	case err := <-sent:
		assert.True(t, errors.Is(err, ErrClosed) || errors.Is(err, context.Canceled), "got %v", err)
	case <-time.After(5 * time.Second): // Close waited for Send's end; this only covers the hand-off to the channel
		t.Fatal("Send did not end after Close")
	}
	assert.ErrorIs(t, s.Send(context.Background(), d.ID), ErrClosed)
	assert.ErrorIs(t, s.SendAcknowledged(context.Background(), d.ID, "x"), ErrClosed)
}
