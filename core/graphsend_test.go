package core

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// graphEnv is an "outlook" whose SMTP refuses AUTH for the mailbox (5.7.139)
// and a stand-in for Graph's sendMail that takes the testoauth server's tokens.
type graphEnv struct {
	*oauthAccEnv
	mu   sync.Mutex
	sent []string // the MIME Graph got
}

func newGraphEnv(t *testing.T) *graphEnv {
	e := &graphEnv{oauthAccEnv: newOAuthEnv(t, false)}
	setFor(t, &oauthConfig, func(string) (auth.OAuthConfig, error) {
		c := e.oa.Config("openid", "email", "offline_access", "https://graph.example/Mail.Send")
		c.GraphSend = "https://graph.example/Mail.Send"
		return c, nil
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !e.oa.ValidAccess(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		b, _ := io.ReadAll(r.Body)
		raw, err := base64.StdEncoding.DecodeString(string(b))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		e.mu.Lock()
		e.sent = append(e.sent, string(raw))
		e.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	setFor(t, &graphSendURL, srv.URL+"/v1.0/me/sendMail")
	e.smtp.DisableAuth(true)
	return e
}

func (e *graphEnv) graphSent() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.sent...)
}

func TestSendThroughGraphWhenSMTPAuthDisabled(t *testing.T) {
	e := newGraphEnv(t)
	e.oa.GrantScope("Mail.Send offline_access") // Microsoft names Graph scopes relative to the resource
	in := e.input(t, "alice@example.com", e.signIn(t, "alice@example.com"))
	r, err := e.s.CheckAccount(bg, in)
	require.NoError(t, err)
	require.True(t, r.OK(), "a Graph token stands in for the SMTP login: %+v", r.Steps)
	e.add(t)

	d, err := e.s.NewDraft(bg, e.acc.ID, "new", 0)
	require.NoError(t, err)
	d.To, d.Bcc, d.Subject, d.HTML = []string{"bob@example.com"}, []string{"carol@example.com"}, "hello", "<p>hi</p>"
	d, err = e.s.SaveDraft(bg, d)
	require.NoError(t, err)
	require.NoError(t, e.s.Send(bg, d.ID))
	e.clock.Advance(UndoSendWindow)
	e.sync(t)

	got := e.graphSent()
	require.Len(t, got, 1)
	assert.True(t, strings.HasPrefix(got[0], "Bcc: <carol@example.com>\r\n"), "Graph takes Bcc from the headers")
	assert.Empty(t, e.smtp.Messages())
	assert.Equal(t, "done", e.state(t))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM accounts WHERE send_protocol = 'graph'"), "stored: a restart sends through Graph too")

	auths := e.smtp.AuthAttempts()
	e.sendNew(t)
	e.sync(t)
	assert.Len(t, e.graphSent(), 2)
	assert.Equal(t, auths, e.smtp.AuthAttempts(), "the account now sends through Graph without trying SMTP")
}

func TestGraphWithoutConsentPausesOutbox(t *testing.T) {
	e := newGraphEnv(t)
	e.add(t)
	e.oa.GrantScope("openid email") // a grant from before Mail.Send was asked for
	e.sendNew(t)
	assert.ErrorIs(t, e.s.SyncNow(bg, e.acc.ID), backend.ErrAuthFailed)
	assert.Empty(t, e.graphSent())
	assert.Equal(t, "queued", e.state(t))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM accounts WHERE smtp_reauth = 1"))
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM accounts WHERE status = 'ok'"), "IMAP's status is untouched")
	a, err := e.s.account(bg, e.acc.ID)
	require.NoError(t, err)
	assert.Equal(t, "auth_rejected", a.StatusDetail, "the user is asked to sign in again, which asks for Mail.Send")
}

func TestCheckFailsWithoutGraphConsent(t *testing.T) {
	e := newGraphEnv(t)
	e.oa.GrantScope("openid email")
	r, err := e.s.CheckAccount(bg, e.input(t, "alice@example.com", e.signIn(t, "alice@example.com")))
	require.NoError(t, err)
	assert.False(t, r.OK())
	assert.Equal(t, conn.FailureAuth, r.Steps[2].Kind)
}

func TestGrantsScope(t *testing.T) {
	const want = "https://graph.microsoft.com/Mail.Send"
	assert.True(t, grantsScope("Mail.Send User.Read", want))
	assert.True(t, grantsScope("https://graph.microsoft.com/Mail.Send", want))
	assert.True(t, grantsScope("openid mail.send", want))
	assert.False(t, grantsScope("openid email https://outlook.office.com/SMTP.Send", want))
	assert.False(t, grantsScope("Mail.Read", want))
}

// New server settings undo the fallback: the next send tries SMTP again.
func TestServerChangeRetriesSMTP(t *testing.T) {
	e := newGraphEnv(t)
	e.add(t)
	e.sendNew(t)
	e.sync(t)
	require.Len(t, e.graphSent(), 1)

	e.smtp.DisableAuth(false)
	srv := e.acc.servers()
	_, err := e.s.UpdateAccount(bg, e.acc.ID, AccountPatch{Servers: &srv})
	require.NoError(t, err)
	assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM accounts WHERE send_protocol = 'smtp'"))
	e.sendNew(t)
	e.sync(t)
	assert.Len(t, e.graphSent(), 1)
	assert.Len(t, e.smtp.Messages(), 1)
}
