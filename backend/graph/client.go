package graph

import (
	"bytes"
	"context"
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

// BaseURL is Graph's v1.0 root.
const BaseURL = "https://graph.microsoft.com/v1.0"

// Config is how to reach a Microsoft Graph account.
type Config struct {
	// URL is the Graph API root, normally BaseURL.
	URL         string
	Credentials backend.CredentialsFunc
	// Wait waits d out, or until ctx is done, when the server asks to back
	// off. Nil waits in real time; tests pass a fake.
	Wait func(ctx context.Context, d time.Duration) error
}

// ResponseError is Graph's refusal of a request (4xx other than 401/403/429).
type ResponseError struct {
	Status  int
	Code    string
	Message string
}

func (e *ResponseError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("graph: %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("graph: HTTP %d", e.Status)
}

func isStatus(err error, status int) bool {
	var re *ResponseError
	return errors.As(err, &re) && re.Status == status
}

func isDeltaReset(err error) bool {
	var re *ResponseError
	if !errors.As(err, &re) {
		return false
	}
	if re.Status == http.StatusGone {
		return true
	}
	return strings.EqualFold(re.Code, "syncStateNotFound") || strings.EqualFold(re.Code, "resyncRequired") ||
		strings.Contains(strings.ToLower(re.Code), "syncstatenotfound")
}

// deltaPageSize is the odata.maxpagesize a delta request asks for.
const deltaPageSize = 200

type client struct {
	cfg  Config
	base string
	http *http.Client

	mu       sync.Mutex
	signedIn bool
}

func newClient(cfg Config) *client {
	base := strings.TrimRight(cfg.URL, "/")
	if base == "" {
		base = BaseURL
	}
	return &client{cfg: cfg, base: base, http: conn.NewHTTPClient(conn.HTTPTransport(), nil)}
}

func (c *client) host() string {
	u, err := url.Parse(c.base)
	if err != nil {
		return ""
	}
	return u.Host
}

func (c *client) account(cr backend.Credentials) string {
	return c.base + "\x00" + cr.Username
}

func (c *client) wait(ctx context.Context, d time.Duration) error {
	if c.cfg.Wait != nil {
		return c.cfg.Wait(ctx, d)
	}
	return conn.Sleep(ctx, d)
}

// do runs one HTTP request against pathOrURL (relative to base unless it has a
// scheme), serialized per account, with Prefer: IdType="ImmutableId" on every
// request and odata.maxpagesize on delta requests.
func (c *client) do(ctx context.Context, method, pathOrURL, contentType string, body []byte) ([]byte, error) {
	return c.doRetrying(ctx, method, pathOrURL, contentType, body, true)
}

// doRetrying is do, sending a throttled request again only when retry is
// set; past the retries it returns a *conn.BusyError.
func (c *client) doRetrying(ctx context.Context, method, pathOrURL, contentType string, body []byte, retry bool) ([]byte, error) {
	cr, err := c.cfg.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	target := pathOrURL
	if strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://") {
		u, err := url.Parse(pathOrURL)
		if err != nil {
			return nil, err
		}
		if u.Host != c.host() {
			return nil, fmt.Errorf("graph: %w", conn.ErrOtherHost)
		}
		if strings.HasPrefix(c.base, "https://") && u.Scheme != "https" {
			return nil, errors.New("graph: refused a link from HTTPS to HTTP")
		}
	} else {
		target = c.base + pathOrURL
	}
	if !retry {
		release, err := conn.Acquire(ctx, c.account(cr))
		if err != nil {
			return nil, err
		}
		defer release()
		return c.sendOnce(ctx, method, target, contentType, body, cr)
	}
	var raw []byte
	err = conn.Serial(ctx, c.account(cr), c.cfg.Wait, func() (err error) {
		raw, err = c.sendOnce(ctx, method, target, contentType, body, cr)
		return err
	})
	return raw, err
}

func (c *client) sendOnce(ctx context.Context, method, target, contentType string, body []byte, cr backend.Credentials) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cr.Token)
	prefer := `IdType="ImmutableId"`
	if strings.HasSuffix(req.URL.Path, "/delta") { // Graph's own page is 10 messages
		prefer += ", odata.maxpagesize=" + strconv.Itoa(deltaPageSize)
	}
	req.Header.Set("Prefer", prefer)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := conn.HTTPStatusErr(resp); err != nil {
		return nil, fmt.Errorf("graph: %w", err)
	}
	switch sc := resp.StatusCode; {
	case sc >= 200 && sc < 300:
		c.mu.Lock()
		c.signedIn = true
		c.mu.Unlock()
		return raw, nil
	case sc == http.StatusForbidden:
		return nil, fmt.Errorf("graph: %w", backend.ErrAuthFailed)
	case sc == http.StatusServiceUnavailable:
		return nil, fmt.Errorf("graph: %w", &conn.BusyError{BackOff: conn.RetryAfter(resp.Header)})
	case sc >= 400 && sc < 500:
		var ge struct {
			Error struct {
				Code       string `json:"code"`
				Message    string `json:"message"`
				InnerError struct {
					Code string `json:"code"`
				} `json:"innerError"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &ge)
		code := ge.Error.Code
		if strings.EqualFold(ge.Error.InnerError.Code, "syncStateNotFound") {
			code = ge.Error.InnerError.Code
		}
		return nil, &ResponseError{Status: sc, Code: code, Message: ge.Error.Message}
	default:
		return nil, fmt.Errorf("graph: HTTP %s", resp.Status)
	}
}

func callJSON[R any](ctx context.Context, c *client, method, pathOrURL string, reqBody any) (R, error) {
	var zero R
	var body []byte
	var ct string
	if reqBody != nil {
		var err error
		body, err = json.Marshal(reqBody)
		if err != nil {
			return zero, err
		}
		ct = "application/json"
	}
	raw, err := c.do(ctx, method, pathOrURL, ct, body)
	if err != nil || len(raw) == 0 {
		return zero, err
	}
	var out R
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, fmt.Errorf("graph: decode response: %w", err)
	}
	return out, nil
}
