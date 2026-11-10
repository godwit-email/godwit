package ews

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/godwit-email/godwit/backend"
)

// DiscoverConfig is what Discover looks an EWS URL up with.
type DiscoverConfig struct {
	Email       string
	Credentials backend.CredentialsFunc
	// Confirmed are hosts outside Email's registrable domain the user agreed
	// to send the credentials to.
	Confirmed []string
	Net       Net
}

// Net is how Discover reaches the network; zero fields are the system's.
// Tests point it at a fake.
type Net struct {
	Dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	RootCAs   *x509.CertPool
	LookupSRV func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
}

// ConfirmError stops Discover before the credentials go to Host, outside
// the address's registrable domain: run it again with Host confirmed.
type ConfirmError struct{ Host string }

func (e *ConfirmError) Error() string {
	return "ews: Autodiscover leads to " + e.Host + ", outside the address's domain: confirm it to send the password there"
}

// ReasonNoAutodiscover is the add-account check's word for Autodiscover
// finding no EWS URL: the user types it in.
const ReasonNoAutodiscover = "ews.no_autodiscover"

// ErrNoAutodiscover is Autodiscover finding no EWS URL. Its message is
// ReasonNoAutodiscover.
var ErrNoAutodiscover = errors.New(ReasonNoAutodiscover)

const (
	maxHops         = 10
	discoverTimeout = 15 * time.Second // each request's
	soapPath        = "/autodiscover/autodiscover.svc"
	poxPath         = "/autodiscover/autodiscover.xml"
)

// Discover finds the EWS URL of cfg.Email by Autodiscover, trying in turn
// https://<domain>, https://autodiscover.<domain>, the redirect of an
// unauthenticated plain-HTTP GET of autodiscover.<domain> and the
// _autodiscover._tcp SRV record; each SOAP first, then POX. The credentials
// go over HTTPS only, and to a host outside the address's registrable domain
// only once confirmed: before that it returns a *ConfirmError naming it.
// Redirects within the domain are followed.
func Discover(ctx context.Context, cfg DiscoverConfig) (string, error) {
	cr, err := cfg.Credentials(ctx)
	if err != nil {
		return "", err
	}
	t := transport()
	if cfg.Net.Dial != nil {
		t.DialContext = cfg.Net.Dial
	}
	if cfg.Net.RootCAs != nil {
		t.TLSClientConfig = &tls.Config{RootCAs: cfg.Net.RootCAs}
	}
	defer t.CloseIdleConnections()
	noFollow := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	d := &discovery{cfg: cfg, cr: cr, domain: domainOf(cfg.Email),
		http:  &http.Client{Transport: signer{t}, CheckRedirect: noFollow, Timeout: discoverTimeout},
		plain: &http.Client{Transport: t, CheckRedirect: noFollow, Timeout: discoverTimeout}}
	if cfg.Net.LookupSRV == nil {
		d.cfg.Net.LookupSRV = net.DefaultResolver.LookupSRV
	}
	email, tried := cfg.Email, []string{}
	for range maxHops {
		u, next, err := d.find(ctx, email)
		if next == "" {
			return u, err
		}
		tried = append(tried, strings.ToLower(email))
		if slices.Contains(tried, strings.ToLower(next)) {
			break
		}
		email = next
	}
	return "", d.failed()
}

type discovery struct {
	cfg               DiscoverConfig
	cr                backend.Credentials
	domain            string // the address's domain, whose registrable domain is trusted
	http, plain       *http.Client
	answered, refused int // servers that answered a signed-in request, and refused it
}

func domainOf(email string) string {
	_, d, _ := strings.Cut(email, "@")
	return strings.ToLower(strings.TrimSuffix(d, "."))
}

// trusted reports whether host may have the credentials: in the address's
// registrable domain, or confirmed.
func (d *discovery) trusted(host string) bool {
	host = strings.ToLower(host)
	if slices.ContainsFunc(d.cfg.Confirmed, func(h string) bool { return strings.EqualFold(h, host) }) || host == d.domain {
		return true
	}
	a, err := publicsuffix.EffectiveTLDPlusOne(host)
	b, err2 := publicsuffix.EffectiveTLDPlusOne(d.domain)
	return err == nil && err2 == nil && net.ParseIP(host) == nil && a == b
}

func (d *discovery) failed() error {
	if d.answered > 0 && d.answered == d.refused {
		return fmt.Errorf("ews: Autodiscover: %w", backend.ErrAuthFailed)
	}
	return ErrNoAutodiscover
}

// find looks email up at its domain's candidates in order: the EWS URL, or
// next, an address to start over with.
func (d *discovery) find(ctx context.Context, email string) (u, next string, err error) {
	domain := domainOf(email)
	try := func(base string) (bool, error) {
		for _, path := range []string{soapPath, poxPath} {
			r, err := d.post(ctx, base+path, email)
			var confirm *ConfirmError
			if errors.As(err, &confirm) {
				return true, err
			}
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			if err == nil {
				u, next = r.ewsURL, r.address
				return true, nil
			}
		}
		return false, nil
	}
	for _, base := range []string{"https://" + domain, "https://autodiscover." + domain} {
		if done, err := try(base); done {
			return u, next, err
		}
	}
	if loc := d.redirectGET(ctx, domain); loc != nil {
		if done, err := try("https://" + loc.Host); done { // its host, at the usual paths: SOAP first here too
			return u, next, err
		}
	}
	if _, srvs, err := d.cfg.Net.LookupSRV(ctx, "autodiscover", "tcp", domain); err == nil && len(srvs) > 0 {
		// net sorts them by priority and weight: the first is the one to use.
		host := strings.TrimSuffix(srvs[0].Target, ".")
		if srvs[0].Port != 443 {
			host = net.JoinHostPort(host, strconv.Itoa(int(srvs[0].Port)))
		}
		if done, err := try("https://" + host); done {
			return u, next, err
		}
	}
	return "", "", d.failed()
}

// redirectGET is where the unauthenticated plain-HTTP GET of
// autodiscover.<domain> redirects to, if to an https URL. It is the only
// plaintext request, and carries no credentials.
func (d *discovery) redirectGET(ctx context.Context, domain string) *url.URL {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://autodiscover."+domain+poxPath, nil)
	if err != nil {
		return nil
	}
	resp, err := d.plain.Do(req)
	if err != nil {
		return nil
	}
	resp.Body.Close()
	loc, err := resp.Location()
	if err != nil || resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently || loc.Scheme != "https" {
		return nil
	}
	return loc
}

// hop is what an Autodiscover answer leads to: the EWS URL, or an
// address to start over with.
type hop struct{ ewsURL, address string }

// post asks target for email's settings in its form (SOAP for .svc, POX
// otherwise), following HTTP redirects and redirectUrl answers.
func (d *discovery) post(ctx context.Context, target, email string) (hop, error) {
	for range maxHops {
		u, err := url.Parse(target)
		if err != nil || u.Scheme != "https" {
			return hop{}, fmt.Errorf("ews: Autodiscover: refused to send credentials to %q", target)
		}
		if !d.trusted(u.Hostname()) {
			return hop{}, &ConfirmError{Host: u.Hostname()}
		}
		soap := strings.HasSuffix(strings.ToLower(u.Path), ".svc")
		body := poxRequest(email)
		if soap {
			body = soapRequest(target, email)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return hop{}, err
		}
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
		req.SetBasicAuth(d.cr.Username, d.cr.Password)
		resp, err := d.http.Do(req)
		if err != nil {
			return hop{}, err
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return hop{}, err
		}
		d.answered++
		switch resp.StatusCode {
		case http.StatusOK:
		case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			loc, err := resp.Location()
			if err != nil {
				return hop{}, err
			}
			target = loc.String()
			continue
		case http.StatusUnauthorized:
			d.refused++
			return hop{}, fmt.Errorf("ews: Autodiscover: %w", backend.ErrAuthFailed)
		default:
			return hop{}, fmt.Errorf("ews: Autodiscover: HTTP %s", resp.Status)
		}
		var a answer
		if soap {
			a, err = readSOAP(raw)
		} else {
			a, err = readPOX(raw)
		}
		switch {
		case err != nil:
			return hop{}, err
		case a.redirectURL != "":
			target = a.redirectURL
		case a.address != "":
			return hop{address: a.address}, nil
		case a.ewsURL != "":
			return hop{ewsURL: a.ewsURL}, nil
		default:
			return hop{}, ErrNoAutodiscover
		}
	}
	return hop{}, errors.New("ews: Autodiscover: too many redirects")
}

// answer is an Autodiscover response: an EWS URL (the external one before
// the internal), or where to go on.
type answer struct{ ewsURL, redirectURL, address string }

func soapRequest(to, email string) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:a="http://schemas.microsoft.com/exchange/2010/Autodiscover" xmlns:wsa="http://www.w3.org/2005/08/addressing"><soap:Header>`)
	b.WriteString(`<a:RequestedServerVersion>Exchange2013</a:RequestedServerVersion><wsa:Action>http://schemas.microsoft.com/exchange/2010/Autodiscover/Autodiscover/GetUserSettings</wsa:Action><wsa:To>`)
	xml.EscapeText(&b, []byte(to))
	b.WriteString(`</wsa:To></soap:Header><soap:Body><a:GetUserSettingsRequestMessage><a:Request><a:Users><a:User><a:Mailbox>`)
	xml.EscapeText(&b, []byte(email))
	b.WriteString(`</a:Mailbox></a:User></a:Users><a:RequestedSettings><a:Setting>ExternalEwsUrl</a:Setting><a:Setting>InternalEwsUrl</a:Setting></a:RequestedSettings></a:Request></a:GetUserSettingsRequestMessage></soap:Body></soap:Envelope>`)
	return b.Bytes()
}

func poxRequest(email string) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<Autodiscover xmlns="http://schemas.microsoft.com/exchange/autodiscover/outlook/requestschema/2006"><Request><EMailAddress>`)
	xml.EscapeText(&b, []byte(email))
	b.WriteString(`</EMailAddress><AcceptableResponseSchema>http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a</AcceptableResponseSchema></Request></Autodiscover>`)
	return b.Bytes()
}

func readSOAP(raw []byte) (answer, error) {
	var env struct {
		User struct {
			ErrorCode      string `xml:"ErrorCode"`
			RedirectTarget string `xml:"RedirectTarget"`
			Settings       []struct {
				Name  string `xml:"Name"`
				Value string `xml:"Value"`
			} `xml:"UserSettings>UserSetting"`
		} `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body>GetUserSettingsResponseMessage>Response>UserResponses>UserResponse"`
	}
	if err := xml.Unmarshal(raw, &env); err != nil {
		return answer{}, fmt.Errorf("ews: Autodiscover response: %w", err)
	}
	u := env.User
	switch u.ErrorCode {
	case "RedirectUrl":
		return answer{redirectURL: u.RedirectTarget}, nil
	case "RedirectAddress":
		return answer{address: u.RedirectTarget}, nil
	case "NoError":
	default:
		return answer{}, fmt.Errorf("ews: Autodiscover: %s", u.ErrorCode)
	}
	var a answer
	for _, s := range u.Settings {
		if s.Name == "ExternalEwsUrl" || s.Name == "InternalEwsUrl" && a.ewsURL == "" {
			a.ewsURL = s.Value
		}
	}
	return a, nil
}

func readPOX(raw []byte) (answer, error) {
	var r struct {
		Action       string `xml:"Response>Account>Action"`
		RedirectURL  string `xml:"Response>Account>RedirectUrl"`
		RedirectAddr string `xml:"Response>Account>RedirectAddr"`
		Protocols    []struct {
			Type   string `xml:"Type"`
			EwsURL string `xml:"EwsUrl"`
		} `xml:"Response>Account>Protocol"`
	}
	if err := xml.Unmarshal(raw, &r); err != nil {
		return answer{}, fmt.Errorf("ews: Autodiscover response: %w", err)
	}
	switch r.Action {
	case "redirectUrl":
		return answer{redirectURL: r.RedirectURL}, nil
	case "redirectAddr":
		return answer{address: r.RedirectAddr}, nil
	}
	var a answer
	for _, p := range r.Protocols {
		if p.EwsURL != "" && (p.Type == "EXPR" || a.ewsURL == "") {
			a.ewsURL = p.EwsURL
		}
	}
	return a, nil
}
