// Package testoauth is a fake OAuth 2.0 authorization and token server for tests,
// with a stand-in for the user's browser.
package testoauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core/auth"
)

// ClientID is the only client the server knows.
const ClientID = "godwit-test"

type grant struct{ challenge, redirect, email string }

type canned struct {
	status int
	body   string
}

// Server issues codes, tokens and unsigned id_tokens. Refresh tokens do not rotate
// unless RotateRefresh(true).
type Server struct {
	AuthURL, TokenURL string

	mu            sync.Mutex
	email         string           // SetEmail override for id_tokens; "" = the signed-in email
	deny          bool             // the user refuses consent
	next          *canned          // RespondNext: the next token response
	rotate        bool             // refreshes issue a new refresh token
	codes         map[string]grant // code -> pending exchange
	refresh       map[string]string
	access        map[string]bool
	tokenRequests int
	auths, noS256 int // authorization requests; those without S256
	exchanges     int // code exchanges
	badVerifier   int // exchanges whose code_verifier did not match
}

// Start runs a Server until the test ends.
func Start(t *testing.T) *Server {
	t.Helper()
	s := &Server{codes: map[string]grant{}, refresh: map[string]string{}, access: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", s.authorize)
	mux.HandleFunc("POST /token", s.token)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	s.AuthURL, s.TokenURL = hs.URL+"/authorize", hs.URL+"/token"
	return s
}

// Config is an OAuthConfig for this server.
func (s *Server) Config(scopes ...string) auth.OAuthConfig {
	return auth.OAuthConfig{ClientID: ClientID, AuthURL: s.AuthURL, TokenURL: s.TokenURL, Scopes: scopes}
}

// SetEmail makes later id_tokens name email whoever signed in; "" undoes it.
func (s *Server) SetEmail(email string) { s.mu.Lock(); s.email = email; s.mu.Unlock() }

// Deny makes the user refuse consent: the server redirects with error=access_denied.
func (s *Server) Deny(deny bool) { s.mu.Lock(); s.deny = deny; s.mu.Unlock() }

// RespondNext makes the token endpoint answer its next request with status and
// a JSON body, whatever was asked.
func (s *Server) RespondNext(status int, body string) {
	s.mu.Lock()
	s.next = &canned{status, body}
	s.mu.Unlock()
}

// RotateRefresh makes each refresh replace the refresh token.
func (s *Server) RotateRefresh(on bool) { s.mu.Lock(); s.rotate = on; s.mu.Unlock() }

// RevokeAll invalidates every refresh and access token issued so far.
func (s *Server) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.refresh)
	clear(s.access)
}

// TokenRequests is how many requests the token endpoint has received.
func (s *Server) TokenRequests() int { s.mu.Lock(); defer s.mu.Unlock(); return s.tokenRequests }

// ValidAccess reports whether token is a live access token.
func (s *Server) ValidAccess(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.access[token]
}

// S256Requested reports whether every authorization request so far asked for
// code_challenge_method=S256 (false before the first).
func (s *Server) S256Requested() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auths > 0 && s.noS256 == 0
}

// VerifierChecked reports whether every code exchange so far sent a code_verifier
// matching its challenge (false before the first).
func (s *Server) VerifierChecked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exchanges > 0 && s.badVerifier == 0
}

// Callback is the browser's visit to the app's loopback redirect.
type Callback struct {
	URL    string
	Status int
	Body   string
}

// Browse plays the user's browser: it opens authURL, signs in as email and
// follows the server's redirect to the loopback address.
func (s *Server) Browse(t *testing.T, authURL, email string) Callback {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("test_email", email) // stands in for the login form
	u.RawQuery = q.Encode()
	hc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := hc.Get(u.String())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound || (loc.Hostname() != "localhost" && loc.Hostname() != "127.0.0.1") {
		t.Fatalf("authorization answered %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return Get(t, loc.String())
}

// Get visits a loopback URL as the browser would.
func Get(t *testing.T, u string) Callback {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return Callback{URL: u, Status: resp.StatusCode, Body: string(b)}
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || q.Get("client_id") != ClientID || q.Get("response_type") != "code" ||
		redirect.Scheme != "http" || redirect.Hostname() != "localhost" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auths++
	if q.Get("code_challenge_method") != "S256" {
		s.noS256++
	}
	back := url.Values{"state": {q.Get("state")}}
	if s.deny {
		back.Set("error", "access_denied")
	} else {
		code := random()
		s.codes[code] = grant{challenge: q.Get("code_challenge"), redirect: redirect.String(), email: q.Get("test_email")}
		back.Set("code", code)
	}
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenRequests++
	if c := s.next; c != nil {
		s.next = nil
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(c.status)
		io.WriteString(w, c.body)
		return
	}
	if r.ParseForm() != nil || r.PostForm.Get("client_id") != ClientID {
		oauthError(w, "invalid_client")
		return
	}
	var email string
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		g, ok := s.codes[r.PostForm.Get("code")]
		delete(s.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		ok = ok && g.redirect == r.PostForm.Get("redirect_uri")
		match := base64.RawURLEncoding.EncodeToString(sum[:]) == g.challenge
		s.exchanges++
		if !match {
			s.badVerifier++
		}
		if !ok || !match {
			oauthError(w, "invalid_grant")
			return
		}
		email = g.email
		rt := random()
		s.refresh[rt] = email
		s.issue(w, email, rt)
	case "refresh_token":
		rt := r.PostForm.Get("refresh_token")
		email, ok := s.refresh[rt]
		if !ok {
			oauthError(w, "invalid_grant")
			return
		}
		if !s.rotate {
			s.issue(w, email, "")
			return
		}
		delete(s.refresh, rt)
		rt = random()
		s.refresh[rt] = email
		s.issue(w, email, rt)
	default:
		oauthError(w, "unsupported_grant_type")
	}
}

// issue answers with a new access token, refreshToken if not "" and an id_token.
func (s *Server) issue(w http.ResponseWriter, email, refreshToken string) {
	if s.email != "" {
		email = s.email
	}
	at := random()
	s.access[at] = true
	claims, _ := json.Marshal(map[string]string{"email": email, "preferred_username": email})
	enc := base64.RawURLEncoding.EncodeToString
	body := map[string]any{"access_token": at, "token_type": "Bearer", "expires_in": 3600,
		"id_token": enc([]byte(`{"alg":"none"}`)) + "." + enc(claims) + "."}
	if refreshToken != "" {
		body["refresh_token"] = refreshToken
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}

func oauthError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func random() string { return rand.Text() }
