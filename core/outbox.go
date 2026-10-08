package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwitmail/godwit/core/conn"
	"github.com/godwitmail/godwit/core/events"
	"github.com/godwitmail/godwit/core/mailsync"
	"github.com/godwitmail/godwit/core/send"
	"github.com/godwitmail/godwit/core/store"
	"github.com/zalando/go-keyring"
)

// ErrInvalidState is returned by ResolveOutbox for an item that needs no decision.
var ErrInvalidState = errors.New("outbox item is not waiting for a decision")

const maxSendAttempts = 5

// OutboxItem is a message that has not finished sending and archiving. State is
// queued, sending, sent, archiving, send_uncertain, archive_uncertain or failed.
type OutboxItem struct {
	ID               int64
	Subject          string
	State, LastError string
	CreatedAt        time.Time
}

// ListOutbox returns the account's unfinished outbox items, oldest first.
func (s *Service) ListOutbox(ctx context.Context, accountID int64) ([]OutboxItem, error) {
	rows, err := s.db.Query(ctx, `SELECT id, subject, state, last_error, created_at FROM outbox
		WHERE account_id = ? AND state != 'done' ORDER BY id`, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]OutboxItem, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, OutboxItem{ID: store.Int(r[0]), Subject: store.Str(r[1]), State: store.Str(r[2]),
			LastError: store.Str(r[3]), CreatedAt: time.Unix(store.Int(r[4]), 0)})
	}
	return out, nil
}

// ResolveOutbox decides an item in send_uncertain, archive_uncertain or failed:
// confirm_sent treats it as sent (it is archived next), resend queues it again
// and discard drops it.
func (s *Service) ResolveOutbox(ctx context.Context, id int64, action string) error {
	rows, err := s.db.Query(ctx, "SELECT account_id, raw_path FROM outbox WHERE id = ?", id)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("outbox item %d not found", id)
	}
	const decidable = "id = ? AND state IN ('send_uncertain', 'archive_uncertain', 'failed')"
	var sql string
	switch action {
	case "confirm_sent":
		sql = "UPDATE outbox SET state = 'sent', last_error = NULL WHERE " + decidable
	case "resend":
		sql = "UPDATE outbox SET state = 'queued', attempts = 0, last_error = NULL WHERE " + decidable
	case "discard":
		sql = "DELETE FROM outbox WHERE " + decidable
	default:
		return fmt.Errorf("unknown outbox action %q", action)
	}
	// Conditional, so a sync that moved the item on in the meantime wins.
	r, err := s.db.Execute(ctx, sql, id)
	if err != nil {
		return err
	}
	if r.Changes == 0 {
		return ErrInvalidState
	}
	if action == "discard" {
		if p := store.Str(rows.Values[0][1]); p != "" {
			os.Remove(p)
		}
	}
	s.bus.Publish(events.Event{Type: events.OutboxChanged, AccountID: store.Int(rows.Values[0][0])})
	return nil
}

// recoverOutbox runs at startup: an item caught mid-SMTP or mid-APPEND by a crash
// may or may not have gone through.
func recoverOutbox(ctx context.Context, db *store.DB) error {
	if _, err := db.Execute(ctx, "UPDATE outbox SET state = 'send_uncertain' WHERE state = 'sending'"); err != nil {
		return err
	}
	_, err := db.Execute(ctx, "UPDATE outbox SET state = 'archive_uncertain' WHERE state = 'archiving'")
	return err
}

type outboxRow struct {
	id                 int64
	messageID, rawPath string
	rcpts              []string
	state              string
}

// processOutbox sends the account's queued messages and files sent ones into the
// Sent folder over c. Every state change is written before the network action it
// guards, and a message whose submission may have succeeded is never sent again
// automatically. Network failures are recorded on the items; only database
// errors are returned.
func (s *Service) processOutbox(ctx context.Context, c *imapclient.Client, accountID int64) error {
	rows, err := s.db.Query(ctx, `SELECT id, message_id_hdr, raw_path, rcpts, state FROM outbox
		WHERE account_id = ? AND state IN ('queued', 'sent', 'send_uncertain', 'archive_uncertain') ORDER BY id`, accountID)
	if err != nil || len(rows.Values) == 0 {
		return err
	}
	defer s.bus.Publish(events.Event{Type: events.OutboxChanged, AccountID: accountID})
	// Writes after a network action use wctx: a cancelled sync must still record
	// what happened, or the item would stay in sending/archiving.
	wctx := context.WithoutCancel(ctx)
	var (
		ep         *conn.Endpoint
		from       string
		sent       string
		sentLooked bool
	)
	sentFolder := func() (string, error) {
		if !sentLooked {
			var err error
			if sent, err = mailsync.SentFolder(c); err != nil {
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
			rcpts: jsonList(store.Str(r[3])), state: store.Str(r[4])}
		switch o.state {
		case "queued":
			if ep == nil {
				acc, err := s.account(ctx, accountID)
				if err != nil {
					return err
				}
				pw, err := keyring.Get(keyringService, keyringUser(accountID))
				if err != nil {
					return err
				}
				ep = &conn.Endpoint{Host: acc.SMTPHost, Port: acc.SMTPPort, Security: acc.SMTPSecurity, Username: acc.Username, Password: pw}
				from = acc.Email
			}
			ok, err := s.sendItem(ctx, wctx, *ep, from, o)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
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
				if err := s.setOutbox(wctx, o.id, "send_uncertain", err.Error()); err != nil {
					return err
				}
			} else if found {
				if err := s.finishOutbox(wctx, o); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// sendItem submits a queued item and reports whether it is now sent.
func (s *Service) sendItem(ctx, wctx context.Context, ep conn.Endpoint, from string, o outboxRow) (bool, error) {
	r, err := s.db.Execute(ctx, "UPDATE outbox SET state = 'sending' WHERE id = ? AND state = 'queued'", o.id)
	if err != nil || r.Changes == 0 {
		return false, err // resolved or discarded meanwhile
	}
	raw, err := os.ReadFile(o.rawPath)
	if err != nil {
		return false, s.setOutbox(wctx, o.id, "failed", err.Error())
	}
	err = send.Send(ctx, ep, from, o.rcpts, raw)
	switch {
	case err == nil:
		return true, s.setOutbox(wctx, o.id, "sent", "")
	case errors.Is(err, send.ErrUncertain):
		return false, s.setOutbox(wctx, o.id, "send_uncertain", err.Error())
	}
	_, derr := s.db.Execute(wctx, `UPDATE outbox SET attempts = attempts + 1, last_error = ?,
		state = CASE WHEN attempts + 1 >= ? THEN 'failed' ELSE 'queued' END WHERE id = ?`, err.Error(), maxSendAttempts, o.id)
	return false, derr
}

// archiveItem files a sent item into the Sent folder unless it is there already
// (the provider saved it, or an earlier uncertain APPEND went through). Without
// a Sent folder there is nowhere to file it, so it is done.
func (s *Service) archiveItem(ctx, wctx context.Context, c *imapclient.Client, sentFolder func() (string, error), o outboxRow) error {
	if _, err := s.db.Execute(ctx, "UPDATE outbox SET state = 'archiving' WHERE id = ?", o.id); err != nil {
		return err
	}
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
		return s.setOutbox(wctx, o.id, "archive_uncertain", err.Error())
	}
	return s.finishOutbox(wctx, o)
}

func (s *Service) setOutbox(ctx context.Context, id int64, state, lastError string) error {
	_, err := s.db.Execute(ctx, "UPDATE outbox SET state = ?, last_error = ? WHERE id = ?", state, lastError, id)
	return err
}

// finishOutbox marks the item done and drops its raw copy, which the Sent folder now holds.
func (s *Service) finishOutbox(ctx context.Context, o outboxRow) error {
	if err := s.setOutbox(ctx, o.id, "done", ""); err != nil {
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
