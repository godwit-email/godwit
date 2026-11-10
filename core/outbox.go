package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/backend/ews"
	"github.com/godwit-email/godwit/backend/graph"
	"github.com/godwit-email/godwit/backend/smtpbackend"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
)

// ErrInvalidState is returned by ResolveOutbox and ResolveApproval for an item
// that is not waiting for a decision, and by CancelOutbox for one no longer queued.
var ErrInvalidState = errors.New("item is not waiting for a decision")

// ErrNoOutboxDraft is CancelOutbox's and ResolveOutbox edit's error for an
// item queued before undo send: it has no draft to bring back.
var ErrNoOutboxDraft = errors.New("the outbox item has no draft to bring back")

// UndoSendWindow is how long a sent message waits in the outbox before it is
// sent, so that CancelOutbox can take it back.
const UndoSendWindow = 10 * time.Second

// beforeOutboxItem, when set, runs after processOutbox read the items and before
// it handles each one. Tests use it to change an item under a running sync.
var beforeOutboxItem func(id int64)

// OutboxItem is a message that has not finished sending and archiving. State is
// queued, sending, sent, archiving, send_uncertain, archive_uncertain or failed.
type OutboxItem struct {
	ID               int64
	Subject          string
	State, LastError string
	CreatedAt        time.Time
	// DraftID is the draft it was sent from, read-only until CancelOutbox or
	// ResolveOutbox edit brings it back; 0 once the message may have gone
	// out, and for items queued before undo send.
	DraftID int64
	// SendAfter is when it is sent at the earliest (the end of the undo
	// window), NextAttempt when it is retried; zero means now.
	SendAfter, NextAttempt time.Time
	Attempts               int
	// Failure is why a failed item failed, for the host to explain in the
	// user's language; nil otherwise, and for a failure without one (its raw
	// copy could not be read; LastError says why).
	Failure *OutboxFailure
}

// OutboxFailure is why an outbox item failed. It is stored as JSON in these
// field names.
type OutboxFailure struct {
	// Reason is "rejected": the server refused the message for good (a 5xx
	// reply), described by the other fields; or "expired": it waited in the
	// queue longer than OutboxExpiry and was not sent.
	Reason string `json:"reason"`
	// Stage is the refused SMTP command, "MAIL", "RCPT" or "DATA" (rejected).
	Stage string `json:"stage,omitempty"`
	// Recipient is the refused address at RCPT (rejected).
	Recipient string `json:"recipient,omitempty"`
	// Code is the SMTP reply code, such as 550 (rejected).
	Code int `json:"code,omitempty"`
	// Enhanced is the enhanced status code, such as "5.1.1"; "" if the server
	// sent none (rejected).
	Enhanced string `json:"enhanced,omitempty"`
	// Message is the server's reply text, untranslated (rejected).
	Message string `json:"message,omitempty"`
}

// OutboxExpiry is how long an item may wait in the queue, counted from when it
// is sent at the earliest (SendAfter; resend restarts it). Past it, the item
// fails as expired instead of being sent.
const OutboxExpiry = 3 * 24 * time.Hour

// Retries of a message the server refused for now or could not take: the wait
// is retryMin after the first try, doubled per try, at most retryMax.
const (
	retryMin = time.Minute
	retryMax = time.Hour
)

// ListOutbox returns the account's unfinished outbox items, oldest first.
func (s *Service) ListOutbox(ctx context.Context, accountID int64) ([]OutboxItem, error) {
	rows, err := s.db.Query(ctx, `SELECT id, subject, state, last_error, created_at, draft_id, send_after, next_attempt_at, attempts, failure
		FROM outbox WHERE account_id = ? AND state != 'done' ORDER BY id`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]OutboxItem, 0, len(rows.Values))
	for _, r := range rows.Values {
		it := OutboxItem{ID: store.Int(r[0]), Subject: store.Str(r[1]), State: store.Str(r[2]),
			LastError: store.Str(r[3]), CreatedAt: time.Unix(store.Int(r[4]), 0), DraftID: store.Int(r[5]),
			SendAfter: unixMilli(r[6]), NextAttempt: unixMilli(r[7]), Attempts: int(store.Int(r[8]))}
		if f := store.Str(r[9]); f != "" {
			it.Failure = new(OutboxFailure)
			if err := json.Unmarshal([]byte(f), it.Failure); err != nil {
				return nil, err
			}
		}
		out = append(out, it)
	}
	return out, nil
}

// unixMilli is a stored time in unix milliseconds; NULL is the zero time.
func unixMilli(v any) time.Time {
	if v == nil {
		return time.Time{}
	}
	return time.UnixMilli(store.Int(v))
}

// CancelOutbox takes a queued message back before it is sent: the item and its
// raw copy go, and its draft is listed and writable again. An item no longer
// queued (sending started, or later) is ErrInvalidState, one without a draft
// ErrNoOutboxDraft. It does not wait for a running sync: the sync claims the
// item for sending, or CancelOutbox deletes it, never both.
func (s *Service) CancelOutbox(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	return s.takeBack(ctx, id, "queued")
}

// takeBack deletes the item if it is in state with a draft, which it leaves
// to the user again.
func (s *Service) takeBack(ctx context.Context, id int64, state string) error {
	rows, err := s.db.Query(ctx, "SELECT account_id, raw_path, state, draft_id FROM outbox WHERE id = ?", id)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("outbox item %d not found", id)
	}
	r := rows.Values[0]
	if err := s.bind(ctx, store.Int(r[0])); err != nil {
		return err
	}
	rs, err := s.db.Transaction(ctx, []sqlite.Statement{
		{SQL: "DELETE FROM reply_records WHERE outbox_id = (SELECT id FROM outbox WHERE id = ? AND state = ? AND draft_id IS NOT NULL)", Args: []any{id, state}},
		{SQL: "DELETE FROM outbox WHERE id = ? AND state = ? AND draft_id IS NOT NULL", Args: []any{id, state}}})
	if err != nil {
		return err
	}
	if rs[1].Changes == 0 {
		if r[3] == nil && store.Str(r[2]) == state {
			return ErrNoOutboxDraft
		}
		return ErrInvalidState
	}
	os.Remove(store.Str(r[1]))
	s.bus.Publish(events.Event{Type: events.OutboxChanged, AccountID: store.Int(r[0])})
	return nil
}

// ResolveOutbox decides an item in send_uncertain, archive_uncertain or failed:
// confirm_sent treats it as sent (it is archived next), resend queues it again
// for now (OutboxExpiry counts from then; never archive_uncertain, which the
// server already accepted) and discard drops it; all three clear
// its Failure, and confirm_sent and discard delete its draft. edit
// takes a failed item back as CancelOutbox does. resend also retries a queued
// item now, its undo window still kept.
func (s *Service) ResolveOutbox(ctx context.Context, id int64, action string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT account_id, raw_path, draft_id FROM outbox WHERE id = ?", id)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("outbox item %d not found", id)
	}
	if err := s.bind(ctx, store.Int(rows.Values[0][0])); err != nil {
		return err
	}
	unlock, err := s.lockAccount(ctx, store.Int(rows.Values[0][0])) // never while a sync is handling the item
	if err != nil {
		return err
	}
	defer unlock()
	if action == "edit" {
		return s.takeBack(ctx, id, "failed")
	}
	o := outboxRow{id: id, draftID: rows.Values[0][2]}
	var ok bool
	switch action {
	case "confirm_sent":
		ok, err = s.moveOutbox(ctx, o, "sent", []string{"send_uncertain", "archive_uncertain", "failed"}, "last_error = NULL, failure = NULL")
	case "resend":
		ok, err = s.moveOutbox(ctx, o, "queued", []string{"send_uncertain", "failed"},
			"attempts = 0, last_error = NULL, failure = NULL, send_after = ?, next_attempt_at = NULL", s.clk().Now().UnixMilli())
		if err == nil && !ok { // retry a queued one now
			ok, err = s.moveOutbox(ctx, o, "queued", []string{"queued"}, "next_attempt_at = NULL")
		}
	case "discard":
		// The draft goes with the item, in the same transaction: it is done with.
		const decidable = "id = ? AND state IN ('send_uncertain', 'archive_uncertain', 'failed')"
		rs, terr := s.db.Transaction(ctx, []sqlite.Statement{
			{SQL: "DELETE FROM drafts WHERE id = (SELECT draft_id FROM outbox WHERE " + decidable + ")", Args: []any{id}},
			{SQL: "DELETE FROM reply_records WHERE outbox_id = (SELECT id FROM outbox WHERE " + decidable + ")", Args: []any{id}},
			{SQL: "DELETE FROM outbox WHERE " + decidable, Args: []any{id}}})
		ok, err = terr == nil && rs[2].Changes == 1, terr
	default:
		return fmt.Errorf("unknown outbox action %q", action)
	}
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidState
	}
	if action == "discard" {
		if p := store.Str(rows.Values[0][1]); p != "" {
			os.Remove(p)
		}
		s.removeDraftDir(o.draftID)
	}
	s.bus.Publish(events.Event{Type: events.OutboxChanged, AccountID: store.Int(rows.Values[0][0])})
	s.wakeWorker(store.Int(rows.Values[0][0]))
	return nil
}

// removeDraftDir removes the directory of a deleted draft, if id is one.
func (s *Service) removeDraftDir(id any) {
	if id != nil {
		// ponytail: a failed removal leaves an orphan directory; sweep drafts/ on startup if it matters.
		os.RemoveAll(s.draftDir(store.Int(id)))
	}
}

// recoverOutbox runs at startup: an item caught mid-SMTP or mid-APPEND by a crash
// may or may not have gone through.
func (s *Service) recoverOutbox(ctx context.Context) error {
	rows, err := s.db.Query(ctx, "SELECT id, state, draft_id FROM outbox WHERE state IN ('sending', 'archiving')")
	if err != nil {
		return err
	}
	for _, r := range rows.Values {
		o := outboxRow{id: store.Int(r[0]), state: store.Str(r[1]), draftID: r[2]}
		to := map[string]string{"sending": "send_uncertain", "archiving": "archive_uncertain"}[o.state]
		if _, err := s.moveOutbox(ctx, o, to, []string{o.state}, ""); err != nil {
			return err
		}
	}
	return nil
}

type outboxRow struct {
	id                 int64
	messageID, rawPath string
	rcpts              []string
	state              string
	draftID            any   // NULL without a draft
	attempts           int   // tries so far
	queuedAt           int64 // unix ms the expiry counts from
}

// processOutbox sends the account's queued messages through its sending
// backend and has recv file sent ones into the Sent folder, unless the sending
// backend saves them itself. Every state change is written before the network
// action it guards, and a message whose submission may have succeeded is never
// sent again automatically. A queued item past OutboxExpiry fails instead of
// being sent. Network failures are recorded on the items; database errors are
// returned, and so is a refused login, which stops the round and pauses the
// account's outbox (smtp_reauth).
func (s *Service) processOutbox(ctx context.Context, recv backend.Receiver, accountID int64) error {
	start := s.clk().Now().UnixMilli()
	defer s.wakeForOutbox(ctx, accountID, start)
	// A queued item waits for its undo window and its retry time.
	rows, err := s.db.Query(ctx, `SELECT id, message_id_hdr, raw_path, rcpts, state, draft_id, attempts,
		COALESCE(send_after, created_at * 1000) FROM outbox
		WHERE account_id = ? AND (state IN ('sent', 'send_uncertain', 'archive_uncertain')
		OR state = 'queued' AND IFNULL(send_after, 0) <= ? AND IFNULL(next_attempt_at, 0) <= ?) ORDER BY id`, accountID, start, start)
	if err != nil || len(rows.Values) == 0 {
		return err
	}
	defer s.bus.Publish(events.Event{Type: events.OutboxChanged, AccountID: accountID})
	// Writes after a network action use wctx: a cancelled sync must still record
	// what happened, or the item would stay in sending/archiving.
	wctx := context.WithoutCancel(ctx)
	acc, err := s.account(ctx, accountID)
	if err != nil {
		return err
	}
	sa := &sendAuth{s: s, acc: acc}
	// Before a sync stored any folder, whether there is a Sent folder is not
	// known: sent items wait for a later round.
	sent, known, err := mailsync.SentFolder(ctx, s.db, accountID)
	if err != nil {
		return err
	}
	keep, err := s.localSent(ctx, accountID)
	if err != nil {
		return err
	}
	for _, r := range rows.Values {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		o := outboxRow{id: store.Int(r[0]), messageID: store.Str(r[1]), rawPath: store.Str(r[2]),
			rcpts: jsonList(store.Str(r[3])), state: store.Str(r[4]), draftID: r[5], attempts: int(store.Int(r[6])),
			queuedAt: store.Int(r[7])}
		if beforeOutboxItem != nil {
			beforeOutboxItem(o.id)
		}
		switch o.state {
		case "queued":
			if o.queuedAt < start-OutboxExpiry.Milliseconds() {
				// Not sent days late without the user knowing: the user decides.
				if err := s.failOutbox(wctx, o, "send: queued for over 3 days",
					OutboxFailure{Reason: "expired"}); err != nil {
					return err
				}
				continue
			}
			var ok bool
			for retry := true; retry; retry = err != nil && sa.retry(ctx, err) {
				if _, err := sa.credentials(ctx); err != nil {
					return err
				}
				ok, err = s.sendItem(ctx, wctx, sa.sender(), acc.Email, o)
			}
			if errors.Is(err, backend.ErrAuthFailed) {
				// Not this message's fault: it is queued again, uncharged, and the
				// outbox waits for new credentials instead of trying each item.
				derr := s.ifCurrent(accountID, sa.ver, func() error {
					_, err := s.db.Execute(wctx, "UPDATE accounts SET smtp_reauth = 1 WHERE id = ?", accountID)
					return err
				})
				return errors.Join(err, derr)
			}
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			o.state = "sent"
			fallthrough
		case "sent", "archive_uncertain":
			saves := sa.sender().SavesSent()
			if !known && !saves {
				continue
			}
			if err := s.archiveItem(ctx, wctx, recv, saves, sent, keep, o); err != nil {
				return err
			}
		case "send_uncertain":
			if sent == "" {
				continue // cannot check; the user decides
			}
			found, err := recv.Find(ctx, sent, o.messageID, 0)
			if err != nil {
				_, err = s.setOutbox(wctx, o, "send_uncertain", err.Error())
			} else if len(found) > 0 {
				err = s.finishOutbox(wctx, o)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// wakeForOutbox has the account's worker sync again when the next queued item
// not due at start (unix milliseconds) comes due, in place of the time set
// before. Items due at start were handled this round.
func (s *Service) wakeForOutbox(ctx context.Context, accountID, start int64) {
	rows, err := s.db.Query(ctx, `SELECT MIN(due) FROM (SELECT MAX(IFNULL(send_after, 0), IFNULL(next_attempt_at, 0)) AS due
		FROM outbox WHERE account_id = ? AND state = 'queued') WHERE due > ?`, accountID, start)
	var next time.Time
	if err == nil && len(rows.Values) > 0 && rows.Values[0][0] != nil {
		next = time.UnixMilli(store.Int(rows.Values[0][0]))
	}
	s.wakeWorkerAt(accountID, wakeOutbox, next)
}

// graphSendURL is where the Graph sending backend posts; tests replace it.
var graphSendURL = graph.SendURL

// sendAuth is an outbox round's sign-in to the account's sending backend:
// SMTP with the account's credentials, or Graph with a Graph token once SMTP
// refused AUTH for the mailbox. It keeps the credentials for the round.
type sendAuth struct {
	s         *Service
	acc       Account
	ver       uint64               // cur's credential version
	cur       *backend.Credentials // nil until read, and after retry
	rejected  string               // an access token the server refused
	refreshed bool                 // a refused access token was refreshed this round
}

func (a *sendAuth) credentials(ctx context.Context) (backend.Credentials, error) {
	if a.cur != nil {
		return *a.cur, nil
	}
	cr := backend.Credentials{Username: a.acc.Username}
	var err error
	if a.acc.Sending.Protocol == auth.Graph {
		cr.Token, a.ver, err = a.s.graphToken(ctx, a.acc)
	} else {
		cr, a.ver, err = a.s.slotCredentials(ctx, a.acc, true, a.rejected)
	}
	if err != nil {
		return backend.Credentials{}, err
	}
	a.cur = &cr
	return cr, nil
}

// sender is the account's sending backend.
func (a *sendAuth) sender() backend.Sender {
	switch a.acc.Sending.Protocol {
	case auth.Graph:
		return graph.New(graphSendURL, a.credentials)
	case auth.EWS:
		return ews.NewSender(ews.Config{URL: ewsURL(a.acc), Credentials: a.credentials})
	}
	return a.smtp()
}

// smtp is the account's SMTP sending backend.
func (a *sendAuth) smtp() *smtpbackend.Sender {
	return smtpbackend.New(smtpbackend.Config{Host: a.acc.Sending.Host, Port: a.acc.Sending.Port,
		Security: a.acc.Sending.Security, Credentials: a.credentials})
}

// retry reports whether a send that failed with err is worth one more try
// with other credentials: SMTP refused AUTH for the mailbox and the account
// can send through Graph instead (from then on it does), or the server
// refused an access token that may just be stale (refreshed once a round).
func (a *sendAuth) retry(ctx context.Context, err error) bool {
	switch {
	case a.s.toGraph(ctx, &a.acc, err):
	case errors.Is(err, backend.ErrAuthFailed) && a.cur != nil && a.cur.Token != "" && !a.refreshed:
		a.refreshed, a.rejected = true, a.cur.Token
	default:
		return false
	}
	a.cur = nil
	return true
}

// toGraph reports whether err is SMTP refusing AUTH for the whole mailbox and
// acc can send through Graph instead; from then on it does, until its servers
// change.
func (s *Service) toGraph(ctx context.Context, acc *Account, err error) bool {
	if !errors.Is(err, conn.ErrSMTPAuthDisabled) || acc.AuthType != "oauth" {
		return false
	}
	if cfg, cerr := oauthConfig(acc.Provider); cerr != nil || cfg.GraphSend == "" {
		return false
	}
	acc.Sending.Protocol = auth.Graph
	if _, err := s.db.Execute(context.WithoutCancel(ctx), "UPDATE accounts SET send_protocol = ? WHERE id = ?", auth.Graph, acc.ID); err != nil {
		slog.Warn("graph sending not stored", "account", acc.ID, "err", err) // this round still sends through Graph
	}
	return true
}

// sendItem submits a queued item and reports whether it is now sent. A refused
// login puts the item back to queued and returns the backend.ErrAuthFailed error.
// A permanent refusal (5xx) fails it; any other error before the server could
// accept it queues it again after a backoff, unless ctx ended: then it is
// queued as it was, with no try counted. The claim (queued to sending)
// races CancelOutbox's delete: one of them wins.
func (s *Service) sendItem(ctx, wctx context.Context, snd backend.Sender, from string, o outboxRow) (bool, error) {
	if ok, err := s.setOutbox(ctx, o, "sending", ""); !ok || err != nil {
		return false, err
	}
	o.state = "sending"
	raw, err := os.ReadFile(o.rawPath)
	if err != nil {
		_, err = s.setOutbox(wctx, o, "failed", err.Error())
		return false, err
	}
	err = snd.Send(ctx, from, o.rcpts, raw)
	var rej *backend.RejectedError
	switch {
	case err == nil:
		return s.setOutbox(wctx, o, "sent", "")
	case errors.Is(err, backend.ErrUncertain):
		_, err = s.setOutbox(wctx, o, "send_uncertain", err.Error())
		return false, err
	case errors.Is(err, backend.ErrAuthFailed):
		_, derr := s.setOutbox(wctx, o, "queued", err.Error())
		return false, errors.Join(err, derr)
	case errors.As(err, &rej) && rej.Code >= 500:
		return false, s.failOutbox(wctx, o, err.Error(), OutboxFailure{Reason: "rejected", Stage: rej.Stage,
			Recipient: rej.Recipient, Code: rej.Code, Enhanced: rej.Enhanced, Message: rej.Message})
	case ctx.Err() != nil: // the sync stopped (quitting, the worker stopping): not a try
		_, err = s.moveOutbox(wctx, o, "queued", []string{"sending"}, "")
		return false, err
	}
	// Refused for now (4xx), or failed before the server could take it: retry later.
	wait := min(retryMin<<min(o.attempts, 6), retryMax) // 2^6 min is past retryMax; the shift cannot overflow
	_, derr := s.moveOutbox(wctx, o, "queued", []string{"sending"}, "attempts = attempts + 1, last_error = ?, next_attempt_at = ?",
		err.Error(), s.clk().Now().Add(wait).UnixMilli())
	return false, derr
}

// failOutbox moves the item from o.state to failed for f.
func (s *Service) failOutbox(ctx context.Context, o outboxRow, lastError string, f OutboxFailure) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = s.moveOutbox(ctx, o, "failed", []string{o.state}, "last_error = ?, failure = ?", lastError, string(b))
	return err
}

// archiveItem files a sent item into the Sent folder sent unless it is there
// already (the provider saved it, or an earlier uncertain append went
// through). It is done without filing when the sending backend saves sent
// copies itself, and when there is no Sent folder (sent is "") or recv cannot
// append: there is nowhere to file it. keep, when set (localSent), files it
// on this computer instead.
func (s *Service) archiveItem(ctx, wctx context.Context, recv backend.Receiver, saves bool, sent backend.FolderRef, keep func(context.Context, []byte) error, o outboxRow) error {
	if ok, err := s.setOutbox(ctx, o, "archiving", ""); !ok || err != nil {
		return err
	}
	o.state = "archiving"
	err := func() error {
		if keep != nil && !saves {
			raw, err := os.ReadFile(o.rawPath)
			if err != nil {
				return err
			}
			return keep(ctx, raw)
		}
		if saves || sent == "" {
			return nil
		}
		found, err := recv.Find(ctx, sent, o.messageID, 0)
		if err != nil || len(found) > 0 {
			return err
		}
		raw, err := os.ReadFile(o.rawPath)
		if err != nil {
			return err
		}
		res, err := recv.Apply(ctx, backend.Append{To: sent, Raw: raw, Flags: backend.Flags{Seen: true}, MessageID: o.messageID})
		if err == nil && res.Outcome == backend.Failed {
			err = fmt.Errorf("the server refused to store the sent copy: %s", res.Reason)
		}
		return err
	}()
	if err != nil {
		// Possibly appended: the next round looks before appending again.
		_, err = s.setOutbox(wctx, o, "archive_uncertain", err.Error())
		return err
	}
	return s.finishOutbox(wctx, o)
}

// localSent keeps a sent copy in the account's local Sent folder when its
// server has only an inbox (POP3, spec #627): read, with its original, and
// not new mail. nil for other accounts. It runs under the sync's account lock.
func (s *Service) localSent(ctx context.Context, accountID int64) (func(context.Context, []byte) error, error) {
	r, err := s.db.Query(ctx, "SELECT "+mailsync.InboxOnly, accountID)
	if err != nil || store.Int(r.Values[0][0]) == 0 {
		return nil, err
	}
	folder, err := s.roleFolder(ctx, accountID, "sent")
	if none := (*ErrNoRoleFolder)(nil); errors.As(err, &none) {
		return nil, nil // no Sent folder: nowhere to keep a copy, as on other servers
	}
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, raw []byte) error {
		_, _, err := s.insertOriginal(ctx, accountID, folder, raw, s.clk().Now()) // a copy already kept is a duplicate
		return err
	}, nil
}

// outboxFrom is the outbox state machine (ADR 0006), the states each state
// is entered from; moveOutbox makes no other change:
//
//	queued → sending (claimed, racing CancelOutbox) | failed (expired)
//	sending → sent | send_uncertain (may have gone out) | queued (retried, login refused, sync stopped) | failed (refused, raw copy unreadable)
//	sent | archive_uncertain → archiving → done | archive_uncertain
//	send_uncertain → send_uncertain (could not check) | done (found in Sent)
//	send_uncertain | archive_uncertain | failed → sent (confirm_sent); send_uncertain | failed → queued (resend); queued → queued (resend now)
//
// Items also leave by deletion: queued (CancelOutbox) and failed (edit), with
// their draft given back; send_uncertain, archive_uncertain and failed
// (discard), with their draft.
var outboxFrom = map[string][]string{
	"sending":           {"queued"},
	"sent":              {"sending", "send_uncertain", "archive_uncertain", "failed"},
	"send_uncertain":    {"sending", "send_uncertain"},
	"queued":            {"sending", "send_uncertain", "failed", "queued"},
	"failed":            {"queued", "sending"},
	"archiving":         {"sent", "archive_uncertain"},
	"archive_uncertain": {"archiving"},
	"done":              {"archiving", "send_uncertain"},
}

// moveOutbox moves the item to state to from one of from, also setting set
// ("column = ?, ...", with args), and reports whether it did: an item changed
// since it was read (resolved, discarded, cancelled) is left alone. Entering
// sent or send_uncertain the message may have gone out: its draft is deleted
// in the same transaction, never to be sent again (draft_id becomes NULL).
func (s *Service) moveOutbox(ctx context.Context, o outboxRow, to string, from []string, set string, args ...any) (bool, error) {
	whereArgs := []any{o.id}
	for _, f := range from {
		if !slices.Contains(outboxFrom[to], f) {
			return false, fmt.Errorf("outbox item %d cannot go from %s to %s", o.id, f, to)
		}
		whereArgs = append(whereArgs, f)
	}
	where := " WHERE id = ? AND state IN (?" + strings.Repeat(", ?", len(from)-1) + ")"
	if set != "" {
		set = ", " + set
	}
	var stmts []sqlite.Statement
	gone := to == "sent" || to == "send_uncertain"
	if gone { // only from from: the update below moves it in the same transaction
		stmts = append(stmts, sqlite.Statement{SQL: "DELETE FROM drafts WHERE id = (SELECT draft_id FROM outbox" + where + ")", Args: whereArgs})
	}
	stmts = append(stmts, sqlite.Statement{SQL: "UPDATE outbox SET state = ?" + set + where,
		Args: append(append([]any{to}, args...), whereArgs...)})
	rs, err := s.db.Transaction(ctx, stmts)
	if err != nil || rs[len(rs)-1].Changes != 1 {
		return false, err
	}
	if gone {
		s.removeDraftDir(o.draftID)
	}
	return true, nil
}

// setOutbox moves the item from o.state to state with lastError, as moveOutbox.
func (s *Service) setOutbox(ctx context.Context, o outboxRow, state, lastError string) (bool, error) {
	return s.moveOutbox(ctx, o, state, []string{o.state}, "last_error = ?", lastError)
}

// finishOutbox marks the item done and drops its raw copy, which the Sent folder now holds.
func (s *Service) finishOutbox(ctx context.Context, o outboxRow) error {
	if ok, err := s.setOutbox(ctx, o, "done", ""); !ok || err != nil {
		return err
	}
	os.Remove(o.rawPath) // ponytail: a failed removal leaves an orphan .eml; sweep outbox/ if it matters
	return nil
}
