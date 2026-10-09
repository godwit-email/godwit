package core

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
)

// message.received hooks are at least once: sync logs each new arrival in
// events_log (in the import transaction) and one dispatcher per running
// plugin with a handler delivers the log after its plugin_cursors.seq, in
// order, moving the cursor only after the handler succeeded. A crash between
// the two delivers again with the same idempotency key, with no upper bound.

const (
	hookBatch     = 50
	maxHookWait   = 5 * time.Minute
	eventsKeepFor = 7 * 24 * time.Hour
)

// hookBackoff is the wait after the nth failure in a row: 2 s, 4 s, 8 s, ... up to 5 min.
func hookBackoff(n int) time.Duration {
	if n > 20 {
		return maxHookWait
	}
	return min(time.Second<<n, maxHookWait)
}

type hookEvent struct {
	seq, accountID, messageID int64
	subject, from             string
	date                      int64
	deleted                   bool
}

// subscribeHooks sets the plugin's cursor to the newest event the first time
// it runs with a message.received handler (one statement: read and write
// together), so it gets what arrives from then on; an existing cursor stays,
// and events that arrived while it was disabled are delivered.
func (s *Service) subscribeHooks(ctx context.Context, name string) error {
	s.hooksMu.Lock()
	defer s.hooksMu.Unlock()
	_, err := s.db.Execute(ctx, `INSERT INTO plugin_cursors(plugin, seq) SELECT ?, COALESCE(MAX(seq), 0) FROM events_log
		WHERE true ON CONFLICT(plugin) DO NOTHING`, name)
	return err
}

// startHooks starts rp's dispatcher if it handles message.received. The
// caller holds pluginsMu; the dispatcher is an explicit call (s.begin), so
// Close cancels and waits for it, and stopPlugin cancels it without waiting.
func (s *Service) startHooks(ctx context.Context, rp *runningPlugin, r pluginRow) error {
	if !rp.p.Handles(plugins.EventReceived) {
		return nil
	}
	if err := s.subscribeHooks(ctx, rp.p.Name); err != nil {
		return err
	}
	hctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return nil // closing: nothing to deliver to
	}
	hctx, rp.stopHooks = context.WithCancel(hctx)
	egress, _ := plugins.EgressFor(plugins.KindHook, r.m, r.granted == r.permHash, false)
	inv := plugins.Invocation{Kind: plugins.KindHook, Write: slices.Contains(r.m.Permissions.Mail, "write"), EgressHosts: egress}
	go func() {
		defer end()
		s.dispatchHooks(hctx, rp, inv)
		if pluginHook != nil {
			pluginHook("hook.stopped", rp.p.Name)
		}
	}()
	return nil
}

// dispatchHooks delivers the plugin's events until ctx ends. Each failure is
// counted (pluginFailed, the third in a row disables the plugin and ends ctx)
// and waits hookBackoff before the same event is tried again.
func (s *Service) dispatchHooks(ctx context.Context, rp *runningPlugin, base plugins.Invocation) {
	name := rp.p.Name
	wake, unsub := s.bus.Subscribe() // before the first read: no arrival is missed
	defer unsub()
	failures := 0
	fail := func() bool {
		failures++
		return s.hookWait(ctx, hookBackoff(failures))
	}
	for {
		evs, err := s.hookEvents(ctx, name)
		if err != nil {
			if ctx.Err() != nil || !fail() {
				return
			}
			continue
		}
		for _, ev := range evs {
			for {
				inv := base
				inv.ID = name + ":" + strconv.FormatInt(ev.seq, 10)
				err := rp.p.Emit(ctx, inv, plugins.EventReceived, map[string]any{
					"id": ev.messageID, "accountId": ev.accountID, "subject": ev.subject, "from": ev.from,
					"date": time.Unix(ev.date, 0).UTC().Format(time.RFC3339), "deleted": ev.deleted,
				}, map[string]any{"idempotencyKey": inv.ID})
				if ctx.Err() != nil || errors.Is(err, plugins.ErrClosed) {
					return // stopped: the event is delivered again by the next instance
				}
				if err == nil {
					failures = 0
					s.pluginSucceeded(ctx, name, rp.gen)
					if pluginHook != nil && pluginHook("hook.advance", name) != nil {
						return // tests: a crash before the cursor moves
					}
					if err = s.advanceCursor(ctx, name, ev.seq); err == nil {
						break
					}
				} else {
					s.pluginFailed(ctx, name, rp.gen)
				}
				if !fail() {
					return
				}
			}
		}
		if len(evs) > 0 {
			s.cleanEvents(ctx) // best effort; the next batch tries again
		}
		if len(evs) == hookBatch {
			continue
		}
		for waiting := true; waiting; {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-wake:
				if !ok {
					return
				}
				waiting = e.Type != events.MessageReceived
			}
		}
	}
}

// hookWait waits d on the hook clock; false if ctx ended first.
func (s *Service) hookWait(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	clock := s.hookClock
	if clock == nil {
		clock = realClock{}
	}
	done := make(chan struct{})
	stop := clock.AfterFunc(d, func() { close(done) })
	select {
	case <-done:
		return true
	case <-ctx.Done():
		stop()
		return false
	}
}

type realClock struct{}

func (realClock) AfterFunc(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

// hookEvents is the next batch after the plugin's cursor, with each event's
// snapshot and whether its message has been deleted since.
func (s *Service) hookEvents(ctx context.Context, name string) ([]hookEvent, error) {
	rows, err := s.db.Query(ctx, `SELECT e.seq, COALESCE(e.account_id, 0), COALESCE(e.message_id, 0), COALESCE(e.subject, ''),
			COALESCE(e.from_addr, ''), COALESCE(e.msg_date, 0), m.id IS NULL
		FROM events_log e LEFT JOIN messages m ON m.id = e.message_id
		WHERE e.type = 'message.received' AND e.seq > (SELECT seq FROM plugin_cursors WHERE plugin = ?)
		ORDER BY e.seq LIMIT ?`, name, hookBatch)
	if err != nil {
		return nil, err
	}
	out := make([]hookEvent, 0, len(rows.Values))
	for _, v := range rows.Values {
		out = append(out, hookEvent{seq: store.Int(v[0]), accountID: store.Int(v[1]), messageID: store.Int(v[2]),
			subject: store.Str(v[3]), from: store.Str(v[4]), date: store.Int(v[5]), deleted: store.Int(v[6]) != 0})
	}
	return out, nil
}

// advanceCursor moves the cursor forward only: an instance being replaced
// never moves it back.
func (s *Service) advanceCursor(ctx context.Context, name string, seq int64) error {
	s.hooksMu.Lock()
	defer s.hooksMu.Unlock()
	_, err := s.db.Execute(ctx, "UPDATE plugin_cursors SET seq = ? WHERE plugin = ? AND seq < ?", seq, name, seq)
	return err
}

// cleanEvents deletes events older than eventsKeepFor that every existing
// cursor has passed (with no cursor, by age alone).
func (s *Service) cleanEvents(ctx context.Context) error {
	s.hooksMu.Lock()
	defer s.hooksMu.Unlock()
	_, err := s.db.Execute(ctx, `DELETE FROM events_log WHERE created_at < ?
		AND seq < COALESCE((SELECT MIN(seq) FROM plugin_cursors), 9223372036854775807)`,
		time.Now().Add(-eventsKeepFor).Unix())
	return err
}
