package core

import (
	"context"
	"time"

	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// SignInCode is a locally extracted sign-in code or sign-in link (or both)
// from a recent inbox arrival.
type SignInCode struct {
	MessageID        int64
	Code, Link, From string
	ReceivedAt       time.Time
}

// RecentSignInCodes reads the arrival log so a dropped UI event cannot lose
// a code. Initial imports and backfills never enter that log. Fifteen minutes
// is a display window, not a claim about the server's validity period. The
// arrival time bounds recency; a sender's clock running ahead does not hide
// a code.
// None while the built-in plugin is off.
func (s *Service) RecentSignInCodes(ctx context.Context, now time.Time) ([]SignInCode, error) {
	if on, err := s.builtinOn(ctx, SignInCodesPlugin); !on || err != nil {
		return nil, err
	}
	since := now.Add(-15 * time.Minute).Unix()
	rows, err := s.db.Query(ctx, `SELECT m.id, m.subject, m.from_addr, m.body_text, m.body_html, MAX(e.created_at)
		FROM events_log e JOIN messages m ON m.id = e.message_id JOIN folders f ON f.id = m.folder_id
		WHERE e.type = 'message.received' AND e.created_at > ? AND m.date > ? AND f.role = 'inbox'
		GROUP BY m.id ORDER BY MAX(e.created_at) DESC, m.id DESC`, since, since)
	if err != nil {
		return nil, err
	}
	var out []SignInCode
	for _, row := range rows.Values {
		subject, text, html := store.Str(row[1]), store.Str(row[3]), store.Str(row[4])
		code, link := mail.SignInCode(subject, text, html), mail.SignInLink(subject, text, html)
		if code != "" || link != "" {
			out = append(out, SignInCode{MessageID: store.Int(row[0]), Code: code, Link: link, From: store.Str(row[2]), ReceivedAt: time.Unix(store.Int(row[5]), 0)})
		}
	}
	return out, nil
}
