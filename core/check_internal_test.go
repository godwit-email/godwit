package core

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/ews"
	"github.com/godwit-email/godwit/backend/gmailapi"
	"github.com/godwit-email/godwit/backend/graph"
	"github.com/godwit-email/godwit/backend/imapbackend"
	"github.com/godwit-email/godwit/backend/jmap"
	"github.com/godwit-email/godwit/backend/pop3"
	"github.com/godwit-email/godwit/backend/smtpbackend"
	"github.com/godwit-email/godwit/core/auth"
)

// An EWS URL keeps its escaped path from discovery to the URL the backend
// is given: escaped once, not twice.
func TestEWSSlotURLRoundTrip(t *testing.T) {
	for _, u := range []string{
		"https://mail.example.com/EWS/Exchange.asmx",
		"https://mail.example.com/Exchange%20Services/Exchange.asmx",
		"http://127.0.0.1:8080/a%2Fb/Exchange.asmx",
	} {
		srv, err := ewsSlot(u)
		if err != nil {
			t.Fatal(err)
		}
		want := u
		if srv.Port == 443 {
			want = "https://mail.example.com:443" + u[len("https://mail.example.com"):]
		}
		if got := slotURL(srv, srv.Path); got != want {
			t.Errorf("ewsSlot(%q) then slotURL = %q, want %q", u, got, want)
		}
	}
}

// The protocol table answers what the protocol comparisons it replaced did.
// Each old* below is one of those comparisons as it stood, the oracle.
func TestProtocolTable(t *testing.T) {
	oldNewReceiver := func(srv Server, email string, creds backend.CredentialsFunc) backend.Receiver { // newReceiver
		switch srv.Protocol {
		case auth.JMAP:
			return jmap.New(jmap.Config{SessionURL: sessionURL(srv), Credentials: creds})
		case auth.EWS:
			return ews.New(ews.Config{URL: slotURL(srv, srv.Path), Address: email, Credentials: creds})
		case auth.POP3:
			return pop3.New(pop3.Config{Host: srv.Host, Port: srv.Port, Security: srv.Security, Credentials: creds})
		case auth.Graph:
			return graph.NewReceiver(graph.Config{URL: graphURL(srv), Credentials: creds})
		case auth.GmailAPI:
			return gmailapi.New(gmailapi.Config{BaseURL: slotURL(srv, srv.Path), Credentials: creds})
		}
		return imapbackend.New(imapbackend.Config{Host: srv.Host, Port: srv.Port, Security: srv.Security, Credentials: creds})
	}
	oldSender := func(acc Account, creds backend.CredentialsFunc, sent *backend.FolderRef) backend.Sender { // sendAuth.sender
		switch acc.Sending.Protocol {
		case auth.Graph:
			u := graphSendURL
			if acc.Receiving.Protocol == auth.Graph {
				u = graphURL(acc.Receiving) + "/me/sendMail"
			}
			return graph.New(u, creds)
		case auth.EWS:
			return ews.NewSender(ews.Config{URL: ewsURL(acc), Credentials: creds})
		case auth.GmailAPI:
			return gmailapi.NewSender(gmailapi.Config{BaseURL: slotURL(acc.Receiving, acc.Receiving.Path), Credentials: creds})
		case auth.JMAP:
			return jmap.NewSender(jmap.Config{SessionURL: sessionURL(acc.Receiving), Credentials: creds, SentFolder: sent})
		}
		return smtpbackend.New(smtpbackend.Config{Host: acc.Sending.Host, Port: acc.Sending.Port, Security: acc.Sending.Security, Credentials: creds})
	}
	oldSending := func(recv string) string { // AccountInput.sending
		switch recv {
		case auth.EWS, auth.JMAP, auth.GmailAPI:
			return recv
		}
		if recv == auth.Graph {
			return auth.Graph
		}
		return auth.SMTP
	}
	// accounts.go and account_import.go: p == auth.EWS || p == auth.JMAP ||
	// p == auth.GmailAPI, each followed by a Graph variant.
	oldViaReceiving := func(p string) bool { return p == auth.EWS || p == auth.JMAP || p == auth.GmailAPI || p == auth.Graph }
	oldReceivable := func(recv string, allowPOP3, allowGmailAPI bool) bool { // validate
		return recv == auth.IMAP || recv == auth.JMAP || recv == auth.EWS || recv == auth.POP3 && allowPOP3 || recv == auth.Graph || recv == auth.GmailAPI && allowGmailAPI
	}
	oldSteps := func(recv string) []string { // check
		switch recv {
		case auth.EWS, auth.Graph, auth.GmailAPI:
			return []string{"settings", recv}
		case auth.JMAP:
			return []string{"settings", recv, "jmap_send"}
		}
		return []string{"settings", recv, "smtp"}
	}
	oldOff := map[string]struct { // AddAccount
		on  func(Settings) bool
		err error
	}{
		auth.JMAP:     {func(s Settings) bool { return s.ExperimentalJMAP }, ErrJMAPOff},
		auth.EWS:      {func(s Settings) bool { return s.ExperimentalEWS }, ErrEWSOff},
		auth.POP3:     {func(s Settings) bool { return s.ExperimentalPOP3 }, errPOP3Off},
		auth.Graph:    {func(s Settings) bool { return s.ExperimentalGraph }, ErrGraphOff},
		auth.GmailAPI: {func(s Settings) bool { return s.ExperimentalGmailAPI }, errGmailAPIOff},
	}
	allOn := Settings{ExperimentalJMAP: true, ExperimentalEWS: true, ExperimentalPOP3: true, ExperimentalGraph: true, ExperimentalGmailAPI: true}

	sent := new(backend.FolderRef)
	for _, p := range []string{"", auth.IMAP, auth.SMTP, auth.POP3, auth.JMAP, auth.EWS, auth.Graph, auth.GmailAPI, "bogus"} {
		srv := Server{Protocol: p, Host: "mail.example.com", Port: 993, Security: SecurityTLS, Path: "/p"}
		// Same type: the clients inside hold closures, which DeepEqual never
		// finds equal; coretest runs each one against its server.
		if got, want := newReceiver(srv, "a@example.com", nil), oldNewReceiver(srv, "a@example.com", nil); reflect.TypeOf(got) != reflect.TypeOf(want) {
			t.Errorf("%q: receiver %#v, want %#v", p, got, want)
		}
		for _, recv := range []string{auth.IMAP, p} { // an SMTP account moved to Graph, and one sending with its own protocol
			acc := Account{Receiving: Server{Protocol: recv, Host: "in.example.com", Port: 443, Security: SecurityTLS, Path: "/r"},
				Sending: Server{Protocol: p, Host: "out.example.com", Port: 465, Security: SecurityTLS}}
			got, want := newSender(acc, nil, sent), oldSender(acc, nil, sent)
			if reflect.TypeOf(got) != reflect.TypeOf(want) || p == auth.Graph && !reflect.DeepEqual(got, want) { // Graph's URL depends on receiving
				t.Errorf("%q receiving %q: sender %#v, want %#v", p, recv, got, want)
			}
		}
		in := AccountInput{Email: "a@example.com", Receiving: Server{Protocol: p}}
		if got, want := in.sending(), oldSending(in.receiving()); got != want {
			t.Errorf("%q: sends with %q, want %q", p, got, want)
		}
		if got, want := sendsViaReceiving(p), oldViaReceiving(p); got != want {
			t.Errorf("%q: sends via receiving %v, want %v", p, got, want)
		}
		for _, allow := range []bool{false, true} {
			in.allowed = allow
			err := in.validate()
			got := err == nil || !strings.Contains(err.Error(), "receiving protocol")
			if want := oldReceivable(in.receiving(), allow, allow); got != want {
				t.Errorf("%q allowed=%v: receivable %v (%v), want %v", p, allow, got, err, want)
			}
		}
		bad := AccountInput{Email: "not an address", Receiving: Server{Protocol: p}}
		r, _ := (&Service{}).check(context.Background(), bad, "")
		var steps []string
		for _, s := range r.Steps {
			steps = append(steps, s.Name)
		}
		if want := oldSteps(bad.receiving()); !slices.Equal(steps, want) {
			t.Errorf("%q: check steps %v, want %v", p, steps, want)
		}
		e := receivingProtocol(p)
		if old, ok := oldOff[p]; ok {
			if e.setting == nil || e.setting(Settings{}) || !e.setting(allOn) || old.on(Settings{}) || e.off != old.err {
				t.Errorf("%q: not gated by its setting with %v", p, old.err)
			}
		} else if e.setting != nil {
			t.Errorf("%q: gated, want not", p)
		}
	}
}
