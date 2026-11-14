package ews

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

const busyMessage = `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>
<m:CreateItemResponse xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages"><m:ResponseMessages>
<m:CreateItemResponseMessage ResponseClass="Error"><m:MessageText>The server cannot service this request right now.</m:MessageText>
<m:ResponseCode>ErrorServerBusy</m:ResponseCode></m:CreateItemResponseMessage></m:ResponseMessages></m:CreateItemResponse></s:Body></s:Envelope>`

const busyFault = `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>
<faultcode xmlns:a="http://schemas.microsoft.com/exchange/services/2006/types">a:ErrorServerBusy</faultcode>
<faultstring>The server cannot service this request right now. Try again later.</faultstring>
<detail><e:ResponseCode xmlns:e="http://schemas.microsoft.com/exchange/services/2006/types">ErrorServerBusy</e:ResponseCode></detail>
</s:Fault></s:Body></s:Envelope>`

// A server too busy to take the message refused it for now: a plain error,
// tried again, neither uncertain nor a refusal for good.
func TestSendBusyIsRetried(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"response message": {http.StatusOK, busyMessage},
		"SOAP fault":       {http.StatusInternalServerError, busyFault},
		"HTTP 503":         {http.StatusServiceUnavailable, ""},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.ReadAll(r.Body)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			err := NewSender(Config{URL: srv.URL, Credentials: func(context.Context) (backend.Credentials, error) {
				return backend.Credentials{Username: "alice", Password: "secret"}, nil
			}, Wait: func(context.Context, time.Duration) error { return nil }}).Send(t.Context(), "a@example.com", []string{"b@example.com"}, []byte("To: b@example.com\r\n\r\nhi\r\n"))
			if err == nil || errors.Is(err, backend.ErrUncertain) || errors.As(err, new(*backend.RejectedError)) {
				t.Errorf("Send to a busy server = %v, want a plain error", err)
			}
		})
	}
}

// A send that runs out of time waiting, for the account's other requests or
// for a busy server's back-off, says so and names the server: the wait
// returned the bare "context deadline exceeded" (#755).
func TestSendTimeoutNamesStepAndHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, busyFault)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cr := backend.Credentials{Username: "alice", Password: "secret"}
	s := NewSender(Config{URL: srv.URL, Credentials: func(context.Context) (backend.Credentials, error) { return cr, nil }})
	send := func() error {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		return s.Send(ctx, "a@example.com", []string{"b@example.com"}, []byte("To: b@example.com\r\n\r\nhi\r\n"))
	}
	check := func(err error, step string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), u.Host+": "+step+": context deadline exceeded") {
			t.Errorf("Send = %v, want the deadline under %q at %s", err, step, u.Host)
		}
	}

	check(send(), "waiting out the server's back-off") // the real back-off outlasts the deadline

	release, err := conn.Acquire(t.Context(), s.c.account(cr)) // another request of the account's is running
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	err = send()
	check(err, "waiting for the account's other requests")
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, backend.ErrUncertain) {
		t.Errorf("Send = %v, want the deadline itself: nothing was written, so it is not uncertain", err)
	}
}

func TestBccOf(t *testing.T) {
	raw := []byte("To: \"B\" <B@x.com>\r\nCc: c@x.com\r\n\r\nhi\r\n")
	got := bccOf(raw, []string{"b@x.com", "C@x.com", "secret@z.com"})
	if len(got) != 1 || got[0] != "secret@z.com" {
		t.Errorf("bccOf = %v, want [secret@z.com]", got)
	}
}
