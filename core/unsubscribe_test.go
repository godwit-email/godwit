package core

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const unsubDate = "Mon, 05 Oct 2026 10:00:00 +0000"

// unsubServer records every request a one-click unsubscribe sends it; reply answers them.
type unsubServer struct {
	*httptest.Server
	mu    sync.Mutex
	reqs  []*http.Request
	body  []string
	reply func(w http.ResponseWriter, r *http.Request)
}

func newUnsubServer(t *testing.T) *unsubServer {
	u := &unsubServer{reply: func(w http.ResponseWriter, _ *http.Request) {}}
	u.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.reqs, u.body = append(u.reqs, r), append(u.body, string(b))
		reply := u.reply
		u.mu.Unlock()
		reply(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *unsubServer) requests() []*http.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]*http.Request(nil), u.reqs...)
}

// newUnsubEnv is a sync environment whose one-click unsubscribes reach ts (on loopback).
func newUnsubEnv(t *testing.T, ts *unsubServer) *syncEnv {
	return newSyncEnvWith(t, Options{UnsubscribeClient: ts.Client()})
}

// newsletter syncs a message with hdr (List-* lines) and returns its ID.
func (e *syncEnv) newsletter(t *testing.T, folder, id string, hdr ...string) int64 {
	t.Helper()
	e.put(t, folder, eml(id, "Shop <news@shop.example>", "alice@example.com", "Deals", unsubDate, "Hello.", hdr...))
	e.sync(t)
	return e.idOf(t, id)
}

func TestOneClickUnsubscribePostsOnce(t *testing.T) {
	ts := newUnsubServer(t)
	e := newUnsubEnv(t, ts)
	link := ts.URL + "/u?t=abc"
	id := e.newsletter(t, "INBOX", "n1@shop.example",
		"List-Unsubscribe: <mailto:unsub@shop.example?subject=unsubscribe>, <"+link+">",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click",
		"List-Id: Shop deals <deals.shop.example>")

	u, err := e.s.UnsubscribeMethod(bg, id)
	require.NoError(t, err)
	assert.Equal(t, Unsubscribe{Method: UnsubscribeOneClick, Host: "127.0.0.1", List: "Shop deals", URL: link}, u)

	res, err := e.s.Unsubscribe(bg, id)
	require.NoError(t, err)
	assert.Equal(t, UnsubscribeResult{Method: UnsubscribeOneClick, Host: "127.0.0.1"}, res)
	reqs := ts.requests()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, http.MethodPost, r.Method)
	assert.Equal(t, "/u?t=abc", r.URL.RequestURI())
	assert.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
	assert.Equal(t, "List-Unsubscribe=One-Click", ts.body[0])
	assert.Empty(t, r.Header.Get("Cookie"))
	assert.Empty(t, r.Header.Get("Referer"))
	assert.Empty(t, e.smtp.Messages())
}

func TestOneClickUnsubscribeIsAuditedByHostOnly(t *testing.T) {
	ts := newUnsubServer(t)
	e := newUnsubEnv(t, ts)
	id := e.newsletter(t, "INBOX", "n1@shop.example", "List-Unsubscribe: <"+ts.URL+"/u?token=secret>",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click")
	_, err := e.s.Unsubscribe(bg, id)
	require.NoError(t, err)
	assert.Equal(t, [][]any{{"user", "unsubscribe", "message", id, "127.0.0.1"}},
		e.rows(t, "SELECT source, tool, target_kind, target_id, destination FROM audit_log WHERE tool = 'unsubscribe'"))
}

func TestUnsubscribeWithoutExactOneClickIsWeb(t *testing.T) {
	for name, post := range map[string]string{
		"no post header": "",
		"other case":     "List-Unsubscribe-Post: list-unsubscribe=one-click",
		"other value":    "List-Unsubscribe-Post: List-Unsubscribe=One-Click, More",
		"other header":   "List-Unsubscribe-Posts: List-Unsubscribe=One-Click",
	} {
		t.Run(name, func(t *testing.T) {
			ts := newUnsubServer(t)
			e := newUnsubEnv(t, ts)
			hdr := []string{"List-Unsubscribe: <" + ts.URL + "/u>"}
			if post != "" {
				hdr = append(hdr, post)
			}
			id := e.newsletter(t, "INBOX", "n1@shop.example", hdr...)
			u, err := e.s.UnsubscribeMethod(bg, id)
			require.NoError(t, err)
			assert.Equal(t, Unsubscribe{Method: UnsubscribeWeb, Host: "127.0.0.1", URL: ts.URL + "/u"}, u)
			_, err = e.s.Unsubscribe(bg, id)
			assert.ErrorIs(t, err, ErrUnsubscribeWeb)
			assert.Empty(t, ts.requests())
		})
	}
}

func TestUnsubscribeWebPrefersHTTPS(t *testing.T) {
	e := newSyncEnv(t)
	id := e.newsletter(t, "INBOX", "n1@shop.example", "List-Unsubscribe: <http://shop.example/u>")
	u, err := e.s.UnsubscribeMethod(bg, id)
	require.NoError(t, err)
	assert.Equal(t, Unsubscribe{Method: UnsubscribeWeb, Host: "shop.example", URL: "http://shop.example/u"}, u)

	// An http link is never POSTed, One-Click or not.
	id = e.newsletter(t, "INBOX", "n2@shop.example", "List-Unsubscribe: <http://shop.example/u>, <https://www.shop.example/u>",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click")
	u, err = e.s.UnsubscribeMethod(bg, id)
	require.NoError(t, err)
	assert.Equal(t, UnsubscribeOneClick, u.Method)
	assert.Equal(t, "https://www.shop.example/u", u.URL)
	id = e.newsletter(t, "INBOX", "n3@shop.example", "List-Unsubscribe: <http://shop.example/u>, <https://www.shop.example/u>")
	u, err = e.s.UnsubscribeMethod(bg, id)
	require.NoError(t, err)
	assert.Equal(t, Unsubscribe{Method: UnsubscribeWeb, Host: "www.shop.example", URL: "https://www.shop.example/u"}, u)
	id = e.newsletter(t, "INBOX", "n4@shop.example", "List-Unsubscribe: <http://shop.example/u>", "List-Unsubscribe-Post: List-Unsubscribe=One-Click")
	_, err = e.s.Unsubscribe(bg, id)
	assert.ErrorIs(t, err, ErrUnsubscribeWeb)
}

func TestOneClickUnsubscribeFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		code    int
		failure string
	}{
		"found":              {http.StatusFound, UnsubscribeFailedRedirect},
		"temporary redirect": {http.StatusTemporaryRedirect, UnsubscribeFailedRedirect},
		"not found":          {http.StatusNotFound, UnsubscribeFailedStatus},
		"server error":       {http.StatusInternalServerError, UnsubscribeFailedStatus},
	} {
		t.Run(name, func(t *testing.T) {
			ts := newUnsubServer(t)
			ts.reply = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/u" {
					w.Header().Set("Location", "/landing")
					w.WriteHeader(tc.code)
				}
			}
			e := newUnsubEnv(t, ts)
			id := e.newsletter(t, "INBOX", "n1@shop.example", "List-Unsubscribe: <"+ts.URL+"/u>",
				"List-Unsubscribe-Post: List-Unsubscribe=One-Click")
			res, err := e.s.Unsubscribe(bg, id)
			require.NoError(t, err)
			assert.Equal(t, UnsubscribeResult{Method: UnsubscribeOneClick, Host: "127.0.0.1", Failure: tc.failure, Status: tc.code}, res)
			reqs := ts.requests()
			require.Len(t, reqs, 1, "the redirect is not followed")
			assert.Equal(t, "/u", reqs[0].URL.Path)
		})
	}
}

func TestOneClickUnsubscribeTimesOut(t *testing.T) {
	ts := newUnsubServer(t)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	ts.reply = func(http.ResponseWriter, *http.Request) { <-done }
	c := ts.Client()
	c.Timeout = 200 * time.Millisecond
	e := newSyncEnvWith(t, Options{UnsubscribeClient: c})
	id := e.newsletter(t, "INBOX", "n1@shop.example", "List-Unsubscribe: <"+ts.URL+"/u>", "List-Unsubscribe-Post: List-Unsubscribe=One-Click")
	res, err := e.s.Unsubscribe(bg, id)
	require.NoError(t, err)
	assert.Equal(t, UnsubscribeFailedTimeout, res.Failure)
}

func TestOneClickUnsubscribeConnectFails(t *testing.T) {
	ts := newUnsubServer(t)
	link := ts.URL + "/u"
	c := ts.Client()
	ts.Close()
	e := newSyncEnvWith(t, Options{UnsubscribeClient: c})
	id := e.newsletter(t, "INBOX", "n1@shop.example", "List-Unsubscribe: <"+link+">", "List-Unsubscribe-Post: List-Unsubscribe=One-Click")
	res, err := e.s.Unsubscribe(bg, id)
	require.NoError(t, err)
	assert.Equal(t, UnsubscribeFailedConnect, res.Failure)
}

// The default client dials as the image client does: public hosts only.
func TestOneClickUnsubscribeDefaultClientRefusesLoopback(t *testing.T) {
	ts := newUnsubServer(t)
	e := newSyncEnv(t)
	id := e.newsletter(t, "INBOX", "n1@shop.example", "List-Unsubscribe: <"+ts.URL+"/u>", "List-Unsubscribe-Post: List-Unsubscribe=One-Click")
	res, err := e.s.Unsubscribe(bg, id)
	require.NoError(t, err)
	assert.Equal(t, UnsubscribeFailedNotPublic, res.Failure)
	assert.Empty(t, ts.requests())
}

func TestOneClickUnsubscribeTwiceAtOnceSendsOnce(t *testing.T) {
	ts := newUnsubServer(t)
	arrived, release := make(chan struct{}), make(chan struct{})
	ts.reply = func(http.ResponseWriter, *http.Request) { close(arrived); <-release }
	e := newUnsubEnv(t, ts)
	id := e.newsletter(t, "INBOX", "n1@shop.example", "List-Unsubscribe: <"+ts.URL+"/u>", "List-Unsubscribe-Post: List-Unsubscribe=One-Click")
	first := make(chan error, 1)
	go func() { _, err := e.s.Unsubscribe(bg, id); first <- err }()
	<-arrived
	_, err := e.s.Unsubscribe(bg, id)
	assert.ErrorIs(t, err, ErrUnsubscribing)
	close(release)
	require.NoError(t, <-first)
	assert.Len(t, ts.requests(), 1)
}

func TestMailtoUnsubscribeMakesDraftNeverSent(t *testing.T) {
	e := newSyncEnv(t)
	id := e.newsletter(t, "INBOX", "n1@lists.example",
		"List-Unsubscribe: <mailto:leave+news@lists.example?cc=owner@lists.example&subject=unsubscribe%20me+now&body=%E9%80%80%E8%AE%A2%0D%0Aa+b%25>",
		"List-Id: <news.lists.example>")
	u, err := e.s.UnsubscribeMethod(bg, id)
	require.NoError(t, err)
	assert.Equal(t, UnsubscribeMail, u.Method)
	assert.Equal(t, "lists.example", u.Host)
	assert.Equal(t, "news.lists.example", u.List)

	res, err := e.s.Unsubscribe(bg, id)
	require.NoError(t, err)
	require.NotZero(t, res.DraftID)
	d, err := e.s.GetDraft(bg, res.DraftID)
	require.NoError(t, err)
	assert.Equal(t, e.acc.ID, d.AccountID)
	assert.Equal(t, []string{"leave+news@lists.example"}, d.To)
	assert.Equal(t, []string{"owner@lists.example"}, d.Cc)
	assert.Equal(t, "unsubscribe me+now", d.Subject)
	assert.Contains(t, d.HTML, "<p>退订<br/>a+b%</p>")
	assert.Empty(t, e.smtp.Messages())
}

func TestNoUnsubscribe(t *testing.T) {
	e := newSyncEnv(t)
	require.NoError(t, e.im.User.Create("Junk", nil))
	long := "List-Unsubscribe: <https://shop.example/u?t=" + strings.Repeat("a", 2100) + ">"
	for _, c := range []struct{ folder, id, hdr string }{
		{"INBOX", "plain@x", ""},
		{"INBOX", "long@x", long},
		{"Junk", "junk@x", "List-Unsubscribe: <mailto:u@shop.example>"},
		{"INBOX", "nobrackets@x", "List-Unsubscribe: https://shop.example/u"},
	} {
		var hdr []string
		if c.hdr != "" {
			hdr = append(hdr, c.hdr)
		}
		e.put(t, c.folder, eml(c.id, "news@shop.example", "alice@example.com", "Deals", unsubDate,
			"Click https://shop.example/unsubscribe to unsubscribe.", hdr...))
	}
	e.sync(t)
	for _, id := range []string{"plain@x", "long@x", "junk@x", "nobrackets@x"} {
		u, err := e.s.UnsubscribeMethod(bg, e.idOf(t, id))
		require.NoError(t, err, id)
		assert.Equal(t, Unsubscribe{}, u, id)
		_, err = e.s.Unsubscribe(bg, e.idOf(t, id))
		assert.ErrorIs(t, err, ErrNoUnsubscribe, id)
	}
}

// A message synced before the List-* headers were stored has them fetched
// once, when asked; one gone from the server has none, without an error.
func TestUnsubscribeFetchesHeadersOfOlderMail(t *testing.T) {
	e := newSyncEnv(t)
	old := e.newsletter(t, "INBOX", "old@shop.example", "List-Unsubscribe: <https://shop.example/u>",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click", "List-Id: \"Shop\" <shop.example>")
	_, err := e.s.db.Execute(bg, "UPDATE messages SET list_unsubscribe = NULL, list_unsubscribe_post = NULL, list_id = NULL")
	require.NoError(t, err)
	want := Unsubscribe{Method: UnsubscribeOneClick, Host: "shop.example", List: "Shop", URL: "https://shop.example/u"}
	u, err := e.s.UnsubscribeMethod(bg, old)
	require.NoError(t, err)
	assert.Equal(t, want, u)

	e.expungeAll(t, "INBOX")
	u, err = e.s.UnsubscribeMethod(bg, old)
	require.NoError(t, err)
	assert.Equal(t, want, u, "stored: the server is not asked again")

	gone := e.newsletter(t, "INBOX", "gone@shop.example", "List-Unsubscribe: <https://shop.example/u>")
	_, err = e.s.db.Execute(bg, "UPDATE messages SET list_unsubscribe = NULL, list_unsubscribe_post = NULL, list_id = NULL WHERE id = ?", gone)
	require.NoError(t, err)
	e.expungeAll(t, "INBOX")
	u, err = e.s.UnsubscribeMethod(bg, gone)
	require.NoError(t, err)
	assert.Equal(t, Unsubscribe{}, u)
}

// expungeAll deletes folder's messages on the server only.
func (e *syncEnv) expungeAll(t *testing.T, folder string) {
	t.Helper()
	c := e.dial(t)
	_, err := c.Select(folder, nil).Wait()
	require.NoError(t, err)
	var all imap.SeqSet
	all.AddRange(1, 0) // 1:*
	require.NoError(t, c.Store(all, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close())
	require.NoError(t, c.Expunge().Close())
}
