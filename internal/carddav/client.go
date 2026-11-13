package carddav

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

var (
	ErrInvalidURL       = errors.New("invalid CardDAV server URL")
	ErrScheme           = errors.New("CardDAV server URL must use https")
	ErrUntrustedOrigin  = errors.New("CardDAV discovery target outside server domain")
	ErrAuthFailed       = errors.New("CardDAV authentication failed")
	ErrNoAddressBooks   = errors.New("no CardDAV address books found")
	ErrSyncTokenInvalid = errors.New("CardDAV sync-token expired or unsupported")
)

const (
	maxResponseBytes = 16 << 20
	multigetBatch    = 100
	requestTimeout   = 30 * time.Second
)

type Credentials struct {
	Username, Password string
	BearerToken        string
}

type DiscoveredCollection struct {
	Href      string
	Name      string
	SyncToken string
	CardCount int
}

type SyncDelta struct {
	NextSyncToken string
	Upserted      map[string]string // canonical path href -> etag
	Deleted       []string          // canonical path hrefs
}

type FetchedCard struct {
	Href     string
	ETag     string
	RawVCard string
}

type Client struct {
	hc *http.Client
}

func NewClient(transport http.RoundTripper) *Client {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Client{
		hc: &http.Client{
			Transport: transport,
			Timeout:   requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// NormalizeURL parses and validates a user-supplied or preset CardDAV URL.
// Hostnames without a scheme default to https://; http:// is permitted only on loopback.
func NormalizeURL(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, ErrInvalidURL
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrInvalidURL
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return nil, ErrScheme
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(defaultPortHost(a), defaultPortHost(b))
}

func defaultPortHost(u *url.URL) string {
	h, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		h = u.Hostname()
		if u.Scheme == "https" {
			return net.JoinHostPort(strings.ToLower(h), "443")
		}
		return net.JoinHostPort(strings.ToLower(h), "80")
	}
	return net.JoinHostPort(strings.ToLower(h), p)
}

func canonicalHost(u *url.URL) string {
	return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
}

func isGoogleCardDAVStart(h string) bool {
	return h == "google.com" || strings.HasSuffix(h, ".google.com") ||
		h == "googleapis.com" || strings.HasSuffix(h, ".googleapis.com")
}

func isGoogleCardDAVTarget(h string) bool {
	return isGoogleCardDAVStart(h) || h == "apidata.googleusercontent.com"
}

func trustedDiscoveryHost(start, target *url.URL) bool {
	sh, th := canonicalHost(start), canonicalHost(target)
	if sh == "" || th == "" {
		return false
	}
	if sh == th {
		return true
	}
	if isLoopbackHost(sh) && isLoopbackHost(th) {
		return true
	}
	if net.ParseIP(sh) != nil || net.ParseIP(th) != nil {
		return false
	}
	if isGoogleCardDAVStart(sh) && isGoogleCardDAVTarget(th) {
		return true
	}
	se, err1 := publicsuffix.EffectiveTLDPlusOne(sh)
	te, err2 := publicsuffix.EffectiveTLDPlusOne(th)
	return err1 == nil && err2 == nil && strings.EqualFold(se, te)
}

func isFatalDiscoverErr(err error) bool {
	return errors.Is(err, ErrAuthFailed) || errors.Is(err, ErrScheme) || errors.Is(err, ErrUntrustedOrigin)
}

func resolveChecked(base *url.URL, ref string, trustStart *url.URL) (*url.URL, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, ErrInvalidURL
	}
	r, err := url.Parse(ref)
	if err != nil || r.User != nil {
		return nil, ErrInvalidURL
	}
	next := base.ResolveReference(r)
	next.Scheme = strings.ToLower(next.Scheme)
	if base.Scheme == "https" && next.Scheme != "https" {
		return nil, ErrScheme
	}
	if next.Scheme != "https" && !(next.Scheme == "http" && isLoopbackHost(next.Hostname())) {
		return nil, ErrScheme
	}
	if trustStart != nil {
		if !trustedDiscoveryHost(trustStart, next) {
			return nil, fmt.Errorf("%w: %s", ErrUntrustedOrigin, next.Hostname())
		}
	} else if !sameOrigin(base, next) {
		return nil, fmt.Errorf("carddav: cross-origin reference %q refused", ref)
	}
	return next, nil
}

func canonHref(home *url.URL, rawHref string) (string, bool) {
	u, err := resolveChecked(home, rawHref, nil)
	if err != nil {
		return "", false
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	return p, true
}

func equalCollectionPath(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

func (c *Client) doXML(ctx context.Context, method string, target *url.URL, depth string, body string, creds Credentials, trustStart *url.URL) (status int, respBody []byte, finalURL *url.URL, err error) {
	if trustStart != nil && !trustedDiscoveryHost(trustStart, target) {
		return 0, nil, nil, fmt.Errorf("%w: %s", ErrUntrustedOrigin, target.Hostname())
	}
	cur := *target
	maxHops := 5
	if trustStart != nil {
		maxHops = 10
	}
	trustedOrigin := defaultPortHost(target)
	for hop := 0; ; hop++ {
		sendCreds := strings.EqualFold(defaultPortHost(&cur), trustedOrigin)
		resp, data, err := c.roundTripXML(ctx, method, &cur, depth, body, creds, sendCreds)
		if err != nil {
			return 0, nil, nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && !sendCreds && trustStart != nil && trustedDiscoveryHost(trustStart, &cur) {
			trustedOrigin = defaultPortHost(&cur)
			resp, data, err = c.roundTripXML(ctx, method, &cur, depth, body, creds, true)
			if err != nil {
				return 0, nil, nil, err
			}
		}

		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			if hop >= maxHops {
				return 0, nil, nil, errors.New("carddav: too many redirects")
			}
			loc := resp.Header.Get("Location")
			next, err := resolveChecked(&cur, loc, trustStart)
			if err != nil {
				return 0, nil, nil, err
			}
			cur = *next
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return resp.StatusCode, data, &cur, ErrAuthFailed
		}
		u := cur
		return resp.StatusCode, data, &u, nil
	}
}

func (c *Client) roundTripXML(ctx context.Context, method string, cur *url.URL, depth, body string, creds Credentials, sendCreds bool) (*http.Response, []byte, error) {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, cur.String(), bodyReader)
	if err != nil {
		return nil, nil, err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	}
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	if sendCreds {
		if creds.BearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+creds.BearerToken)
		} else if creds.Username != "" || creds.Password != "" {
			req.SetBasicAuth(creds.Username, creds.Password)
		}
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	resp.Body.Close()
	if readErr != nil {
		return nil, nil, readErr
	}
	if len(data) > maxResponseBytes {
		return nil, nil, errors.New("carddav: response exceeded size limit")
	}
	return resp, data, nil
}

type xmlMultiStatus struct {
	Responses []xmlResponse `xml:"response"`
	SyncToken string        `xml:"sync-token"`
}

type xmlResponse struct {
	Href      string        `xml:"href"`
	Status    string        `xml:"status"`
	PropStats []xmlPropStat `xml:"propstat"`
}

type xmlPropStat struct {
	Prop   xmlProp `xml:"prop"`
	Status string  `xml:"status"`
}

type xmlHrefBox struct {
	Href string `xml:"href"`
}

type xmlResourceType struct {
	Present       bool
	IsCollection  bool
	IsAddressBook bool
}

func (rt *xmlResourceType) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	rt.Present = true
	depth := 1
	for depth > 0 {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch strings.ToLower(t.Name.Local) {
			case "collection":
				rt.IsCollection = true
			case "addressbook":
				rt.IsAddressBook = true
			}
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

type xmlProp struct {
	ResourceType         xmlResourceType `xml:"resourcetype"`
	DisplayName          string          `xml:"displayname"`
	GetETag              string          `xml:"getetag"`
	SyncToken            string          `xml:"sync-token"`
	AddressData          string          `xml:"address-data"`
	CurrentUserPrincipal xmlHrefBox      `xml:"current-user-principal"`
	AddressBookHomeSet   xmlHrefBox      `xml:"addressbook-home-set"`
}

func parseHTTPStatusCode(statusLine string) int {
	// e.g. "HTTP/1.1 200 OK" or "HTTP/2 404"
	fields := strings.Fields(statusLine)
	if len(fields) < 2 {
		return 0
	}
	var code int
	fmt.Sscanf(fields[1], "%d", &code)
	return code
}

func okProps(r xmlResponse) (xmlProp, bool) {
	if len(r.PropStats) == 0 {
		code := parseHTTPStatusCode(r.Status)
		return xmlProp{}, code == 0 || (code >= 200 && code < 300)
	}
	var merged xmlProp
	found := false
	for _, ps := range r.PropStats {
		code := parseHTTPStatusCode(ps.Status)
		if code == 0 || (code >= 200 && code < 300) {
			found = true
			if ps.Prop.ResourceType.Present {
				merged.ResourceType.Present = true
			}
			if ps.Prop.ResourceType.IsCollection {
				merged.ResourceType.IsCollection = true
			}
			if ps.Prop.ResourceType.IsAddressBook {
				merged.ResourceType.IsAddressBook = true
			}
			if merged.DisplayName == "" {
				merged.DisplayName = ps.Prop.DisplayName
			}
			if merged.GetETag == "" {
				merged.GetETag = ps.Prop.GetETag
			}
			if merged.SyncToken == "" {
				merged.SyncToken = ps.Prop.SyncToken
			}
			if merged.AddressData == "" {
				merged.AddressData = ps.Prop.AddressData
			}
			if merged.CurrentUserPrincipal.Href == "" {
				merged.CurrentUserPrincipal = ps.Prop.CurrentUserPrincipal
			}
			if merged.AddressBookHomeSet.Href == "" {
				merged.AddressBookHomeSet = ps.Prop.AddressBookHomeSet
			}
		}
	}
	return merged, found
}

func responseFailure(r xmlResponse, op, href string) error {
	if code := parseHTTPStatusCode(r.Status); code >= 400 && code != http.StatusNotFound && code != http.StatusGone {
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			return ErrAuthFailed
		}
		return fmt.Errorf("carddav: %s %s: HTTP %d", op, href, code)
	}
	for _, ps := range r.PropStats {
		code := parseHTTPStatusCode(ps.Status)
		if code >= 400 && code != http.StatusNotFound && code != http.StatusGone {
			if code == http.StatusUnauthorized || code == http.StatusForbidden {
				return ErrAuthFailed
			}
			return fmt.Errorf("carddav: %s %s: propstat HTTP %d", op, href, code)
		}
	}
	return nil
}

func isDeletedResponse(r xmlResponse) bool {
	if code := parseHTTPStatusCode(r.Status); code == http.StatusNotFound || code == http.StatusGone {
		return true
	}
	if len(r.PropStats) == 0 {
		return false
	}
	sawNotFound := false
	for _, ps := range r.PropStats {
		code := parseHTTPStatusCode(ps.Status)
		if code >= 200 && code < 300 {
			return false
		}
		if code == http.StatusNotFound || code == http.StatusGone {
			sawNotFound = true
		}
	}
	return sawNotFound
}

const discoverPropfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">
  <d:prop>
    <d:resourcetype/>
    <d:displayname/>
    <d:current-user-principal/>
    <c:addressbook-home-set/>
  </d:prop>
</d:propfind>`

const homeSetPropfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">
  <d:prop>
    <c:addressbook-home-set/>
  </d:prop>
</d:propfind>`

const collectionsPropfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav">
  <d:prop>
    <d:resourcetype/>
    <d:displayname/>
    <d:sync-token/>
  </d:prop>
</d:propfind>`

const collectionETagsPropfindBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:resourcetype/>
    <d:displayname/>
    <d:getetag/>
    <d:sync-token/>
  </d:prop>
</d:propfind>`

func withPath(u *url.URL, p string) *url.URL {
	cp := *u
	cp.Path = p
	cp.RawPath = ""
	return &cp
}

// Discover resolves an addressbook-home-set URL and its addressbook collections
// following RFC 6764 (/.well-known/carddav) and RFC 6352 (principal -> home-set).
func (c *Client) Discover(ctx context.Context, start *url.URL, creds Credentials) (string, []DiscoveredCollection, error) {
	var candidates []*url.URL
	switch start.Path {
	case "", "/":
		candidates = []*url.URL{withPath(start, "/.well-known/carddav"), withPath(start, "/")}
	case "/.well-known/carddav":
		candidates = []*url.URL{start}
	default:
		candidates = []*url.URL{start, withPath(start, "/.well-known/carddav")}
	}

	var lastErr error
	sawEmptyHome := false
	for _, cand := range candidates {
		status, data, effURL, err := c.doXML(ctx, "PROPFIND", cand, "0", discoverPropfindBody, creds, start)
		if isFatalDiscoverErr(err) {
			return "", nil, err
		}
		if err != nil {
			lastErr = err
			continue
		}
		if status != http.StatusMultiStatus {
			if status == http.StatusForbidden {
				lastErr = ErrAuthFailed
			} else {
				lastErr = fmt.Errorf("carddav: PROPFIND %s: HTTP %d", cand.Path, status)
			}
			continue
		}

		var ms xmlMultiStatus
		if err := xml.Unmarshal(data, &ms); err != nil {
			lastErr = err
			continue
		}

		var principalHref, homeHref string
		isDirectAddressBook := false
		for _, r := range ms.Responses {
			p, ok := okProps(r)
			if !ok {
				continue
			}
			if p.AddressBookHomeSet.Href != "" && homeHref == "" {
				homeHref = p.AddressBookHomeSet.Href
			}
			if p.CurrentUserPrincipal.Href != "" && principalHref == "" {
				principalHref = p.CurrentUserPrincipal.Href
			}
			if p.ResourceType.IsAddressBook {
				isDirectAddressBook = true
			}
		}

		var homeURL *url.URL
		switch {
		case homeHref != "":
			homeURL, err = resolveChecked(effURL, homeHref, start)
			if err != nil {
				if isFatalDiscoverErr(err) {
					return "", nil, err
				}
				lastErr = err
				continue
			}
		case isDirectAddressBook:
			homeURL = effURL
		case principalHref != "":
			pURL, err := resolveChecked(effURL, principalHref, start)
			if err != nil {
				if isFatalDiscoverErr(err) {
					return "", nil, err
				}
				lastErr = err
				continue
			}
			pStatus, pData, pEffURL, err := c.doXML(ctx, "PROPFIND", pURL, "0", homeSetPropfindBody, creds, start)
			if isFatalDiscoverErr(err) {
				return "", nil, err
			}
			if err != nil || pStatus != http.StatusMultiStatus {
				lastErr = err
				continue
			}
			var pms xmlMultiStatus
			if err := xml.Unmarshal(pData, &pms); err != nil {
				lastErr = err
				continue
			}
			for _, r := range pms.Responses {
				if p, ok := okProps(r); ok && p.AddressBookHomeSet.Href != "" {
					homeHref = p.AddressBookHomeSet.Href
					break
				}
			}
			if homeHref == "" {
				continue
			}
			homeURL, err = resolveChecked(pEffURL, homeHref, start)
			if err != nil {
				if isFatalDiscoverErr(err) {
					return "", nil, err
				}
				lastErr = err
				continue
			}
		default:
			// Candidate may already be an addressbook-home-set container.
			homeURL = effURL
		}

		if !trustedDiscoveryHost(start, homeURL) {
			return "", nil, fmt.Errorf("%w: %s", ErrUntrustedOrigin, homeURL.Hostname())
		}
		cols, err := c.ListCollections(ctx, homeURL, creds, true)
		if isFatalDiscoverErr(err) {
			return "", nil, err
		}
		if err != nil {
			lastErr = err
			continue
		}
		if len(cols) == 0 {
			if homeHref != "" {
				sawEmptyHome = true
			}
			continue
		}
		return homeURL.String(), cols, nil
	}

	if sawEmptyHome {
		return "", nil, ErrNoAddressBooks
	}
	if lastErr != nil {
		return "", nil, lastErr
	}
	return "", nil, ErrNoAddressBooks
}

func defaultCollectionName(href string) string {
	trimmed := strings.Trim(href, "/")
	if trimmed == "" {
		return "Contacts"
	}
	base := path.Base(trimmed)
	if decoded, err := url.PathUnescape(base); err == nil && decoded != "" {
		return decoded
	}
	return base
}

// ListCollections lists all card:addressbook collections under home (or home itself if it is one).
func (c *Client) ListCollections(ctx context.Context, home *url.URL, creds Credentials, countCards bool) ([]DiscoveredCollection, error) {
	status, data, _, err := c.doXML(ctx, "PROPFIND", home, "1", collectionsPropfindBody, creds, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusForbidden {
		return nil, ErrAuthFailed
	}
	if status != http.StatusMultiStatus {
		return nil, fmt.Errorf("carddav: PROPFIND %s: HTTP %d", home.Path, status)
	}
	var ms xmlMultiStatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		return nil, err
	}

	var cols []DiscoveredCollection
	seen := map[string]bool{}
	for _, r := range ms.Responses {
		h, ok := canonHref(home, r.Href)
		if !ok || seen[h] {
			continue
		}
		if isDeletedResponse(r) {
			continue
		}
		if err := responseFailure(r, "PROPFIND", h); err != nil {
			return nil, err
		}
		for _, ps := range r.PropStats {
			if code := parseHTTPStatusCode(ps.Status); code != 0 && (code < 200 || code >= 300) && ps.Prop.ResourceType.Present {
				return nil, fmt.Errorf("carddav: PROPFIND %s: resourcetype HTTP %d", h, code)
			}
		}
		p, ok := okProps(r)
		if !ok {
			return nil, fmt.Errorf("carddav: PROPFIND %s: missing ok propstat", h)
		}
		if !p.ResourceType.IsAddressBook {
			continue
		}
		seen[h] = true
		name := strings.TrimSpace(p.DisplayName)
		if name == "" {
			name = defaultCollectionName(h)
		}
		cols = append(cols, DiscoveredCollection{
			Href:      h,
			Name:      name,
			SyncToken: strings.TrimSpace(p.SyncToken),
		})
	}

	if countCards {
		for i := range cols {
			etags, _, _, err := c.ListCollectionETags(ctx, home, cols[i].Href, creds)
			if err != nil {
				return nil, err
			}
			cols[i].CardCount = len(etags)
		}
	}
	return cols, nil
}

// ListCollectionETags lists every member vCard href -> ETag in colHref via PROPFIND Depth: 1.
func (c *Client) ListCollectionETags(ctx context.Context, home *url.URL, colHref string, creds Credentials) (map[string]string, string, string, error) {
	colURL, err := resolveChecked(home, colHref, nil)
	if err != nil {
		return nil, "", "", err
	}
	status, data, _, err := c.doXML(ctx, "PROPFIND", colURL, "1", collectionETagsPropfindBody, creds, nil)
	if err != nil {
		return nil, "", "", err
	}
	if status != http.StatusMultiStatus {
		return nil, "", "", fmt.Errorf("carddav: PROPFIND %s: HTTP %d", colURL.Path, status)
	}
	var ms xmlMultiStatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		return nil, "", "", err
	}
	colCanon, _ := canonHref(home, colHref)
	etags := map[string]string{}
	syncToken := strings.TrimSpace(ms.SyncToken)
	displayName := ""
	for _, r := range ms.Responses {
		h, ok := canonHref(home, r.Href)
		if !ok {
			continue
		}
		if isDeletedResponse(r) {
			continue
		}
		if err := responseFailure(r, "PROPFIND", h); err != nil {
			return nil, "", "", err
		}
		p, ok := okProps(r)
		if !ok {
			return nil, "", "", fmt.Errorf("carddav: PROPFIND %s: missing ok propstat", h)
		}
		if equalCollectionPath(h, colCanon) || p.ResourceType.IsCollection {
			if p.SyncToken != "" && syncToken == "" {
				syncToken = strings.TrimSpace(p.SyncToken)
			}
			if p.DisplayName != "" && displayName == "" {
				displayName = strings.TrimSpace(p.DisplayName)
			}
			continue
		}
		etags[h] = strings.TrimSpace(p.GetETag)
	}
	return etags, syncToken, displayName, nil
}

// SyncCollection queries incremental changes since syncToken via RFC 6578 sync-collection REPORT.
// Returns ErrSyncTokenInvalid when the token is expired or the server does not support sync-collection.
func (c *Client) SyncCollection(ctx context.Context, home *url.URL, colHref, syncToken string, creds Credentials) (SyncDelta, error) {
	colURL, err := resolveChecked(home, colHref, nil)
	if err != nil {
		return SyncDelta{}, err
	}
	var reqBuf bytes.Buffer
	reqBuf.WriteString(`<?xml version="1.0" encoding="utf-8"?><d:sync-collection xmlns:d="DAV:"><d:sync-token>`)
	_ = xml.EscapeText(&reqBuf, []byte(syncToken))
	reqBuf.WriteString(`</d:sync-token><d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`)

	status, data, _, err := c.doXML(ctx, "REPORT", colURL, "0", reqBuf.String(), creds, nil)
	if err != nil {
		return SyncDelta{}, err
	}
	switch status {
	case http.StatusForbidden, http.StatusMethodNotAllowed, http.StatusConflict, http.StatusPreconditionFailed, http.StatusNotImplemented, http.StatusBadRequest:
		return SyncDelta{}, ErrSyncTokenInvalid
	}
	if status != http.StatusMultiStatus {
		return SyncDelta{}, fmt.Errorf("carddav: sync-collection %s: HTTP %d", colURL.Path, status)
	}

	var ms xmlMultiStatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		return SyncDelta{}, err
	}
	nextTok := strings.TrimSpace(ms.SyncToken)
	if nextTok == "" {
		return SyncDelta{}, ErrSyncTokenInvalid
	}

	colCanon, _ := canonHref(home, colHref)
	delta := SyncDelta{
		NextSyncToken: nextTok,
		Upserted:      map[string]string{},
	}
	for _, r := range ms.Responses {
		h, ok := canonHref(home, r.Href)
		if !ok || equalCollectionPath(h, colCanon) {
			continue
		}
		if isDeletedResponse(r) {
			delta.Deleted = append(delta.Deleted, h)
			continue
		}
		if err := responseFailure(r, "sync-collection", h); err != nil {
			return SyncDelta{}, err
		}
		p, ok := okProps(r)
		if !ok {
			return SyncDelta{}, fmt.Errorf("carddav: sync-collection %s: missing ok propstat", h)
		}
		if p.ResourceType.IsCollection {
			continue
		}
		delta.Upserted[h] = strings.TrimSpace(p.GetETag)
	}
	return delta, nil
}

// FetchCards downloads vCard payloads for hrefs in batches via card:addressbook-multiget REPORT.
func (c *Client) FetchCards(ctx context.Context, home *url.URL, colHref string, hrefs []string, creds Credentials) ([]FetchedCard, error) {
	if len(hrefs) == 0 {
		return nil, nil
	}
	colURL, err := resolveChecked(home, colHref, nil)
	if err != nil {
		return nil, err
	}
	var out []FetchedCard
	for start := 0; start < len(hrefs); start += multigetBatch {
		end := min(start+multigetBatch, len(hrefs))
		batch := hrefs[start:end]

		var reqBuf bytes.Buffer
		reqBuf.WriteString(`<?xml version="1.0" encoding="utf-8"?><c:addressbook-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:carddav"><d:prop><d:getetag/><c:address-data/></d:prop>`)
		for _, h := range batch {
			reqBuf.WriteString(`<d:href>`)
			_ = xml.EscapeText(&reqBuf, []byte(h))
			reqBuf.WriteString(`</d:href>`)
		}
		reqBuf.WriteString(`</c:addressbook-multiget>`)

		status, data, _, err := c.doXML(ctx, "REPORT", colURL, "1", reqBuf.String(), creds, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusMultiStatus {
			return nil, fmt.Errorf("carddav: addressbook-multiget %s: HTTP %d", colURL.Path, status)
		}
		var ms xmlMultiStatus
		if err := xml.Unmarshal(data, &ms); err != nil {
			return nil, err
		}
		seen := make(map[string]bool, len(batch))
		for _, r := range ms.Responses {
			h, ok := canonHref(home, r.Href)
			if !ok {
				return nil, fmt.Errorf("carddav: addressbook-multiget %s: invalid href %q", colURL.Path, r.Href)
			}
			if isDeletedResponse(r) {
				seen[h] = true
				continue
			}
			if err := responseFailure(r, "addressbook-multiget", h); err != nil {
				return nil, err
			}
			p, ok := okProps(r)
			if !ok || strings.TrimSpace(p.AddressData) == "" {
				return nil, fmt.Errorf("carddav: addressbook-multiget %s: missing address-data", h)
			}
			seen[h] = true
			out = append(out, FetchedCard{
				Href:     h,
				ETag:     strings.TrimSpace(p.GetETag),
				RawVCard: p.AddressData,
			})
		}
		for _, reqHref := range batch {
			if canon, ok := canonHref(home, reqHref); ok && !seen[canon] {
				return nil, fmt.Errorf("carddav: addressbook-multiget %s: missing response for %s", colURL.Path, canon)
			}
		}
	}
	return out, nil
}
