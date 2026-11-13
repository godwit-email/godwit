package app

import (
	"cmp"
	"context"
	"errors"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/auth"
)

// signInRequest is what a browser sign-in signs in to.
type signInRequest struct {
	Provider, Hint string
	Client         core.OAuthClient
	// Public, if not nil, is signed in with when this build has no OAuth
	// client for Client (auth.ErrOAuthNotConfigured).
	Public *auth.PublicClient
}

// signIn is a finished browser sign-in: the flow whose grant the account or
// service takes, the email it signed in as and the client it signed in with.
type signIn struct {
	Flow, Email string
	Client      core.OAuthClient
}

// signInBrowser runs a whole browser sign-in: it begins the flow, opens its
// page, tells opened (if not nil) the page is open, and waits for the
// browser to come back. A page that does not open ends the sign-in with that
// error: waiting on it would only time out. Once ctx is done the flow is
// cancelled, its grant with it.
func signInBrowser(ctx context.Context, oauth OAuthFlow, open func(url string) error, req signInRequest, opened func(url string)) (signIn, error) {
	s := signIn{Client: req.Client}
	flow, url, err := oauth.Begin(ctx, req.Provider, req.Hint, s.Client)
	if errors.Is(err, auth.ErrOAuthNotConfigured) && req.Public != nil {
		s.Client = core.OAuthClient{ID: req.Public.ID, Secret: req.Public.Secret}
		flow, url, err = oauth.Begin(ctx, req.Provider, req.Hint, s.Client)
	}
	if err != nil {
		return signIn{}, err
	}
	openErr := open(url)
	if openErr == nil {
		if opened != nil {
			opened(url)
		}
		s.Email, err = oauth.Await(ctx, flow)
	}
	if openErr != nil || ctx.Err() != nil {
		oauth.Cancel(flow) // it may still wait; a finished one drops its grant
		return signIn{}, cmp.Or(openErr, ctx.Err())
	}
	if err != nil {
		return signIn{}, err
	}
	s.Flow = flow
	return s, nil
}

// browserLogin is a screen's browser sign-in: Start runs signInBrowser and a
// new Start or Stop cancels the one before. The zero value is ready; its
// state lives on the UI thread.
type browserLogin struct {
	run latest
	url string // the sign-in page while it is open, for 重新打开
}

// Start signs in off the UI thread, then on it brings the window back from
// the browser (not after context.Canceled: the user ended it here) and calls
// done, unless Start or Stop was called since.
func (b *browserLogin) Start(a *App, req signInRequest, done func(signIn, error)) {
	b.Stop()
	oauth, open := a.oauth, a.plat.OpenURL
	var s signIn
	var gen uint64 // set below, read on the UI thread
	b.run.Run(a, func(ctx context.Context) (err error) {
		s, err = signInBrowser(ctx, oauth, open, req, func(url string) {
			a.onUI(func() {
				if b.run.Gen() == gen {
					b.url = url
				}
			})
		})
		return err
	}, func(err error) {
		b.url = ""
		if !errors.Is(err, context.Canceled) {
			a.plat.ShowWindow()
		}
		done(s, err)
	})
	gen = b.run.Gen()
}

// Stop cancels the sign-in in flight, if any, and drops its done.
func (b *browserLogin) Stop() {
	b.run.Stop()
	b.url = ""
}

// waiting: a sign-in is in flight.
func (b *browserLogin) waiting() bool { return b.run.loading }
