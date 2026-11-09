package send

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/conn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const graphRaw = "From: a@outlook.com\r\nTo: \"B\" <b@x.com>\r\nCc: C@x.com\r\nSubject: hi\r\n\r\nbody\r\n"

func graphServer(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	old, oldProxy := GraphURL, conn.SystemProxy
	GraphURL, conn.SystemProxy = srv.URL+"/v1.0/me/sendMail", func(*url.URL) *url.URL { return nil }
	t.Cleanup(func() { GraphURL, conn.SystemProxy = old, oldProxy })
}

func TestSendGraphPostsMIMEWithBcc(t *testing.T) {
	var got string
	graphServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		assert.Equal(t, "text/plain", r.Header.Get("Content-Type"))
		b, _ := io.ReadAll(r.Body)
		d, err := base64.StdEncoding.DecodeString(string(b))
		assert.NoError(t, err)
		got = string(d)
		w.WriteHeader(http.StatusAccepted)
	})
	require.NoError(t, SendGraph(context.Background(), "tok", []string{"b@x.com", "c@x.com", "secret@z.com"}, []byte(graphRaw)))
	assert.Equal(t, "Bcc: secret@z.com\r\n"+graphRaw, got)
}

func TestSendGraphWithoutBccIsUnchanged(t *testing.T) {
	assert.Equal(t, graphRaw, string(withBcc([]byte(graphRaw), []string{"B@X.com", "c@x.com"})))
}

func TestSendGraphErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		check  func(t *testing.T, err error)
	}{
		{401, func(t *testing.T, err error) { assert.ErrorIs(t, err, conn.ErrAuthFailed) }},
		{403, func(t *testing.T, err error) { assert.ErrorIs(t, err, conn.ErrAuthFailed) }},
		{504, func(t *testing.T, err error) { assert.ErrorIs(t, err, ErrUncertain) }},
		{503, func(t *testing.T, err error) { // retried later
			assert.False(t, errors.Is(err, ErrUncertain) || errors.As(err, new(*RejectedError)))
		}},
		{400, func(t *testing.T, err error) {
			var rej *RejectedError
			require.ErrorAs(t, err, &rej)
			assert.GreaterOrEqual(t, rej.Code, 500) // fails for good
			assert.Contains(t, rej.Message, "ErrorInvalidRecipients")
		}},
	} {
		graphServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			io.WriteString(w, `{"error":{"code":"ErrorInvalidRecipients","message":"bad"}}`)
		})
		err := SendGraph(context.Background(), "tok", []string{"b@x.com"}, []byte(graphRaw))
		require.Error(t, err, tc.status)
		tc.check(t, err)
	}
}

func TestSendGraphTooLarge(t *testing.T) {
	graphServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("sent") })
	raw := graphRaw + strings.Repeat("x", 3<<20+1)
	var rej *RejectedError
	require.ErrorAs(t, SendGraph(context.Background(), "tok", []string{"b@x.com"}, []byte(raw)), &rej)
	assert.Equal(t, 552, rej.Code)
}

func TestSendGraphLostAnswerIsUncertain(t *testing.T) {
	graphServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		c, _, _ := http.NewResponseController(w).Hijack()
		c.Close()
	})
	assert.ErrorIs(t, SendGraph(context.Background(), "tok", []string{"b@x.com"}, []byte(graphRaw)), ErrUncertain)
}

func TestSendGraphStalledAnswerIsUncertain(t *testing.T) {
	release := make(chan struct{})
	graphServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		<-release
	})
	t.Cleanup(func() { close(release) })
	old := graphClient.Transport.(*http.Transport).ResponseHeaderTimeout
	graphClient.Transport.(*http.Transport).ResponseHeaderTimeout = 100 * time.Millisecond
	t.Cleanup(func() { graphClient.Transport.(*http.Transport).ResponseHeaderTimeout = old })
	assert.ErrorIs(t, SendGraph(context.Background(), "tok", []string{"b@x.com"}, []byte(graphRaw)), ErrUncertain)
}
