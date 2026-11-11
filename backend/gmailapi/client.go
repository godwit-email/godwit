// Package gmailapi is the Gmail REST API v1 protocol backend (ADR 0037): a
// backend.Receiver and backend.Sender over https://gmail.googleapis.com.
package gmailapi

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

// DefaultBaseURL is Google's Gmail REST API host.
const DefaultBaseURL = "https://gmail.googleapis.com"

// Reason keys, translated by the UI and Wit.
const (
	ReasonAPIDisabled       = "gmailapi.api_disabled"
	ReasonNoCopy            = "gmailapi.no_copy"
	ReasonNoAnswered        = "gmailapi.no_answered"
	ReasonNoDraft           = "gmailapi.no_draft"
	ReasonNoKeywords        = "gmailapi.no_keywords"
	ReasonNoPush            = "gmailapi.no_push"
	ReasonFoldersPending    = "gmailapi.folders_pending"
	ReasonSystemLabelsFixed = "gmailapi.system_labels_fixed"
)

// ErrAPIDisabled means the OAuth client's Google Cloud project has not enabled
// gmail.googleapis.com (HTTP 403 accessNotConfigured). Its message is ReasonAPIDisabled.
var ErrAPIDisabled = errors.New(ReasonAPIDisabled)

// Config is how to reach a Gmail account over the REST API.
type Config struct {
	// BaseURL is the API origin, "" for DefaultBaseURL; tests point it at httptest.
	BaseURL     string
	Credentials backend.CredentialsFunc
	// Wait waits d out, or until ctx is done, when the server asks to back off.
	// Nil waits in real time; tests pass a fake.
	Wait func(ctx context.Context, d time.Duration) error
}

const (
	maxRetries     = 3
	maxBackOff     = time.Minute
	defaultBackOff = time.Second
)

var errOtherHost = errors.New("gmailapi: refused a redirect to another host")

type client struct {
	cfg     Config
	baseURL string
	http    *http.Client
	// stream is http without the overall timeout, for a whole message
	// (format=raw); its body is bounded by conn.StallBody instead.
	stream *http.Client
}

// Timeouts of one HTTP request, so a server that stops answering cannot hold
// the account's sync forever; vars so tests can shorten them.
var (
	headerTimeout  = time.Minute     // until the response headers arrive
	requestTimeout = 5 * time.Minute // a whole receiving request, except a whole message
	stallTimeout   = time.Minute     // a response body with no byte arriving
)

func newClient(cfg Config, timeout time.Duration) *client {
	base := strings.TrimRight(cmp.Or(cfg.BaseURL, DefaultBaseURL), "/")
	tr := conn.HTTPTransport()
	tr.ResponseHeaderTimeout = headerTimeout
	c := &client{
		cfg:     cfg,
		baseURL: base,
		http: &http.Client{
			Timeout:   timeout,
			Transport: tr,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				switch {
				case len(via) >= 10:
					return errors.New("gmailapi: too many redirects")
				case req.URL.Host != via[0].URL.Host:
					return errOtherHost
				case via[0].URL.Scheme == "https" && req.URL.Scheme != "https":
					return errors.New("gmailapi: refused a redirect from HTTPS to HTTP")
				}
				return nil
			},
		},
	}
	stream := *c.http
	stream.Timeout = 0
	c.stream = &stream
	return c
}

func (c *client) host() string {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

func (c *client) accountKey(cr backend.Credentials) string {
	return c.baseURL + "\x00" + cmp.Or(cr.Username, cr.Token)
}

func (c *client) wait(ctx context.Context, d time.Duration) error {
	if c.cfg.Wait != nil {
		return c.cfg.Wait(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// accounts holds a one-slot channel per account (API origin + user), taken for
// the length of a call including rate-limit back-off waits (modeled on EWS #664).
var accounts = struct {
	sync.Mutex
	m map[string]*slot
}{m: map[string]*slot{}}

type slot struct {
	ch    chan struct{}
	users int
}

func acquire(ctx context.Context, account string) (release func(), err error) {
	accounts.Lock()
	s := accounts.m[account]
	if s == nil {
		s = &slot{ch: make(chan struct{}, 1)}
		accounts.m[account] = s
	}
	s.users++
	accounts.Unlock()
	done := func() {
		accounts.Lock()
		if s.users--; s.users == 0 {
			delete(accounts.m, account)
		}
		accounts.Unlock()
	}
	select {
	case s.ch <- struct{}{}:
		return func() { <-s.ch; done() }, nil
	case <-ctx.Done():
		done()
		return nil, ctx.Err()
	}
}

type apiError struct {
	StatusCode int
	Status     string
	Reason     string
	Message    string
}

func (e *apiError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("gmailapi: HTTP %d (%s): %s", e.StatusCode, cmp.Or(e.Reason, e.Status), e.Message)
	}
	return fmt.Sprintf("gmailapi: HTTP %s", e.Status)
}

type rateLimitError struct {
	backOff time.Duration
	err     error
}

func (e *rateLimitError) Error() string { return e.err.Error() }
func (e *rateLimitError) Unwrap() error { return e.err }

func parseError(resp *http.Response, body []byte) error {
	var env struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Errors  []struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"errors"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	reason := ""
	if len(env.Error.Errors) > 0 {
		reason = env.Error.Errors[0].Reason
	}
	for _, d := range env.Error.Details {
		if d.Reason == "SERVICE_DISABLED" && reason == "" {
			reason = "accessNotConfigured"
		}
	}
	ae := &apiError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Reason:     reason,
		Message:    env.Error.Message,
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("gmailapi: %w", backend.ErrAuthFailed)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("gmailapi: %w", backend.ErrNotFound)
	case resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode == http.StatusForbidden && isRateLimitReason(reason):
		return &rateLimitError{backOff: retryAfter(resp.Header.Get("Retry-After")), err: ae}
	case resp.StatusCode == http.StatusForbidden && (reason == "accessNotConfigured" || env.Error.Details != nil && hasServiceDisabled(env.Error.Details)):
		return ErrAPIDisabled
	case resp.StatusCode == http.StatusForbidden && (reason == "insufficientPermissions" || reason == "authError"):
		return fmt.Errorf("gmailapi: %w", backend.ErrAuthFailed)
	}
	return ae
}

func hasServiceDisabled(ds []struct {
	Reason string `json:"reason"`
}) bool {
	for _, d := range ds {
		if d.Reason == "SERVICE_DISABLED" {
			return true
		}
	}
	return false
}

func isRateLimitReason(r string) bool {
	return r == "rateLimitExceeded" || r == "userRateLimitExceeded" || r == "quotaExceeded"
}

func retryAfter(hdr string) time.Duration {
	hdr = strings.TrimSpace(hdr)
	if hdr == "" {
		return 0
	}
	if secs, err := strconv.Atoi(hdr); err == nil && secs > 0 {
		return min(time.Duration(secs)*time.Second, maxBackOff)
	}
	if t, err := http.ParseTime(hdr); err == nil {
		if d := time.Until(t); d > 0 {
			return min(d, maxBackOff)
		}
	}
	return 0
}

var errPurgeCancelled = errors.New("gmailapi: purge cancelled")

// doJSON performs one authenticated Gmail REST API call under the account's
// serialization lock, backing off on 429 / 403 rateLimitExceeded.
func (c *client) doJSON(ctx context.Context, method, path string, query url.Values, reqBody, out any) error {
	_, err := c.doRaw(ctx, method, path, query, reqBody, out, nil)
	return err
}

func (c *client) doRaw(ctx context.Context, method, path string, query url.Values, reqBody, out any, confirm func() bool) (int, error) {
	if confirm != nil && !confirm() {
		return 0, errPurgeCancelled
	}
	if c.cfg.Credentials == nil {
		return 0, fmt.Errorf("gmailapi: %w", backend.ErrAuthFailed)
	}
	cr, err := c.cfg.Credentials(ctx)
	if err != nil {
		return 0, err
	}
	if cr.Token == "" {
		return 0, fmt.Errorf("gmailapi: %w", backend.ErrAuthFailed)
	}
	release, err := acquire(ctx, c.accountKey(cr))
	if err != nil {
		return 0, err
	}
	defer release()

	var payload []byte
	if reqBody != nil {
		if payload, err = json.Marshal(reqBody); err != nil {
			return 0, err
		}
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	hc := c.http
	if query.Get("format") == "raw" { // a whole message may outlast requestTimeout
		hc = c.stream
	}
	for try := 0; ; try++ {
		if confirm != nil && !confirm() {
			return 0, errPurgeCancelled
		}
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		rctx, cancel := context.WithCancel(ctx)
		req, err := http.NewRequestWithContext(rctx, method, u, body)
		if err != nil {
			cancel()
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+cr.Token)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := hc.Do(req)
		if err != nil {
			cancel()
			return 0, err
		}
		rbody := conn.StallBody(resp.Body, stallTimeout, cancel)
		raw, rerr := io.ReadAll(rbody)
		rbody.Close()
		if rerr != nil {
			return resp.StatusCode, rerr
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out != nil && len(raw) > 0 {
				if err := json.Unmarshal(raw, out); err != nil {
					return resp.StatusCode, fmt.Errorf("gmailapi: %s: %w", path, err)
				}
			}
			return resp.StatusCode, nil
		}
		perr := parseError(resp, raw)
		var rl *rateLimitError
		if !errors.As(perr, &rl) || try == maxRetries {
			return resp.StatusCode, perr
		}
		d := rl.backOff
		if d <= 0 {
			d = min(defaultBackOff<<try, maxBackOff)
		}
		if err := c.wait(ctx, d); err != nil {
			return resp.StatusCode, err
		}
	}
}

type profileResponse struct {
	EmailAddress string `json:"emailAddress"`
	HistoryID    string `json:"historyId"`
}

func (c *client) getProfile(ctx context.Context) (profileResponse, error) {
	var p profileResponse
	err := c.doJSON(ctx, http.MethodGet, "/gmail/v1/users/me/profile", nil, nil, &p)
	return p, err
}

type apiLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "system" or "user"
}

func (c *client) listLabels(ctx context.Context) ([]apiLabel, error) {
	var resp struct {
		Labels []apiLabel `json:"labels"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/gmail/v1/users/me/labels", nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Labels, nil
}

type apiMessage struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	LabelIDs     []string `json:"labelIds"`
	HistoryID    string   `json:"historyId"`
	InternalDate string   `json:"internalDate"` // unix ms
	SizeEstimate int64    `json:"sizeEstimate"`
	Raw          string   `json:"raw"` // base64url
}

func (m *apiMessage) receivedAt() time.Time {
	ms, err := strconv.ParseInt(m.InternalDate, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

const listPageMax = 500

// listMessages lists message IDs matching labelID ("" for All Mail) and q,
// newest first, bounded to limit when limit > 0.
func (c *client) listMessages(ctx context.Context, labelID, q string, includeSpamTrash bool, limit int) ([]string, error) {
	var ids []string
	maxPage := listPageMax
	if limit > 0 && limit < maxPage {
		maxPage = limit
	}
	err := c.listMessagesPages(ctx, labelID, q, includeSpamTrash, maxPage, func(page []string) (bool, error) {
		for _, id := range page {
			ids = append(ids, id)
			if limit > 0 && len(ids) >= limit {
				return true, nil
			}
		}
		return false, nil
	})
	return ids, err
}

func (c *client) listMessagesPages(ctx context.Context, labelID, q string, includeSpamTrash bool, pageMax int, onPage func([]string) (stop bool, err error)) error {
	if pageMax <= 0 || pageMax > listPageMax {
		pageMax = listPageMax
	}
	pageToken := ""
	for {
		qv := url.Values{"maxResults": {strconv.Itoa(pageMax)}}
		if labelID != "" {
			qv.Set("labelIds", labelID)
		}
		if q != "" {
			qv.Set("q", q)
		}
		if includeSpamTrash {
			qv.Set("includeSpamTrash", "true")
		}
		if pageToken != "" {
			qv.Set("pageToken", pageToken)
		}
		var resp struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.doJSON(ctx, http.MethodGet, "/gmail/v1/users/me/messages", qv, nil, &resp); err != nil {
			return err
		}
		if len(resp.Messages) == 0 {
			return nil
		}
		page := make([]string, len(resp.Messages))
		for i, m := range resp.Messages {
			page[i] = m.ID
		}
		stop, err := onPage(page)
		if err != nil || stop || resp.NextPageToken == "" {
			return err
		}
		pageToken = resp.NextPageToken
	}
}

func (c *client) getMessage(ctx context.Context, id, format string) (*apiMessage, error) {
	qv := url.Values{"format": {format}}
	var m apiMessage
	if err := c.doJSON(ctx, http.MethodGet, "/gmail/v1/users/me/messages/"+url.PathEscape(id), qv, nil, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

type historyRecord struct {
	ID              string        `json:"id"`
	MessagesAdded   []historyItem `json:"messagesAdded"`
	MessagesDeleted []historyItem `json:"messagesDeleted"`
	LabelsAdded     []historyItem `json:"labelsAdded"`
	LabelsRemoved   []historyItem `json:"labelsRemoved"`
}

type historyItem struct {
	Message  apiMessage `json:"message"`
	LabelIDs []string   `json:"labelIds"`
}

func (c *client) listHistory(ctx context.Context, startHistoryID string) ([]historyRecord, string, error) {
	var all []historyRecord
	latest := startHistoryID
	pageToken := ""
	for {
		qv := url.Values{
			"startHistoryId": {startHistoryID},
			"maxResults":     {strconv.Itoa(listPageMax)},
		}
		if pageToken != "" {
			qv.Set("pageToken", pageToken)
		}
		var resp struct {
			History       []historyRecord `json:"history"`
			HistoryID     string          `json:"historyId"`
			NextPageToken string          `json:"nextPageToken"`
		}
		if err := c.doJSON(ctx, http.MethodGet, "/gmail/v1/users/me/history", qv, nil, &resp); err != nil {
			return nil, "", err
		}
		all = append(all, resp.History...)
		if resp.HistoryID != "" {
			latest = resp.HistoryID
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return all, latest, nil
}

func (c *client) modifyMessage(ctx context.Context, id string, add, remove []string) error {
	body := struct {
		AddLabelIDs    []string `json:"addLabelIds,omitempty"`
		RemoveLabelIDs []string `json:"removeLabelIds,omitempty"`
	}{AddLabelIDs: add, RemoveLabelIDs: remove}
	return c.doJSON(ctx, http.MethodPost, "/gmail/v1/users/me/messages/"+url.PathEscape(id)+"/modify", nil, body, nil)
}

func (c *client) trashMessage(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodPost, "/gmail/v1/users/me/messages/"+url.PathEscape(id)+"/trash", nil, struct{}{}, nil)
}

func (c *client) untrashMessage(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodPost, "/gmail/v1/users/me/messages/"+url.PathEscape(id)+"/untrash", nil, struct{}{}, nil)
}

func (c *client) deleteMessage(ctx context.Context, id string, confirm func() bool) error {
	_, err := c.doRaw(ctx, http.MethodDelete, "/gmail/v1/users/me/messages/"+url.PathEscape(id), nil, nil, nil, confirm)
	return err
}

func (c *client) insertMessage(ctx context.Context, raw []byte, labelIDs []string) (string, error) {
	body := struct {
		Raw      string   `json:"raw"`
		LabelIDs []string `json:"labelIds,omitempty"`
	}{
		Raw:      base64.RawURLEncoding.EncodeToString(raw),
		LabelIDs: labelIDs,
	}
	var res apiMessage
	qv := url.Values{"internalDateSource": {"dateHeader"}}
	if err := c.doJSON(ctx, http.MethodPost, "/gmail/v1/users/me/messages", qv, body, &res); err != nil {
		return "", err
	}
	return res.ID, nil
}
