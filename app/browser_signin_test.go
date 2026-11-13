package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
)

func signInApp(t *testing.T) (a *App, step func(), fo *fakeOAuth, plat *fakePlatform) {
	a, step = uiApp(t)
	fo, plat = &fakeOAuth{flows: map[string]chan flowResult{}}, &fakePlatform{}
	a.oauth, a.plat = fo, plat
	return a, step, fo, plat
}

var outlookReq = signInRequest{Provider: "outlook", Hint: "me@outlook.com"}

func TestBrowserLoginSignsIn(t *testing.T) {
	a, step, fo, plat := signInApp(t)
	var b browserLogin
	var got signIn
	b.Start(a, outlookReq, func(s signIn, err error) {
		if err != nil {
			t.Errorf("err %v", err)
		}
		got = s
	})
	if !b.waiting() {
		t.Fatal("not waiting")
	}
	step() // the page is open
	if b.url != "https://login.example/authorize?n=1" {
		t.Fatalf("url %q while waiting", b.url)
	}
	fo.finish("f1", "me@outlook.com")
	step()
	if want := (signIn{Flow: "f1", Email: "me@outlook.com"}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if b.waiting() || b.url != "" {
		t.Fatalf("after: waiting %v, url %q", b.waiting(), b.url)
	}
	if !slices.Contains(plat.Calls(), "ShowWindow") {
		t.Fatal("the window stayed behind the browser")
	}
	if c := fo.Canceled(); len(c) != 0 {
		t.Fatalf("canceled %q: the account needs the grant", c)
	}
}

// Stop cancels the flow; nothing lands and the window stays where it is.
func TestBrowserLoginStop(t *testing.T) {
	a, step, fo, plat := signInApp(t)
	var b browserLogin
	b.Start(a, outlookReq, func(signIn, error) { t.Error("a stopped sign-in landed") })
	step() // the page is open
	b.Stop()
	step() // the sign-in's end, dropped
	if c := fo.Canceled(); !slices.Equal(c, []string{"f1"}) {
		t.Fatalf("canceled %q", c)
	}
	if b.waiting() || b.url != "" || slices.Contains(plat.Calls(), "ShowWindow") {
		t.Fatalf("waiting %v, url %q, calls %q", b.waiting(), b.url, plat.Calls())
	}
}

// A new sign-in cancels the one before, whose result never lands.
func TestBrowserLoginSupersedes(t *testing.T) {
	a, step, fo, _ := signInApp(t)
	var b browserLogin
	var got []string
	b.Start(a, outlookReq, func(s signIn, _ error) { got = append(got, "first "+s.Flow) })
	step() // the first page is open
	b.Start(a, outlookReq, func(s signIn, _ error) { got = append(got, s.Flow) })
	step() // the first's end and the second's page, in either order
	step()
	if b.url != "https://login.example/authorize?n=2" {
		t.Fatalf("url %q, want the second page", b.url)
	}
	fo.finish("f2", "me@outlook.com")
	step()
	if !slices.Equal(got, []string{"f2"}) || !slices.Equal(fo.Canceled(), []string{"f1"}) {
		t.Fatalf("landed %q, canceled %q", got, fo.Canceled())
	}
}

// A page that does not open ends the sign-in with its error and cancels the flow.
func TestBrowserLoginNoBrowser(t *testing.T) {
	a, step, fo, plat := signInApp(t)
	noBrowser := errors.New("no browser")
	plat.openErr = noBrowser
	var b browserLogin
	var got error
	b.Start(a, outlookReq, func(_ signIn, err error) { got = err })
	step()
	if !errors.Is(got, noBrowser) || !slices.Equal(fo.Canceled(), []string{"f1"}) || b.waiting() {
		t.Fatalf("err %v, canceled %q, waiting %v", got, fo.Canceled(), b.waiting())
	}
}

// With no OAuth client in this build for the one asked, the public one signs in.
func TestSignInFallsBackToPublic(t *testing.T) {
	fo, plat := &fakeOAuth{flows: map[string]chan flowResult{}, noClient: true}, &fakePlatform{}
	pub := &auth.PublicClient{ID: "pub", Secret: "pub-secret"}
	type result struct {
		s   signIn
		err error
	}
	done := make(chan result, 1)
	go func() {
		s, err := signInBrowser(context.Background(), fo, plat.OpenURL,
			signInRequest{Provider: core.ProviderGoogleCardDAV, Client: core.OAuthClient{AddressBook: 7}, Public: pub}, nil)
		done <- result{s, err}
	}()
	for deadline := time.Now().Add(5 * time.Second); len(fo.began()) < 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("began %+v", fo.began())
		}
	}
	fo.finish("f1", "me@gmail.com")
	r := <-done
	public := core.OAuthClient{ID: "pub", Secret: "pub-secret"}
	if r.err != nil || r.s != (signIn{Flow: "f1", Email: "me@gmail.com", Client: public}) {
		t.Fatalf("got %+v, %v", r.s, r.err)
	}
	if want := []core.OAuthClient{{AddressBook: 7}, public}; !slices.Equal(fo.began(), want) {
		t.Fatalf("began %+v, want %+v", fo.began(), want)
	}
}
