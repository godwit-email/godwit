package core

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
