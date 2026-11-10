package ews

import (
	"context"
	"net/http"
	"testing"
)

// An account's slot is kept only while a call holds it or waits for it, so
// accounts seen once do not pile up.
func TestAccountSlotReleased(t *testing.T) {
	release, err := acquire(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := acquire(ctx, "a"); err == nil {
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

// A redirect may stay on the same host, but never from HTTPS to plain HTTP:
// the request body and, when challenged, the password would go in the clear.
func TestRedirectKeepsHTTPS(t *testing.T) {
	c := newClient(Config{URL: "https://mail.example/EWS/Exchange.asmx"})
	from, _ := http.NewRequest(http.MethodPost, "https://mail.example/EWS/Exchange.asmx", nil)
	for target, ok := range map[string]bool{
		"https://mail.example/EWS/Other.asmx":       true,
		"http://mail.example:443/EWS/Exchange.asmx": false,
		"http://mail.example/EWS/Exchange.asmx":     false,
		"https://other.example/EWS/Exchange.asmx":   false,
	} {
		to, _ := http.NewRequest(http.MethodPost, target, nil)
		if err := c.http.CheckRedirect(to, []*http.Request{from}); (err == nil) != ok {
			t.Errorf("redirect to %s: err %v, want allowed %v", target, err, ok)
		}
	}
}
