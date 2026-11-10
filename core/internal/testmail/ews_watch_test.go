package testmail_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/backendtest"
	"github.com/godwit-email/godwit/core/internal/testmail"
)

// The streaming connection signs in over NTLM like any other request.
func TestEWSWatchNTLM(t *testing.T) {
	s := testmail.StartEWS(t)
	s.UseNTLM()
	wakes, done := watching(t, ewsNTLMServer{ewsServer{s}}.Receiver(t))
	nextWake(t, wakes, done)
	s.WaitStreams(t, 1)
	s.Deliver("INBOX", backendtest.Message("a@example.com", "a"))
	nextWake(t, wakes, done)
}

// A server closed while a client still streams from it closes at once,
// not when the client's stream goes idle: the client's own cleanup, as a
// service's in coretest, may run only after the server's.
func TestEWSCloseWhileStreaming(t *testing.T) {
	ctx, cancel := context.WithCancel(tctx(t))
	defer cancel() // after the subtest's server has closed
	start := time.Now()
	t.Run("streaming", func(t *testing.T) {
		s := testmail.StartEWS(t)
		go ewsReceiver(t, s).Watch(ctx, func([]backend.FolderRef) {})
		s.WaitStreams(t, 1)
	})
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("closing took %v", d)
	}
}

// watching runs Watch until the test ends; wakes gets each wake's folders,
// done Watch's return.
func watching(t *testing.T, r backend.Receiver) (wakes chan []backend.FolderRef, done chan error) {
	ctx, cancel := context.WithCancel(tctx(t))
	wakes, done = make(chan []backend.FolderRef, 100), make(chan error, 1)
	go func() {
		done <- r.Watch(ctx, func(fs []backend.FolderRef) { wakes <- fs })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Watch did not return when cancelled")
		}
	})
	return wakes, done
}

// nextWake is the next wake's folders.
func nextWake(t *testing.T, wakes chan []backend.FolderRef, done chan error) []backend.FolderRef {
	t.Helper()
	select {
	case fs := <-wakes:
		return fs
	case err := <-done:
		t.Fatalf("Watch = %v, want a wake", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no wake")
	}
	return nil
}

// inboxRef is the inbox's FolderRef.
func inboxRef(t *testing.T, s *testmail.EWS) backend.FolderRef {
	var l local
	l.sync(t, s)
	return l.ref(t, "Inbox")
}

// New mail wakes Watch for the folder it landed in, and a connection that
// reached its 30 minutes is opened again on the same subscription.
func TestEWSWatchWakesForFolder(t *testing.T) {
	s := testmail.StartEWS(t)
	inbox := inboxRef(t, s)
	wakes, done := watching(t, ewsReceiver(t, s))
	nextWake(t, wakes, done) // as it starts watching
	s.WaitStreams(t, 1)
	s.Deliver("INBOX", backendtest.Message("a@example.com", "a"))
	if fs := nextWake(t, wakes, done); !slices.Equal(fs, []backend.FolderRef{inbox}) {
		t.Errorf("woken for %v, want the inbox %v", fs, inbox)
	}
	s.Advance(30 * time.Minute)
	s.WaitStreams(t, 2)
	s.Deliver("INBOX", backendtest.Message("b@example.com", "b"))
	if fs := nextWake(t, wakes, done); !slices.Equal(fs, []backend.FolderRef{inbox}) {
		t.Errorf("after the connection expired, woken for %v, want the inbox", fs)
	}
	if n := s.Subscriptions(); n != 1 {
		t.Errorf("%d subscriptions, want the one kept across connections", n)
	}
}

// A lost subscription is made again, with a wake for what was missed.
func TestEWSWatchSubscriptionLost(t *testing.T) {
	s := testmail.StartEWS(t)
	inbox := inboxRef(t, s)
	wakes, done := watching(t, ewsReceiver(t, s))
	nextWake(t, wakes, done)
	s.WaitStreams(t, 1)
	s.LoseSubscriptions()
	if fs := nextWake(t, wakes, done); fs != nil {
		t.Errorf("woken for %v after subscribing again, want anywhere", fs)
	}
	s.WaitStreams(t, 2)
	s.Deliver("INBOX", backendtest.Message("a@example.com", "a"))
	if fs := nextWake(t, wakes, done); !slices.Equal(fs, []backend.FolderRef{inbox}) {
		t.Errorf("woken for %v, want the inbox", fs)
	}
	if n := s.Subscriptions(); n != 1 {
		t.Errorf("%d subscriptions, want 1", n)
	}
}

// A dropped connection ends Watch with an error, so core polls.
func TestEWSWatchConnectionDropped(t *testing.T) {
	s := testmail.StartEWS(t)
	wakes, done := watching(t, ewsReceiver(t, s))
	nextWake(t, wakes, done)
	s.WaitStreams(t, 1)
	s.DropStreams()
	select {
	case err := <-done:
		done <- err // for the cleanup
		if err == nil || errors.Is(err, context.Canceled) {
			t.Errorf("Watch after the connection dropped = %v, want an error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Watch kept going after the connection dropped")
	}
}

// The first Subscribe asks for affinity with the account's mailbox; every
// later request carries the backend cookie it set, the sync's included,
// and the stream does not hold up the account's other requests.
func TestEWSWatchAffinity(t *testing.T) {
	s := testmail.StartEWS(t)
	wakes, done := watching(t, ewsReceiver(t, s))
	nextWake(t, wakes, done)
	s.WaitStreams(t, 1)
	syncAll(t, ewsReceiver(t, s))
	hs := s.Affinity()
	// from the Subscribe: a closed server on the same port may have left this account a subscription
	hs = hs[max(0, slices.IndexFunc(hs, func(h testmail.EWSAffinity) bool { return h.Op == "Subscribe" })):]
	if len(hs) == 0 || hs[0].Op != "Subscribe" || hs[0].Anchor != "alice@example.com" || hs[0].Prefer != "true" || hs[0].Cookie != "" {
		t.Fatalf("first request %+v, want Subscribe anchored on alice@example.com", hs)
	}
	for _, h := range hs[1:] {
		if h.Cookie == "" || h.Prefer != "true" {
			t.Errorf("%s without the backend cookie: %+v", h.Op, h)
		}
	}
	if !slices.ContainsFunc(hs, func(h testmail.EWSAffinity) bool { return h.Op == "SyncFolderItems" }) {
		t.Errorf("requests %+v, want the sync's among them", hs)
	}
}
