package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/imapbackend"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/mcpclients"
)

const defaultBenchPoll = 50 * time.Millisecond

// BenchmarkNotifyLatency measures server-arrival-to-Platform.Notify latency on
// a seeded mailbox (ScaleSmall, seed 716) for push (IMAP IDLE) and polling
// (WithoutIdle). App's 1s notifyWait batching window is advanced on the fake
// clock as soon as mailArrived arms it, so ns/op reports pipeline latency
// excluding that fixed 1s wait.
func BenchmarkNotifyLatency(b *testing.B) {
	b.Run("idle", func(b *testing.B) {
		core.SetChatterWithinForTest(b, 0)
		runNotifyLatencyBench(b)
	})
	b.Run("poll", func(b *testing.B) {
		poll := benchPollInterval(b)
		old := imapbackend.PollInterval
		imapbackend.PollInterval = poll
		b.Cleanup(func() { imapbackend.PollInterval = old })
		runNotifyLatencyBench(b, coretest.WithoutIdle())
	})
}

func runNotifyLatencyBench(b *testing.B, opts ...coretest.Option) {
	b.Helper()
	b.ReportAllocs()
	nb := newNotifyBench(b, opts...)
	var totalDown int64

	b.StopTimer()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		subj := fmt.Sprintf("Bench arrival %d", i)
		raw := coretest.Msg("Bob <bob@example.org>", nb.acc.Email, subj, "benchmark arrival body "+subj)
		im := nb.env.IMAP(nb.acc.ID)
		before := im.BytesSent()

		b.StartTimer()
		if _, err := im.User.Append("INBOX", bytes.NewReader(raw), &imap.AppendOptions{}); err != nil {
			b.Fatal(err)
		}
		n := nb.awaitNote()
		b.StopTimer()

		if n.Subtitle != subj || n.MessageID == 0 {
			b.Fatalf("notification %+v, want subtitle %q", n, subj)
		}
		totalDown += im.BytesSent() - before
		nb.finishRound(n.MessageID)
	}
	b.ReportMetric(float64(totalDown)/float64(b.N), "bytes-down/op")
}

func benchPollInterval(tb testing.TB) time.Duration {
	tb.Helper()
	if s := os.Getenv("GODWIT_BENCH_POLL"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			tb.Fatalf("invalid GODWIT_BENCH_POLL %q: %v", s, err)
		}
		return d
	}
	return defaultBenchPoll
}

type notifyBench struct {
	tb         testing.TB
	env        *coretest.Env
	acc        core.Account
	app        *App
	plat       *fakePlatform
	clock      *fakeClock
	syncCh     <-chan events.Event
	notes      chan Notification
	wake       chan struct{}
	batchArmed atomic.Bool

	mu    sync.Mutex
	queue []func()
}

func newNotifyBench(tb testing.TB, opts ...coretest.Option) *notifyBench {
	tb.Helper()
	env := coretest.New(tb, opts...)
	syncCh, unsub := env.Svc.Subscribe()
	tb.Cleanup(unsub)

	acc := env.AddAccount(tb, "alice@example.com", coretest.Seeded(coretest.ScaleSmall, coretest.DefaultSeed))
	nb := &notifyBench{
		tb:     tb,
		env:    env,
		acc:    acc,
		plat:   &fakePlatform{svc: env.Svc, supported: true},
		clock:  newFakeClock(),
		syncCh: syncCh,
		notes:  make(chan Notification, 16),
		wake:   make(chan struct{}, 1),
	}
	nb.awaitInitialSync()

	nb.plat.onNotify = func(n Notification) {
		nb.notes <- n
		nb.signal()
	}
	nb.clock.onAfter = func(d time.Duration) {
		if d == notifyWait {
			nb.batchArmed.Store(true)
			nb.signal()
		}
	}
	nb.app = New(Deps{
		Svc:            env.Svc,
		Platform:       nb.plat,
		Post:           nb.post,
		Clock:          nb.clock,
		SystemLanguage: "zh-CN",
		Logf:           func(string, ...any) {},
	})
	home := tb.TempDir()
	nb.app.mcpEnv = func() (mcpclients.Env, error) {
		return mcpclients.Env{Home: home, OS: "darwin", Temp: home, Run: func(string, ...string) error { return nil }}, nil
	}
	ctx, stop := context.WithCancel(context.Background())
	tb.Cleanup(stop)
	nb.app.Start(ctx)
	tb.Cleanup(nb.app.Close)

	nb.waitLoaded()
	nb.clock.advance(notifyWait)
	nb.drain()
	nb.batchArmed.Store(false)
	nb.resetPlat()
	return nb
}

func (nb *notifyBench) signal() {
	select {
	case nb.wake <- struct{}{}:
	default:
	}
}

func (nb *notifyBench) post(f func()) {
	nb.mu.Lock()
	nb.queue = append(nb.queue, f)
	nb.mu.Unlock()
	nb.signal()
}

func (nb *notifyBench) drain() {
	for {
		nb.mu.Lock()
		q := nb.queue
		nb.queue = nil
		nb.mu.Unlock()
		if len(q) == 0 {
			return
		}
		for _, f := range q {
			f()
		}
	}
}

func (nb *notifyBench) awaitInitialSync() {
	nb.tb.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-nb.syncCh:
			if ev.AccountID != nb.acc.ID {
				continue
			}
			if ev.Type == events.SyncFailed {
				nb.tb.Fatalf("initial sync failed: %s", ev.Error)
			}
			if ev.Type == events.SyncFinished {
				return
			}
		case <-deadline:
			nb.tb.Fatal("timed out waiting for initial sync")
		}
	}
}

func (nb *notifyBench) awaitNote() Notification {
	nb.tb.Helper()
	deadline := time.After(10 * time.Second)
	for {
		nb.drain()
		if nb.batchArmed.Swap(false) {
			nb.clock.advance(notifyWait)
			continue
		}
		select {
		case n := <-nb.notes:
			return n
		case <-nb.wake:
		case <-deadline:
			nb.tb.Fatal("timed out waiting for Platform.Notify")
		}
	}
}

func (nb *notifyBench) finishRound(msgID int64) {
	nb.tb.Helper()
	deadline := time.After(10 * time.Second)
	sawArrival := false
	for {
		select {
		case ev := <-nb.syncCh:
			if ev.AccountID != nb.acc.ID {
				continue
			}
			if ev.Type == events.SyncFailed {
				nb.tb.Fatalf("sync failed: %s", ev.Error)
			}
			if ev.Type == events.MessageReceived && slices.Contains(ev.MessageIDs, msgID) {
				sawArrival = true
			}
			if sawArrival && ev.Type == events.SyncFinished {
				nb.drain()
				if nb.app.refreshStop != nil {
					nb.clock.advance(refreshEvery)
					nb.drain()
				}
				nb.batchArmed.Store(false)
				nb.resetPlat()
				return
			}
		case <-deadline:
			nb.tb.Fatalf("timed out waiting for sync round of message %d to finish", msgID)
		}
	}
}

func (nb *notifyBench) waitLoaded() {
	nb.tb.Helper()
	deadline := time.After(10 * time.Second)
	for {
		nb.drain()
		if nb.app.loaded && !nb.app.mail.loading {
			return
		}
		select {
		case <-nb.wake:
		case <-deadline:
			nb.tb.Fatal("timed out waiting for initial App load")
		}
	}
}

func (nb *notifyBench) resetPlat() {
	nb.plat.mu.Lock()
	nb.plat.calls = nb.plat.calls[:0]
	nb.plat.notes = nb.plat.notes[:0]
	nb.plat.mu.Unlock()
}

func (nb *notifyBench) deliverAndAwait(raw []byte) (Notification, int64) {
	nb.tb.Helper()
	im := nb.env.IMAP(nb.acc.ID)
	before := im.BytesSent()
	if _, err := im.User.Append("INBOX", bytes.NewReader(raw), &imap.AppendOptions{}); err != nil {
		nb.tb.Fatal(err)
	}
	n := nb.awaitNote()
	down := im.BytesSent() - before
	nb.finishRound(n.MessageID)
	return n, down
}

func TestNotifyArrivalLatencyPaths(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		core.SetChatterWithinForTest(t, 0)
		nb := newNotifyBench(t)
		if nb.acc.Features[backend.FeaturePush].Level != backend.LevelSupported {
			t.Fatalf("push feature = %+v, want supported", nb.acc.Features[backend.FeaturePush])
		}
		if notes := nb.plat.Notes(); len(notes) != 0 {
			t.Fatalf("seeded mail produced %d notifications: %+v", len(notes), notes)
		}
		for i := range 3 {
			subj := fmt.Sprintf("Push arrival %d", i)
			raw := coretest.Msg("Bob <bob@example.org>", nb.acc.Email, subj, "push body "+subj)
			n, down := nb.deliverAndAwait(raw)
			if n.Subtitle != subj || n.Title != "Bob" || !strings.Contains(n.Body, "push body "+subj) || n.MessageID == 0 {
				t.Fatalf("notification %+v, want subtitle %q", n, subj)
			}
			if down <= 0 {
				t.Fatalf("bytes down = %d, want > 0", down)
			}
		}
	})

	t.Run("poll", func(t *testing.T) {
		t.Setenv("GODWIT_BENCH_POLL", "15ms")
		poll := benchPollInterval(t)
		if poll != 15*time.Millisecond {
			t.Fatalf("benchPollInterval = %v, want 15ms", poll)
		}
		old := imapbackend.PollInterval
		imapbackend.PollInterval = poll
		t.Cleanup(func() { imapbackend.PollInterval = old })

		nb := newNotifyBench(t, coretest.WithoutIdle())
		if nb.acc.Features[backend.FeaturePush].Level != backend.LevelUnsupported {
			t.Fatalf("push feature = %+v, want unsupported", nb.acc.Features[backend.FeaturePush])
		}
		if notes := nb.plat.Notes(); len(notes) != 0 {
			t.Fatalf("seeded mail produced %d notifications: %+v", len(notes), notes)
		}
		for i := range 3 {
			subj := fmt.Sprintf("Poll arrival %d", i)
			raw := coretest.Msg("Bob <bob@example.org>", nb.acc.Email, subj, "poll body "+subj)
			n, down := nb.deliverAndAwait(raw)
			if n.Subtitle != subj || n.Title != "Bob" || !strings.Contains(n.Body, "poll body "+subj) || n.MessageID == 0 {
				t.Fatalf("notification %+v, want subtitle %q", n, subj)
			}
			if down <= 0 {
				t.Fatalf("bytes down = %d, want > 0", down)
			}
		}
	})
}
