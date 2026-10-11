package events

import (
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

	// The fast subscriber keeps only the most recent 64 events, the last one included.
	var last Event
	n := 0
	for len(fast) > 0 {
		last = <-fast
		n++
	}
	require.Equal(t, 64, n)
	assert.Equal(t, int64(999), last.FolderID)
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
