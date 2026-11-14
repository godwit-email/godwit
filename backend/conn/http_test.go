package conn

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// shortTimeouts shortens the HTTP backends' timeouts for one test; they are
// read when a client is made.
func shortTimeouts(t *testing.T) {
	h, r, s := HeaderTimeout, RequestTimeout, StallTimeout
	HeaderTimeout, RequestTimeout, StallTimeout = 200*time.Millisecond, time.Second, 200*time.Millisecond
	t.Cleanup(func() { HeaderTimeout, RequestTimeout, StallTimeout = h, r, s })
}

// within fails the test unless f returns an error within d.
func within(t *testing.T, d time.Duration, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("no error from a server that never answers")
		}
		return err
	case <-time.After(d):
		t.Fatalf("still waiting after %v", d)
		return nil
	}
}

func server(t *testing.T, h http.HandlerFunc) *httptest.Server {
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv
}

// get reads url's whole body on c, its body bounded by StallTimeout.
func get(c *http.Client, url string) ([]byte, error) {
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	b := StallBody(resp.Body, StallTimeout, cancel)
	defer b.Close()
	return io.ReadAll(b)
}

// A server that never answers fails the request, on Streaming too, which
// has no overall timeout: the response headers have HeaderTimeout.
func TestSilentServerFails(t *testing.T) {
	shortTimeouts(t)
	srv := server(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	c := NewHTTPClient(HTTPTransport(), nil)
	within(t, 3*time.Second, func() error { _, err := get(c, srv.URL); return err })
	within(t, 3*time.Second, func() error { _, err := get(Streaming(c), srv.URL); return err })
}

// A body that stops arriving fails with ErrStalled.
func TestStalledBodyFails(t *testing.T) {
	shortTimeouts(t)
	srv := server(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "From: a\r\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	c := Streaming(NewHTTPClient(HTTPTransport(), nil))
	if err := within(t, 3*time.Second, func() error { _, err := get(c, srv.URL); return err }); !errors.Is(err, ErrStalled) {
		t.Fatalf("err = %v, want ErrStalled", err)
	}
}

// A body on Streaming is not cut off by RequestTimeout while bytes keep
// arriving.
func TestSlowBodyOutlastsRequestTimeout(t *testing.T) {
	shortTimeouts(t)
	RequestTimeout = 300 * time.Millisecond
	srv := server(t, func(w http.ResponseWriter, r *http.Request) {
		for range 6 {
			io.WriteString(w, "x")
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	})
	c := NewHTTPClient(HTTPTransport(), nil)
	if b, err := get(Streaming(c), srv.URL); err != nil || string(b) != "xxxxxx" {
		t.Fatalf("read %q, %v", b, err)
	}
	if _, err := get(c, srv.URL); err == nil {
		t.Fatal("a request outlasted RequestTimeout off Streaming")
	}
}

// A redirect may stay on the same host, but never from HTTPS to plain HTTP:
// the request body and the credentials would go in the clear.
func TestSameHost(t *testing.T) {
	from, _ := http.NewRequest(http.MethodPost, "https://mail.example/EWS/Exchange.asmx", nil)
	for target, want := range map[string]error{
		"https://mail.example/EWS/Other.asmx":       nil,
		"http://mail.example:443/EWS/Exchange.asmx": errors.New("refused a redirect from HTTPS to HTTP"),
		"http://mail.example/EWS/Exchange.asmx":     errors.New("refused a redirect from HTTPS to HTTP"),
		"https://other.example/EWS/Exchange.asmx":   ErrOtherHost,
	} {
		to, _ := http.NewRequest(http.MethodPost, target, nil)
		if err := SameHost(to, []*http.Request{from}); (err == nil) != (want == nil) || err != nil && err.Error() != want.Error() {
			t.Errorf("redirect to %s: err %v, want %v", target, err, want)
		}
	}
}

// An account's slot is kept only while a call holds it or waits for it, so
// accounts seen once do not pile up.
func TestAccountSlotReleased(t *testing.T) {
	release, err := Acquire(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Acquire(ctx, "a"); err == nil {
		t.Fatal("took a held slot")
	}
	release()
	accounts.Lock()
	n := len(accounts.m)
	accounts.Unlock()
	if n != 0 {
		t.Errorf("%d slots kept after the calls ended, want 0", n)
	}
}

func TestBackOff(t *testing.T) {
	for _, tc := range []struct {
		err  error
		try  int
		want time.Duration
		ok   bool
	}{
		{&BusyError{BackOff: 3 * time.Second}, 0, 3 * time.Second, true},
		{&BusyError{BackOff: time.Hour}, 1, MaxBackOff, true},
		{&BusyError{}, 0, time.Second, true},
		{&BusyError{}, 2, 4 * time.Second, true},
		{&BusyError{}, MaxRetries, 0, false},
		{errors.New("no"), 0, 0, false},
	} {
		if d, ok := BackOff(tc.err, tc.try); d != tc.want || ok != tc.ok {
			t.Errorf("BackOff(%v, %d) = %v, %v; want %v, %v", tc.err, tc.try, d, ok, tc.want, tc.ok)
		}
	}
	h := http.Header{}
	for v, want := range map[string]time.Duration{"": 0, "7": 7 * time.Second, "-1": 0, "soon": 0} {
		h.Set("Retry-After", v)
		if d := RetryAfter(h); d != want {
			t.Errorf("RetryAfter(%q) = %v, want %v", v, d, want)
		}
	}
}
