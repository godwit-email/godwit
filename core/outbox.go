package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	netmail "net/mail"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/send"
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
	res, err := s.db.Execute(ctx, "DELETE FROM outbox WHERE id = ? AND state = ? AND draft_id IS NOT NULL", id, state)
	if err != nil {
		return err
	}
	if res.Changes == 0 {
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
// for now (OutboxExpiry counts from then) and discard drops it; all three clear
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
	const decidable = "id = ? AND state IN ('send_uncertain', 'archive_uncertain', 'failed')"
	// The draft goes with the item, in the same transaction: it is done with.
	dropDraft := sqlite.Statement{SQL: "DELETE FROM drafts WHERE id = (SELECT draft_id FROM outbox WHERE " + decidable + ")", Args: []any{id}}
	var stmts []sqlite.Statement
	switch action {
	case "confirm_sent":
		stmts = []sqlite.Statement{dropDraft, {SQL: "UPDATE outbox SET state = 'sent', last_error = NULL, failure = NULL WHERE " + decidable, Args: []any{id}}}
	case "resend":
		stmts = []sqlite.Statement{{SQL: `UPDATE outbox SET state = 'queued', attempts = 0, last_error = NULL, failure = NULL, send_after = ?,
			next_attempt_at = NULL WHERE ` + decidable, Args: []any{s.clk().Now().UnixMilli(), id}}}
	case "discard":
		stmts = []sqlite.Statement{dropDraft, {SQL: "DELETE FROM outbox WHERE " + decidable, Args: []any{id}}}
	default:
		return fmt.Errorf("unknown outbox action %q", action)
	}
	rs, err := s.db.Transaction(ctx, stmts)
	if err != nil {
		return err
	}
	if rs[len(rs)-1].Changes == 0 && action == "resend" { // retry a queued one now
		rs, err = s.db.Transaction(ctx, []sqlite.Statement{{SQL: "UPDATE outbox SET next_attempt_at = NULL WHERE id = ? AND state = 'queued'", Args: []any{id}}})
		if err != nil {
			return err
		}
	}
	if rs[len(rs)-1].Changes == 0 {
		return ErrInvalidState
	}
	if action == "discard" {
		if p := store.Str(rows.Values[0][1]); p != "" {
			os.Remove(p)
		}
	}
	if action != "resend" {
		s.removeDraftDir(rows.Values[0][2])
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
// may or may not have gone through. One that may have gone out takes its draft
// with it, as setOutbox does.
func (s *Service) recoverOutbox(ctx context.Context) error {
	rows, err := s.db.Query(ctx, "SELECT draft_id FROM outbox WHERE state = 'sending' AND draft_id IS NOT NULL")
	if err != nil {
		return err
	}
	if _, err := s.db.Transaction(ctx, []sqlite.Statement{
		{SQL: "DELETE FROM drafts WHERE id IN (SELECT draft_id FROM outbox WHERE state = 'sending')"}, // draft_id becomes NULL
		{SQL: "UPDATE outbox SET state = 'send_uncertain' WHERE state = 'sending'"},
	}); err != nil {
		return err
	}
	for _, r := range rows.Values {
		s.removeDraftDir(r[0])
	}
	_, err = s.db.Execute(ctx, "UPDATE outbox SET state = 'archive_uncertain' WHERE state = 'archiving'")
	return err
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

// processOutbox sends the account's queued messages and files sent ones into the
// Sent folder over c. Every state change is written before the network action it
// guards, and a message whose submission may have succeeded is never sent again
// automatically. A queued item past OutboxExpiry fails instead of being sent.
// Network failures are recorded on the items; database errors are returned, and
// so is a refused SMTP login, which stops the round and pauses the account's
// outbox (smtp_reauth).
func (s *Service) processOutbox(ctx context.Context, c *imapclient.Client, accountID int64) error {
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
	var (
		ep         *conn.Endpoint
		epVer      uint64 // ep's credential version
		from       string
		sent       string
		sentLooked bool
		refreshed  bool // the access token was refreshed after SMTP refused it
	)
	sentFolder := func() (string, error) {
		if !sentLooked {
			var err error
			if sent, err = mailsync.SentFolder(ctx, s.db, c, accountID); err != nil {
				return "", err
			}
			sentLooked = true
		}
		return sent, nil
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
			if ep == nil {
				e, f, v, err := s.smtpEndpoint(ctx, accountID, "")
				if err != nil {
					return err
				}
				ep, from, epVer = &e, f, v
			}
			ok, err := s.sendItem(ctx, wctx, *ep, from, o)
			if errors.Is(err, conn.ErrAuthFailed) && ep.AccessToken != "" && !refreshed {
				// The access token may just be stale: refresh it once per round.
				refreshed = true
				e, _, v, rerr := s.smtpEndpoint(ctx, accountID, ep.AccessToken)
				if rerr != nil {
					return rerr
				}
				ep, epVer = &e, v
				ok, err = s.sendItem(ctx, wctx, *ep, from, o)
			}
			if errors.Is(err, conn.ErrAuthFailed) {
				// Not this message's fault: it is queued again, uncharged, and the
				// outbox waits for new credentials instead of trying each item.
				derr := s.ifCurrent(accountID, epVer, func() error {
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
			if err := s.archiveItem(ctx, wctx, c, sentFolder, o); err != nil {
				return err
			}
		case "send_uncertain":
			folder, err := sentFolder()
			if err != nil || folder == "" {
				continue // cannot check; the user decides
			}
			found, err := inFolder(c, folder, o.messageID)
			if err != nil {
				_, err = s.setOutbox(wctx, o, "send_uncertain", err.Error())
			} else if found {
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
	s.wakeWorkerAt(accountID, next)
}

// smtpEndpoint returns the account's SMTP server with its credentials, its
// address and the credential version. rejected: an access token SMTP refused, or "".
func (s *Service) smtpEndpoint(ctx context.Context, accountID int64, rejected string) (conn.Endpoint, string, uint64, error) {
	acc, err := s.account(ctx, accountID)
	if err != nil {
		return conn.Endpoint{}, "", 0, err
	}
	pw, tok, ver, err := s.credentials(ctx, acc, rejected)
	if err != nil {
		return conn.Endpoint{}, "", 0, err
	}
	return conn.Endpoint{Host: acc.SMTPHost, Port: acc.SMTPPort, Security: acc.SMTPSecurity, Username: acc.Username,
		Password: pw, AccessToken: tok}, acc.Email, ver, nil
}

// sendItem submits a queued item and reports whether it is now sent. A refused
// login puts the item back to queued and returns the conn.ErrAuthFailed error.
// A permanent refusal (5xx) fails it; any other error before the server could
// accept it queues it again after a backoff. The claim (queued to sending)
// races CancelOutbox's delete: one of them wins.
func (s *Service) sendItem(ctx, wctx context.Context, ep conn.Endpoint, from string, o outboxRow) (bool, error) {
	if ok, err := s.setOutbox(ctx, o, "sending", ""); !ok || err != nil {
		return false, err
	}
	o.state = "sending"
	raw, err := os.ReadFile(o.rawPath)
	if err != nil {
		_, err = s.setOutbox(wctx, o, "failed", err.Error())
		return false, err
	}
	err = send.Send(ctx, ep, from, o.rcpts, raw)
	var rej *send.RejectedError
	switch {
	case err == nil:
		return s.setOutbox(wctx, o, "sent", "")
	case errors.Is(err, send.ErrUncertain):
		_, err = s.setOutbox(wctx, o, "send_uncertain", err.Error())
		return false, err
	case errors.Is(err, conn.ErrAuthFailed):
		_, derr := s.setOutbox(wctx, o, "queued", err.Error())
		return false, errors.Join(err, derr)
	case errors.As(err, &rej) && rej.Code >= 500:
		return false, s.failOutbox(wctx, o, err.Error(), OutboxFailure{Reason: "rejected", Stage: rej.Stage,
			Recipient: rej.Recipient, Code: rej.Code, Enhanced: rej.Enhanced, Message: rej.Message})
	}
	// Refused for now (4xx), or failed before the server could take it: retry later.
	wait := min(retryMin<<min(o.attempts, 6), retryMax) // 2^6 min is past retryMax; the shift cannot overflow
	_, derr := s.db.Execute(wctx, `UPDATE outbox SET state = 'queued', attempts = attempts + 1, last_error = ?,
		next_attempt_at = ? WHERE id = ? AND state = 'sending'`, err.Error(), s.clk().Now().Add(wait).UnixMilli(), o.id)
	return false, derr
}

// failOutbox moves the item from o.state to failed for f.
func (s *Service) failOutbox(ctx context.Context, o outboxRow, lastError string, f OutboxFailure) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = s.db.Execute(ctx, "UPDATE outbox SET state = 'failed', last_error = ?, failure = ? WHERE id = ? AND state = ?",
		lastError, string(b), o.id, o.state)
	return err
}

// archiveItem files a sent item into the Sent folder unless it is there already
// (the provider saved it, or an earlier uncertain APPEND went through). Without
// a Sent folder there is nowhere to file it, so it is done.
func (s *Service) archiveItem(ctx, wctx context.Context, c *imapclient.Client, sentFolder func() (string, error), o outboxRow) error {
	if ok, err := s.setOutbox(ctx, o, "archiving", ""); !ok || err != nil {
		return err
	}
	o.state = "archiving"
	err := func() error {
		folder, err := sentFolder()
		if err != nil || folder == "" {
			return err
		}
		found, err := inFolder(c, folder, o.messageID)
		if err != nil || found {
			return err
		}
		raw, err := os.ReadFile(o.rawPath)
		if err != nil {
			return err
		}
		cmd := c.Append(folder, int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagSeen}})
		if _, err := cmd.Write(raw); err != nil {
			cmd.Close()
			return err
		}
		if err := cmd.Close(); err != nil {
			return err
		}
		_, err = cmd.Wait()
		return err
	}()
	if err != nil {
		// Possibly appended: the next round looks before appending again.
		_, err = s.setOutbox(wctx, o, "archive_uncertain", err.Error())
		return err
	}
	return s.finishOutbox(wctx, o)
}

// setOutbox moves the item from o.state to state and reports whether it did: an
// item changed since it was read (resolved, discarded, cancelled) is left alone.
// From sending to sent or send_uncertain the message may have gone out: its
// draft is deleted in the same transaction, never to be sent again.
func (s *Service) setOutbox(ctx context.Context, o outboxRow, state, lastError string) (bool, error) {
	stmts := []sqlite.Statement{{SQL: "UPDATE outbox SET state = ?, last_error = ? WHERE id = ? AND state = ?", Args: []any{state, lastError, o.id, o.state}}}
	gone := o.state == "sending" && (state == "sent" || state == "send_uncertain") && o.draftID != nil
	if gone { // only if the update moved it: the item is in state then (draft_id becomes NULL)
		stmts = append(stmts, sqlite.Statement{SQL: "DELETE FROM drafts WHERE id = (SELECT draft_id FROM outbox WHERE id = ? AND state = ?)",
			Args: []any{o.id, state}})
	}
	rs, err := s.db.Transaction(ctx, stmts)
	if err != nil || rs[0].Changes != 1 {
		return false, err
	}
	if gone {
		s.removeDraftDir(o.draftID)
	}
	return true, nil
}

// finishOutbox marks the item done and drops its raw copy, which the Sent folder now holds.
func (s *Service) finishOutbox(ctx context.Context, o outboxRow) error {
	if ok, err := s.setOutbox(ctx, o, "done", ""); !ok || err != nil {
		return err
	}
	os.Remove(o.rawPath) // ponytail: a failed removal leaves an orphan .eml; sweep outbox/ if it matters
	return nil
}

// inFolder reports whether folder holds a message whose Message-ID is exactly
// messageID. SEARCH HEADER matches substrings, so each hit is checked.
func inFolder(c *imapclient.Client, folder, messageID string) (bool, error) {
	if _, err := c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return false, err
	}
	res, err := c.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: messageID}}}, nil).Wait()
	if err != nil {
		return false, err
	}
	uids := res.AllUIDs()
	if len(uids) == 0 {
		return false, nil
	}
	sec := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"Message-ID"}, Peek: true}
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}}).Collect()
	if err != nil {
		return false, err
	}
	for _, m := range msgs {
		h, err := netmail.ReadMessage(bytes.NewReader(append(m.FindBodySection(sec), "\r\n\r\n"...)))
		if err != nil {
			continue
		}
		if strings.Trim(strings.TrimSpace(h.Header.Get("Message-Id")), "<>") == messageID {
			return true, nil
		}
	}
	return false, nil
}
