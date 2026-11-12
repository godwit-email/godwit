package app

import (
	"cmp"
	"context"
	"log"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
)

// NotifyWait is how long new mail gathers per account before reading it for notifications.
const NotifyWait = time.Second

const notifyWait = NotifyWait

// arrivals are an account's new messages and their threads awaiting notifications.
type arrivals struct{ msgs, threads []int64 }

// MailNotifierService is what MailNotifier reads and updates on the mail engine.
type MailNotifierService interface {
	ListFolders(ctx context.Context, accountID int64) ([]core.Folder, error)
	GetThread(ctx context.Context, threadID int64) (core.Thread, error)
	BlockedSenders(ctx context.Context) ([]string, error)
	MoveToRole(ctx context.Context, messageIDs []int64, role string) error
	RecentSignInCodes(ctx context.Context, now time.Time) ([]core.SignInCode, error)
	GetSettings(ctx context.Context) (core.Settings, error)
}

// NotificationSink delivers system notifications for MailNotifier.
type NotificationSink interface {
	NotificationsSupported() bool
	SignInCodeBannerSupported() bool
	Notify(n Notification) error
}

// MailNotifierDeps are MailNotifier's collaborators.
type MailNotifierDeps struct {
	Svc        MailNotifierService
	Sink       NotificationSink
	Clock      Clock
	Foreground func() bool
	Language   func() string
	Post       func(func())
	Logf       func(format string, args ...any)
}

// MailNotifier batches newly received mail per account and decides what to
// move to Junk or show as a system notification, without needing a window.
type MailNotifier struct {
	svc        MailNotifierService
	sink       NotificationSink
	clock      Clock
	foreground func() bool
	lang       func() string
	post       func(func())
	logf       func(format string, args ...any)

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	newMail map[int64]*arrivals
	timers  map[int64]func() bool
}

// NewMailNotifier constructs a MailNotifier.
func NewMailNotifier(d MailNotifierDeps) *MailNotifier {
	ctx, cancel := context.WithCancel(context.Background())
	n := &MailNotifier{
		svc:        d.Svc,
		sink:       d.Sink,
		clock:      d.Clock,
		foreground: d.Foreground,
		lang:       d.Language,
		post:       d.Post,
		logf:       d.Logf,
		ctx:        ctx,
		cancel:     cancel,
	}
	if n.clock == nil {
		n.clock = realClock{}
	}
	if n.lang == nil {
		n.lang = i18n.Language
	}
	if n.post == nil {
		n.post = func(f func()) { f() }
	}
	if n.logf == nil {
		n.logf = log.Printf
	}
	return n
}

// HandleEvent gathers new messages from MessageReceived events for notifications,
// shown NotifyWait after the first arrival for that account.
func (n *MailNotifier) HandleEvent(ev events.Event) {
	if ev.Type != events.MessageReceived || len(ev.MessageIDs) == 0 {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ctx.Err() != nil {
		return
	}
	if n.newMail == nil {
		n.newMail = map[int64]*arrivals{}
	}
	if n.timers == nil {
		n.timers = map[int64]func() bool{}
	}
	acc := ev.AccountID
	got := n.newMail[acc]
	if got == nil {
		got = &arrivals{}
		n.newMail[acc] = got
		n.timers[acc] = n.clock.AfterFunc(NotifyWait, func() {
			n.post(func() { n.flush(acc, got) })
		})
	}
	got.msgs = append(got.msgs, ev.MessageIDs...)
	got.threads = append(got.threads, ev.ThreadIDs...)
}

// Close cancels pending batches and in-flight notification work.
func (n *MailNotifier) Close() {
	n.cancel()
	n.mu.Lock()
	for acc, stop := range n.timers {
		stop()
		delete(n.timers, acc)
	}
	clear(n.newMail)
	n.mu.Unlock()
}

func (n *MailNotifier) flush(acc int64, got *arrivals) {
	n.mu.Lock()
	if n.ctx.Err() != nil || n.newMail[acc] != got {
		n.mu.Unlock()
		return
	}
	delete(n.newMail, acc)
	delete(n.timers, acc)
	n.mu.Unlock()
	ctx := n.ctx
	fg := n.foreground != nil && n.foreground()
	banner := n.sink != nil && n.sink.SignInCodeBannerSupported()
	now := n.clock.Now()
	go n.notifyBatch(ctx, acc, got, fg, banner, now)
}

func (n *MailNotifier) notifyBatch(ctx context.Context, acc int64, got *arrivals, fg, banner bool, now time.Time) {
	msgs, err := inboxArrivals(ctx, n.svc, acc, got)
	if err != nil {
		if ctx.Err() == nil {
			n.logf("notify: account %d: reading new mail: %v", acc, err)
		}
		return
	}
	blockedSenders, err := n.svc.BlockedSenders(ctx) // the stored list: a block takes effect at once
	if err != nil && ctx.Err() == nil {
		n.logf("notify: account %d: reading blocked senders: %v", acc, err)
	}
	var junk []int64
	msgs = slices.DeleteFunc(msgs, func(m core.MessageSummary) bool {
		blocked := slices.Contains(blockedSenders, mail.AddrKey(m.From))
		if blocked {
			junk = append(junk, m.ID)
		}
		return blocked
	})
	if len(junk) > 0 {
		// Never deleted instead: a failed move leaves the mail in the inbox, unnotified.
		if err := n.svc.MoveToRole(ctx, junk, "junk"); err != nil && ctx.Err() == nil {
			n.logf("notify: account %d: moving blocked senders' mail to junk: %v", acc, err)
		}
	}
	if len(msgs) == 0 || fg || n.sink == nil {
		return
	}
	st, err := n.svc.GetSettings(ctx)
	if err != nil {
		if ctx.Err() == nil {
			n.logf("notify: account %d: reading settings: %v", acc, err)
		}
		return
	}
	if !st.NotifyNewMail || !n.sink.NotificationsSupported() {
		return
	}
	if st.NotifyPreview && banner {
		// Only the newest code takes the banner; older ones in the same batch
		// keep their ordinary notification.
		if codes, err := n.svc.RecentSignInCodes(ctx, now); err == nil && len(codes) > 0 {
			msgs = slices.DeleteFunc(msgs, func(m core.MessageSummary) bool { return m.ID == codes[0].MessageID })
		}
	}
	texts := i18n.In(i18n.Match(cmp.Or(st.UILanguage, n.lang())))
	for _, m := range msgs {
		note := Notification{ID: "message:" + strconv.FormatInt(m.ID, 10), MessageID: m.ID, Title: "Godwit", Body: texts.NotifyNewMail()}
		if st.NotifyPreview {
			note.Title, note.Subtitle, note.Body = displayName(m.From), m.Subject, m.Snippet
			note.Sender, note.ThreadID = m.From, m.ThreadID
		}
		if err := n.sink.Notify(note); err != nil && ctx.Err() == nil {
			n.logf("notify: account %d: %v", acc, err)
		}
	}
}

// inboxArrivals are the arrived messages that are in an inbox-role folder.
func inboxArrivals(ctx context.Context, svc MailNotifierService, acc int64, got *arrivals) ([]core.MessageSummary, error) {
	fs, err := svc.ListFolders(ctx, acc)
	if err != nil {
		return nil, err
	}
	inbox := map[int64]bool{}
	for _, f := range fs {
		if f.Role == "inbox" {
			inbox[f.ID] = true
		}
	}
	want := map[int64]bool{}
	for _, id := range got.msgs {
		want[id] = true
	}
	var out []core.MessageSummary
	for _, tid := range slices.Compact(slices.Sorted(slices.Values(got.threads))) {
		th, err := svc.GetThread(ctx, tid)
		if err != nil {
			return nil, err
		}
		for _, m := range th.Messages {
			if want[m.ID] && inbox[m.FolderID] {
				delete(want, m.ID) // a message in two gathered threads counts once
				out = append(out, m)
			}
		}
	}
	return out, nil
}
