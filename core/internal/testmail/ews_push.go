package testmail

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The streaming notifications side of the fake EWS server: Subscribe
// (streaming, all folders) and GetStreamingEvents, with the affinity cookie
// of a server behind several CAS, and fault injection (LoseSubscriptions,
// DropStreams, Advance for the connections' 30 minutes).

// EWSAffinity is what a signed-in request carried for subscription affinity.
type EWSAffinity struct {
	Op     string
	Anchor string // X-AnchorMailbox
	Prefer string // X-PreferServerAffinity
	Cookie string // the X-BackEndOverrideCookie cookie
}

// ewsSubscription is a streaming subscription: it reports what changed
// since seq, the sequence number it last reported up to.
type ewsSubscription struct {
	id, cookie string
	seq        int
}

// backEndCookie is the X-BackEndOverrideCookie the server sets: the
// mailbox server holding the subscriptions.
const backEndCookie = "MAIL03~1942061003"

// Affinity is what each signed-in request carried for affinity, in order.
func (s *EWS) Affinity() []EWSAffinity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.affinity)
}

// Subscriptions is how many subscriptions the server holds.
func (s *EWS) Subscriptions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// Streams is how many GetStreamingEvents connections have been opened.
func (s *EWS) Streams() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams
}

// WaitStreams waits until n GetStreamingEvents connections have been opened.
func (s *EWS) WaitStreams(t testing.TB, n int) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); s.Streams() < n; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("fake EWS: %d streaming connections, want %d", s.Streams(), n)
		}
	}
}

// LoseSubscriptions drops every subscription, as a mailbox server that
// restarted does: open connections end with ErrorSubscriptionNotFound.
func (s *EWS) LoseSubscriptions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = nil
	s.broadcast()
}

// RefuseSubscribe makes the server answer every Subscribe with ResponseCode
// code, such as ErrorExceededSubscriptionCount when other clients hold the
// account's subscriptions.
func (s *EWS) RefuseSubscribe(code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuseSub = code
}

// DropStreams breaks the open streaming connections mid-response.
func (s *EWS) DropStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drops++
	s.broadcast()
}

// Advance moves the server's clock on by d: streaming connections whose
// ConnectionTimeout it passes are closed.
func (s *EWS) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skew += d
	s.broadcast()
}

func (s *EWS) now() time.Time { return time.Now().Add(s.skew) }

// broadcast wakes the open streams to look at the mailbox again. Hold mu.
func (s *EWS) broadcast() {
	if s.changes != nil {
		close(s.changes)
	}
	s.changes = make(chan struct{})
}

func (s *EWS) sub(id string) *ewsSubscription {
	for _, sub := range s.subs {
		if sub.id == id {
			return sub
		}
	}
	return nil
}

func (s *EWS) subscribe(w http.ResponseWriter, r *http.Request, req ewsRequest) {
	reply := func(msg string) {
		s.respond(w, styleEnvelopeDecl, "Subscribe", "<m:ResponseMessages>"+msg+"</m:ResponseMessages>")
	}
	if !req.allFolders || !slices.Equal(req.eventTypes, []string{"NewMailEvent", "CreatedEvent", "DeletedEvent", "ModifiedEvent", "MovedEvent", "CopiedEvent"}) {
		s.t.Errorf("fake EWS: Subscribe to all folders %v for %v", req.allFolders, req.eventTypes)
		reply(message("Subscribe", "ErrorInvalidRequest", ""))
		return
	}
	if s.refuseSub != "" {
		reply(message("Subscribe", s.refuseSub, ""))
		return
	}
	sub := &ewsSubscription{id: s.newID("SubscriptionId"), seq: s.seq}
	switch {
	case cookieOf(r) != "":
		sub.cookie = cookieOf(r)
	case r.Header.Get("X-AnchorMailbox") != "" && r.Header.Get("X-PreferServerAffinity") == "true":
		sub.cookie = backEndCookie
		http.SetCookie(w, &http.Cookie{Name: "X-BackEndOverrideCookie", Value: backEndCookie, Path: "/", HttpOnly: true})
	}
	s.subs = append(s.subs, sub)
	reply(message("Subscribe", "", "<m:SubscriptionId>"+sub.id+"</m:SubscriptionId>"))
}

func cookieOf(r *http.Request) string {
	if c, err := r.Cookie("X-BackEndOverrideCookie"); err == nil {
		return c.Value
	}
	return ""
}

// getStreamingEvents holds the connection open, writing a notification of
// what changed for each change, until the ConnectionTimeout passes on the
// server's clock (ConnectionStatus Closed), the subscription is lost, or
// DropStreams. Called with mu held; it lets go of it while it waits.
// A subscription made with affinity is found only with its cookie, as
// another mailbox server would not have it.
func (s *EWS) getStreamingEvents(w http.ResponseWriter, r *http.Request, req ewsRequest) {
	const op = "GetStreamingEvents"
	write := func(msg string) {
		fmt.Fprintf(w, `<Envelope xmlns="%s"><soap11:Header xmlns:soap11="%s">%s</soap11:Header><soap11:Body xmlns:soap11="%s"><m:%sResponse xmlns:m="%s" xmlns:t="%s"><m:ResponseMessages>%s</m:ResponseMessages></m:%sResponse></soap11:Body></Envelope>`,
			nsSOAP, nsSOAP, s.versionHeader("t", true), nsSOAP, op, nsMessages, nsTypes, msg, op)
		w.(http.Flusher).Flush()
	}
	minutes, err := strconv.Atoi(req.text["ConnectionTimeout"])
	if err != nil || minutes < 1 || minutes > 30 {
		s.t.Errorf("fake EWS: ConnectionTimeout %q", req.text["ConnectionTimeout"])
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	sub := s.sub(req.text["SubscriptionId"])
	if sub == nil || sub.cookie != "" && sub.cookie != cookieOf(r) {
		write(message(op, "ErrorSubscriptionNotFound", ""))
		return
	}
	s.streams++
	end, drops := s.now().Add(time.Duration(minutes)*time.Minute), s.drops
	for {
		if events := s.events(sub); events != "" {
			write(message(op, "", "<m:Notifications><m:Notification><t:SubscriptionId>"+sub.id+"</t:SubscriptionId>"+events+"</m:Notification></m:Notifications><m:ConnectionStatus>OK</m:ConnectionStatus>"))
		}
		switch {
		case s.drops != drops:
			panic(http.ErrAbortHandler) // serve's deferred Unlock still runs
		case s.sub(sub.id) == nil:
			write(message(op, "ErrorSubscriptionNotFound", ""))
			return
		case !s.now().Before(end):
			write(message(op, "", "<m:ConnectionStatus>Closed</m:ConnectionStatus>"))
			return
		}
		changes := s.changes
		s.mu.Unlock()
		select {
		case <-changes:
		case <-r.Context().Done():
		}
		s.mu.Lock()
		if r.Context().Err() != nil {
			return
		}
	}
}

// events is what changed since the subscription last reported, as the
// notification's events, and moves it on.
func (s *EWS) events(sub *ewsSubscription) string {
	var b strings.Builder
	stamp := s.now().UTC().Format(time.RFC3339)
	for _, f := range s.folders[1:] {
		if f.modseq > sub.seq {
			kind := "Modified"
			switch {
			case f.gone:
				kind = "Deleted"
			case f.created > sub.seq:
				kind = "Created"
			}
			fmt.Fprintf(&b, `<t:%sEvent><t:TimeStamp>%s</t:TimeStamp><t:FolderId Id="%s"/><t:ParentFolderId Id="%s"/></t:%sEvent>`, kind, stamp, f.id, f.parent, kind)
		}
		for _, it := range f.items {
			if it.modseq <= sub.seq {
				continue
			}
			kinds := []string{"Modified"}
			switch {
			case it.gone:
				kinds = []string{"Deleted"}
			case it.created > sub.seq:
				kinds = []string{"Created", "NewMail"}
			}
			for _, kind := range kinds {
				fmt.Fprintf(&b, `<t:%sEvent><t:TimeStamp>%s</t:TimeStamp><t:ItemId Id="%s" ChangeKey="CQAAAA=="/><t:ParentFolderId Id="%s" ChangeKey="AQAAAA=="/></t:%sEvent>`, kind, stamp, it.id, f.id, kind)
			}
		}
	}
	sub.seq = s.seq
	return b.String()
}
