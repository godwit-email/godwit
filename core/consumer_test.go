package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// subjects records what a built-in consumer was handed, in order.
type subjects struct {
	mu  sync.Mutex
	got []string
}

func (r *subjects) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, s)
}

func (r *subjects) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

func (r *subjects) wait(t *testing.T, n int) []string {
	t.Helper()
	require.Eventually(t, func() bool { return len(r.list()) >= n }, 10*time.Second, 10*time.Millisecond)
	return r.list()
}

// A built-in consumer gets new arrivals in order; one that ended before its
// cursor moved gets the event again after a restart.
func TestConsumerAtLeastOnceAcrossRestart(t *testing.T) {
	e := newSyncedEnv(t)
	var rec subjects
	require.NoError(t, e.s.startConsumer(bg, "test", []string{"message.received"}, func(ctx context.Context, ev hookEvent) error {
		rec.add(ev.subject)
		<-ctx.Done() // stuck until Close: the cursor never moves
		return ctx.Err()
	}))
	e.newMail(t, "one@x", "one")
	e.newMail(t, "two@x", "two")
	e.sync(t)
	rec.wait(t, 1)
	e.reopen(t)

	require.NoError(t, e.s.startConsumer(bg, "test", []string{"message.received"}, func(_ context.Context, ev hookEvent) error {
		rec.add(ev.subject)
		return nil
	}))
	rec.wait(t, 3)
	e.newMail(t, "three@x", "three")
	e.sync(t)
	assert.Equal(t, []string{"one", "one", "two", "three"}, rec.wait(t, 4))
}

// A failed delivery is tried again after the backoff, and the next event waits for it.
func TestConsumerRetriesFailure(t *testing.T) {
	clock := useFakeClock(t)
	e := newSyncedEnv(t)
	var rec subjects
	failed := false
	require.NoError(t, e.s.startConsumer(bg, "test", []string{"message.received"}, func(_ context.Context, ev hookEvent) error {
		rec.add(ev.subject)
		if !failed {
			failed = true
			return errors.New("busy")
		}
		return nil
	}))
	e.newMail(t, "one@x", "one")
	e.newMail(t, "two@x", "two")
	e.sync(t)
	assert.Equal(t, []string{"one", "one", "two"}, rec.wait(t, 3))
	assert.Contains(t, clock.got(), hookBackoff(1))
}

// A built-in consumer's cursor is its own: a plugin of the same name keeps a separate one.
func TestConsumerNameApartFromPlugins(t *testing.T) {
	e := newSyncedEnv(t)
	startTestPlugin(t, e.s, "test", hookPerms, recorder)
	var rec subjects
	require.NoError(t, e.s.startConsumer(bg, "test", []string{"message.received"}, func(_ context.Context, ev hookEvent) error {
		rec.add(ev.subject)
		return nil
	}))
	e.newMail(t, "new@x", "fresh")
	e.sync(t)
	assert.Equal(t, []string{"fresh"}, rec.wait(t, 1))
	assert.Equal(t, "fresh", waitLog(t, e.s, "test", 1)[0].Subject)
}

// Cleanup keeps old events a built-in consumer has not passed.
func TestCleanupKeepsConsumerEvents(t *testing.T) {
	s := newMailContextService(t)
	require.NoError(t, s.startConsumer(bg, "test", []string{"message.received"}, func(ctx context.Context, _ hookEvent) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	old := time.Now().Add(-8 * 24 * time.Hour).Unix()
	for range 3 {
		_, err := s.db.Execute(bg, `INSERT INTO events_log(type, account_id, message_id, created_at) VALUES ('message.received', 1, 1, ?)`, old)
		require.NoError(t, err)
	}
	require.NoError(t, s.cleanEvents(bg))
	rows, err := s.db.Query(bg, "SELECT count(*) FROM events_log")
	require.NoError(t, err)
	assert.Equal(t, int64(3), store.Int(rows.Values[0][0]))
}
