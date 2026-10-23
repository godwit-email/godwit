// Package events is a lossy in-process event bus for UI notifications.
// It is not a reliable delivery channel; plugin hooks read the database cursor instead.
package events

import "sync"

const (
	MessageReceived   = "message.received"
	MessagesChanged   = "messages.changed"
	SyncStarted       = "sync.started"
	SyncFinished      = "sync.finished"
	SyncFailed        = "sync.failed"
	OutboxChanged     = "outbox.changed"
	SettingsChanged   = "settings.changed"
	ApprovalsChanged  = "approvals.changed"
	RunsChanged       = "runs.changed" // a run of an ask conversation started or ended
	PluginsChanged    = "plugins.changed"
	MCPChanged        = "mcp.changed"
	LabelsChanged     = "labels.changed"
	SignaturesChanged = "signatures.changed"
	ModelsChanged     = "models.changed" // a plugin model's files were downloaded or removed
)

// subscriberBuffer is the per-subscriber queue capacity.
const subscriberBuffer = 64

// Event describes something that happened. Only IDs are carried, never IMAP UIDs.
type Event struct {
	Type       string
	AccountID  int64
	FolderID   int64
	ThreadIDs  []int64
	MessageIDs []int64
	Error      string
	// Kind is a sync.failed's conn.FailureKind (dns, connect, timeout, tls,
	// auth, protocol), or empty when the connection was fine.
	Kind string
	// ConversationID is set on approvals.changed and runs.changed.
	ConversationID int64
}

// Bus fans events out to subscribers. Publish never blocks.
type Bus struct {
	mu   sync.Mutex
	next uint64
	subs map[uint64]chan Event
}

// New returns an empty Bus.
func New() *Bus {
	return &Bus{subs: make(map[uint64]chan Event)}
}

// Subscribe returns a channel of events and a func that unsubscribes it.
// The unsubscribe func closes the channel and is safe to call more than once.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	ch := make(chan Event, subscriberBuffer)
	b.subs[id] = ch
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
}

// Publish delivers e to every subscriber without blocking.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
			// ponytail: UI-only, lossy by design. A full queue drops its oldest event so the
			// newest one still gets through. Reliable delivery for plugins lives in the plugin plan.
			select {
			case <-ch:
			default:
			}
			ch <- e // only Publish sends, and it holds the lock, so there is room now
		}
	}
}
