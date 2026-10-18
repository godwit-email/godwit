package core

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecentSignInCodes(t *testing.T) {
	e := newSyncEnv(t)
	now := time.Now().Truncate(time.Second)
	put := func(id, folder, subject string, date time.Time) {
		e.put(t, folder, eml(id, "Login <login@example.com>", e.acc.Email, subject, date.Format(time.RFC1123Z), "Do not share this code."))
	}
	put("initial@x", "INBOX", "Verification code: 111111", now)
	e.sync(t) // importing a mailbox must not populate the recent-code menu
	put("new@x", "INBOX", "Verification code: aB0123", now)
	put("stale@x", "INBOX", "Verification code: 222222", now.Add(-48*time.Hour)) // an old mail moved back in
	put("late@x", "INBOX", "Verification code: 666666", now.Add(-time.Hour))     // delivered late: it just arrived
	put("archive@x", "Archive", "Verification code: 333333", now)
	put("ordinary@x", "INBOX", "Order 444444", now)
	e.put(t, "INBOX", eml("link@x", "Acme <id@acme.example>", e.acc.Email, "Sign in to Acme", now.Format(time.RFC1123Z), "Log in with this link:\nhttps://acme.example/l/1"))
	put("skewed@x", "INBOX", "Verification code: 555555", now.Add(2*time.Hour)) // the sender's clock runs ahead
	e.sync(t)
	got, err := e.s.RecentSignInCodes(bg, now)
	require.NoError(t, err)
	require.Len(t, got, 4)
	require.True(t, slices.ContainsFunc(got, func(c SignInCode) bool { return c.Code == "555555" }), "a future Date header hid a code that just arrived")
	require.True(t, slices.ContainsFunc(got, func(c SignInCode) bool { return c.Code == "666666" }), "a late Date header hid a code that just arrived")
	code := got[slices.IndexFunc(got, func(c SignInCode) bool { return c.MessageID == e.idOf(t, "new@x") })]
	require.Equal(t, SignInCode{MessageID: e.idOf(t, "new@x"), Code: "aB0123", From: "Login <login@example.com>", ReceivedAt: code.ReceivedAt}, code)
	link := got[slices.IndexFunc(got, func(c SignInCode) bool { return c.MessageID == e.idOf(t, "link@x") })]
	require.Equal(t, "https://acme.example/l/1", link.Link)
	require.Empty(t, link.Code)

	got, err = e.s.RecentSignInCodes(bg, time.Now().Add(15*time.Minute)) // not now: the sync may have run in a later second
	require.NoError(t, err)
	require.Empty(t, got, "the menu expires entries without deleting the mail")
	require.NotZero(t, e.idOf(t, "new@x"))
}
