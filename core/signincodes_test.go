package core

import (
	"slices"
	"strings"
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

// On mail under the Codes label, how a mail looks finds what no lexicon
// reads: a code set apart, a lone button. Elsewhere it does not.
func TestSignInCodesByLook(t *testing.T) {
	e := newSyncEnv(t)
	now := time.Now().Truncate(time.Second)
	e.sync(t)
	html := func(id, subject, body string) string {
		return strings.Replace(eml(id, "Acme <id@acme.example>", e.acc.Email, subject, now.Format(time.RFC1123Z), body), "text/plain", "text/html", 1)
	}
	e.put(t, "INBOX", html("code@x", "Acme", `<p>Συνέχεια:</p><p style="font-size:28px;letter-spacing:4px">482913</p>`))
	e.put(t, "INBOX", html("button@x", "Acme", `<p>Συνέχεια:</p><table><tr><td bgcolor="#06c"><a href="https://acme.example/l/1">Είσοδος</a></td></tr></table>`))
	e.put(t, "INBOX", html("order@x", "Acme", `<p>Παραγγελία</p><p><b>731904</b></p>`))
	e.sync(t)
	l, err := e.s.CreateLabel(bg, LabelInput{Name: "Codes", Color: "red", Icon: "shield-keyhole"}) // the preset, which a test start does not seed
	require.NoError(t, err)
	_, err = e.s.db.Execute(bg, "UPDATE labels SET slug = 'otp' WHERE id = ?", l.ID)
	require.NoError(t, err)
	otp := l.ID

	codes, err := e.s.RecentSignInCodes(bg, now)
	require.NoError(t, err)
	require.Empty(t, codes, "unlabeled mail is read by its words alone")

	require.NoError(t, e.s.SetLabel(bg, []int64{e.idOf(t, "code@x"), e.idOf(t, "button@x")}, otp, true))
	codes, err = e.s.RecentSignInCodes(bg, now)
	require.NoError(t, err)
	require.Len(t, codes, 2)
	code := codes[slices.IndexFunc(codes, func(c SignInCode) bool { return c.MessageID == e.idOf(t, "code@x") })]
	require.Equal(t, "482913", code.Code)
	require.Empty(t, code.Link)
	button := codes[slices.IndexFunc(codes, func(c SignInCode) bool { return c.MessageID == e.idOf(t, "button@x") })]
	require.Equal(t, "https://acme.example/l/1", button.Link)
	require.Equal(t, "signin", button.LinkKind)

	tags, err := e.s.PluginTags(bg, e.idOf(t, "code@x"))
	require.NoError(t, err)
	require.Equal(t, []Tag{{Plugin: SignInCodesPlugin, Builtin: true, Kind: "copy", Text: "482913", Value: "482913"}}, tags)
	tags, err = e.s.PluginTags(bg, e.idOf(t, "order@x"))
	require.NoError(t, err)
	require.Empty(t, tags)

	require.NoError(t, e.s.SetLabel(bg, []int64{e.idOf(t, "code@x")}, otp, false))
	tags, err = e.s.PluginTags(bg, e.idOf(t, "code@x"))
	require.NoError(t, err)
	require.Empty(t, tags, "taken off by hand: no longer a code mail")
}
