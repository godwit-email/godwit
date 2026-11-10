package ews

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/godwit-email/godwit/backend"
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
			}}).Send(t.Context(), "a@example.com", []string{"b@example.com"}, []byte("To: b@example.com\r\n\r\nhi\r\n"))
			if err == nil || errors.Is(err, backend.ErrUncertain) || errors.As(err, new(*backend.RejectedError)) {
				t.Errorf("Send to a busy server = %v, want a plain error", err)
			}
		})
	}
}

func TestBccOf(t *testing.T) {
	raw := []byte("To: \"B\" <B@x.com>\r\nCc: c@x.com\r\n\r\nhi\r\n")
	got := bccOf(raw, []string{"b@x.com", "C@x.com", "secret@z.com"})
	if len(got) != 1 || got[0] != "secret@z.com" {
		t.Errorf("bccOf = %v, want [secret@z.com]", got)
	}
}
