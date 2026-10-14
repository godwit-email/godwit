package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

func (r tokenResp) grant() Grant {
	exp := time.Now().Add(noExpiryTTL)
	if r.ExpiresIn > 0 {
		exp = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	return Grant{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		Expiry:       exp,
		Email:        emailFromIDToken(r.IDToken),
		Scope:        r.Scope,
	}
}

// RefreshForm trades refreshToken for a new access token with a form POST that
// includes cfg.TokenParams. Grant.RefreshToken is "" unless the server rotated it.
func RefreshForm(ctx context.Context, cfg OAuthConfig, hc *http.Client, refreshToken string) (Grant, error) {
	if cfg.ClientID == "" {
		return Grant{}, ErrOAuthNotConfigured
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {cfg.ClientID},
	}
	for k, v := range cfg.TokenParams {
		form.Set(k, v)
	}
	b, status, err := postForm(ctx, hc, cfg.TokenURL, form)
	if err != nil {
		return Grant{}, err
	}
	var tr tokenResp
	if json.Unmarshal(b, &tr) != nil {
		return Grant{}, errBadTokenResponse
	}
	if tr.Error != "" || status < 200 || status >= 300 {
		return Grant{}, tokenCodeError(tr.Error)
	}
	if tr.AccessToken == "" {
		return Grant{}, errBadTokenResponse
	}
	g := tr.grant()
	if g.RefreshToken == refreshToken {
		g.RefreshToken = ""
	}
	return g, nil
}

func postForm(ctx context.Context, hc *http.Client, endpoint string, form url.Values) ([]byte, int, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, errBadTokenResponse
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, tokenError(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, 0, tokenError(err)
	}
	return b, resp.StatusCode, nil
}
