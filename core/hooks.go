package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
)

// message.received hooks are at least once: sync logs each new arrival in
// events_log (in the import transaction) and one dispatcher per running
// plugin with a handler, or per built-in consumer (startConsumer), delivers
// the log after its cursor, in order, moving the cursor only after the
// handler succeeded. A crash between the two delivers again (to a plugin with
// the same idempotency key), with no upper bound.

const (
	hookBatch     = 50
	maxHookWait   = 5 * time.Minute
	hookPoll      = 30 * time.Second // dispatchers re-query and the janitor cleans this often
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
	typ, subject, from        string
	detail                    string // label.applied: the slug; security.verdict: the status
	date                      int64
	deleted                   bool
}

// eventCursor is a consumer's cursor row: a plugin's in plugin_cursors (gone
// with the plugin), a built-in consumer's in event_cursors, so no plugin name
// collides with a built-in one. types are the event types it reads.
type eventCursor struct {
	table, key, name string
	types            []string
}

func pluginCursor(name string) eventCursor {
	return eventCursor{"plugin_cursors", "plugin", name, []string{"message.received"}}
}
func builtinCursor(name string, types []string) eventCursor {
	return eventCursor{"event_cursors", "consumer", name, types}
}

// errStopConsumer from a deliver ends its consumer without moving the cursor.
var errStopConsumer = errors.New("consumer stopped")

// subscribeCursor sets the cursor to the newest event the first time the consumer
// runs (for a plugin, with a message.received handler; one statement: read
// and write together), so it gets what arrives from then on; an existing
// cursor stays, and events that arrived while it was off are delivered.
func (s *Service) subscribeCursor(ctx context.Context, c eventCursor) error {
	s.hooksMu.Lock()
	defer s.hooksMu.Unlock()
	_, err := s.db.Execute(ctx, `INSERT INTO `+c.table+`(`+c.key+`, seq) SELECT ?, COALESCE(MAX(seq), 0) FROM events_log
		WHERE true ON CONFLICT(`+c.key+`) DO NOTHING`, c.name)
	return err
}

// addWake registers a dispatcher's wake-up (before its first read: no
// arrival is missed); the func removes it.
func (s *Service) addWake() (chan struct{}, func()) {
	wake := make(chan struct{}, 1)
	s.hooksMu.Lock()
	defer s.hooksMu.Unlock()
	if s.hookWakes == nil {
		s.hookWakes = map[chan struct{}]struct{}{}
	}
	s.hookWakes[wake] = struct{}{}
	return wake, func() {
		s.hooksMu.Lock()
		defer s.hooksMu.Unlock()
		delete(s.hookWakes, wake)
	}
}

// startConsumer runs a built-in consumer of the event types until Close:
// deliver gets each event after the consumer's cursor, in order, at least
// once, and an error is tried again after hookBackoff.
func (s *Service) startConsumer(ctx context.Context, name string, types []string, deliver func(context.Context, hookEvent) error) error {
	c := builtinCursor(name, types)
	if err := s.subscribeCursor(ctx, c); err != nil {
		return err
	}
	cctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return nil // closing: nothing to deliver to
	}
	wake, unwake := s.addWake()
	go func() {
		defer end()
		defer unwake()
		s.consume(cctx, c, wake, nil, deliver)
	}()
	return nil
}

// startHooks starts rp's dispatcher if it handles message.received. The
// caller holds pluginsMu; the dispatcher is an explicit call (s.begin), so
// Close cancels and waits for it, and stopPlugin cancels it without waiting.
func (s *Service) startHooks(ctx context.Context, rp *runningPlugin, r pluginRow) error {
	if !rp.p.Handles(plugins.EventReceived) {
		return nil
	}
	if err := s.subscribeCursor(ctx, pluginCursor(rp.p.Name)); err != nil {
		return err
	}
	hctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return nil // closing: nothing to deliver to
	}
	hctx, rp.stopHooks = context.WithCancel(hctx)
	wake, unwake := s.addWake()
	egress, _ := plugins.EgressFor(plugins.KindHook, r.m, r.granted == r.permHash, true, false)
	inv := plugins.Invocation{Kind: plugins.KindHook, Write: slices.Contains(r.m.Permissions.Mail, "write"), EgressHosts: egress}
	go func() {
		defer end()
		defer unwake()
		s.dispatchHooks(hctx, rp, inv, wake)
		if pluginHook != nil {
			pluginHook("hook.stopped", rp.p.Name)
		}
	}()
	return nil
}

// dispatchHooks delivers the plugin's events until ctx ends. Each failure is
// counted (pluginFailed, the third in a row disables the plugin and ends ctx)
// and waits hookBackoff before the same event is tried again.
func (s *Service) dispatchHooks(ctx context.Context, rp *runningPlugin, base plugins.Invocation, wake <-chan struct{}) {
	name := rp.p.Name
	holdsMail := func(ctx context.Context) error { return s.markHoldsMail(ctx, name) } // events carry mail content
	s.consume(ctx, pluginCursor(name), wake, holdsMail, func(ctx context.Context, ev hookEvent) error {
		inv := base
		inv.ID, inv.Config = name+":"+strconv.FormatInt(ev.seq, 10), rp.host.snapshot()
		err := rp.p.Emit(ctx, inv, plugins.EventReceived, map[string]any{
			"id": ev.messageID, "accountId": ev.accountID, "subject": ev.subject, "from": ev.from,
			"date": time.Unix(ev.date, 0).UTC().Format(time.RFC3339), "deleted": ev.deleted,
		}, map[string]any{"idempotencyKey": inv.ID})
		if ctx.Err() != nil || errors.Is(err, plugins.ErrClosed) {
			return errStopConsumer // stopped: the event is delivered again by the next instance
		}
		if err != nil {
			s.pluginFailed(ctx, name, rp.gen)
			return err
		}
		s.pluginSucceeded(ctx, name, rp.gen)
		if pluginHook != nil && pluginHook("hook.advance", name) != nil {
			return errStopConsumer // tests: a crash before the cursor moves
		}
		return nil
	})
}

// consume delivers c's events until ctx ends or deliver returns
// errStopConsumer. before (may be nil) runs ahead of each non-empty batch;
// a failure of it, of deliver or of the cursor move waits hookBackoff before
// the same step is tried again.
func (s *Service) consume(ctx context.Context, c eventCursor, wake <-chan struct{},
	before func(context.Context) error, deliver func(context.Context, hookEvent) error) {
	failures := 0
	fail := func() bool {
		failures++
		return s.hookWait(ctx, hookBackoff(failures), nil)
	}
	for {
		evs, err := s.hookEvents(ctx, c)
		if err != nil {
			if ctx.Err() != nil || !fail() {
				return
			}
			continue
		}
		if len(evs) > 0 && before != nil {
			if err := before(ctx); err != nil {
				if ctx.Err() != nil || !fail() {
					return
				}
				continue
			}
		}
		for _, ev := range evs {
			for {
				err := deliver(ctx, ev)
				if ctx.Err() != nil || errors.Is(err, errStopConsumer) {
					return
				}
				if err == nil {
					failures = 0
					if err = s.advanceCursor(ctx, c, ev.seq); err == nil {
						break
					}
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
		if !s.hookWait(ctx, hookPoll, wake) {
			return
		}
	}
}

// hookWait waits d on the hook clock, or less if wake (may be nil) fires;
// false if ctx ended first.
func (s *Service) hookWait(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
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
	case <-wake:
		stop()
		return true
	case <-ctx.Done():
		stop()
		return false
	}
}

// hookEvents is the next batch after c, with each event's snapshot and
// whether its message has been deleted since.
func (s *Service) hookEvents(ctx context.Context, c eventCursor) ([]hookEvent, error) {
	var args []any
	for _, t := range c.types {
		args = append(args, t)
	}
	args = append(args, c.name, hookBatch)
	rows, err := s.db.Query(ctx, `SELECT e.seq, COALESCE(e.account_id, 0), COALESCE(e.message_id, 0), COALESCE(e.subject, ''),
			COALESCE(e.from_addr, ''), COALESCE(e.msg_date, 0), m.id IS NULL, e.type, COALESCE(e.detail, '')
		FROM events_log e LEFT JOIN messages m ON m.id = e.message_id
		WHERE e.type IN (`+placeholders(len(c.types))+`) AND e.seq > (SELECT seq FROM `+c.table+` WHERE `+c.key+` = ?)
		ORDER BY e.seq LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	out := make([]hookEvent, 0, len(rows.Values))
	for _, v := range rows.Values {
		out = append(out, hookEvent{seq: store.Int(v[0]), accountID: store.Int(v[1]), messageID: store.Int(v[2]),
			subject: store.Str(v[3]), from: store.Str(v[4]), date: store.Int(v[5]), deleted: store.Int(v[6]) != 0,
			typ: store.Str(v[7]), detail: store.Str(v[8])})
	}
	return out, nil
}

// advanceCursor moves the cursor forward only: an instance being replaced
// never moves it back.
func (s *Service) advanceCursor(ctx context.Context, c eventCursor, seq int64) error {
	s.hooksMu.Lock()
	defer s.hooksMu.Unlock()
	_, err := s.db.Execute(ctx, "UPDATE "+c.table+" SET seq = ? WHERE "+c.key+" = ? AND seq < ?", seq, c.name, seq)
	return err
}

// cleanEvents deletes events older than eventsKeepFor that every existing
// cursor, plugin or built-in, has passed (with no cursor, by age alone).
func (s *Service) cleanEvents(ctx context.Context) error {
	s.hooksMu.Lock()
	defer s.hooksMu.Unlock()
	_, err := s.db.Execute(ctx, `DELETE FROM events_log WHERE created_at < ?
		AND seq < COALESCE((SELECT MIN(seq) FROM (SELECT seq FROM plugin_cursors UNION ALL SELECT seq FROM event_cursors)),
			9223372036854775807)`,
		time.Now().Add(-eventsKeepFor).Unix())
	return err
}

// testHookClock, if set, is the hook clock of Services created afterwards (tests).
var testHookClock plugins.Clock

// notifyHooks wakes every dispatcher. Each has a 1-slot channel, so a signal
// sent while it is busy is kept; the periodic re-query covers anything else.
func (s *Service) notifyHooks() {
	s.hooksMu.Lock()
	defer s.hooksMu.Unlock()
	for ch := range s.hookWakes {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// startJanitor cleans events_log and ai_cache every hookPoll until Close, hook plugins or not.
func (s *Service) startJanitor() {
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	go func() {
		defer end()
		for s.hookWait(ctx, hookPoll, nil) {
			// best effort; the next tick tries again
			s.cleanEvents(ctx)
			s.cleanAICache(ctx)
		}
	}()
}

// message.beforeSend: Send reads the draft and the checking plugins under
// editMu, runs the checks outside it (a check cannot write mail, so it never
// waits for the lock), and queues the draft only if, under editMu again, its
// revision and the plugin list are still the checked ones.

const (
	maxSendText     = 500 // runes of a warning or block message
	sendCheckTTL    = 10 * time.Minute
	sendCheckBudget = 5 * time.Second // each checker's call, waiting for its VM included
)

// SendWarning is a plugin's warning about a draft; the user may still send.
// Code is WarningPlugin (Message is the plugin's) or WarningNoResponse (the
// check failed or timed out; Message is a fallback in English).
type SendWarning struct{ Plugin, Code, Message string }

const (
	WarningPlugin     = "plugin_warning"
	WarningNoResponse = "no_response"
)

// SendCheck is a draft revision's beforeSend outcome with warnings. Plugins
// are the checking plugins as name@source_hash@config_version, sorted by name. Digest binds
// all of it: SendAcknowledged(DraftID, Digest) queues that revision without
// checking again while the draft and the plugins are unchanged.
type SendCheck struct {
	DraftID, Revision int64
	Plugins           []string
	Warnings          []SendWarning
	Digest            string
}

// SendBlockedError: a plugin refused the draft. It cannot be acknowledged;
// the user edits the draft or disables the plugin.
type SendBlockedError struct{ Plugin, Message string }

func (e *SendBlockedError) Error() string {
	return "plugin " + e.Plugin + " blocked sending: " + e.Message
}

// SendWarningsError: nothing was queued; show the warnings and call
// SendAcknowledged with Check.Digest to send anyway.
type SendWarningsError struct{ Check SendCheck }

func (e *SendWarningsError) Error() string {
	return fmt.Sprintf("%d plugin warning(s) to acknowledge before sending", len(e.Check.Warnings))
}

// testPluginClock, if set, is the plugin VM clock of Services created afterwards (tests).
var testPluginClock plugins.Clock

// duringSendCheck, if set, runs before each beforeSend call, outside editMu (tests).
var duringSendCheck func()

type sendChecker struct {
	id, name string // id is name@source_hash@config_version
	gen      uint64
	p        *plugins.Plugin
	cfg      *plugins.ConfigSnapshot // the config it checks with
}

// sendCheckers are the running plugins with a beforeSend handler, by name.
func (s *Service) sendCheckers() []sendChecker {
	s.pluginsMu.RLock()
	defer s.pluginsMu.RUnlock()
	return s.sendCheckersLocked()
}

// sendCheckersLocked is sendCheckers for a caller holding pluginsMu.
func (s *Service) sendCheckersLocked() []sendChecker {
	var out []sendChecker
	for name, rp := range s.running {
		if rp.p != nil && rp.p.Handles(plugins.EventBeforeSend) { // a native plugin has no hooks
			cfg := rp.host.snapshot()
			id := name + "@" + rp.hash + "@" + strconv.FormatInt(cfg.Version, 10)
			out = append(out, sendChecker{id: id, name: name, gen: rp.gen, p: rp.p, cfg: cfg})
		}
	}
	slices.SortFunc(out, func(a, b sendChecker) int { return strings.Compare(a.name, b.name) })
	return out
}

func checkerIDs(cs []sendChecker) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.id
	}
	return out
}

// runSendChecks calls each checker in order, each within sendCheckBudget
// (its VM busy with other work counts); the caller holds no lock. A block
// ends the checks with *SendBlockedError; an error or timeout is a warning
// and a failure of that plugin.
func (s *Service) runSendChecks(ctx context.Context, d Draft, cs []sendChecker) ([]SendWarning, error) {
	html := d.HTML
	if len(html) > plugins.MaxBodyBytes {
		html = strings.ToValidUTF8(html[:plugins.MaxBodyBytes], "")
	}
	egress, _ := plugins.EgressFor(plugins.KindBeforeSend, plugins.Manifest{}, false, true, false) // none, whatever the plugin holds
	var warns []SendWarning
	for _, c := range cs {
		if duringSendCheck != nil {
			duringSendCheck()
		}
		inv := plugins.Invocation{ID: fmt.Sprintf("beforeSend:%d:%d", d.ID, d.Revision), Kind: plugins.KindBeforeSend,
			EgressHosts: egress, Config: c.cfg}
		// A fresh argument each: what one plugin does to it, the next never sees.
		arg := map[string]any{"id": d.ID, "accountId": d.AccountID, "to": slices.Clone(d.To), "cc": slices.Clone(d.Cc),
			"subject": d.Subject, "html": html}
		if err := s.markHoldsMail(ctx, c.name); err != nil { // the host's failure: not the plugin's
			warns = append(warns, SendWarning{Plugin: c.name, Code: WarningNoResponse, Message: "plugin " + c.name + " did not respond"})
			continue
		}
		cctx, end := s.budget(ctx, sendCheckBudget)
		res, err := c.p.EmitResult(cctx, inv, plugins.EventBeforeSend, arg)
		end()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var kind, text string
		if err == nil {
			kind, text, err = sendResult(res)
		}
		if err != nil {
			s.pluginFailed(ctx, c.name, c.gen)
			warns = append(warns, SendWarning{Plugin: c.name, Code: WarningNoResponse, Message: "plugin " + c.name + " did not respond"})
			continue
		}
		s.pluginSucceeded(ctx, c.name, c.gen)
		switch text = s.sendText(text); kind {
		case "block":
			if text == "" {
				text = "plugin " + c.name + " blocked sending this message"
			}
			return nil, &SendBlockedError{Plugin: c.name, Message: text}
		case "warn":
			if text == "" {
				text = "plugin " + c.name + " warned about this message"
			}
			warns = append(warns, SendWarning{Plugin: c.name, Code: WarningPlugin, Message: text})
		}
	}
	return warns, nil
}

// sendResult reads a handler's result: nothing, or an object whose block
// (else warn) field, when present, is the kind; a non-string value there
// gives empty text (the default message).
func sendResult(res any) (kind, text string, err error) {
	if res == nil {
		return "", "", nil
	}
	m, ok := res.(map[string]any)
	if !ok {
		return "", "", errors.New("beforeSend must return nothing, {warn: string} or {block: string}")
	}
	for _, k := range []string{"block", "warn"} {
		if v, has := m[k]; has {
			text, _ = v.(string)
			return k, text, nil
		}
	}
	return "", "", nil
}

// sendText is a plugin's message as the user sees it: secrets redacted,
// each run of control characters one space, at most maxSendText runes.
func (s *Service) sendText(t string) string {
	t = s.pluginText(t)
	if r := []rune(t); len(r) > maxSendText {
		t = string(r[:maxSendText])
	}
	return t
}

// pluginText is plugin text as the user sees it: secrets redacted, each run
// of control characters one space.
func (s *Service) pluginText(t string) string { return s.pluginTextKeep(t, "") }

// pluginTextKeep is pluginText that keeps the control characters in keep.
func (s *Service) pluginTextKeep(t, keep string) string {
	var b strings.Builder
	ctl := false
	for _, r := range s.secrets.Redact(strings.ToValidUTF8(t, "")) {
		if unicode.IsControl(r) && !strings.ContainsRune(keep, r) {
			if !ctl {
				b.WriteByte(' ')
			}
			ctl = true
			continue
		}
		ctl = false
		b.WriteRune(r)
	}
	return b.String()
}

// clone copies the slices, so a caller's changes never reach a stored check.
func (c SendCheck) clone() SendCheck {
	c.Plugins, c.Warnings = slices.Clone(c.Plugins), slices.Clone(c.Warnings)
	return c
}

func (s *Service) now() time.Time {
	if s.sendNow != nil {
		return s.sendNow()
	}
	return time.Now()
}

func sendDigest(c SendCheck) string {
	b, _ := json.Marshal([]any{c.DraftID, c.Revision, c.Plugins, c.Warnings})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type keptCheck struct {
	c       SendCheck
	expires time.Time
}

// keepSendCheck remembers c for sendCheckTTL, dropping expired ones.
func (s *Service) keepSendCheck(c SendCheck) {
	s.checksMu.Lock()
	defer s.checksMu.Unlock()
	now := s.now()
	for k, e := range s.sendChecks {
		if !now.Before(e.expires) {
			delete(s.sendChecks, k)
		}
	}
	if s.sendChecks == nil {
		s.sendChecks = map[string]keptCheck{}
	}
	s.sendChecks[c.Digest] = keptCheck{c.clone(), now.Add(sendCheckTTL)}
}

// sendCheck is the stored check with digest, if it has not expired.
func (s *Service) sendCheck(digest string) (SendCheck, bool) {
	s.checksMu.Lock()
	defer s.checksMu.Unlock()
	e, ok := s.sendChecks[digest]
	return e.c.clone(), ok && s.now().Before(e.expires)
}

func (s *Service) dropSendCheck(digest string) {
	s.checksMu.Lock()
	defer s.checksMu.Unlock()
	delete(s.sendChecks, digest)
}

// SendAcknowledged sends the draft despite the warnings of the check with
// digest: if the draft's revision and the checking plugins are still the
// checked ones it is queued without checking again; otherwise (or with no
// such check, e.g. expired) it is a plain Send, which may warn again. A block
// has no check, so it can never be acknowledged.
func (s *Service) SendAcknowledged(ctx context.Context, draftID int64, digest string) error {
	return s.sendAcknowledged(ctx, draftID, -1, digest)
}

// sendAcknowledged is SendAcknowledged; a want >= 0 sends only that
// revision, as send.
func (s *Service) sendAcknowledged(ctx context.Context, draftID, want int64, digest string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	if c, ok := s.sendCheck(digest); ok && c.DraftID == draftID && (want < 0 || c.Revision == want) {
		err := s.enqueue(ctx, draftID, c.Revision, c.Plugins, nil)
		if !errors.Is(err, errSendChanged) {
			if err == nil {
				s.dropSendCheck(digest)
			}
			return err
		}
	}
	return s.send(ctx, draftID, want, nil)
}
