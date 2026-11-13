package events

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFanOut(t *testing.T) {
	b := New()
	a, cancelA := b.Subscribe()
	defer cancelA()
	c, cancelC := b.Subscribe()
	defer cancelC()

	b.Publish(Event{Type: MessageReceived, AccountID: 1, FolderID: 2, MessageIDs: []int64{3}})

	for _, ch := range []<-chan Event{a, c} {
		select {
		case ev := <-ch:
			assert.Equal(t, MessageReceived, ev.Type)
			assert.Equal(t, int64(1), ev.AccountID)
			assert.Equal(t, int64(2), ev.FolderID)
			assert.Equal(t, []int64{3}, ev.MessageIDs)
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive event")
		}
	}
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	b := New()
	_, cancelSlow := b.Subscribe() // never read
	defer cancelSlow()
	fast, cancelFast := b.Subscribe()
	defer cancelFast()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			b.Publish(Event{Type: MessagesChanged, FolderID: int64(i)})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}

	// The fast subscriber keeps a bounded number (its buffer, one in hand and
	// a full queue), the last one included.
	var last Event
	n := 0
	for ev := range drain(fast) {
		last = ev
		n++
	}
	require.LessOrEqual(t, n, 2*subscriberBuffer+1)
	assert.Equal(t, int64(999), last.FolderID)
}

// drain is what ch delivers until it stays quiet for 100 ms.
func drain(ch <-chan Event) <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		for {
			select {
			case ev := <-ch:
				out <- ev
			case <-time.After(100 * time.Millisecond):
				return
			}
		}
	}()
	return out
}

// A command (the tray's Quit) survives a burst of updates behind a slow
// reader, in its place: only updates are dropped.
func TestCommandSurvivesBurst(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	defer cancel()
	for range subscriberBuffer { // fills the channel: what follows queues
		b.Publish(Event{Type: MessagesChanged, FolderID: -1})
	}
	b.Publish(Event{Type: QuitRequested})
	for i := range 3 * subscriberBuffer {
		b.Publish(Event{Type: MessagesChanged, FolderID: int64(i)})
	}
	var got []Event
	for ev := range drain(ch) {
		got = append(got, ev)
	}
	q := slices.IndexFunc(got, func(e Event) bool { return e.Type == QuitRequested })
	require.GreaterOrEqual(t, q, 0, "the command was dropped")
	for _, e := range got[:q] { // the channel's, then at most one the forwarder held
		assert.Equal(t, int64(-1), e.FolderID)
	}
	assert.Equal(t, int64(3*subscriberBuffer-1), got[len(got)-1].FolderID)
	for i := q + 2; i < len(got); i++ {
		assert.Less(t, got[i-1].FolderID, got[i].FolderID, "order kept")
	}
}

func TestUnsubscribeClosesChannel(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()
	cancel()
	cancel() // safe to call twice

	_, ok := <-ch
	assert.False(t, ok, "channel should be closed after unsubscribe")

	// Publishing after unsubscribe must not panic.
	assert.NotPanics(t, func() { b.Publish(Event{Type: SyncStarted}) })
}
