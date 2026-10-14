package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// DeviceFlow is one RFC 8628 device-authorization flow, polling the token
// endpoint while the user approves the code in a browser.
type DeviceFlow struct {
	cfg        OAuthConfig
	hc         *http.Client
	deviceCode string
	userCode   string
	authURL    string
	interval   time.Duration
	deadline   time.Time
	ctx        context.Context
	cancel     context.CancelFunc

	finishOnce sync.Once
	done       chan struct{}
	grant      Grant
	err        error
}

type deviceResp struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
	Error                   string `json:"error"`
}

// deviceStartTimeout bounds the device-code request.
const deviceStartTimeout = 30 * time.Second

// StartDevice requests a device code at cfg.DeviceURL and starts polling
// cfg.TokenURL in the background.
func StartDevice(ctx context.Context, cfg OAuthConfig, hc *http.Client) (*DeviceFlow, error) {
	if cfg.ClientID == "" || cfg.DeviceURL == "" {
		return nil, ErrOAuthNotConfigured
	}
	form := url.Values{"client_id": {cfg.ClientID}}
	if len(cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	for k, v := range cfg.AuthParams {
		form.Set(k, v)
	}
	// Bounded: no flow exists yet for the user to cancel while this waits.
	rctx, cancel := context.WithTimeout(ctx, deviceStartTimeout)
	b, status, err := postForm(rctx, hc, cfg.DeviceURL, form)
	cancel()
	if err != nil {
		return nil, err
	}
	var dr deviceResp
	if json.Unmarshal(b, &dr) != nil {
		return nil, errBadTokenResponse
	}
	if dr.Error != "" || status < 200 || status >= 300 {
		return nil, tokenCodeError(dr.Error)
	}
	// The complete URI only: Godwit opens it and never shows the user code to type in.
	authURL := dr.VerificationURIComplete
	if dr.DeviceCode == "" || !validDeviceURI(authURL) {
		return nil, errBadTokenResponse
	}
	interval := 5 * time.Second
	if dr.Interval > 0 {
		interval = time.Duration(dr.Interval) * time.Second
	}
	timeout := FlowTimeout
	if dr.ExpiresIn > 0 {
		timeout = min(time.Duration(dr.ExpiresIn)*time.Second, FlowTimeout)
	}
	f := &DeviceFlow{
		cfg:        cfg,
		hc:         hc,
		deviceCode: dr.DeviceCode,
		userCode:   dr.UserCode,
		authURL:    authURL,
		interval:   interval,
		deadline:   time.Now().Add(timeout),
		done:       make(chan struct{}),
	}
	f.ctx, f.cancel = context.WithCancel(context.Background())
	go f.poll()
	return f, nil
}

func validDeviceURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	default:
		return false
	}
}

// AuthURL is the verification URL (verification_uri_complete when given, else verification_uri).
func (f *DeviceFlow) AuthURL() string { return f.authURL }

// UserCode is the short code shown to the user.
func (f *DeviceFlow) UserCode() string { return f.userCode }

// Wait blocks until the device flow finishes or ctx is done, then closes the flow.
func (f *DeviceFlow) Wait(ctx context.Context) (Grant, error) {
	select {
	case <-f.done:
	case <-ctx.Done():
		f.finish(Grant{}, ctx.Err())
	}
	f.Close()
	return f.grant, f.err
}

// Close cancels polling; a pending Wait returns context.Canceled.
func (f *DeviceFlow) Close() {
	f.finish(Grant{}, context.Canceled)
	f.cancel()
}

func (f *DeviceFlow) finish(g Grant, err error) {
	f.finishOnce.Do(func() { f.grant, f.err = g, err; close(f.done) })
}

func (f *DeviceFlow) poll() {
	interval := f.interval
	deadline := f.deadline
	for {
		wait := interval
		if rem := time.Until(deadline); rem < wait {
			wait = max(rem, 0)
		}
		t := time.NewTimer(wait)
		select {
		case <-f.ctx.Done():
			t.Stop()
			f.finish(Grant{}, context.Canceled)
			return
		case <-t.C:
		}
		if !time.Now().Before(deadline) {
			f.finish(Grant{}, ErrFlowExpired)
			return
		}
		form := url.Values{
			"grant_type":  {deviceGrantType},
			"device_code": {f.deviceCode},
			"client_id":   {f.cfg.ClientID},
		}
		for k, v := range f.cfg.TokenParams {
			form.Set(k, v)
		}
		reqCtx, cancel := context.WithDeadline(f.ctx, deadline)
		b, status, err := postForm(reqCtx, f.hc, f.cfg.TokenURL, form)
		cancel()
		if err != nil {
			if f.ctx.Err() != nil {
				f.finish(Grant{}, context.Canceled)
			} else if errors.Is(err, context.DeadlineExceeded) || !time.Now().Before(deadline) {
				f.finish(Grant{}, ErrFlowExpired)
			} else {
				f.finish(Grant{}, err)
			}
			return
		}
		var tr tokenResp
		if json.Unmarshal(b, &tr) != nil {
			f.finish(Grant{}, errBadTokenResponse)
			return
		}
		switch tr.Error {
		case "":
			if status < 200 || status >= 300 || tr.AccessToken == "" {
				f.finish(Grant{}, errBadTokenResponse)
				return
			}
			g := tr.grant()
			if f.cfg.RequireScope != "" && !slices.Contains(strings.Fields(g.Scope), f.cfg.RequireScope) {
				f.finish(Grant{}, ErrScopeMissing)
				return
			}
			if g.Email == "" {
				f.finish(Grant{}, errors.New("oauth: the token response named no email address"))
				return
			}
			f.finish(g, nil)
			return
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second // RFC 8628: the code's lifetime stays as it was
			continue
		case "access_denied", "authorization_denied":
			f.finish(Grant{}, ErrAccessDenied)
			return
		case "expired_token":
			f.finish(Grant{}, ErrFlowExpired)
			return
		default:
			f.finish(Grant{}, tokenCodeError(tr.Error))
			return
		}
	}
}
