package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend/conn"
	"github.com/godwit-email/godwit/core/internal/testmail"
	"github.com/godwit-email/godwit/internal/imagecache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

type avatarFixture struct {
	s        *Service
	mu       sync.Mutex
	requests []string
}

func avatarPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 64, 64))))
	return b.Bytes()
}

func newAvatarFixture(t *testing.T, handler http.HandlerFunc) *avatarFixture {
	t.Helper()
	f := &avatarFixture{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Host+r.URL.RequestURI())
		f.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig.ServerName = u.Hostname()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, u.Host)
	}
	t.Cleanup(tr.CloseIdleConnections)
	keyring.MockInit()
	f.s, err = New(t.Context(), Options{DataDir: t.TempDir(), ImageClient: &http.Client{Transport: tr}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.s.Close()) })
	_, err = f.s.db.Execute(t.Context(), "INSERT INTO accounts(id, email) VALUES (1, 'me@example.com')")
	require.NoError(t, err)
	_, err = f.s.db.Execute(t.Context(), "INSERT INTO folders(id, account_id, name, role) VALUES (1, 1, 'INBOX', 'inbox')")
	require.NoError(t, err)
	return f
}

func (f *avatarFixture) hits() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *avatarFixture) policy(t *testing.T, mode string, trusted bool, junk bool) {
	t.Helper()
	st, err := f.s.GetSettings(t.Context())
	require.NoError(t, err)
	st.RemoteImages = mode
	_, err = f.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	if trusted {
		require.NoError(t, f.s.TrustImageSender(t.Context(), "bob@example.com"))
	} else {
		require.NoError(t, f.s.UntrustImageSender(t.Context(), "bob@example.com"))
	}
	role := "inbox"
	if junk {
		role = "junk"
	}
	_, err = f.s.db.Execute(t.Context(), "UPDATE folders SET role = ? WHERE id = 1", role)
	require.NoError(t, err)
}

func TestSenderAvatarSources(t *testing.T) {
	good := avatarPNG(t)
	for _, tc := range []struct {
		name, from, success, source string
		want                        []string
	}{
		{"favicon stops", "Bob <bob@example.com>", "example.com", "favicon", []string{"example.com/apple-touch-icon.png"}},
		{"provider skips favicon", "Bob <bob@VIP.QQ.COM>", "gravatar.com", "gravatar", []string{"gravatar.com/avatar/9a769de62220aee23929c7dd335380771141197fd8918fecc2daedc772c8198c?s=64&d=404&r=g"}},
		{"original before organization", "bob@mail.example.com", "example.com", "favicon", []string{"mail.example.com/apple-touch-icon.png", "mail.example.com/", "mail.example.com/favicon.ico", "example.com/apple-touch-icon.png"}},
		{"both sites miss then gravatar", "bob@mail.example.com", "gravatar.com", "gravatar", []string{"mail.example.com/apple-touch-icon.png", "mail.example.com/", "mail.example.com/favicon.ico", "example.com/apple-touch-icon.png", "example.com/", "example.com/favicon.ico", "gravatar.com/avatar/652ca155ec68c373734da5a2adff1d89cc59ec6fa6bc32fd1b6b959e08c202ff?s=64&d=404&r=g"}},
		{"404 is empty", "bob@gmail.com", "", "", []string{"gravatar.com/avatar/300db02db75af922c759f08a0efa5a400128eac1f531b802697450b5c6da6e9f?s=64&d=404&r=g"}},
		{"bad address", "invalid address", "", "", nil},
		{"local domain", "bob@localhost", "", "", nil},
		{"IP domain", "bob@[127.0.0.1]", "", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Host == tc.success {
					w.Write(good)
				} else {
					http.NotFound(w, r)
				}
			})
			id := addMessage(t, f.s, "s", tc.from, "plain", "", 1)
			got, err := f.s.SenderAvatar(t.Context(), id, false)
			require.NoError(t, err)
			assert.Equal(t, tc.source, got.Source)
			if tc.source != "" {
				assert.Equal(t, good, got.Data)
				assert.Equal(t, 64, got.Width)
				assert.Equal(t, "png", got.Format)
			} else {
				assert.Empty(t, got.Data)
			}
			assert.Equal(t, tc.want, f.hits())
			_, err = f.s.SenderAvatar(t.Context(), id, false)
			require.NoError(t, err)
			assert.Equal(t, tc.want, f.hits(), "site/URL success and misses avoid repeated discovery")
		})
	}
}

// TestSenderAvatarBIMI: the organization's DNS logo comes before any favicon, even
// a cached one, and is cached itself; without BIMI the favicon path runs as before.
func TestSenderAvatarBIMI(t *testing.T) {
	const logo = `<svg xmlns="http://www.w3.org/2000/svg" version="1.2" baseProfile="tiny-ps" viewBox="0 0 32 32"><title>Logo</title><path d="M0 0L32 0L32 32Z"/></svg>`
	good := avatarPNG(t)
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Host + r.URL.Path {
		case "example.com/bimi.svg":
			w.Write([]byte(logo))
		case "example.com/apple-touch-icon.png", "other.com/apple-touch-icon.png":
			w.Write(good)
		default:
			http.NotFound(w, r)
		}
	})
	var mu sync.Mutex
	var lookups []string
	f.s.opts.LookupTXT = func(_ context.Context, name string) ([]string, error) {
		mu.Lock()
		lookups = append(lookups, name)
		mu.Unlock()
		switch name {
		case "default._bimi.example.com":
			return []string{"v=BIMI1; l=https://example.com/bimi.svg"}, nil
		case "_dmarc.example.com":
			return []string{"v=DMARC1; p=reject"}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	_, err := f.s.images.PutAvatar(t.Context(), imagecache.AvatarKey("favicon", "example.com"), good, "favicon", f.s.images.AvatarGeneration())
	require.NoError(t, err)

	for _, id := range []int64{addMessage(t, f.s, "b", "OpenAI <noreply@tm.example.com>", "plain", "", 2), addMessage(t, f.s, "c", "noreply@tm.example.com", "plain", "", 3)} {
		got, err := f.s.SenderAvatar(t.Context(), id, false)
		require.NoError(t, err)
		assert.Equal(t, "bimi", got.Source)
		assert.Equal(t, "svg", got.Format)
		assert.Equal(t, []byte(logo), got.Data)
	}
	assert.Equal(t, []string{"example.com/bimi.svg"}, f.hits(), "one logo download, then cached")
	assert.Equal(t, []string{"default._bimi.tm.example.com", "default._bimi.example.com", "_dmarc.example.com", "_dmarc.tm.example.com"}, lookups)

	got, err := f.s.SenderAvatar(t.Context(), addMessage(t, f.s, "d", "bob@other.com", "plain", "", 4), false)
	require.NoError(t, err)
	assert.Equal(t, "favicon", got.Source)
}

// TestSenderAvatarCachedOrganizationFirst: with the organization's icon cached, a
// sender host is not asked again, as account.tiktok.com, which never answers, was.
func TestSenderAvatarCachedOrganizationFirst(t *testing.T) {
	good := avatarPNG(t)
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "example.com" {
			w.Write(good)
		} else {
			http.NotFound(w, r)
		}
	})
	_, err := f.s.SenderAvatar(t.Context(), addMessage(t, f.s, "a", "bob@example.com", "plain", "", 1), false)
	require.NoError(t, err)
	got, err := f.s.SenderAvatar(t.Context(), addMessage(t, f.s, "b", "bob@mail.example.com", "plain", "", 2), false)
	require.NoError(t, err)
	assert.Equal(t, "favicon", got.Source)
	assert.Equal(t, []string{"example.com/apple-touch-icon.png"}, f.hits())
}

func TestAvatarPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, mode                 string
		trusted, junk, allow, want bool
	}{
		{"auto", "auto", false, false, false, true},
		{"trusted sender", "trusted", true, false, false, true},
		{"untrusted sender", "trusted", false, false, false, false},
		{"ask", "ask", false, false, false, false},
		{"junk auto", "auto", false, true, false, false},
		{"junk trusted", "trusted", true, true, false, false},
		{"manual ask", "ask", false, false, true, true},
		{"manual junk", "ask", false, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			good := avatarPNG(t)
			f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write(good) })
			id := addMessage(t, f.s, "s", "Bob <bob@example.com>", "", "", 1)
			f.policy(t, tc.mode, tc.trusted, tc.junk)
			got, err := f.s.SenderAvatar(t.Context(), id, tc.allow)
			require.NoError(t, err)
			assert.Equal(t, tc.want, len(got.Data) > 0)
			n := 0
			if tc.want {
				n = 1
			}
			assert.Len(t, f.hits(), n)
		})
	}
}

func TestAvatarPolicyCachedAndChangedDuringFetch(t *testing.T) {
	good := avatarPNG(t)
	started, release := make(chan struct{}), make(chan struct{})
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-release; w.Write(good) })
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	id := addMessage(t, f.s, "s", "bob@example.com", "", "", 1)
	done := make(chan Avatar, 1)
	go func() { a, _ := f.s.SenderAvatar(bg, id, false); done <- a }()
	waitSignal(t, started, "avatar request", time.Second)
	f.policy(t, "ask", false, false)
	close(release)
	assert.Empty(t, (<-done).Data, "late result respects the current policy")
	got, err := f.s.SenderAvatar(t.Context(), id, false)
	require.NoError(t, err)
	assert.Empty(t, got.Data, "cache does not bypass ask")
	got, err = f.s.SenderAvatar(t.Context(), id, true)
	require.NoError(t, err)
	assert.NotEmpty(t, got.Data)
	assert.Len(t, f.hits(), 1)
	for _, tc := range []struct {
		mode          string
		junk, trusted bool
	}{
		{"auto", true, false}, {"trusted", true, true}, {"trusted", false, false},
	} {
		f.policy(t, tc.mode, tc.trusted, tc.junk)
		got, err = f.s.SenderAvatar(t.Context(), id, false)
		require.NoError(t, err)
		assert.Empty(t, got.Data, "cached avatar remains policy-gated")
		got, err = f.s.SenderAvatar(t.Context(), id, true)
		require.NoError(t, err)
		assert.NotEmpty(t, got.Data, "manual permission applies to cached images too")
		assert.Len(t, f.hits(), 1)
	}
	f.policy(t, "ask", false, false)
	other := addMessage(t, f.s, "s2", "bob@example.com", "", "", 2)
	got, err = f.s.SenderAvatar(t.Context(), other, false)
	require.NoError(t, err)
	assert.Empty(t, got.Data, "permission is not inherited by another message")
	f.policy(t, "auto", false, false)
	got, err = f.s.SenderAvatar(t.Context(), other, false)
	require.NoError(t, err)
	assert.NotEmpty(t, got.Data)
	assert.Len(t, f.hits(), 1)
}

func TestAvatarCancellation(t *testing.T) {
	for _, action := range []string{"caller", "close", "delete"} {
		t.Run(action, func(t *testing.T) {
			started, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) {
				once.Do(func() { close(started) })
				defer close(stopped)
				select {
				case <-release:
				case <-r.Context().Done():
				}
			})
			defer close(release)
			id := addMessage(t, f.s, "s", "bob@example.com", "", "", 1)
			ctx, cancel := context.WithCancel(bg)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				a, err := f.s.SenderAvatar(ctx, id, false)
				if len(a.Data) != 0 {
					done <- assert.AnError
				} else {
					done <- err
				}
			}()
			waitSignal(t, started, "avatar request", time.Second)
			switch action {
			case "caller":
				cancel()
			case "close":
				require.NoError(t, f.s.Close())
			case "delete":
				require.NoError(t, f.s.RemoveAccount(t.Context(), 1))
			}
			select {
			case err := <-done:
				assert.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("avatar did not cancel")
			}
			assert.Len(t, f.hits(), 1, "cancellation stops further source requests")
			if action == "close" {
				waitSignal(t, stopped, "closed service's actual HTTP request", time.Second)
				_, err := f.s.SenderAvatar(t.Context(), id, true)
				assert.ErrorIs(t, err, ErrClosed)
			}
		})
	}
}

func TestAvatarPolicyStopsPendingFaviconRequests(t *testing.T) {
	for _, stage := range []string{"/apple-touch-icon.png", "/", "/one", "/two"} {
		for _, manual := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/manual=%v", stage, manual), func(t *testing.T) {
				good := avatarPNG(t)
				started, release := make(chan struct{}), make(chan struct{})
				f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == stage {
						close(started)
						<-release
					}
					switch r.URL.Path {
					case "/":
						w.Write([]byte(`<link rel="icon" href="/one"><link rel="icon" href="/two">`))
					case "/two":
						if stage == "/two" {
							http.NotFound(w, r)
						} else {
							w.Write(good)
						}
					case "/favicon.ico":
						w.Write(good)
					default:
						http.NotFound(w, r)
					}
				})
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				id := addMessage(t, f.s, "s", "bob@example.com", "", "", 1)
				done := make(chan Avatar, 1)
				go func() {
					a, err := f.s.SenderAvatar(bg, id, manual)
					if err != nil {
						t.Errorf("SenderAvatar: %v", err)
					}
					done <- a
				}()
				waitSignal(t, started, "blocked favicon request", time.Second)
				f.policy(t, "ask", false, false)
				close(release)
				var got Avatar
				select {
				case got = <-done:
				case <-time.After(time.Second):
					t.Fatal("avatar call did not stop")
				}
				want := []string{"example.com/apple-touch-icon.png", "example.com/", "example.com/one", "example.com/two"}
				if stage == "/two" && manual {
					want = append(want, "example.com/favicon.ico")
				}
				if manual {
					assert.Equal(t, "favicon", got.Source)
					assert.Equal(t, good, got.Data)
				} else {
					assert.Empty(t, got.Data)
					switch stage {
					case "/apple-touch-icon.png":
						want = want[:1]
					case "/":
						want = want[:2]
					case "/one":
						want = want[:3]
					}
				}
				assert.Equal(t, want, f.hits(), "no new automatic discovery/candidate after switching to ask")
			})
		}
	}
}

func TestAvatarPolicyFaviconRecheckError(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apple-touch-icon.png" {
			close(started)
			<-release
		}
		http.NotFound(w, r)
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	id := addMessage(t, f.s, "s", "bob@example.com", "", "", 1)
	done := make(chan error, 1)
	go func() { _, err := f.s.SenderAvatar(bg, id, false); done <- err }()
	waitSignal(t, started, "blocked favicon request", time.Second)
	_, err := f.s.db.Execute(t.Context(), "DROP TABLE settings")
	require.NoError(t, err)
	close(release)
	select {
	case err = <-done:
		require.ErrorContains(t, err, "no such table")
	case <-time.After(time.Second):
		t.Fatal("avatar did not stop after policy error")
	}
	assert.Equal(t, []string{"example.com/apple-touch-icon.png"}, f.hits(), "policy errors stop subsequent requests and return to the caller")
}

func TestAvatarCancellationBeforeRequest(t *testing.T) {
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") })
	id := addMessage(t, f.s, "s", "bob@example.com", "", "", 1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a, err := f.s.SenderAvatar(ctx, id, true)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, a.Data)
	assert.Empty(t, f.hits())
}

func TestSenderAvatarDoesNotWaitForAccountLock(t *testing.T) {
	good := avatarPNG(t)
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write(good) })
	id := addMessage(t, f.s, "s", "bob@example.com", "", "", 1)
	unlock, err := f.s.lockAccount(t.Context(), 1)
	require.NoError(t, err)
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	a, err := f.s.SenderAvatar(ctx, id, false)
	require.NoError(t, err)
	assert.NotEmpty(t, a.Data)
	assert.Len(t, f.hits(), 1)
}

func TestSenderAvatarUnavailableSourcesFallBack(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			good := avatarPNG(t)
			f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Host == "gravatar.com" {
					w.Write(good)
				} else {
					w.WriteHeader(status)
				}
			})
			id := addMessage(t, f.s, "s", "bob@example.com", "", "", 1)
			a, err := f.s.SenderAvatar(t.Context(), id, false)
			require.NoError(t, err)
			assert.Equal(t, "gravatar", a.Source)
			assert.Equal(t, []string{"example.com/apple-touch-icon.png", "example.com/", "example.com/favicon.ico", "gravatar.com/avatar/5ff860bf1190596c7188ab851db691f0f3169c453936e9e1eba2f9a47f7a0018?s=64&d=404&r=g"}, f.hits())
		})
	}
}

func TestSenderAvatarPreservesGravatarAliases(t *testing.T) {
	good := avatarPNG(t)
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write(good) })
	for _, from := range []string{"Bob <First.Last+tag@GMAIL.COM>", "firstlast@gmail.com", "first.last@gmail.com"} {
		id := addMessage(t, f.s, "s", from, "", "", 1)
		a, err := f.s.SenderAvatar(t.Context(), id, false)
		require.NoError(t, err)
		assert.Equal(t, "gravatar", a.Source)
	}
	want := []string{
		"gravatar.com/avatar/a3997a3fbbd1a0839c1eb9240489103ddc167f6b5cfa1c429c48ff2190346a4f?s=64&d=404&r=g",
		"gravatar.com/avatar/ed6c35e905a6daa500da471136a733ccccf446b294e80ae03a1d5d865fe1044c?s=64&d=404&r=g",
		"gravatar.com/avatar/3fba33b305fe1c0eeaf728b3b7aa3c668ae460a9807da214c47185d5e2befb98?s=64&d=404&r=g",
	}
	assert.Equal(t, want, f.hits(), "each complete address has a distinct request/cache identity")
}

func TestSenderAvatarStaleGravatar(t *testing.T) {
	good := avatarPNG(t)
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	id := addMessage(t, f.s, "s", "bob@gmail.com", "", "", 1)
	key := imagecache.AvatarKey("gravatar", "https://gravatar.com/avatar/300db02db75af922c759f08a0efa5a400128eac1f531b802697450b5c6da6e9f?s=64&d=404&r=g")
	dir := filepath.Join(f.s.dataDir, "cache", "images")
	name := fmt.Sprintf("%s-png-64x64-%d", key, time.Now().Add(-25*time.Hour).Unix())
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), good, 0600))
	f.s.images.Close()
	var err error
	f.s.images, err = imagecache.Open(dir, 0, f.s.opts.ImageClient)
	require.NoError(t, err)
	for range 2 {
		a, err := f.s.SenderAvatar(t.Context(), id, false)
		require.NoError(t, err)
		assert.Equal(t, "gravatar", a.Source)
		assert.Equal(t, good, a.Data)
	}
	assert.Len(t, f.hits(), 1, "failed refresh is throttled without losing the stale avatar")
	_, fresh, err := f.s.images.LookupAvatar(t.Context(), key)
	require.NoError(t, err)
	assert.False(t, fresh, "fallback must not reset the freshness deadline")
	f.policy(t, "ask", false, false)
	a, err := f.s.SenderAvatar(t.Context(), id, false)
	require.NoError(t, err)
	assert.Empty(t, a.Data, "stale avatar cannot bypass policy")
}

func TestSenderAvatarDatabaseError(t *testing.T) {
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") })
	_, err := f.s.SenderAvatar(t.Context(), 123, true)
	require.ErrorContains(t, err, "not found")
	_, err = f.s.db.Execute(t.Context(), "DROP TABLE messages")
	require.NoError(t, err)
	_, err = f.s.SenderAvatar(t.Context(), 123, true)
	require.ErrorContains(t, err, "no such table")
	assert.Empty(t, f.hits())
}

func encodeB64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func newCardDAVServerForAvatarTest(t *testing.T) *testmail.CardDAV {
	t.Helper()
	prevRoots := conn.TLSRoots
	conn.TLSRoots = testmail.CardDAVRoots()
	t.Cleanup(func() { conn.TLSRoots = prevRoots })
	return testmail.StartCardDAV(t)
}

func vcardWithPhoto(fn string, emails []string, photoB64 string, photoURL string) string {
	lines := []string{"BEGIN:VCARD", "VERSION:3.0", "FN:" + fn}
	for i, em := range emails {
		if i == 0 {
			lines = append(lines, "EMAIL;TYPE=PREF:"+em)
		} else {
			lines = append(lines, "EMAIL:"+em)
		}
	}
	if photoB64 != "" {
		lines = append(lines, "PHOTO;ENCODING=b;TYPE=PNG:"+photoB64)
	}
	if photoURL != "" {
		lines = append(lines, "PHOTO;VALUE=URI:"+photoURL)
	}
	lines = append(lines, "END:VCARD")
	return strings.Join(lines, "\r\n")
}

func TestSenderAvatarCardDAVPriorityZeroNetAndOwnMail(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()

	var netHits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		netHits++
		w.Write(avatarPNG(t))
	}))
	t.Cleanup(srv.Close)

	s := newCardDAVService(t, dir, false)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	var dnsHits int
	s.opts.LookupTXT = func(_ context.Context, _ string) ([]string, error) {
		dnsHits++
		return []string{"v=BIMI1; l=" + srv.URL + "/bimi.svg"}, nil
	}

	_, err := s.db.Execute(t.Context(), "INSERT INTO accounts(id, email) VALUES (1, 'me@example.com')")
	require.NoError(t, err)
	_, err = s.db.Execute(t.Context(), "INSERT INTO folders(id, account_id, name, role) VALUES (1, 1, 'INBOX', 'inbox'), (2, 1, 'Sent', 'sent')")
	require.NoError(t, err)

	st, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	st.RemoteImages = "ask"
	_, err = s.SetSettings(t.Context(), st)
	require.NoError(t, err)

	dav1 := newCardDAVServerForAvatarTest(t)
	dav2 := newCardDAVServerForAvatarTest(t)

	photoAlice1 := makePNGBytes(t, 20, 20)
	photoAlice2 := makePNGBytes(t, 28, 28)
	photoBob := makePNGBytes(t, 24, 24)
	photoMe := makePNGBytes(t, 18, 18)
	photoCoworker := makePNGBytes(t, 22, 22)

	extBobURL := dav2.ServePhoto("bob.png", "image/png", photoBob)

	// Book 1 (higher priority initially): Alice has inline photoAlice1; Bob has two emails but no photo;
	// shared team card has me@example.com + coworker@example.com without photo.
	dav1.PutCollection("main", "Main")
	dav1.PutCard("main", "alice", vcardWithPhoto("Alice", []string{"alice@example.com"}, encodeB64(photoAlice1), ""))
	dav1.PutCard("main", "bob-work", vcardWithPhoto("Bob", []string{"bob@work.example", "bob@home.example"}, "", ""))
	dav1.PutCard("main", "team", vcardWithPhoto("Team", []string{"me@example.com", "coworker@example.com"}, "", ""))

	// Book 2 (lower priority initially): Alice has photoAlice2; Bob has external photoBob on bob@home.example;
	// coworker has personal card with photoCoworker.
	dav2.PutCollection("other", "Other")
	dav2.PutCard("other", "alice2", vcardWithPhoto("Alice Two", []string{"alice@example.com"}, encodeB64(photoAlice2), ""))
	dav2.PutCard("other", "bob-home", vcardWithPhoto("Bob Home", []string{"bob@home.example"}, "", extBobURL))
	dav2.PutCard("other", "coworker", vcardWithPhoto("Coworker", []string{"coworker@example.com"}, encodeB64(photoCoworker), ""))

	ab1, err := s.AddAddressBook(t.Context(), AddressBookInput{Preset: PresetCustom, URL: dav1.URL(), Username: "alice", Password: "secret"})
	require.NoError(t, err)
	ab2, err := s.AddAddressBook(t.Context(), AddressBookInput{Preset: PresetCustom, URL: dav2.URL(), Username: "alice", Password: "secret"})
	require.NoError(t, err)
	assert.Equal(t, 1, dav2.PhotoHits("bob.png"), "external PHOTO downloaded once at sync time")

	// Block CardDAV servers completely during mail reading to prove zero network access.
	dav1.FailRequests(http.StatusInternalServerError)
	dav2.FailRequests(http.StatusInternalServerError)
	reqsBefore := len(dav1.Requests()) + len(dav2.Requests())

	// 1. Alice's mail under RemoteImages="ask" gets Book 1's photo with zero network/DNS hits.
	aliceMsg := addMessage(t, s, "hi", "Alice <alice@example.com>", "hello", "", 10)
	got, err := s.SenderAvatar(t.Context(), aliceMsg, false)
	require.NoError(t, err)
	assert.Equal(t, "carddav", got.Source)
	assert.Equal(t, "png", got.Format)
	assert.Equal(t, 20, got.Width)
	assert.Equal(t, photoAlice1, got.Data)
	assert.Zero(t, netHits)
	assert.Zero(t, dnsHits)

	// 2. Bob's mail from bob@work.example resolves via connected component to Book 2's photoBob.
	bobMsg := addMessage(t, s, "work", "bob@work.example", "hello", "", 11)
	got, err = s.SenderAvatar(t.Context(), bobMsg, false)
	require.NoError(t, err)
	assert.Equal(t, "carddav", got.Source)
	assert.Equal(t, 24, got.Width)
	assert.Equal(t, photoBob, got.Data)

	// 3. Reordering Book 2 ahead of Book 1 switches Alice's photo to photoAlice2.
	require.NoError(t, s.ReorderAddressBooks(t.Context(), []int64{ab2.ID, ab1.ID}))
	got, err = s.SenderAvatar(t.Context(), aliceMsg, false)
	require.NoError(t, err)
	assert.Equal(t, "carddav", got.Source)
	assert.Equal(t, 28, got.Width)
	assert.Equal(t, photoAlice2, got.Data)

	// 4. Own sent mail: coworker's photo does NOT leak via shared team card,
	//    but adding a personal card for me@example.com shows photoMe on sent mail.
	res, err := s.db.Execute(t.Context(), `INSERT INTO messages(account_id, subject, from_addr, date, body_text, body_html, snippet, has_attachments)
		VALUES (1, 'sent', 'Me <me@example.com>', 12, 'hi', '', 'snip', 0)`)
	require.NoError(t, err)
	sentMsg := res.LastInsertID
	showIn(t, s, sentMsg, 2)

	got, err = s.SenderAvatar(t.Context(), sentMsg, false)
	require.NoError(t, err)
	assert.Empty(t, got.Data, "shared card with own+external email must not leak external contact's photo onto own mail")

	assert.Equal(t, reqsBefore, len(dav1.Requests())+len(dav2.Requests()), "zero CardDAV requests during SenderAvatar reads")
	assert.Equal(t, 1, dav2.PhotoHits("bob.png"), "zero external photo fetches during SenderAvatar reads")

	dav1.FailRequests(0)
	dav1.PutCard("main", "me", vcardWithPhoto("Me", []string{"me@example.com"}, encodeB64(photoMe), ""))
	require.NoError(t, s.SyncAddressBook(t.Context(), ab1.ID))

	got, err = s.SenderAvatar(t.Context(), sentMsg, false)
	require.NoError(t, err)
	assert.Equal(t, "carddav", got.Source)
	assert.Equal(t, 18, got.Width)
	assert.Equal(t, photoMe, got.Data)

	// 5. Disabling Book 2's collection drops photoAlice2 and falls back to Book 1's photoAlice1.
	require.NoError(t, s.SetAddressBookCollectionEnabled(t.Context(), ab2.Collections[0].ID, false))
	got, err = s.SenderAvatar(t.Context(), aliceMsg, false)
	require.NoError(t, err)
	assert.Equal(t, photoAlice1, got.Data)
}

func TestSenderAvatarCardDAVAntiSpoofingGates(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	s := newCardDAVService(t, dir, false)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	_, err := s.db.Execute(t.Context(), "INSERT INTO accounts(id, email) VALUES (1, 'me@example.com')")
	require.NoError(t, err)
	_, err = s.db.Execute(t.Context(), `INSERT INTO folders(id, account_id, name, role, role_override) VALUES
		(1, 1, 'INBOX', 'inbox', ''),
		(2, 1, 'Spam', 'junk', ''),
		(3, 1, 'Suspect', '', 'junk'),
		(4, 1, 'Projects', '', '')`)
	require.NoError(t, err)

	dav := newCardDAVServerForAvatarTest(t)
	cardPhoto := makePNGBytes(t, 24, 24)
	dav.PutCollection("contacts", "Contacts")
	dav.PutCard("contacts", "alice", vcardWithPhoto("Alice", []string{"alice@example.com"}, encodeB64(cardPhoto), ""))
	_, err = s.AddAddressBook(t.Context(), AddressBookInput{Preset: PresetCustom, URL: dav.URL(), Username: "alice", Password: "secret"})
	require.NoError(t, err)

	// Baseline: ordinary inbox message shows CardDAV PHOTO.
	okMsg := addMessage(t, s, "lunch", "Alice <alice@example.com>", "see you", "", 1)
	got, err := s.SenderAvatar(t.Context(), okMsg, false)
	require.NoError(t, err)
	assert.Equal(t, "carddav", got.Source)

	// 1. Message in junk folder (role='junk'): hides CardDAV PHOTO even when RemoteImages="auto".
	res, err := s.db.Execute(t.Context(), `INSERT INTO messages(account_id, subject, from_addr, date, body_text, body_html, snippet, has_attachments)
		VALUES (1, 'junk1', 'Alice <alice@example.com>', 2, 'hi', '', 'snip', 0)`)
	require.NoError(t, err)
	junkMsg := res.LastInsertID
	showIn(t, s, junkMsg, 2)

	got, err = s.SenderAvatar(t.Context(), junkMsg, false)
	require.NoError(t, err)
	assert.Empty(t, got.Data, "junk folder must hide CardDAV PHOTO")
	got, err = s.SenderAvatar(t.Context(), junkMsg, true)
	require.NoError(t, err)
	assert.NotEqual(t, "carddav", got.Source, "junk folder must never return CardDAV PHOTO even with allowRemote")

	// 2. Folder with role_override='junk': also hides CardDAV PHOTO.
	res, err = s.db.Execute(t.Context(), `INSERT INTO messages(account_id, subject, from_addr, date, body_text, body_html, snippet, has_attachments)
		VALUES (1, 'junk2', 'Alice <alice@example.com>', 3, 'hi', '', 'snip', 0)`)
	require.NoError(t, err)
	overrideJunkMsg := res.LastInsertID
	showIn(t, s, overrideJunkMsg, 3)

	got, err = s.SenderAvatar(t.Context(), overrideJunkMsg, false)
	require.NoError(t, err)
	assert.Empty(t, got.Data, "role_override='junk' must hide CardDAV PHOTO")

	// 3. Multi-location message shown in both a custom label folder and a junk folder: hides CardDAV PHOTO.
	res, err = s.db.Execute(t.Context(), `INSERT INTO messages(account_id, subject, from_addr, date, body_text, body_html, snippet, has_attachments)
		VALUES (1, 'multi-junk', 'Alice <alice@example.com>', 4, 'hi', '', 'snip', 0)`)
	require.NoError(t, err)
	multiJunkMsg := res.LastInsertID
	showIn(t, s, multiJunkMsg, 4)
	showIn(t, s, multiJunkMsg, 2)

	got, err = s.SenderAvatar(t.Context(), multiJunkMsg, false)
	require.NoError(t, err)
	assert.Empty(t, got.Data, "message shown in both custom folder and junk folder must hide CardDAV PHOTO")

	// 4. Deterministic SecurityFraud before AI scan (dmarc=fail on spoofed From): hides CardDAV PHOTO even with allowRemote=true.
	res, err = s.db.Execute(t.Context(), `INSERT INTO messages(account_id, subject, from_addr, date, body_text, body_html, snippet, has_attachments, auth_results)
		VALUES (1, 'wire transfer', 'Alice <alice@example.com>', 5, 'urgent', '', 'snip', 0, 'mx.example.org; dmarc=fail header.from=example.com')`)
	require.NoError(t, err)
	dmarcFailMsg := res.LastInsertID
	showIn(t, s, dmarcFailMsg, 1)

	got, err = s.SenderAvatar(t.Context(), dmarcFailMsg, false)
	require.NoError(t, err)
	assert.Empty(t, got.Data, "unscanned DMARC fail fraud must hide CardDAV PHOTO")
	got, err = s.SenderAvatar(t.Context(), dmarcFailMsg, true)
	require.NoError(t, err)
	assert.Empty(t, got.Data, "SecurityFraud must fall back to initial tile even when allowRemote=true")

	// 5. Stored AI/manual fraud verdict (security_status='warning', security_reason='ai_fraud'): hides CardDAV PHOTO.
	res, err = s.db.Execute(t.Context(), `INSERT INTO messages(account_id, subject, from_addr, date, body_text, body_html, snippet, has_attachments, security_status, security_reason, security_scanned_at)
		VALUES (1, 'phish', 'Alice <alice@example.com>', 6, 'click here', '', 'snip', 0, 'warning', 'ai_fraud', 100)`)
	require.NoError(t, err)
	aiFraudMsg := res.LastInsertID
	showIn(t, s, aiFraudMsg, 1)

	got, err = s.SenderAvatar(t.Context(), aiFraudMsg, true)
	require.NoError(t, err)
	assert.Empty(t, got.Data, "stored AI SecurityFraud verdict must hide CardDAV PHOTO")

	// 6. Manual safe override (security_status='neutral', security_reason='manual_safe'): restores CardDAV PHOTO.
	_, err = s.db.Execute(t.Context(), `UPDATE messages SET security_status = 'neutral', security_reason = 'manual_safe' WHERE id = ?`, dmarcFailMsg)
	require.NoError(t, err)
	got, err = s.SenderAvatar(t.Context(), dmarcFailMsg, false)
	require.NoError(t, err)
	assert.Equal(t, "carddav", got.Source)
	assert.Equal(t, cardPhoto, got.Data)
}

func TestContactAvatarZeroMailConnectedComponentAndFallback(t *testing.T) {
	good := avatarPNG(t)
	f := newAvatarFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "acme.example.com" {
			w.Write(good)
		} else {
			http.NotFound(w, r)
		}
	})
	_, err := f.s.db.Execute(t.Context(), `INSERT INTO folders(id, account_id, name, role) VALUES
		(2, 1, 'Sent', 'sent'),
		(3, 1, 'Spam', 'junk'),
		(4, 1, 'Trash', 'trash')`)
	require.NoError(t, err)

	dav := newCardDAVServerForAvatarTest(t)
	cardPhoto := makePNGBytes(t, 26, 26)
	ownPhoto := makePNGBytes(t, 16, 16)
	dav.PutCollection("contacts", "Contacts")
	// Zero-mail contact with PHOTO across two cards sharing an email.
	dav.PutCard("contacts", "zoe-work", vcardWithPhoto("Zoe", []string{"zoe@work.example", "zoe@personal.example"}, "", ""))
	dav.PutCard("contacts", "zoe-home", vcardWithPhoto("Zoe", []string{"zoe@personal.example"}, encodeB64(cardPhoto), ""))
	// Card without PHOTO sharing two addresses; one address has a received message with remote favicon.
	dav.PutCard("contacts", "dan", vcardWithPhoto("Dan", []string{"dan@nomail.example", "dan@acme.example.com"}, "", ""))
	// Own account card with PHOTO: ContactAvatar excludes own accounts.
	dav.PutCard("contacts", "me", vcardWithPhoto("Me", []string{"me@example.com"}, encodeB64(ownPhoto), ""))

	_, err = f.s.AddAddressBook(t.Context(), AddressBookInput{Preset: PresetCustom, URL: dav.URL(), Username: "alice", Password: "secret"})
	require.NoError(t, err)

	// 1. Zero-mail CardDAV contact returns PHOTO across connected component, even under RemoteImages="ask".
	f.policy(t, "ask", false, false)
	got, err := f.s.ContactAvatar(t.Context(), "Zoe <zoe@work.example>")
	require.NoError(t, err)
	assert.Equal(t, "carddav", got.Source)
	assert.Equal(t, cardPhoto, got.Data)
	assert.Empty(t, f.hits())

	// 2. Own account returns empty Avatar from ContactAvatar.
	got, err = f.s.ContactAvatar(t.Context(), "me@example.com")
	require.NoError(t, err)
	assert.Empty(t, got.Data)

	// 3. Card without PHOTO and only junk/trash mail does NOT use junk/trash mail for fallback.
	f.policy(t, "auto", false, false)
	res, err := f.s.db.Execute(t.Context(), `INSERT INTO messages(account_id, subject, from_addr, date, body_text, body_html, snippet, has_attachments)
		VALUES (1, 'spam', 'Dan <dan@acme.example.com>', 10, 'hi', '', 'snip', 0)`)
	require.NoError(t, err)
	showIn(t, f.s, res.LastInsertID, 3) // junk
	got, err = f.s.ContactAvatar(t.Context(), "dan@nomail.example")
	require.NoError(t, err)
	assert.Empty(t, got.Data)
	assert.Empty(t, f.hits())

	// 4. Once an inbox message arrives on dan@acme.example.com, ContactAvatar("dan@nomail.example")
	//    falls back across the connected component to SenderAvatar of that latest received message.
	addMessage(t, f.s, "hello", "Dan <dan@acme.example.com>", "hi", "", 20)
	got, err = f.s.ContactAvatar(t.Context(), "dan@nomail.example")
	require.NoError(t, err)
	assert.Equal(t, "favicon", got.Source)
	assert.Equal(t, good, got.Data)
}
