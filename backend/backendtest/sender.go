package backendtest

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/backend"
)

// SendServer is one fresh account on a fixture's sending server.
type SendServer interface {
	// Sender starts a new sending backend on the account.
	Sender(t *testing.T) backend.Sender
	// Accepted is every message the server has accepted, in order.
	Accepted(t *testing.T) []Delivery
	// Refuse makes the server refuse, for good, every message to rcpt.
	Refuse(t *testing.T, rcpt string)
	// LoseVerdict makes the server take the whole next message and then drop
	// the connection without answering.
	LoseVerdict(t *testing.T)
	// RejectAuth makes the server refuse the account's credentials from now on.
	RejectAuth(t *testing.T)
}

// Delivery is one message a sending server accepted.
type Delivery struct {
	Rcpts []string // the addresses it goes to
	Raw   []byte   // as the server got it; a backend may add header fields
}

// SendFixture starts a SendServer for one test.
type SendFixture func(t *testing.T) SendServer

// RunSender runs the sending part of the contract against the backend start gives.
func RunSender(t *testing.T, start SendFixture) {
	for _, c := range []struct {
		name string
		test func(*testing.T, SendFixture)
	}{
		{"Send", testSend},
		{"Refused", testRefused},
		{"Uncertain", testUncertain},
		{"SendAuthFailed", testSendAuthFailed},
	} {
		t.Run(c.name, func(t *testing.T) { c.test(t, start) })
	}
}

// The message reaches every recipient, a Bcc one too.
func testSend(t *testing.T, start SendFixture) {
	s := start(t)
	raw := Message("sent@example.com", "hello")
	if err := s.Sender(t).Send(ctx(t), "a@example.com", []string{"b@example.com", "hidden@example.com"}, raw); err != nil {
		t.Fatal(err)
	}
	got := s.Accepted(t)
	if len(got) != 1 {
		t.Fatalf("accepted %d messages, want 1", len(got))
	}
	rcpts := slices.Sorted(slices.Values(got[0].Rcpts))
	for i := range rcpts {
		rcpts[i] = strings.ToLower(rcpts[i])
	}
	if !slices.Equal(rcpts, []string{"b@example.com", "hidden@example.com"}) {
		t.Errorf("recipients = %v, want b@ and hidden@", got[0].Rcpts)
	}
	if !bytes.Contains(got[0].Raw, raw) {
		t.Errorf("the server got %q, want it to hold the message", got[0].Raw)
	}
}

// A refused recipient is a *RejectedError, and nothing is accepted.
func testRefused(t *testing.T, start SendFixture) {
	s := start(t)
	s.Refuse(t, "nobody@example.com")
	err := s.Sender(t).Send(ctx(t), "a@example.com", []string{"nobody@example.com"}, Message("refused@example.com", "no"))
	var re *backend.RejectedError
	if !errors.As(err, &re) || re.Code < 500 {
		t.Fatalf("Send = %v, want a permanent *RejectedError", err)
	}
	if n := len(s.Accepted(t)); n != 0 {
		t.Errorf("accepted %d messages, want none", n)
	}
}

// A message whose verdict is lost may have been accepted: ErrUncertain.
func testUncertain(t *testing.T, start SendFixture) {
	s := start(t)
	s.LoseVerdict(t)
	err := s.Sender(t).Send(ctx(t), "a@example.com", []string{"b@example.com"}, Message("lost@example.com", "lost"))
	if !errors.Is(err, backend.ErrUncertain) {
		t.Fatalf("Send = %v, want ErrUncertain", err)
	}
}

// Refused credentials are ErrAuthFailed.
func testSendAuthFailed(t *testing.T, start SendFixture) {
	s := start(t)
	s.RejectAuth(t)
	err := s.Sender(t).Send(ctx(t), "a@example.com", []string{"b@example.com"}, Message("auth@example.com", "auth"))
	if !errors.Is(err, backend.ErrAuthFailed) {
		t.Fatalf("Send = %v, want ErrAuthFailed", err)
	}
}
