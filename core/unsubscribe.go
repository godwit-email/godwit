package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/internal/imagefetch"
)

// The ways a message can be unsubscribed from, by its List-Unsubscribe and
// List-Unsubscribe-Post headers only (RFC 2369, RFC 8058); links in the
// body never count.
const (
	UnsubscribeOneClick = "one_click" // Godwit POSTs List-Unsubscribe=One-Click to URL
	UnsubscribeWeb      = "web"       // the user opens URL in the browser
	UnsubscribeMail     = "mailto"    // the user sends a draft made from URL
)

// Unsubscribe is how a message can be unsubscribed from: Method ("" none),
// Host the request or mail goes to, List the List-Id's name (shown only)
// and URL the address used.
type Unsubscribe struct {
	Method, Host, List, URL string
}

// UnsubscribeResult is what Unsubscribe did. A one-click one was sent when
// Failure is "", else it is why not (an UnsubscribeFailed* value, Status
// the HTTP status for a redirect or a refusal); a mailto one made DraftID.
type UnsubscribeResult struct {
	Method, Host string
	Failure      string
	Status       int
	DraftID      int64
}

// Why a one-click unsubscribe failed.
const (
	UnsubscribeFailedStatus    = "status"     // a status other than 2xx or 3xx
	UnsubscribeFailedRedirect  = "redirect"   // a 3xx, never followed
	UnsubscribeFailedTimeout   = "timeout"    // no answer in time
	UnsubscribeFailedNotPublic = "not_public" // the host is not a public address
	UnsubscribeFailedConnect   = "connect"    // no connection, or it broke
)

var (
	ErrNoUnsubscribe  = errors.New("message has no unsubscribe header")
	ErrUnsubscribeWeb = errors.New("this unsubscribe is a web page: open it in the browser")
	ErrUnsubscribing  = errors.New("already unsubscribing from this message")
)

// errUnsubscribeChanged: the one-click host is not the one a Wit approval
// listed. It goes to the model only, never over RPC.
var errUnsubscribeChanged = errors.New("the unsubscribe address changed since it was approved: ask again")

// oneClickBody is RFC 8058's request body, the only one sent.
const oneClickBody = "List-Unsubscribe=One-Click"

// UnsubscribeMethod is how message can be unsubscribed from; none in a junk
// folder. A message synced before these headers were stored has them fetched
// first, from its account's IMAP server only; one not there (not pushed yet,
// UIDVALIDITY changed, offline, gone) has none until asked again.
func (s *Service) UnsubscribeMethod(ctx context.Context, messageID int64) (Unsubscribe, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Unsubscribe{}, err
	}
	defer end()
	return s.unsubscribeMethod(ctx, messageID)
}

// Unsubscribe unsubscribes from message by its headers, read again (no
// address is taken from the caller): a one-click one is audited (its host
// only) and POSTed; a mailto one becomes a draft from message's account, never
// sent. A web one is ErrUnsubscribeWeb, none ErrNoUnsubscribe; a second
// one-click call while one is under way is ErrUnsubscribing.
func (s *Service) Unsubscribe(ctx context.Context, messageID int64) (UnsubscribeResult, error) {
	return s.unsubscribe(ctx, messageID, nil)
}

// unsubscribe is Unsubscribe for the user's click (approved nil), or for a
// Wit approval: a one-click one then POSTs only to a host it approved, and
// is audited as its call.
func (s *Service) unsubscribe(ctx context.Context, messageID int64, approved *Approval) (UnsubscribeResult, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return UnsubscribeResult{}, err
	}
	defer end()
	u, err := s.unsubscribeMethod(ctx, messageID)
	if err != nil {
		return UnsubscribeResult{}, err
	}
	res := UnsubscribeResult{Method: u.Method, Host: u.Host}
	switch u.Method {
	case UnsubscribeOneClick:
		return s.oneClick(ctx, messageID, u, approved)
	case UnsubscribeMail:
		rows, err := s.db.Query(ctx, "SELECT account_id FROM messages WHERE id = ?", messageID)
		if err != nil {
			return res, err
		}
		if len(rows.Values) == 0 {
			return res, fmt.Errorf("message %d not found", messageID)
		}
		d, err := s.NewMailtoDraft(ctx, store.Int(rows.Values[0][0]), u.URL)
		res.DraftID = d.ID
		return res, err
	case UnsubscribeWeb:
		return res, ErrUnsubscribeWeb
	}
	return res, ErrNoUnsubscribe
}

// NewMailtoDraft starts a new draft from account to a mailto: link's
// addresses, with its subject and (as plain text) body; it is never sent.
func (s *Service) NewMailtoDraft(ctx context.Context, accountID int64, link string) (Draft, error) {
	m, err := mail.ParseMailto(link)
	if err != nil {
		return Draft{}, err
	}
	d, err := s.NewDraft(ctx, accountID, "new", 0)
	if err != nil {
		return Draft{}, err
	}
	d.To, d.Cc, d.Bcc, d.Subject = m.To, m.Cc, m.Bcc, defuseRefs(m.Subject)
	if m.Body != "" {
		d.HTML = "<p>" + escapeLines(defuseRefs(m.Body)) + "</p>" + d.HTML
	}
	return s.SaveDraft(ctx, d)
}

func (s *Service) unsubscribeMethod(ctx context.Context, messageID int64) (Unsubscribe, error) {
	rows, err := s.db.Query(ctx, "SELECT m.account_id, m.list_unsubscribe, m.list_unsubscribe_post, m.list_id, "+inJunk+
		" FROM messages m LEFT JOIN folders f ON f.id = m.folder_id WHERE m.id = ?", messageID)
	if err != nil {
		return Unsubscribe{}, err
	}
	if len(rows.Values) == 0 {
		return Unsubscribe{}, fmt.Errorf("message %d not found", messageID)
	}
	r := rows.Values[0]
	if err := s.bind(ctx, store.Int(r[0])); err != nil { // removing the account stops this call, POST included
		return Unsubscribe{}, err
	}
	if store.Int(r[4]) != 0 { // confirming a spammer's list only confirms the address
		return Unsubscribe{}, nil
	}
	if r[1] == nil {
		h, ok := s.fetchListHeaders(ctx, store.Int(r[0]), messageID)
		if !ok {
			return Unsubscribe{}, ctx.Err()
		}
		r = []any{r[0], h.ListUnsubscribe, h.ListUnsubscribePost, h.ListID}
	}
	return unsubscribeOf(store.Str(r[1]), store.Str(r[2]), store.Str(r[3])), nil
}

// fetchListHeaders gets message's List-* headers from its server location
// and stores them, as GetRawMessage downloads it; false when it cannot.
func (s *Service) fetchListHeaders(ctx context.Context, accountID, messageID int64) (*mail.Parsed, bool) {
	unlock, err := s.lockAccount(ctx, accountID)
	if err != nil {
		return nil, false
	}
	defer unlock()
	// Read under the lock: a sync may have moved, deleted or filled it meanwhile.
	rows, err := s.db.Query(ctx, `SELECT f.name, m.server_uidvalidity, m.server_uid, m.list_unsubscribe, m.list_unsubscribe_post, m.list_id
		FROM messages m LEFT JOIN folders f ON f.id = m.server_folder_id WHERE m.id = ?`, messageID)
	if err != nil || len(rows.Values) == 0 {
		return nil, false
	}
	r := rows.Values[0]
	if r[3] != nil {
		return &mail.Parsed{ListUnsubscribe: store.Str(r[3]), ListUnsubscribePost: store.Str(r[4]), ListID: store.Str(r[5])}, true
	}
	if store.Str(r[0]) == "" || r[2] == nil {
		return nil, false
	}
	c, release, _, err := s.dialAccount(ctx, accountID, nil)
	if err != nil {
		return nil, false
	}
	defer release()
	if sel, err := c.Select(store.Str(r[0]), &imap.SelectOptions{ReadOnly: true}).Wait(); err != nil || sel.UIDValidity != uint32(store.Int(r[1])) {
		return nil, false
	}
	sec := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true,
		HeaderFields: []string{"LIST-UNSUBSCRIBE", "LIST-UNSUBSCRIBE-POST", "LIST-ID"}}
	msgs, err := c.Fetch(imap.UIDSetNum(imap.UID(store.Int(r[2]))), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}}).Collect()
	if err != nil || len(msgs) == 0 {
		return nil, false
	}
	h, err := mail.Parse(msgs[0].FindBodySection(sec))
	if err != nil {
		return nil, false
	}
	if _, err := s.db.Execute(ctx, "UPDATE messages SET list_unsubscribe = ?, list_unsubscribe_post = ?, list_id = ? WHERE id = ?",
		h.ListUnsubscribe, h.ListUnsubscribePost, h.ListID, messageID); err != nil {
		return nil, false
	}
	return h, true
}

// unsubscribeOf decides a message's Unsubscribe from its headers as sent:
// one-click needs the Post header exactly and an https URI; else web (the
// first https URI, else http); else mailto; else none. Only the <>-bracketed
// URIs of List-Unsubscribe count, in order.
func unsubscribeOf(listUnsubscribe, post, listID string) Unsubscribe {
	var https, plain, mailto Unsubscribe
	for rest := listUnsubscribe; ; {
		i := strings.IndexByte(rest, '<')
		j := strings.IndexByte(rest[i+1:], '>')
		if i < 0 || j < 0 {
			break
		}
		uri := strings.Join(strings.Fields(rest[i+1:i+1+j]), "")
		rest = rest[i+2+j:]
		u, err := url.Parse(uri)
		if err != nil {
			continue
		}
		switch strings.ToLower(u.Scheme) {
		case "https":
			if https.URL == "" && u.Hostname() != "" {
				https = Unsubscribe{Method: UnsubscribeWeb, Host: mail.ASCIIHost(u.Hostname()), URL: uri}
			}
		case "http":
			if plain.URL == "" && u.Hostname() != "" {
				plain = Unsubscribe{Method: UnsubscribeWeb, Host: mail.ASCIIHost(u.Hostname()), URL: uri}
			}
		case "mailto":
			if m, err := mail.ParseMailto(uri); mailto.URL == "" && err == nil && len(m.To) > 0 {
				mailto = Unsubscribe{Method: UnsubscribeMail, Host: strings.TrimSuffix(gateway.Host(m.To[0]), ">"), URL: uri}
			}
		}
	}
	var u Unsubscribe
	switch {
	case https.URL != "" && strings.TrimSpace(post) == oneClickBody:
		u, u.Method = https, UnsubscribeOneClick
	case https.URL != "":
		u = https
	case plain.URL != "":
		u = plain
	default:
		u = mailto
	}
	if u.Method != "" {
		u.List = listName(listID)
	}
	return u
}

// listName is a List-Id's phrase, MIME words decoded, else its id.
func listName(listID string) string {
	phrase, id, _ := strings.Cut(listID, "<")
	if phrase = strings.Trim(strings.TrimSpace(phrase), `"`); phrase != "" {
		if d, err := new(mime.WordDecoder).DecodeHeader(phrase); err == nil {
			return d
		}
		return phrase
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(id), ">"))
}

// oneClick POSTs RFC 8058's request to u.URL once, after auditing its host
// (as approved's call, which must list it, if set); a call for message while
// another is under way is ErrUnsubscribing.
func (s *Service) oneClick(ctx context.Context, messageID int64, u Unsubscribe, approved *Approval) (UnsubscribeResult, error) {
	res := UnsubscribeResult{Method: u.Method, Host: u.Host}
	s.unsubMu.Lock()
	if s.unsubbing[messageID] {
		s.unsubMu.Unlock()
		return res, ErrUnsubscribing
	}
	if s.unsubbing == nil {
		s.unsubbing = map[int64]bool{}
	}
	s.unsubbing[messageID] = true
	s.unsubMu.Unlock()
	defer func() {
		s.unsubMu.Lock()
		delete(s.unsubbing, messageID)
		s.unsubMu.Unlock()
	}()
	e := gateway.Entry{Source: gateway.SourceUser, Tool: "unsubscribe", TargetKind: "message",
		TargetID: messageID, Destinations: []string{u.Host}, Decision: "allow"}
	if approved != nil {
		if !slices.Contains(approved.Destinations, u.Host) {
			return res, errUnsubscribeChanged
		}
		// before the POST: the approval's own audit row is written only once it finishes
		e.Source, e.CallID = gateway.SourceWit, approved.CallID
	}
	if err := s.gateway().Record(ctx, e); err != nil {
		return res, err
	}
	res.Failure, res.Status = s.postOneClick(ctx, u.URL)
	return res, ctx.Err() // the caller gave up: not the host's failure
}

// postOneClick sends the request: https only, no cookies, no Referer, no
// redirect followed; only a 2xx is success.
func (s *Service) postOneClick(ctx context.Context, link string) (failure string, status int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, link, strings.NewReader(oneClickBody))
	if err != nil || req.URL.Scheme != "https" {
		return UnsubscribeFailedConnect, 0
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible)")
	c := *s.opts.UnsubscribeClient
	c.Jar = nil
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		var ne net.Error
		switch {
		case errors.Is(err, imagefetch.ErrHost):
			return UnsubscribeFailedNotPublic, 0
		case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout():
			return UnsubscribeFailedTimeout, 0
		}
		return UnsubscribeFailedConnect, 0
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10)) // never read, shown or parsed
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return "", 0
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return UnsubscribeFailedRedirect, resp.StatusCode
	}
	return UnsubscribeFailedStatus, resp.StatusCode
}
