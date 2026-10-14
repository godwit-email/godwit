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

// ClientID is the only client the server knows unless Dynamic(true).
const (
	ClientID        = "godwit-test"
	DynamicClientID = "godwit-test-dynamic"
	IssuedClientID  = "godwit-test-issued"
)

type grant struct{ challenge, redirect, email string }

type canned struct {
	status int
	body   string
}

// Server issues codes, tokens and unsigned id_tokens. Refresh tokens do not rotate
// unless RotateRefresh(true).
type Server struct {
	AuthURL, DeviceURL, TokenURL string

	mu                 sync.Mutex
	email              string           // SetEmail override for id_tokens; "" = the signed-in email
	deny               bool             // the user refuses consent
	next               *canned          // RespondNext: the next token response
	hang               chan struct{}    // Hang: token requests wait until it closes
	rotate             bool             // refreshes issue a new refresh token
	noExpiry           bool             // token responses omit expires_in
	dynamic            bool             // Dynamic: accept DynamicClientID and issue IssuedClientID
	reqParam, reqValue string           // Require: extra parameter required at /authorize, /device/code and /token
	scope              string           // GrantScope: scope field in token responses
	deviceCode         string           // active device_code issued by /device/code
	deviceEmail        string           // Approve: email for device-code grant
	deviceDenied       bool             // DenyDevice
	deviceExpired      bool             // DeviceExpired
	slowDown           bool             // SlowDownOnce
	codes              map[string]grant // code -> pending exchange
	refresh            map[string]string
	access             map[string]bool
	tokenRequests      int
	auths, noS256      int // authorization requests; those without S256
	exchanges          int // code exchanges
	badVerifier        int // exchanges whose code_verifier did not match
}

// Start runs a Server until the test ends.
func Start(t *testing.T) *Server {
	t.Helper()
	s := &Server{codes: map[string]grant{}, refresh: map[string]string{}, access: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", s.authorize)
	mux.HandleFunc("POST /device/code", s.device)
	mux.HandleFunc("POST /token", s.token)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	s.AuthURL, s.DeviceURL, s.TokenURL = hs.URL+"/authorize", hs.URL+"/device/code", hs.URL+"/token"
	return s
}

// Config is an OAuthConfig for this server.
func (s *Server) Config(scopes ...string) auth.OAuthConfig {
	return auth.OAuthConfig{ClientID: ClientID, AuthURL: s.AuthURL, TokenURL: s.TokenURL, Scopes: scopes}
}

// DeviceConfig is an OAuthConfig for this server's device-code endpoint.
func (s *Server) DeviceConfig(scopes ...string) auth.OAuthConfig {
	return auth.OAuthConfig{ClientID: ClientID, DeviceURL: s.DeviceURL, TokenURL: s.TokenURL, Scopes: scopes}
}

// Approve makes the device-code flow succeed as email on its next poll.
func (s *Server) Approve(email string) { s.mu.Lock(); s.deviceEmail = email; s.mu.Unlock() }

// DenyDevice makes the device-code flow answer access_denied.
func (s *Server) DenyDevice() { s.mu.Lock(); s.deviceDenied = true; s.mu.Unlock() }

// SlowDownOnce makes the next device-code poll answer slow_down.
func (s *Server) SlowDownOnce() { s.mu.Lock(); s.slowDown = true; s.mu.Unlock() }

// DeviceExpired makes the device-code flow answer expired_token.
func (s *Server) DeviceExpired() { s.mu.Lock(); s.deviceExpired = true; s.mu.Unlock() }

// SetEmail makes later id_tokens name email whoever signed in; "" undoes it.
func (s *Server) SetEmail(email string) { s.mu.Lock(); s.email = email; s.mu.Unlock() }

// Deny makes the user refuse consent: the server redirects with error=access_denied.
func (s *Server) Deny(deny bool) { s.mu.Lock(); s.deny = deny; s.mu.Unlock() }

// Dynamic makes the server accept DynamicClientID at /authorize, return
// client_id=IssuedClientID on the callback, and accept IssuedClientID at /token.
func (s *Server) Dynamic(on bool) { s.mu.Lock(); s.dynamic = on; s.mu.Unlock() }

// Require makes /authorize and /token reject requests missing param=value.
func (s *Server) Require(param, value string) {
	s.mu.Lock()
	s.reqParam, s.reqValue = param, value
	s.mu.Unlock()
}

// GrantScope sets the scope string returned in token responses when non-empty.
func (s *Server) GrantScope(scope string) { s.mu.Lock(); s.scope = scope; s.mu.Unlock() }

// RespondNext makes the token endpoint answer its next request with status and
// a JSON body, whatever was asked.
func (s *Server) RespondNext(status int, body string) {
	s.mu.Lock()
	s.next = &canned{status, body}
	s.mu.Unlock()
}

// Hang makes token requests wait, without an answer, until release is called
// or the client goes away. release also runs when the test ends.
func (s *Server) Hang(t *testing.T) (release func()) {
	c := make(chan struct{})
	s.mu.Lock()
	s.hang = c
	s.mu.Unlock()
	var once sync.Once
	release = func() {
		once.Do(func() {
			s.mu.Lock()
			s.hang = nil
			s.mu.Unlock()
			close(c)
		})
	}
	t.Cleanup(release) // before Start's cleanup closes the server, which waits for requests
	return release
}

// RotateRefresh makes each refresh issue a new refresh token. The old one stays
// valid, as Microsoft's do until they expire.
func (s *Server) RotateRefresh(on bool) { s.mu.Lock(); s.rotate = on; s.mu.Unlock() }

// RevokeAll invalidates every refresh and access token issued so far.
func (s *Server) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.refresh)
	clear(s.access)
}

// OmitExpiry makes token responses leave out expires_in.
func (s *Server) OmitExpiry(on bool) { s.mu.Lock(); s.noExpiry = on; s.mu.Unlock() }

// RevokeAccess invalidates every access token issued so far; refresh tokens still work.
func (s *Server) RevokeAccess() { s.mu.Lock(); clear(s.access); s.mu.Unlock() }

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
	s.mu.Lock()
	defer s.mu.Unlock()
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	cid := q.Get("client_id")
	okClient := cid == ClientID || (s.dynamic && (cid == DynamicClientID || cid == IssuedClientID))
	host := ""
	if err == nil {
		host = redirect.Hostname()
	}
	if err != nil || !okClient || q.Get("response_type") != "code" ||
		redirect.Scheme != "http" || (host != "localhost" && host != "127.0.0.1") || q.Get("code_challenge") == "" ||
		(s.reqParam != "" && q.Get(s.reqParam) != s.reqValue) {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
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
		if s.dynamic {
			back.Set("client_id", IssuedClientID)
		}
	}
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) device(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ParseForm() != nil || r.PostForm.Get("client_id") != ClientID ||
		(s.reqParam != "" && r.PostForm.Get(s.reqParam) != s.reqValue) {
		oauthError(w, "invalid_request")
		return
	}
	dc, uc := random(), random()[:8]
	s.deviceCode = dc
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"device_code":               dc,
		"user_code":                 uc,
		"verification_uri":          s.AuthURL,
		"verification_uri_complete": s.AuthURL + "?user_code=" + uc,
		"interval":                  1,
		"expires_in":                30, // room for a slow_down, which no longer extends it
	})
}

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	hang := s.hang
	s.mu.Unlock()
	if hang != nil {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}
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
	if r.ParseForm() != nil {
		oauthError(w, "invalid_request")
		return
	}
	cid := r.PostForm.Get("client_id")
	if cid != ClientID && (!s.dynamic || cid != IssuedClientID) {
		oauthError(w, "invalid_client")
		return
	}
	gt := r.PostForm.Get("grant_type")
	if gt != deviceGrantType && s.reqParam != "" && r.PostForm.Get(s.reqParam) != s.reqValue {
		oauthError(w, "invalid_request")
		return
	}
	var email string
	switch gt {
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
		rt = random()
		s.refresh[rt] = email
		s.issue(w, email, rt)
	case deviceGrantType:
		if s.deviceCode == "" || r.PostForm.Get("device_code") != s.deviceCode {
			oauthError(w, "invalid_grant")
			return
		}
		if s.slowDown {
			s.slowDown = false
			oauthError(w, "slow_down")
			return
		}
		if s.deviceDenied {
			oauthError(w, "access_denied")
			return
		}
		if s.deviceExpired {
			oauthError(w, "expired_token")
			return
		}
		if s.deviceEmail == "" {
			oauthError(w, "authorization_pending")
			return
		}
		email = s.deviceEmail
		s.deviceCode, s.deviceEmail = "", ""
		rt := random()
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
	if s.scope != "" {
		body["scope"] = s.scope
	}
	if s.noExpiry {
		delete(body, "expires_in")
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
