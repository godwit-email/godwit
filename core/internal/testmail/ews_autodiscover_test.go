package testmail_test

import (
	"context"
	"errors"
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/ews"
	"github.com/godwit-email/godwit/core/internal/testmail"
)

func discover(t *testing.T, s *testmail.EWS, ad *testmail.Autodiscover, email string, confirmed ...string) (string, error) {
	t.Helper()
	return ews.Discover(tctx(t), ews.DiscoverConfig{Email: email, Confirmed: confirmed, Net: ad.Net(),
		Credentials: func(context.Context) (backend.Credentials, error) {
			return backend.Credentials{Username: "alice", Password: s.Password()}, nil
		}})
}

// Autodiscover finds the EWS URL in either form, on the address's domain or
// its autodiscover host, and follows redirects within the domain.
func TestEWSAutodiscover(t *testing.T) {
	for name, setup := range map[string]func(*testmail.Autodiscover){
		"domain SOAP":  func(ad *testmail.Autodiscover) { ad.Serve("example.com") },
		"autodiscover": func(ad *testmail.Autodiscover) { ad.Serve("autodiscover.example.com") },
		"POX only":     func(ad *testmail.Autodiscover) { ad.ServePOX("autodiscover.example.com") },
		"same-domain URL": func(ad *testmail.Autodiscover) {
			ad.RedirectURL("example.com", "mail.example.com")
			ad.Serve("mail.example.com")
		},
		"same-domain 302": func(ad *testmail.Autodiscover) {
			ad.Moved("example.com", "mail.example.com")
			ad.Serve("mail.example.com")
		},
		"same-domain GET": func(ad *testmail.Autodiscover) {
			ad.RedirectGET("autodiscover.example.com", "mail.example.com")
			ad.ServePOX("mail.example.com")
		},
		"same-domain SRV": func(ad *testmail.Autodiscover) {
			ad.SRV("example.com", "mail.example.com")
			ad.Serve("mail.example.com")
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := testmail.StartEWS(t)
			ad := s.StartAutodiscover()
			setup(ad)
			got, err := discover(t, s, ad, "alice@example.com")
			if err != nil || got != s.URL() {
				t.Fatalf("Discover = %q, %v; want %q", got, err, s.URL())
			}
		})
	}
}

// A hop to a host outside the address's registrable domain stops before the
// credentials go there, naming the host; once confirmed, it is followed.
func TestEWSAutodiscoverOtherDomain(t *testing.T) {
	for name, setup := range map[string]func(*testmail.Autodiscover){
		"redirectUrl":  func(ad *testmail.Autodiscover) { ad.RedirectURL("example.com", "ad.example.net") },
		"302":          func(ad *testmail.Autodiscover) { ad.Moved("example.com", "ad.example.net") },
		"plain GET":    func(ad *testmail.Autodiscover) { ad.RedirectGET("autodiscover.example.com", "ad.example.net") },
		"SRV":          func(ad *testmail.Autodiscover) { ad.SRV("example.com", "ad.example.net") },
		"redirectAddr": func(ad *testmail.Autodiscover) { ad.RedirectAddr("example.com", "alice@ad.example.net") },
	} {
		t.Run(name, func(t *testing.T) {
			s := testmail.StartEWS(t)
			ad := s.StartAutodiscover()
			setup(ad)
			ad.Serve("ad.example.net")
			_, err := discover(t, s, ad, "alice@example.com")
			var confirm *ews.ConfirmError
			if !errors.As(err, &confirm) || confirm.Host != "ad.example.net" {
				t.Fatalf("Discover err = %v, want a confirmation of ad.example.net", err)
			}
			if ad.Credentialed("ad.example.net") {
				t.Fatal("the credentials went to ad.example.net before it was confirmed")
			}
			got, err := discover(t, s, ad, "alice@example.com", "ad.example.net")
			if err != nil || got != s.URL() {
				t.Fatalf("confirmed: Discover = %q, %v; want %q", got, err, s.URL())
			}
		})
	}
}

// An EWS URL outside the address's registrable domain, from an Autodiscover
// inside it, is confirmed before it is returned to sign in at.
func TestEWSAutodiscoverOtherDomainEWSURL(t *testing.T) {
	const other = "https://ews.example.net/EWS/Exchange.asmx"
	for name, serve := range map[string]func(*testmail.Autodiscover){
		"SOAP": func(ad *testmail.Autodiscover) { ad.Serve("example.com") },
		"POX":  func(ad *testmail.Autodiscover) { ad.ServePOX("example.com") },
	} {
		t.Run(name, func(t *testing.T) {
			s := testmail.StartEWS(t)
			ad := s.StartAutodiscover()
			serve(ad)
			ad.AnswerEWSURL(other)
			got, err := discover(t, s, ad, "alice@example.com")
			var confirm *ews.ConfirmError
			if !errors.As(err, &confirm) || confirm.Host != "ews.example.net" {
				t.Fatalf("Discover = %q, %v; want a confirmation of ews.example.net", got, err)
			}
			if got, err := discover(t, s, ad, "alice@example.com", "ews.example.net"); err != nil || got != other {
				t.Fatalf("confirmed: Discover = %q, %v; want %q", got, err, other)
			}
		})
	}
}

// Nowhere to find it is ErrNoAutodiscover; a wrong password everywhere it
// was asked is ErrAuthFailed.
func TestEWSAutodiscoverFails(t *testing.T) {
	s := testmail.StartEWS(t)
	ad := s.StartAutodiscover()
	if _, err := discover(t, s, ad, "alice@example.com"); !errors.Is(err, ews.ErrNoAutodiscover) {
		t.Fatalf("nothing served: err = %v, want ErrNoAutodiscover", err)
	}
	ad.Serve("autodiscover.example.com")
	s.RejectAuth()
	if _, err := discover(t, s, ad, "alice@example.com"); !errors.Is(err, backend.ErrAuthFailed) {
		t.Fatalf("wrong password: err = %v, want ErrAuthFailed", err)
	}
}

// Autodiscover signs in over NTLM where that is what the server offers.
func TestEWSAutodiscoverNTLM(t *testing.T) {
	s := testmail.StartEWS(t)
	s.UseNTLM()
	ad := s.StartAutodiscover()
	ad.Serve("example.com")
	got, err := ews.Discover(tctx(t), ews.DiscoverConfig{Email: "alice@example.com", Net: ad.Net(),
		Credentials: func(context.Context) (backend.Credentials, error) {
			return backend.Credentials{Username: `EXAMPLE\alice`, Password: "secret"}, nil
		}})
	if err != nil || got != s.URL() {
		t.Fatalf("Discover = %q, %v; want %q", got, err, s.URL())
	}
}
