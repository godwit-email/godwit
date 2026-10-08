package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/oauth2"
)

// FlowTimeout bounds an authorization flow from StartFlow to the callback.
var FlowTimeout = 10 * time.Minute

// Grant is the outcome of an authorization or a refresh. Secrets: never log it.
// Expiry is never zero: without expires_in it is 30 minutes after receipt.
type Grant struct {
	Email, RefreshToken, AccessToken string
	Expiry                           time.Time
}

// Flow is one authorization-code flow with PKCE, waiting for the browser on a
// loopback listener.
type Flow struct {
	cfg      oauth2.Config
	hc       *http.Client
	verifier string
	state    string
	authURL  string
	ln       net.Listener
	srv      *http.Server
	ctx      context.Context // ends on Close or after FlowTimeout; bounds the token exchange
	cancel   context.CancelFunc
	claimed  atomic.Bool // a callback with the right state arrived

	finishOnce sync.Once
	done       chan struct{}
	grant      Grant
	err        error

	closeOnce sync.Once
}

func config(cfg OAuthConfig) oauth2.Config {
	return oauth2.Config{ClientID: cfg.ClientID, Scopes: cfg.Scopes,
		Endpoint: oauth2.Endpoint{AuthURL: cfg.AuthURL, TokenURL: cfg.TokenURL, AuthStyle: oauth2.AuthStyleInParams}}
}

func withClient(ctx context.Context, hc *http.Client) context.Context {
	if hc == nil {
		hc = http.DefaultClient
	}
	return context.WithValue(ctx, oauth2.HTTPClient, hc)
}

// StartFlow listens on 127.0.0.1 for the redirect and builds the authorization
// URL. The listener closes when the flow ends: callback handled, Wait's ctx
// done, FlowTimeout passed or Close.
func StartFlow(cfg OAuthConfig, loginHint string, hc *http.Client) (*Flow, error) {
	if cfg.ClientID == "" {
		return nil, ErrOAuthNotConfigured
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	state := make([]byte, 32)
	rand.Read(state)
	f := &Flow{cfg: config(cfg), hc: hc, verifier: oauth2.GenerateVerifier(),
		state: base64.RawURLEncoding.EncodeToString(state), done: make(chan struct{})}
	f.cfg.RedirectURL = fmt.Sprintf("http://localhost:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(f.verifier), oauth2.SetAuthURLParam("prompt", "select_account")}
	if loginHint != "" {
		opts = append(opts, oauth2.SetAuthURLParam("login_hint", loginHint))
	}
	f.authURL = f.cfg.AuthCodeURL(f.state, opts...)
	f.ctx, f.cancel = context.WithTimeout(context.Background(), FlowTimeout)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", f.callback)
	f.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	f.ln = ln
	go f.srv.Serve(ln)
	// Registered last: it may run Close, which reads every field.
	context.AfterFunc(f.ctx, func() { f.finish(Grant{}, ErrFlowExpired); f.Close() })
	return f, nil
}

// AuthURL is the address to open in the user's browser.
func (f *Flow) AuthURL() string { return f.authURL }

// Wait returns the grant once the browser came back and the code was exchanged,
// or the flow's error. The listener is closed when it returns.
func (f *Flow) Wait(ctx context.Context) (Grant, error) {
	select {
	case <-f.done:
	case <-ctx.Done():
		f.finish(Grant{}, ctx.Err())
	}
	f.Close()
	return f.grant, f.err
}

// Close ends the flow (a pending Wait returns context.Canceled) and releases the listener.
func (f *Flow) Close() {
	f.finish(Grant{}, context.Canceled)
	f.closeOnce.Do(func() {
		f.cancel()
		f.ln.Close() // now, even if Serve has not started yet
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if f.srv.Shutdown(ctx) != nil { // the listener is closed either way; this only waits for the page
			f.srv.Close()
		}
	})
}

func (f *Flow) finish(g Grant, err error) {
	f.finishOnce.Do(func() { f.grant, f.err = g, err; close(f.done) })
}

const (
	pageOK   = `<!doctype html><html lang="zh"><meta charset="utf-8"><title>Godwit</title><p>登录完成，可以关闭此页面回到 Godwit。</p></html>`
	pageFail = `<!doctype html><html lang="zh"><meta charset="utf-8"><title>Godwit</title><p>登录没有完成，请回到 Godwit 重试。</p></html>`
)

// callback answers the browser with a fixed page that echoes nothing. A request
// without the flow's state is refused and the flow keeps waiting.
func (f *Flow) callback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet { // HEAD too: a prefetch must not spend the code
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(f.state)) != 1 || !f.claimed.CompareAndSwap(false, true) {
		page(w, false)
		return
	}
	g, err := f.exchange(q)
	page(w, err == nil)
	f.finish(g, err)
	go f.Close() // release the port now, Wait or not; Close waits for this handler to return
}

func page(w http.ResponseWriter, ok bool) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	if ok {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, pageOK)
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprint(w, pageFail)
}

func (f *Flow) exchange(q url.Values) (Grant, error) {
	switch e := q.Get("error"); e {
	case "":
	case "access_denied":
		return Grant{}, ErrAccessDenied
	default:
		return Grant{}, fmt.Errorf("oauth: authorization refused: %s", errorCode(e))
	}
	code := q.Get("code")
	if code == "" {
		return Grant{}, errors.New("oauth: the callback carried no authorization code")
	}
	tok, err := f.cfg.Exchange(withClient(f.ctx, f.hc), code, oauth2.VerifierOption(f.verifier))
	if err != nil {
		return Grant{}, tokenError(err)
	}
	g := grantFrom(tok)
	if g.Email == "" {
		return Grant{}, errors.New("oauth: the token response named no email address")
	}
	return g, nil
}

// Refresh trades refreshToken for a new access token. Grant.RefreshToken is ""
// unless the server rotated it; Grant.Email is "" without an id_token.
func Refresh(ctx context.Context, cfg OAuthConfig, hc *http.Client, refreshToken string) (Grant, error) {
	if cfg.ClientID == "" {
		return Grant{}, ErrOAuthNotConfigured
	}
	c := config(cfg)
	tok, err := c.TokenSource(withClient(ctx, hc), &oauth2.Token{RefreshToken: refreshToken}).Token()
	if err != nil {
		return Grant{}, tokenError(err)
	}
	g := grantFrom(tok)
	if g.RefreshToken == refreshToken { // x/oauth2 fills in the old one when none came back
		g.RefreshToken = ""
	}
	return g, nil
}

// noExpiryTTL is how long an access token whose response gave no expires_in is
// trusted, counted from its receipt.
const noExpiryTTL = 30 * time.Minute

func grantFrom(tok *oauth2.Token) Grant {
	g := Grant{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, Expiry: tok.Expiry}
	if g.Expiry.IsZero() {
		g.Expiry = time.Now().Add(noExpiryTTL)
	}
	if id, ok := tok.Extra("id_token").(string); ok {
		g.Email = emailFromIDToken(id)
	}
	return g
}

// emailFromIDToken reads email, else preferred_username, from the JWT payload.
// The token came straight from the token endpoint over TLS, so it is not verified.
func emailFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var c struct {
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
	}
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	if c.Email == "" {
		c.Email = c.PreferredUsername
	}
	return strings.ToLower(strings.TrimSpace(c.Email))
}

// tokenMessages are the fixed texts for token endpoint error codes. Nothing from
// the response body reaches an error: it may echo the code, a token or the verifier.
var tokenMessages = map[string]string{
	"invalid_client":      "oauth: the token endpoint does not accept this client (invalid_client)",
	"invalid_request":     "oauth: the token endpoint rejected the request (invalid_request)",
	"unauthorized_client": "oauth: this client may not use this grant (unauthorized_client)",
	"invalid_scope":       "oauth: the token endpoint rejected the scopes (invalid_scope)",
}

var errBadTokenResponse = errors.New("oauth: the token endpoint returned an invalid response")

// tokenError maps x/oauth2's errors, which quote the response body, to fixed ones.
// Context cancellation and deadlines still match with errors.Is.
func tokenError(err error) error {
	var re *oauth2.RetrieveError
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("oauth: token request: %w", context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("oauth: token request: %w", context.DeadlineExceeded)
	case errors.As(err, &re):
		switch re.ErrorCode {
		case "invalid_grant":
			return ErrInvalidGrant
		case "access_denied":
			return ErrAccessDenied
		}
		if m, ok := tokenMessages[re.ErrorCode]; ok {
			return errors.New(m)
		}
		return errBadTokenResponse
	}
	var ue *url.Error
	if errors.As(err, &ue) { // transport failure: the request never got an answer
		return errors.New("oauth: could not reach the token endpoint")
	}
	return errBadTokenResponse
}

// errorCode keeps an OAuth error code printable and short: it comes from the network.
func errorCode(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	return s[:min(len(s), 64)]
}
