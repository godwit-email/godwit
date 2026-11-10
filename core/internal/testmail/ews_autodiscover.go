package testmail

import (
	"context"
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
	"strings"
	"sync"
	"time"

	"github.com/godwit-email/godwit/backend/ews"
)

// Autodiscover is a fake Autodiscover service for an EWS server's mailbox.
// It answers as the hosts it is told to, each in its own way: with the EWS
// URL (Serve, ServePOX), or by sending the client on (RedirectURL,
// RedirectAddr, Moved, RedirectGET, SRV), to the same domain or another.
// Both forms are spoken: SOAP (GetUserSettings at autodiscover.svc) and POX
// (autodiscover.xml). Hosts are reached through Net, over HTTPS with a
// certificate for example.com, example.net, example.org and their subdomains; only
// RedirectGET's hosts answer plain HTTP, and a request carrying credentials
// there fails the test.
type Autodiscover struct {
	ews          *EWS
	tls, plain   *httptest.Server
	roots        *x509.CertPool
	mu           sync.Mutex
	hosts        map[string]*adHost
	srv          map[string]*net.SRV // by domain
	credentialed map[string]bool     // hosts a request with credentials reached
	ewsURL       string              // the EWS URL answered, if not ews's
}

// adHost is how one host answers.
type adHost struct {
	soap, pox    bool   // the forms it answers with the EWS URL
	redirectURL  string // a host it sends the client to (RedirectUrl, redirectUrl)
	redirectAddr string // an address it sends the client on with (RedirectAddress, redirectAddr)
	moved        string // a host its POSTs are answered 302 to
	get          string // a host its plain-HTTP GET is answered 302 to
}

const (
	nsADSOAP = "http://schemas.microsoft.com/exchange/2010/Autodiscover"
	nsADResp = "http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006"
	nsADPOX  = "http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a"
)

// StartAutodiscover starts Autodiscover for s, giving s.URL() as the EWS
// URL. It signs in as s does; it is closed when the test ends.
func (s *EWS) StartAutodiscover() *Autodiscover {
	a := &Autodiscover{ews: s, hosts: map[string]*adHost{}, srv: map[string]*net.SRV{}, credentialed: map[string]bool{}}
	cert, err := adCert()
	if err != nil {
		s.t.Fatal(err)
	}
	a.roots = AutodiscoverRoots()
	a.tls = httptest.NewUnstartedServer(http.HandlerFunc(a.serve))
	a.tls.Config.ConnContext = connContext
	a.tls.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	a.tls.StartTLS()
	a.plain = httptest.NewServer(http.HandlerFunc(a.serveGET))
	s.t.Cleanup(a.tls.Close)
	s.t.Cleanup(a.plain.Close)
	return a
}

// Serve makes host answer both forms with the EWS URL.
func (a *Autodiscover) Serve(host string) {
	a.set(host, func(h *adHost) { h.soap, h.pox = true, true })
}

// ServePOX makes host answer POX only, as Exchange 2007 did; SOAP is 404.
func (a *Autodiscover) ServePOX(host string) { a.set(host, func(h *adHost) { h.pox = true }) }

// RedirectURL makes host answer both forms by sending the client to the
// same form on host to.
func (a *Autodiscover) RedirectURL(host, to string) {
	a.set(host, func(h *adHost) { h.redirectURL = to })
}

// RedirectAddr makes host answer both forms by sending the client to start
// over with the address email.
func (a *Autodiscover) RedirectAddr(host, email string) {
	a.set(host, func(h *adHost) { h.redirectAddr = email })
}

// Moved makes host answer its POSTs with an HTTP 302 to the same path on to.
func (a *Autodiscover) Moved(host, to string) { a.set(host, func(h *adHost) { h.moved = to }) }

// RedirectGET makes host answer the unauthenticated plain-HTTP GET of
// autodiscover.xml with a 302 to it on https://to.
func (a *Autodiscover) RedirectGET(host, to string) { a.set(host, func(h *adHost) { h.get = to }) }

// SRV publishes _autodiscover._tcp.domain pointing at target:443.
func (a *Autodiscover) SRV(domain, target string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.srv[domain] = &net.SRV{Target: target + ".", Port: 443, Priority: 0, Weight: 100}
}

// AnswerEWSURL makes every host answer u as the EWS URL instead of the EWS
// server's own.
func (a *Autodiscover) AnswerEWSURL(u string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ewsURL = u
}

// Credentialed reports whether a request carrying credentials reached host.
func (a *Autodiscover) Credentialed(host string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.credentialed[host]
}

// Net reaches the fake's hosts (and nothing else) and its DNS.
func (a *Autodiscover) Net() ews.Net {
	return ews.Net{Dial: a.Dial, RootCAs: a.roots, LookupSRV: a.LookupSRV}
}

// Dial connects to host:443 of a host the fake answers as, or host:80 of a
// RedirectGET host; any other address does not resolve.
func (a *Autodiscover) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	h := a.hosts[host]
	a.mu.Unlock()
	to := ""
	switch {
	case h != nil && port == "443" && h.get == "":
		to = a.tls.Listener.Addr().String()
	case h != nil && port == "80" && h.get != "":
		to = a.plain.Listener.Addr().String()
	default:
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	return (&net.Dialer{}).DialContext(ctx, network, to)
}

// LookupSRV answers _autodiscover._tcp lookups SRV published.
func (a *Autodiscover) LookupSRV(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r, ok := a.srv[name]; ok && service == "autodiscover" && proto == "tcp" {
		return "", []*net.SRV{r}, nil
	}
	return "", nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (a *Autodiscover) set(host string, do func(*adHost)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h := a.hosts[host]
	if h == nil {
		h = &adHost{}
		a.hosts[host] = h
	}
	do(h)
}

// serveGET answers the plain-HTTP step: a GET without credentials.
func (a *Autodiscover) serveGET(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	a.mu.Lock()
	h := a.hosts[host]
	a.mu.Unlock()
	if r.Header.Get("Authorization") != "" || r.Method != http.MethodGet {
		a.ews.t.Errorf("fake Autodiscover: %s over plain HTTP to %s with Authorization %q", r.Method, host, r.Header.Get("Authorization"))
	}
	if h == nil || h.get == "" || r.URL.Path != "/autodiscover/autodiscover.xml" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "https://"+h.get+"/autodiscover/autodiscover.xml", http.StatusFound)
}

func (a *Autodiscover) serve(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	a.mu.Lock()
	h := a.hosts[host]
	if r.Header.Get("Authorization") != "" {
		a.credentialed[host] = true
	}
	ewsURL := a.ewsURL
	a.mu.Unlock()
	if ewsURL == "" {
		ewsURL = a.ews.URL()
	}
	soap := strings.EqualFold(r.URL.Path, "/autodiscover/autodiscover.svc")
	if h == nil || !soap && !strings.EqualFold(r.URL.Path, "/autodiscover/autodiscover.xml") || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	a.ews.mu.Lock()
	ok := a.ews.signIn(w, r)
	a.ews.mu.Unlock()
	if !ok {
		return
	}
	if h.moved != "" {
		http.Redirect(w, r, "https://"+h.moved+r.URL.Path, http.StatusFound)
		return
	}
	body, _ := io.ReadAll(r.Body)
	email := adMailbox(body, soap)
	if email == "" {
		a.ews.t.Errorf("fake Autodiscover: no address in %s", body)
		http.Error(w, "no address", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	switch {
	case soap && h.redirectURL != "":
		a.soap(w, "RedirectUrl", "https://"+h.redirectURL+r.URL.Path, "")
	case soap && h.redirectAddr != "":
		a.soap(w, "RedirectAddress", h.redirectAddr, "")
	case soap && h.soap:
		a.soap(w, "NoError", "", ewsURL)
	case !soap && h.redirectURL != "":
		a.pox(w, "redirectUrl", "<RedirectUrl>https://"+esc(h.redirectURL+r.URL.Path)+"</RedirectUrl>")
	case !soap && h.redirectAddr != "":
		a.pox(w, "redirectAddr", "<RedirectAddr>"+esc(h.redirectAddr)+"</RedirectAddr>")
	case !soap && h.pox:
		a.pox(w, "settings", "<Protocol><Type>EXCH</Type><Server>mbx01.example.com</Server><EwsUrl>https://mbx01.example.com/EWS/Exchange.asmx</EwsUrl></Protocol>"+
			"<Protocol><Type>EXPR</Type><Server>mail.example.com</Server><EwsUrl>"+esc(ewsURL)+"</EwsUrl></Protocol>")
	default:
		http.NotFound(w, r)
	}
}

// adMailbox is the address a SOAP request's Mailbox or a POX request's
// EMailAddress names.
func adMailbox(body []byte, soap bool) string {
	want := "EMailAddress"
	if soap {
		want = "Mailbox"
	}
	d := xml.NewDecoder(strings.NewReader(string(body)))
	var cur string
	for {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		switch el := tok.(type) {
		case xml.StartElement:
			cur = el.Name.Local
		case xml.CharData:
			if cur == want {
				return strings.TrimSpace(string(el))
			}
		case xml.EndElement:
			cur = ""
		}
	}
}

// soap answers GetUserSettings: code is the user's ErrorCode, target its
// RedirectTarget; the EWS URL is the ExternalEwsUrl, with an internal one
// beside it.
func (a *Autodiscover) soap(w http.ResponseWriter, code, target, ewsURL string) {
	settings, redirect := "", `<RedirectTarget i:nil="true"/>`
	if target != "" {
		redirect = "<RedirectTarget>" + esc(target) + "</RedirectTarget>"
	}
	if ewsURL != "" {
		settings = `<UserSetting i:type="StringSetting"><Name>InternalEwsUrl</Name><Value>https://mbx01.example.com/EWS/Exchange.asmx</Value></UserSetting>` +
			`<UserSetting i:type="StringSetting"><Name>ExternalEwsUrl</Name><Value>` + esc(ewsURL) + `</Value></UserSetting>`
	}
	fmt.Fprintf(w, `%s<s:Envelope xmlns:s="%s" xmlns:a="http://www.w3.org/2005/08/addressing"><s:Header>`+
		`<a:Action s:mustUnderstand="1">%s/Autodiscover/GetUserSettingsResponse</a:Action>`+
		`<h:ServerVersionInfo xmlns:h="%s" xmlns:i="http://www.w3.org/2001/XMLSchema-instance"><h:MajorVersion>15</h:MajorVersion><h:MinorVersion>2</h:MinorVersion></h:ServerVersionInfo></s:Header>`+
		`<s:Body><GetUserSettingsResponseMessage xmlns="%s"><Response xmlns:i="http://www.w3.org/2001/XMLSchema-instance">`+
		`<ErrorCode>NoError</ErrorCode><ErrorMessage/><UserResponses><UserResponse><ErrorCode>%s</ErrorCode><ErrorMessage/>%s<UserSettingErrors/>`+
		`<UserSettings>%s</UserSettings></UserResponse></UserResponses></Response></GetUserSettingsResponseMessage></s:Body></s:Envelope>`,
		xml.Header, nsSOAP, nsADSOAP, nsADSOAP, nsADSOAP, code, redirect, settings)
}

// pox answers a POX request with action and the Account's rest.
func (a *Autodiscover) pox(w http.ResponseWriter, action, rest string) {
	fmt.Fprintf(w, `%s<Autodiscover xmlns="%s"><Response xmlns="%s"><User><DisplayName>Alice</DisplayName></User>`+
		`<Account><AccountType>email</AccountType><Action>%s</Action>%s</Account></Response></Autodiscover>`,
		xml.Header, nsADResp, nsADPOX, action, rest)
}

var adCert = sync.OnceValues(func() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		DNSNames: []string{"example.com", "*.example.com", "example.net", "*.example.net", "example.org", "*.example.org"}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, err
})

// AutodiscoverRoots trusts the certificate every Autodiscover presents.
func AutodiscoverRoots() *x509.CertPool {
	cert, err := adCert()
	if err != nil {
		panic(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)
	return roots
}
