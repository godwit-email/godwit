// Package events is a lossy in-process event bus for UI notifications.
// It is not a reliable delivery channel; plugin hooks read the database cursor instead.
package events

import "sync"

const (
	MessageReceived     = "message.received"
	MessagesChanged     = "messages.changed"
	SyncStarted         = "sync.started"
	SyncFinished        = "sync.finished"
	SyncFailed          = "sync.failed"
	OutboxChanged       = "outbox.changed"
	SettingsChanged     = "settings.changed"
	SkillsChanged       = "skills.changed"
	ApprovalsChanged    = "approvals.changed"
	RunsChanged         = "runs.changed" // a run of an ask conversation started or ended
	PluginsChanged      = "plugins.changed"
	MCPChanged          = "mcp.changed"
	LabelsChanged       = "labels.changed"
	SignaturesChanged   = "signatures.changed"
	ModelsChanged       = "models.changed"        // a plugin model's files were downloaded or removed
	MCPSendsChanged     = "mcp_sends.changed"     // godwit mcp's send requests waiting for the user changed
	ImportProgress      = "import.progress"       // an .eml import into AccountID handled Done of Total files
	AddressBooksChanged = "address_books.changed" // address books, collections, order or synced cards changed
	// NotificationClicked: the backend got a click on notification Notification
	// while a ui client is connected; the interface opens what it shows. With
	// none, the backend starts the interface for it instead.
	NotificationClicked = "notification.clicked"
	// QuitRequested: Quit was chosen in the backend's tray; the interface quits as its own Quit does.
	QuitRequested = "quit.requested"
	// OpenRequested and ComposeRequested: Open or Compose was chosen in the
	// backend's tray while a ui client is connected; the interface shows its
	// main window, or a new compose window.
	OpenRequested    = "open.requested"
	ComposeRequested = "compose.requested"
	// SignInCodePreview: a development build's interface asked to preview
	// MessageIDs[0]'s sign-in code card (Kind "link": its sign-in link card);
	// the backend's tray, which the card belongs to, shows it.
	SignInCodePreview = "signin_code.preview"
)

// Command reports whether an event of type t asks the interface to do
// something (a click, the tray's Open, Compose or Quit) rather than telling it
// what changed: a reader that falls behind may lose the latter only.
func Command(t string) bool {
	return t == NotificationClicked || t == QuitRequested || t == OpenRequested || t == ComposeRequested || t == SignInCodePreview
}

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
	// Done and Total are set on import.progress.
	Done, Total int
	// Notification is set on notification.clicked: the clicked notification's ID.
	Notification string
}

// Bus fans events out to subscribers. Publish never blocks.
type Bus struct {
	mu   sync.Mutex
	next uint64
	subs map[uint64]*sub
}

// sub is one subscriber. Events go straight into out while it has room and
// nothing waits before them; past that they queue in q (under the Bus's
// lock), which the forwarder hands on in order.
type sub struct {
	out  chan Event    // buffered subscriberBuffer
	q    []Event       // waiting for room in out
	busy bool          // the forwarder holds q's head, sending it
	wake chan struct{} // q grew; buffered 1
	done chan struct{} // closed by unsubscribe
}

// New returns an empty Bus.
func New() *Bus {
	return &Bus{subs: make(map[uint64]*sub)}
}

// Subscribe returns a channel of events and a func that unsubscribes it.
// The unsubscribe func closes the channel and is safe to call more than once.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	s := &sub{out: make(chan Event, subscriberBuffer), wake: make(chan struct{}, 1), done: make(chan struct{})}
	b.subs[id] = s
	go b.forward(s)
	return s.out, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(s.done)
		}
	}
}

// forward hands s's queue on to s.out, oldest first, until unsubscribed.
func (b *Bus) forward(s *sub) {
	defer close(s.out)
	for {
		b.mu.Lock()
		if len(s.q) == 0 {
			s.busy = false
			b.mu.Unlock()
			select {
			case <-s.wake:
				continue
			case <-s.done:
				return
			}
		}
		e := s.q[0]
		s.q = s.q[1:]
		s.busy = true // Publish must not pass e
		b.mu.Unlock()
		select {
		case s.out <- e:
		case <-s.done:
			return
		}
	}
}

// Publish delivers e to every subscriber without blocking. Past a full out,
// a subscriber's queue keeps at most subscriberBuffer events, losing its
// oldest that is not a Command (UI only, lossy by design; plugins read the
// database cursor): the tray's Open or Quit and clicks survive a burst.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs {
		if !s.busy && len(s.q) == 0 {
			select {
			case s.out <- e: // at once, as a reader polling right after expects
				continue
			default:
			}
		}
		s.q = append(s.q, e)
		if len(s.q) > subscriberBuffer {
			i := 0
			for i < len(s.q)-1 && Command(s.q[i].Type) {
				i++
			}
			s.q = append(s.q[:i], s.q[i+1:]...)
		}
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}
