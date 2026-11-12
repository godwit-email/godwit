package testmail

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// CardDAVRequest records one HTTP request to the fake CardDAV server.
type CardDAVRequest struct {
	Method     string
	Path       string
	ReportKind string
	HasAuth    bool
}

type cardEntry struct {
	href    string
	etag    string
	vcard   string
	deleted bool
	rev     int
}

type cardCollection struct {
	slug        string
	displayName string
	order       int
	rev         int
	minValidRev int
	cards       map[string]*cardEntry // by canonical path href
}

type photoAsset struct {
	contentType string
	data        []byte
	hits        int
}

// CardDAV is an in-process HTTPS CardDAV server for tests, supporting
// /.well-known/carddav discovery, PROPFIND, sync-collection (RFC 6578),
// addressbook-multiget (RFC 6352), Basic/OAuth auth, external HTTPS PHOTO
// assets, and fault injection.
type CardDAV struct {
	t   testing.TB
	srv *httptest.Server

	mu                    sync.Mutex
	user, pass            string
	oauthValid            func(string) bool
	rejectAuth            bool
	failStatus            int
	wellKnownRedirect     string
	principalOverride     string
	homeSetOverride       string
	noCollections         bool
	disableSyncCollection bool
	multigetHook          func(href string) int
	colSeq                int
	collections           map[string]*cardCollection
	photos                map[string]*photoAsset
	requests              []CardDAVRequest
}

var carddavCert = sync.OnceValues(func() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames: []string{
			"localhost",
			"contacts.icloud.com", "*.icloud.com",
			"carddav.fastmail.com",
			"www.googleapis.com", "apidata.googleusercontent.com",
			"example.com", "*.example.com",
			"example.net", "*.example.net",
			"example.org", "*.example.org",
			"evil.example", "*.evil.example",
		},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback, net.IPv4(93, 184, 216, 34)},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, err
})

// CardDAVCert returns the shared TLS certificate presented by fake CardDAV servers.
func CardDAVCert() tls.Certificate {
	c, err := carddavCert()
	if err != nil {
		panic(err)
	}
	return c
}

// CardDAVRoots returns a CertPool trusting both Autodiscover and CardDAV test servers.
func CardDAVRoots() *x509.CertPool {
	roots := AutodiscoverRoots()
	roots.AddCert(CardDAVCert().Leaf)
	return roots
}

// StartCardDAV starts a fake HTTPS CardDAV server with default Basic credentials alice/secret.
func StartCardDAV(t testing.TB) *CardDAV {
	t.Helper()
	s := &CardDAV{
		t:           t,
		user:        "alice",
		pass:        "secret",
		collections: map[string]*cardCollection{},
		photos:      map[string]*photoAsset{},
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{CardDAVCert()}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	s.srv = srv
	return s
}

func (s *CardDAV) URL() string          { return s.srv.URL }
func (s *CardDAV) PrincipalURL() string { return s.srv.URL + "/dav/principals/alice/" }
func (s *CardDAV) HomeSetURL() string   { return s.srv.URL + "/dav/addressbooks/alice/" }
func (s *CardDAV) CollectionURL(slug string) string {
	return s.srv.URL + "/dav/addressbooks/alice/" + strings.Trim(slug, "/") + "/"
}

func (s *CardDAV) SetBasicAuth(user, pass string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.user, s.pass, s.oauthValid = user, pass, nil
}

func (s *CardDAV) SetOAuthValidator(fn func(token string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.oauthValid = fn
}

func (s *CardDAV) RejectAuth(reject bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectAuth = reject
}

func (s *CardDAV) FailRequests(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failStatus = status
}

func (s *CardDAV) SetWellKnownRedirect(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wellKnownRedirect = target
}

func (s *CardDAV) SetHomeSetURL(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.homeSetOverride = u
}

func (s *CardDAV) SetPrincipalOnlyURL(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.principalOverride = u
}

func (s *CardDAV) SetNoCollections(empty bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noCollections = empty
	if empty {
		s.collections = map[string]*cardCollection{}
	}
}

func (s *CardDAV) DisableSyncCollection(off bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disableSyncCollection = off
}

func (s *CardDAV) InvalidateSyncTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.collections {
		c.rev++
		c.minValidRev = c.rev
	}
}

func (s *CardDAV) ensureCollectionLocked(slug, displayName string) *cardCollection {
	slug = strings.Trim(slug, "/")
	if c, ok := s.collections[slug]; ok {
		if displayName != "" {
			c.displayName = displayName
		}
		return c
	}
	if displayName == "" {
		if slug == "contacts" {
			displayName = "Contacts"
		} else {
			displayName = slug
		}
	}
	s.colSeq++
	c := &cardCollection{
		slug:        slug,
		displayName: displayName,
		order:       s.colSeq,
		rev:         1,
		minValidRev: 1,
		cards:       map[string]*cardEntry{},
	}
	s.collections[slug] = c
	return c
}

func (s *CardDAV) PutCollection(slug, displayName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noCollections = false
	s.ensureCollectionLocked(slug, displayName)
}

func (s *CardDAV) RemoveCollection(slug string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.collections, strings.Trim(slug, "/"))
}

func (s *CardDAV) PutCard(collectionSlug, name, vcard string) (href, etag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noCollections = false
	c := s.ensureCollectionLocked(collectionSlug, "")
	c.rev++
	name = strings.TrimLeft(name, "/")
	if !strings.HasSuffix(name, ".vcf") {
		name += ".vcf"
	}
	href = "/dav/addressbooks/alice/" + c.slug + "/" + name
	etag = fmt.Sprintf(`"rev-%d"`, c.rev)
	c.cards[href] = &cardEntry{href: href, etag: etag, vcard: vcard, rev: c.rev}
	return href, etag
}

func (s *CardDAV) DeleteCard(collectionSlug, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.collections[strings.Trim(collectionSlug, "/")]
	if !ok {
		return
	}
	name = strings.TrimLeft(name, "/")
	if !strings.HasSuffix(name, ".vcf") {
		name += ".vcf"
	}
	href := "/dav/addressbooks/alice/" + c.slug + "/" + name
	if e, exists := c.cards[href]; exists && !e.deleted {
		c.rev++
		e.deleted = true
		e.etag = ""
		e.vcard = ""
		e.rev = c.rev
	}
}

func (s *CardDAV) ServePhoto(name, contentType string, data []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.TrimLeft(name, "/")
	s.photos[key] = &photoAsset{contentType: contentType, data: append([]byte(nil), data...)}
	return s.srv.URL + "/photos/" + key
}

func (s *CardDAV) PhotoHits(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.photos[strings.TrimLeft(name, "/")]; p != nil {
		return p.hits
	}
	return 0
}

func (s *CardDAV) Requests() []CardDAVRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CardDAVRequest(nil), s.requests...)
}

func (s *CardDAV) ResetRequests() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

func syncTokenFor(c *cardCollection) string {
	return fmt.Sprintf("http://godwit.test/ns/sync/%s/%d", c.slug, c.rev)
}

func parseSyncToken(c *cardCollection, tok string) (int, bool) {
	prefix := "http://godwit.test/ns/sync/" + c.slug + "/"
	if !strings.HasPrefix(tok, prefix) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(tok, prefix))
	if err != nil || n < c.minValidRev || n > c.rev {
		return 0, false
	}
	return n, true
}

func (s *CardDAV) checkAuthLocked(r *http.Request) bool {
	if s.rejectAuth {
		return false
	}
	if s.oauthValid != nil {
		const pfx = "Bearer "
		h := r.Header.Get("Authorization")
		return strings.HasPrefix(h, pfx) && s.oauthValid(strings.TrimPrefix(h, pfx))
	}
	u, p, ok := r.BasicAuth()
	return ok && u == s.user && p == s.pass
}

func (s *CardDAV) serve(w http.ResponseWriter, r *http.Request) {
	if after, ok := strings.CutPrefix(r.URL.Path, "/photos/"); ok {
		s.mu.Lock()
		p := s.photos[after]
		if p != nil {
			p.hits++
		}
		s.mu.Unlock()
		if p == nil {
			http.NotFound(w, r)
			return
		}
		if p.contentType != "" {
			w.Header().Set("Content-Type", p.contentType)
		}
		w.WriteHeader(http.StatusOK)
		w.Write(p.data)
		return
	}

	body, _ := io.ReadAll(r.Body)
	reportKind := ""
	if r.Method == "REPORT" {
		d := xml.NewDecoder(strings.NewReader(string(body)))
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			if se, ok := tok.(xml.StartElement); ok {
				reportKind = se.Name.Local
				break
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, CardDAVRequest{
		Method:     r.Method,
		Path:       r.URL.Path,
		ReportKind: reportKind,
		HasAuth:    r.Header.Get("Authorization") != "",
	})

	if s.failStatus != 0 {
		http.Error(w, "carddav failure", s.failStatus)
		return
	}

	if r.URL.Path == "/.well-known/carddav" {
		target := s.wellKnownRedirect
		if target == "" {
			target = "/dav/principals/alice/"
		}
		w.Header().Set("Location", target)
		w.WriteHeader(http.StatusMovedPermanently)
		return
	}

	if !s.checkAuthLocked(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="CardDAV"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if len(s.collections) == 0 && !s.noCollections {
		s.ensureCollectionLocked("contacts", "Contacts")
	}

	p := r.URL.Path
	if p != "/" && !strings.HasSuffix(p, "/") {
		p += "/"
	}

	switch {
	case r.Method == "PROPFIND" && (p == "/" || p == "/dav/" || p == "/dav/principals/alice/"):
		principal := "/dav/principals/alice/"
		homeXML := ""
		if s.principalOverride != "" && p != "/dav/principals/alice/" {
			principal = s.principalOverride
		} else {
			home := "/dav/addressbooks/alice/"
			if s.homeSetOverride != "" {
				home = s.homeSetOverride
			}
			homeXML = fmt.Sprintf(`<C:addressbook-home-set><D:href>%s</D:href></C:addressbook-home-set>`, xmlEsc(home))
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>`+
			`<D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">`+
			`<D:response><D:href>%s</D:href><D:propstat><D:prop>`+
			`<D:resourcetype><D:collection/></D:resourcetype>`+
			`<D:current-user-principal><D:href>%s</D:href></D:current-user-principal>`+
			`%s`+
			`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`+
			`</D:multistatus>`, xmlEsc(r.URL.Path), xmlEsc(principal), homeXML)
		return

	case r.Method == "PROPFIND" && p == "/dav/addressbooks/alice/":
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">`)
		b.WriteString(`<D:response><D:href>/dav/addressbooks/alice/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
		if r.Header.Get("Depth") != "0" {
			cols := make([]*cardCollection, 0, len(s.collections))
			for _, c := range s.collections {
				cols = append(cols, c)
			}
			slices.SortFunc(cols, func(a, b *cardCollection) int { return a.order - b.order })
			for _, c := range cols {
				colHref := "/dav/addressbooks/alice/" + c.slug + "/"
				fmt.Fprintf(&b, `<D:response><D:href>%s</D:href><D:propstat><D:prop>`+
					`<D:resourcetype><D:collection/><C:addressbook/></D:resourcetype>`+
					`<D:displayname>%s</D:displayname>`+
					`<D:sync-token>%s</D:sync-token>`+
					`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`,
					xmlEsc(colHref), xmlEsc(c.displayName), xmlEsc(syncTokenFor(c)))
			}
		}
		b.WriteString(`</D:multistatus>`)
		w.Write([]byte(b.String()))
		return
	}

	if slug, ok := strings.CutPrefix(p, "/dav/addressbooks/alice/"); ok {
		slug = strings.Trim(slug, "/")
		col, exists := s.collections[slug]
		if !exists {
			http.NotFound(w, r)
			return
		}
		colHref := "/dav/addressbooks/alice/" + col.slug + "/"
		switch {
		case r.Method == "PROPFIND":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			var b strings.Builder
			b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">`)
			fmt.Fprintf(&b, `<D:response><D:href>%s</D:href><D:propstat><D:prop>`+
				`<D:resourcetype><D:collection/><C:addressbook/></D:resourcetype>`+
				`<D:displayname>%s</D:displayname>`+
				`<D:sync-token>%s</D:sync-token>`+
				`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`,
				xmlEsc(colHref), xmlEsc(col.displayName), xmlEsc(syncTokenFor(col)))
			if r.Header.Get("Depth") != "0" {
				hrefs := make([]string, 0, len(col.cards))
				for h, ce := range col.cards {
					if !ce.deleted {
						hrefs = append(hrefs, h)
					}
				}
				slices.Sort(hrefs)
				for _, h := range hrefs {
					ce := col.cards[h]
					fmt.Fprintf(&b, `<D:response><D:href>%s</D:href><D:propstat><D:prop>`+
						`<D:resourcetype/>`+
						`<D:getetag>%s</D:getetag>`+
						`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`,
						xmlEsc(ce.href), xmlEsc(ce.etag))
				}
			}
			b.WriteString(`</D:multistatus>`)
			w.Write([]byte(b.String()))
			return

		case r.Method == "REPORT" && reportKind == "sync-collection":
			if s.disableSyncCollection {
				http.Error(w, "sync-collection disabled", http.StatusNotImplemented)
				return
			}
			var req struct {
				SyncToken string `xml:"sync-token"`
			}
			_ = xml.Unmarshal(body, &req)
			sinceRev, valid := parseSyncToken(col, strings.TrimSpace(req.SyncToken))
			if !valid {
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?><D:error xmlns:D="DAV:"><D:valid-sync-token/></D:error>`))
				return
			}
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			var b strings.Builder
			b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:">`)
			hrefs := make([]string, 0, len(col.cards))
			for h, ce := range col.cards {
				if ce.rev > sinceRev {
					hrefs = append(hrefs, h)
				}
			}
			slices.Sort(hrefs)
			for _, h := range hrefs {
				ce := col.cards[h]
				if ce.deleted {
					fmt.Fprintf(&b, `<D:response><D:href>%s</D:href><D:status>HTTP/1.1 404 Not Found</D:status></D:response>`, xmlEsc(ce.href))
				} else {
					fmt.Fprintf(&b, `<D:response><D:href>%s</D:href><D:propstat><D:prop><D:getetag>%s</D:getetag></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`,
						xmlEsc(ce.href), xmlEsc(ce.etag))
				}
			}
			fmt.Fprintf(&b, `<D:sync-token>%s</D:sync-token></D:multistatus>`, xmlEsc(syncTokenFor(col)))
			w.Write([]byte(b.String()))
			return

		case r.Method == "REPORT" && reportKind == "addressbook-multiget":
			var req struct {
				Hrefs []string `xml:"href"`
			}
			_ = xml.Unmarshal(body, &req)
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			var b strings.Builder
			b.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:carddav">`)
			for _, rawHref := range req.Hrefs {
				h := rawHref
				if u, err := url.Parse(rawHref); err == nil && u.Path != "" {
					h = u.Path
				}
				hook := s.multigetHook
				if hook != nil {
					s.mu.Unlock()
					status := hook(h)
					s.mu.Lock()
					if status >= 400 {
						fmt.Fprintf(&b, `<D:response><D:href>%s</D:href><D:propstat><D:prop><C:address-data/></D:prop><D:status>HTTP/1.1 %d %s</D:status></D:propstat></D:response>`,
							xmlEsc(h), status, http.StatusText(status))
						continue
					}
				}
				ce := col.cards[h]
				if ce == nil || ce.deleted {
					fmt.Fprintf(&b, `<D:response><D:href>%s</D:href><D:status>HTTP/1.1 404 Not Found</D:status></D:response>`, xmlEsc(h))
					continue
				}
				fmt.Fprintf(&b, `<D:response><D:href>%s</D:href><D:propstat><D:prop>`+
					`<D:getetag>%s</D:getetag>`+
					`<C:address-data>%s</C:address-data>`+
					`</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`,
					xmlEsc(ce.href), xmlEsc(ce.etag), xmlEsc(ce.vcard))
			}
			b.WriteString(`</D:multistatus>`)
			w.Write([]byte(b.String()))
			return
		}
	}

	http.NotFound(w, r)
}

func xmlEsc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// SetMultigetHook installs an optional callback invoked per requested href in
// addressbook-multiget without holding the server mutex. Returning an HTTP status
// >= 400 emits a failing propstat for that href.
func (s *CardDAV) SetMultigetHook(fn func(href string) int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.multigetHook = fn
}
