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

type client struct {
	cfg     Config
	baseURL string
	http    *http.Client
	// stream is http without the overall timeout, for a whole message
	// (format=raw); its body is bounded by conn.StallBody instead.
	stream *http.Client
}

func newClient(cfg Config) *client {
	hc := conn.NewHTTPClient(conn.HTTPTransport(), nil)
	return &client{cfg: cfg, baseURL: strings.TrimRight(cmp.Or(cfg.BaseURL, DefaultBaseURL), "/"), http: hc, stream: conn.Streaming(hc)}
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
	if err := conn.HTTPStatusErr(resp); err != nil {
		if busy, ok := err.(*conn.BusyError); ok {
			busy.Err = ae
			return busy
		}
		return fmt.Errorf("gmailapi: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("gmailapi: %w", backend.ErrNotFound)
	case resp.StatusCode == http.StatusForbidden && isRateLimitReason(reason):
		return &conn.BusyError{BackOff: conn.RetryAfter(resp.Header), Err: ae}
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

var errPurgeCancelled = errors.New("gmailapi: purge cancelled")

// doJSON performs one authenticated Gmail REST API call under the account's
// conn.Serial, backing off on 429 / 403 rateLimitExceeded.
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
	if query.Get("format") == "raw" { // a whole message may outlast conn.RequestTimeout
		hc = c.stream
	}
	var status int
	err = conn.Serial(ctx, c.accountKey(cr), c.cfg.Wait, func() error {
		if confirm != nil && !confirm() {
			return errPurgeCancelled
		}
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		rctx, cancel := context.WithCancel(ctx)
		req, err := http.NewRequestWithContext(rctx, method, u, body)
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Authorization", "Bearer "+cr.Token)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := hc.Do(req)
		if err != nil {
			cancel()
			return err
		}
		status = resp.StatusCode
		rbody := conn.StallBody(resp.Body, conn.StallTimeout, cancel)
		raw, err := io.ReadAll(rbody)
		rbody.Close()
		if err != nil {
			return err
		}
		if status < 200 || status >= 300 {
			return parseError(resp, raw)
		}
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("gmailapi: %s: %w", path, err)
			}
		}
		return nil
	})
	return status, err
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
