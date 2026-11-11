package testmail

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Graph is an in-process fake Microsoft Graph v1.0 server with one mailbox
// (alice@outlook.com) that starts with the standard Outlook mail folders.
// It speaks /me, /me/mailFolders (listing, childFolders, well-known names,
// delta), /me/mailFolders/{id}/messages (listing, $filter, delta),
// /me/messages/{id} (GET, PATCH, DELETE, /$value, /move, /copy), POST
// /me/messages (MIME upload), and POST /me/sendMail.
type Graph struct {
	t   testing.TB
	srv *httptest.Server

	mu            sync.Mutex
	token         string
	validToken    func(string) bool
	rejected      bool
	seq           int
	nextID        int
	folders       []*graphFolder
	messages      map[string]*graphItem // keyed by immutableID
	sent          []GraphSent
	deltaPageSize int // max items per delta page; 0 means 50
	listPageSize  int // max folders/messages per list page; 0 means 250

	throttleCount  int
	throttleStatus int
	throttleRetry  string

	resetStatus int    // status code for invalidated deltaLink (default 410)
	resetInner  bool   // put syncStateNotFound in error.innerError.code on 400
	partialFlag bool   // omit unchanged flag properties on delta updates
	omitRoles   bool   // omit wellKnownName on folder list responses
	nonImmutReq int    // requests received without Prefer: IdType="ImmutableId"
	requests    int    // authenticated requests served

	latency          atomic.Int64
	inFlight, maxFly atomic.Int32
}

// GraphSent is a message submitted through /me/sendMail.
type GraphSent struct {
	Raw []byte
}

type graphFolder struct {
	id            string
	parent        string
	name          string
	wellKnownName string
	epoch         int
	created       int
	modseq        int
	gone          bool
	// tombstones records immutableID -> seq when an item left or was deleted
	// from this folder, so messages/delta emits @removed.
	tombstones map[string]int
}

type graphItem struct {
	immutableID string
	mutableID   string
	folderID    string
	raw         []byte
	messageID   string
	received    time.Time
	read        bool
	draft       bool
	flagged     bool
	categories  []string
	modseq      int
	readOnlyMod bool // last update only touched isRead
}

const graphBasePath = "/v1.0"

// StartGraph starts an in-process fake Microsoft Graph server, closed when t ends.
func StartGraph(t testing.TB) *Graph {
	s := &Graph{
		t:           t,
		token:       "graph-token",
		messages:    map[string]*graphItem{},
		resetStatus: http.StatusGone,
	}
	for _, f := range [][2]string{
		{"Inbox", "inbox"},
		{"Sent Items", "sentitems"},
		{"Drafts", "drafts"},
		{"Deleted Items", "deleteditems"},
		{"Junk Email", "junkemail"},
		{"Archive", "archive"},
	} {
		s.addFolder("", f[0], f[1])
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the Graph v1.0 root URL.
func (s *Graph) URL() string { return s.srv.URL + graphBasePath }

// SendURL is the Graph v1.0 sendMail endpoint URL.
func (s *Graph) SendURL() string { return s.srv.URL + graphBasePath + "/me/sendMail" }

// HostURL is the server's origin without /v1.0.
func (s *Graph) HostURL() string { return s.srv.URL }

// Token returns the valid Bearer token, or a rejected one after RejectAuth.
func (s *Graph) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rejected {
		return "revoked-token"
	}
	return s.token
}

// RejectAuth causes future requests with Token() or the old token to fail with 401.
func (s *Graph) RejectAuth() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected = true
	s.token = "rotated-away"
}

// AllowToken sets the accepted Bearer token and clears RejectAuth.
func (s *Graph) AllowToken(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected = false
	s.token = tok
}

// SetTokenValidator checks Bearer tokens with fn (such as testoauth.Server.ValidAccess)
// when non-nil, and clears RejectAuth.
func (s *Graph) SetTokenValidator(fn func(string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected = false
	s.validToken = fn
}

// SetDeltaPageSize limits how many entries each delta response page carries.
func (s *Graph) SetDeltaPageSize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deltaPageSize = n
}

// SetListPageSize limits how many folders/messages a non-delta page carries.
func (s *Graph) SetListPageSize(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listPageSize = n
}

// SetPartialFlagDeltas makes delta responses omit unchanged flag fields on a
// read-only update, exercising Receiver's full-flags fallback.
func (s *Graph) SetPartialFlagDeltas(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partialFlag = on
}

// SetOmitWellKnownNames omits wellKnownName in folder list responses so the
// client must fall back to probing well-known folder paths.
func (s *Graph) SetOmitWellKnownNames(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.omitRoles = on
}

// Throttle makes the next n requests answer with status (429 or 503) and
// Retry-After header retryAfter.
func (s *Graph) Throttle(n, status int, retryAfter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.throttleCount = n
	s.throttleStatus = status
	s.throttleRetry = retryAfter
}

// SetLatency delays each request so concurrency tests can observe overlap.
func (s *Graph) SetLatency(d time.Duration) { s.latency.Store(int64(d)) }

// MaxInFlight returns the maximum concurrent in-flight requests observed.
func (s *Graph) MaxInFlight() int { return int(s.maxFly.Load()) }

// NonImmutableRequests returns how many authenticated requests lacked
// Prefer: IdType="ImmutableId".
func (s *Graph) NonImmutableRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nonImmutReq
}

// Sent returns copies of all messages submitted via /me/sendMail.
func (s *Graph) Sent() []GraphSent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

// MessageState returns the server-side folder ID, read/flagged state and
// categories of immutableID.
func (s *Graph) MessageState(immutableID string) (folderID string, read, flagged bool, categories []string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.messages[immutableID]
	if it == nil {
		return "", false, false, nil, false
	}
	return it.folderID, it.read, it.flagged, slices.Clone(it.categories), true
}

func (s *Graph) addFolder(parent, name, wellKnown string) *graphFolder {
	s.nextID++
	s.seq++
	f := &graphFolder{
		id:            fmt.Sprintf("fld-%d", s.nextID),
		parent:        parent,
		name:          name,
		wellKnownName: wellKnown,
		epoch:         1,
		created:       s.seq,
		modseq:        s.seq,
		tombstones:    map[string]int{},
	}
	s.folders = append(s.folders, f)
	return f
}

// Folder returns the id of folder name (creating nested segments on "/" if
// missing). "INBOX" resolves to the Inbox folder.
func (s *Graph) Folder(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureFolderLocked(name).id
}

func (s *Graph) ensureFolderLocked(name string) *graphFolder {
	if strings.EqualFold(name, "INBOX") {
		for _, f := range s.folders {
			if !f.gone && f.wellKnownName == "inbox" {
				return f
			}
		}
	}
	parts := strings.Split(name, "/")
	parent := ""
	var cur *graphFolder
	for _, seg := range parts {
		found := (*graphFolder)(nil)
		for _, f := range s.folders {
			if !f.gone && f.parent == parent && strings.EqualFold(f.name, seg) {
				found = f
				break
			}
		}
		if found == nil {
			found = s.addFolder(parent, seg, "")
		}
		cur = found
		parent = cur.id
	}
	return cur
}

func (s *Graph) findFolderLocked(idOrName string) *graphFolder {
	low := strings.ToLower(idOrName)
	for _, f := range s.folders {
		if f.gone {
			continue
		}
		if f.id == idOrName || (f.wellKnownName != "" && f.wellKnownName == low) {
			return f
		}
	}
	return nil
}

// Deliver adds raw to folder (creating it if needed) and returns its ImmutableId.
func (s *Graph) Deliver(folder string, raw []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.ensureFolderLocked(folder)
	return s.storeLocked(f, raw, false, false).immutableID
}

func (s *Graph) storeLocked(f *graphFolder, raw []byte, read, draft bool) *graphItem {
	s.nextID++
	s.seq++
	msgID, recv := parseMeta(raw)
	if recv.IsZero() {
		recv = time.Now().UTC().Truncate(time.Second)
	}
	it := &graphItem{
		immutableID: fmt.Sprintf("imm-%d", s.nextID),
		mutableID:   fmt.Sprintf("mut-%s-%d", f.id, s.nextID),
		folderID:    f.id,
		raw:         bytes.Clone(raw),
		messageID:   msgID,
		received:    recv,
		read:        read,
		draft:       draft,
		modseq:      s.seq,
	}
	delete(f.tombstones, it.immutableID)
	s.messages[it.immutableID] = it
	return it
}

func parseMeta(raw []byte) (string, time.Time) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", time.Time{}
	}
	mid := strings.TrimSpace(m.Header.Get("Message-ID"))
	dt, _ := m.Header.Date()
	return mid, dt
}

// Reset invalidates deltaLinks for folder with HTTP 410 Gone.
func (s *Graph) Reset(folder string) {
	s.ResetWith(folder, http.StatusGone, false)
}

// ResetWith invalidates deltaLinks for folder using status (e.g. 410 or 400)
// and optionally nesting syncStateNotFound inside error.innerError.code.
func (s *Graph) ResetWith(folder string, status int, inner bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.ensureFolderLocked(folder)
	f.epoch++
	s.resetStatus = status
	s.resetInner = inner
}

// MarkRead updates a message's isRead state as another client would.
func (s *Graph) MarkRead(immutableID string, read bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it := s.messages[immutableID]; it != nil {
		s.seq++
		it.read = read
		it.modseq = s.seq
		it.readOnlyMod = true
	}
}

// MarkFlagged updates a message's flag state as another client would.
func (s *Graph) MarkFlagged(immutableID string, flagged bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it := s.messages[immutableID]; it != nil {
		s.seq++
		it.flagged = flagged
		it.modseq = s.seq
		it.readOnlyMod = false
	}
}

// SetCategories sets a message's categories as another client would.
func (s *Graph) SetCategories(immutableID string, cats []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it := s.messages[immutableID]; it != nil {
		s.seq++
		it.categories = slices.Clone(cats)
		it.modseq = s.seq
		it.readOnlyMod = false
	}
}

// Remove deletes a message on the server as another client would.
func (s *Graph) Remove(immutableID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if it := s.messages[immutableID]; it != nil {
		s.seq++
		if f := s.findFolderLocked(it.folderID); f != nil {
			f.tombstones[it.immutableID] = s.seq
		}
		delete(s.messages, immutableID)
	}
}

// RenameFolder renames a folder on the server as another client would.
func (s *Graph) RenameFolder(folderID, newName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.findFolderLocked(folderID); f != nil {
		s.seq++
		f.name = newName
		f.modseq = s.seq
	}
}

func wantsImmutable(r *http.Request) bool {
	for _, v := range r.Header.Values("Prefer") {
		for p := range strings.SplitSeq(v, ",") {
			p = strings.TrimSpace(p)
			if strings.EqualFold(p, `IdType="ImmutableId"`) || strings.EqualFold(p, "IdType=ImmutableId") {
				return true
			}
		}
	}
	return false
}

func (s *Graph) itemID(it *graphItem, immutable bool) string {
	if immutable {
		return it.immutableID
	}
	return it.mutableID
}

func (s *Graph) lookupItemLocked(id string, immutable bool) *graphItem {
	if immutable {
		return s.messages[id]
	}
	for _, it := range s.messages {
		if it.mutableID == id {
			return it
		}
	}
	return s.messages[id]
}

func (s *Graph) serve(w http.ResponseWriter, r *http.Request) {
	cur := s.inFlight.Add(1)
	for {
		old := s.maxFly.Load()
		if cur <= old || s.maxFly.CompareAndSwap(old, cur) {
			break
		}
	}
	defer s.inFlight.Add(-1)
	if d := time.Duration(s.latency.Load()); d > 0 {
		time.Sleep(d)
	}

	s.mu.Lock()
	authHdr := r.Header.Get("Authorization")
	tok, hasBearer := strings.CutPrefix(authHdr, "Bearer ")
	valid := hasBearer && tok == s.token
	if s.validToken != nil {
		valid = hasBearer && s.validToken(tok)
	}
	if s.rejected || !valid {
		s.mu.Unlock()
		writeGraphError(w, http.StatusUnauthorized, "InvalidAuthenticationToken", "Access token validation failure.", "")
		return
	}
	immut := wantsImmutable(r)
	if !immut {
		s.nonImmutReq++
	}
	s.requests++
	if s.throttleCount > 0 {
		s.throttleCount--
		st, ra := s.throttleStatus, s.throttleRetry
		s.mu.Unlock()
		if ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		writeGraphError(w, st, "ApplicationThrottled", "Application is over its MailboxConcurrency or rate limit.", "")
		return
	}
	s.mu.Unlock()

	if immut {
		w.Header().Set("Preference-Applied", `IdType="ImmutableId"`)
	}

	if !strings.HasPrefix(r.URL.Path, graphBasePath+"/") {
		writeGraphError(w, http.StatusNotFound, "BadRequest", "Resource not found", "")
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, graphBasePath)

	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && sub == "/me":
		writeJSON(w, http.StatusOK, map[string]any{
			"id":                "user-alice",
			"mail":              "alice@outlook.com",
			"userPrincipalName": "alice@outlook.com",
		})
	case r.Method == http.MethodPost && sub == "/me/sendMail":
		s.handleSendMail(w, r)
	case r.Method == http.MethodGet && sub == "/me/mailFolders":
		s.handleListFolders(w, r, "")
	case r.Method == http.MethodGet && sub == "/me/mailFolders/delta":
		s.handleFoldersDelta(w, r)
	case r.Method == http.MethodPost && sub == "/me/messages":
		s.handleCreateMessage(w, r, immut)
	case strings.HasPrefix(sub, "/me/mailFolders/"):
		s.handleMailFolderSub(w, r, strings.TrimPrefix(sub, "/me/mailFolders/"), immut)
	case strings.HasPrefix(sub, "/me/messages/"):
		s.handleMessageSub(w, r, strings.TrimPrefix(sub, "/me/messages/"), immut)
	default:
		writeGraphError(w, http.StatusNotFound, "ResourceNotFound", "Unknown path "+sub, "")
	}
}

func (s *Graph) folderJSON(f *graphFolder) map[string]any {
	childCount := 0
	for _, c := range s.folders {
		if !c.gone && c.parent == f.id {
			childCount++
		}
	}
	parent := f.parent
	if parent == "" {
		parent = "msgfolderroot"
	}
	m := map[string]any{
		"id":               f.id,
		"displayName":      f.name,
		"parentFolderId":   parent,
		"childFolderCount": childCount,
	}
	if !s.omitRoles && f.wellKnownName != "" {
		m["wellKnownName"] = f.wellKnownName
	}
	return m
}

func (s *Graph) handleListFolders(w http.ResponseWriter, r *http.Request, parent string) {
	var list []map[string]any
	for _, f := range s.folders {
		if !f.gone && f.parent == parent {
			list = append(list, s.folderJSON(f))
		}
	}
	s.paginateSlice(w, r, list)
}

func (s *Graph) handleFoldersDelta(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := 0
	if tok := q.Get("$deltatoken"); tok != "" {
		n, err := strconv.Atoi(tok)
		if err != nil {
			writeGraphError(w, http.StatusGone, "syncStateNotFound", "Invalid folder delta token", "")
			return
		}
		since = n
	}
	var items []map[string]any
	for _, f := range s.folders {
		if since == 0 {
			if !f.gone {
				items = append(items, s.folderJSON(f))
			}
			continue
		}
		if f.modseq <= since {
			continue
		}
		if f.gone {
			items = append(items, map[string]any{
				"id":       f.id,
				"@removed": map[string]string{"reason": "deleted"},
			})
		} else {
			items = append(items, s.folderJSON(f))
		}
	}
	s.paginateDelta(w, r, items, strconv.Itoa(s.seq))
}

func (s *Graph) handleMailFolderSub(w http.ResponseWriter, r *http.Request, rest string, immut bool) {
	idPart, tail, hasTail := strings.Cut(rest, "/")
	idPart, _ = url.PathUnescape(idPart)
	f := s.findFolderLocked(idPart)
	if f == nil {
		writeGraphError(w, http.StatusNotFound, "ErrorItemNotFound", "Folder not found", "")
		return
	}
	if !hasTail {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, s.folderJSON(f))
			return
		}
		writeGraphError(w, http.StatusMethodNotAllowed, "BadRequest", "Unsupported method", "")
		return
	}
	switch {
	case r.Method == http.MethodGet && tail == "childFolders":
		s.handleListFolders(w, r, f.id)
	case r.Method == http.MethodGet && tail == "messages/delta":
		s.handleMessagesDelta(w, r, f, immut)
	case r.Method == http.MethodGet && tail == "messages":
		s.handleFolderMessages(w, r, f, immut)
	default:
		writeGraphError(w, http.StatusNotFound, "ResourceNotFound", "Unknown folder path "+tail, "")
	}
}

func (s *Graph) messageJSON(it *graphItem, immut, partial bool) map[string]any {
	cats := it.categories
	if cats == nil {
		cats = []string{}
	}
	flagStatus := "notFlagged"
	if it.flagged {
		flagStatus = "flagged"
	}
	m := map[string]any{
		"id":                s.itemID(it, immut),
		"parentFolderId":    it.folderID,
		"internetMessageId": it.messageID,
		"receivedDateTime":  it.received.UTC().Format(time.RFC3339),
		"isRead":            it.read,
	}
	if !partial {
		m["isDraft"] = it.draft
		m["categories"] = cats
		m["flag"] = map[string]any{"flagStatus": flagStatus}
	}
	return m
}

func (s *Graph) handleMessagesDelta(w http.ResponseWriter, r *http.Request, f *graphFolder, immut bool) {
	q := r.URL.Query()
	targetSeq := s.seq
	since := 0
	if dtok := q.Get("$deltatoken"); dtok != "" {
		epStr, seqStr, ok := strings.Cut(dtok, ":")
		ep, err1 := strconv.Atoi(epStr)
		sq, err2 := strconv.Atoi(seqStr)
		if !ok || err1 != nil || err2 != nil || ep != f.epoch {
			if s.resetInner {
				writeGraphError(w, s.resetStatus, "BadRequest", "Sync state not found", "syncStateNotFound")
			} else {
				writeGraphError(w, s.resetStatus, "syncStateNotFound", "Sync state not found", "")
			}
			return
		}
		since = sq
	}
	if stok := q.Get("$skiptoken"); stok != "" {
		// $skiptoken encodes "<epoch>:<since>:<targetSeq>:<offset>"
		parts := strings.Split(stok, ":")
		if len(parts) == 4 {
			ep, _ := strconv.Atoi(parts[0])
			if ep != f.epoch {
				writeGraphError(w, s.resetStatus, "syncStateNotFound", "Sync state expired during paging", "")
				return
			}
			since, _ = strconv.Atoi(parts[1])
			targetSeq, _ = strconv.Atoi(parts[2])
		}
	}

	var live []*graphItem
	for _, it := range s.messages {
		if it.folderID == f.id && (since == 0 || it.modseq > since) && it.modseq <= targetSeq {
			live = append(live, it)
		}
	}
	slices.SortFunc(live, func(a, b *graphItem) int {
		if !a.received.Equal(b.received) {
			return b.received.Compare(a.received) // newest first
		}
		return strings.Compare(a.immutableID, b.immutableID)
	})

	var entries []map[string]any
	for _, it := range live {
		partial := since > 0 && s.partialFlag && it.readOnlyMod
		entries = append(entries, s.messageJSON(it, immut, partial))
	}
	if since > 0 {
		var goneIDs []string
		for id, tseq := range f.tombstones {
			if tseq > since && tseq <= targetSeq {
				if cur := s.messages[id]; cur != nil && cur.folderID == f.id {
					continue
				}
				goneIDs = append(goneIDs, id)
			}
		}
		slices.Sort(goneIDs)
		for _, id := range goneIDs {
			entries = append(entries, map[string]any{
				"id":       id,
				"@removed": map[string]string{"reason": "deleted"},
			})
		}
	}

	offset := 0
	if stok := q.Get("$skiptoken"); stok != "" {
		parts := strings.Split(stok, ":")
		if len(parts) == 4 {
			offset, _ = strconv.Atoi(parts[3])
		}
	}
	pageSize := s.deltaPageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if offset > len(entries) {
		offset = len(entries)
	}
	end := min(offset+pageSize, len(entries))
	page := entries[offset:end]
	if page == nil {
		page = []map[string]any{}
	}

	baseURL := s.srv.URL + r.URL.Path
	resp := map[string]any{"value": page}
	if end < len(entries) {
		u, _ := url.Parse(baseURL)
		nq := url.Values{}
		if sel := q.Get("$select"); sel != "" {
			nq.Set("$select", sel)
		}
		nq.Set("$skiptoken", fmt.Sprintf("%d:%d:%d:%d", f.epoch, since, targetSeq, end))
		u.RawQuery = nq.Encode()
		resp["@odata.nextLink"] = u.String()
	} else {
		u, _ := url.Parse(baseURL)
		dq := url.Values{}
		if sel := q.Get("$select"); sel != "" {
			dq.Set("$select", sel)
		}
		dq.Set("$deltatoken", fmt.Sprintf("%d:%d", f.epoch, targetSeq))
		u.RawQuery = dq.Encode()
		resp["@odata.deltaLink"] = u.String()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Graph) handleFolderMessages(w http.ResponseWriter, r *http.Request, f *graphFolder, immut bool) {
	q := r.URL.Query()
	filter := q.Get("$filter")
	var wantMid string
	var hasFilter bool
	if strings.HasPrefix(filter, "internetMessageId eq '") && strings.HasSuffix(filter, "'") {
		hasFilter = true
		wantMid = strings.TrimSuffix(strings.TrimPrefix(filter, "internetMessageId eq '"), "'")
		wantMid = strings.ReplaceAll(wantMid, "''", "'")
	}

	var items []*graphItem
	for _, it := range s.messages {
		if it.folderID != f.id {
			continue
		}
		if hasFilter && it.messageID != wantMid {
			continue
		}
		items = append(items, it)
	}
	slices.SortFunc(items, func(a, b *graphItem) int {
		if !a.received.Equal(b.received) {
			return b.received.Compare(a.received)
		}
		return strings.Compare(b.immutableID, a.immutableID)
	})

	skip := 0
	if sv := q.Get("$skip"); sv != "" {
		skip, _ = strconv.Atoi(sv)
	}
	if skip > len(items) {
		skip = len(items)
	}
	items = items[skip:]

	top := s.listPageSize
	if top <= 0 {
		top = 250
	}
	if tv := q.Get("$top"); tv != "" {
		if n, err := strconv.Atoi(tv); err == nil && n > 0 && n < top {
			top = n
		}
	}
	explicitTop := q.Get("$top") != "" && s.listPageSize <= 0
	var nextLink string
	if len(items) > top {
		items = items[:top]
		if !explicitTop || q.Get("$skip") == "" {
			u, _ := url.Parse(s.srv.URL + r.URL.Path)
			nq := mapsCloneQuery(q)
			nq.Set("$skip", strconv.Itoa(skip+top))
			u.RawQuery = nq.Encode()
			nextLink = u.String()
		}
	}

	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, s.messageJSON(it, immut, false))
	}
	resp := map[string]any{"value": out}
	if nextLink != "" {
		resp["@odata.nextLink"] = nextLink
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Graph) handleCreateMessage(w http.ResponseWriter, r *http.Request, immut bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "BadRequest", err.Error(), "")
		return
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, "text/plain") {
		writeGraphError(w, http.StatusBadRequest, "BadRequest", "Expected text/plain base64 MIME", "")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "BadRequest", "Invalid base64 MIME", "")
		return
	}
	drafts := s.findFolderLocked("drafts")
	if drafts == nil {
		drafts = s.ensureFolderLocked("Drafts")
	}
	it := s.storeLocked(drafts, raw, false, true)
	writeJSON(w, http.StatusCreated, s.messageJSON(it, immut, false))
}

func (s *Graph) handleMessageSub(w http.ResponseWriter, r *http.Request, rest string, immut bool) {
	idPart, tail, hasTail := strings.Cut(rest, "/")
	idPart, _ = url.PathUnescape(idPart)
	it := s.lookupItemLocked(idPart, immut)
	if it == nil {
		writeGraphError(w, http.StatusNotFound, "ErrorItemNotFound", "The specified object was not found in the store.", "")
		return
	}
	if !hasTail {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, s.messageJSON(it, immut, false))
		case http.MethodPatch:
			s.handlePatchMessage(w, r, it, immut)
		case http.MethodDelete:
			s.seq++
			if f := s.findFolderLocked(it.folderID); f != nil {
				f.tombstones[it.immutableID] = s.seq
			}
			delete(s.messages, it.immutableID)
			w.WriteHeader(http.StatusNoContent)
		default:
			writeGraphError(w, http.StatusMethodNotAllowed, "BadRequest", "Unsupported method", "")
		}
		return
	}
	switch {
	case r.Method == http.MethodGet && tail == "$value":
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(it.raw)
	case r.Method == http.MethodPost && (tail == "move" || tail == "copy"):
		s.handleMoveOrCopy(w, r, it, tail == "copy", immut)
	default:
		writeGraphError(w, http.StatusNotFound, "ResourceNotFound", "Unknown message action "+tail, "")
	}
}

func (s *Graph) handlePatchMessage(w http.ResponseWriter, r *http.Request, it *graphItem, immut bool) {
	var body struct {
		IsRead     *bool     `json:"isRead"`
		Categories *[]string `json:"categories"`
		Flag       *struct {
			FlagStatus string `json:"flagStatus"`
		} `json:"flag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeGraphError(w, http.StatusBadRequest, "BadRequest", err.Error(), "")
		return
	}
	s.seq++
	readOnly := body.IsRead != nil && body.Categories == nil && body.Flag == nil
	if body.IsRead != nil {
		it.read = *body.IsRead
	}
	if body.Flag != nil {
		it.flagged = strings.EqualFold(body.Flag.FlagStatus, "flagged")
	}
	if body.Categories != nil {
		it.categories = slices.Clone(*body.Categories)
	}
	it.modseq = s.seq
	it.readOnlyMod = readOnly
	writeJSON(w, http.StatusOK, s.messageJSON(it, immut, false))
}

func (s *Graph) handleMoveOrCopy(w http.ResponseWriter, r *http.Request, it *graphItem, copyOp, immut bool) {
	var body struct {
		DestinationID string `json:"destinationId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DestinationID == "" {
		writeGraphError(w, http.StatusBadRequest, "BadRequest", "Missing destinationId", "")
		return
	}
	dst := s.findFolderLocked(body.DestinationID)
	if dst == nil {
		writeGraphError(w, http.StatusNotFound, "ErrorFolderNotFound", "Destination folder not found", "")
		return
	}
	if copyOp {
		cp := s.storeLocked(dst, it.raw, it.read, it.draft)
		cp.flagged = it.flagged
		cp.categories = slices.Clone(it.categories)
		writeJSON(w, http.StatusCreated, s.messageJSON(cp, immut, false))
		return
	}
	s.seq++
	if old := s.findFolderLocked(it.folderID); old != nil && old.id != dst.id {
		old.tombstones[it.immutableID] = s.seq
	}
	s.nextID++
	it.folderID = dst.id
	it.mutableID = fmt.Sprintf("mut-%s-%d", dst.id, s.nextID) // mutable ID changes on move; immutableID stays!
	it.modseq = s.seq
	it.readOnlyMod = false
	delete(dst.tombstones, it.immutableID)
	writeJSON(w, http.StatusCreated, s.messageJSON(it, immut, false))
}

func (s *Graph) handleSendMail(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "BadRequest", err.Error(), "")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		writeGraphError(w, http.StatusBadRequest, "ErrorMimeContentInvalidBase64String", "Invalid base64 MIME", "")
		return
	}
	s.sent = append(s.sent, GraphSent{Raw: bytes.Clone(raw)})
	if sentFolder := s.findFolderLocked("sentitems"); sentFolder != nil {
		s.storeLocked(sentFolder, stripBccHeader(raw), true, false)
	}
	w.WriteHeader(http.StatusAccepted)
}

func stripBccHeader(raw []byte) []byte {
	// Sender prepends "Bcc: ...\r\n" (possibly folded) to raw when Bcc recipients exist.
	if !bytes.HasPrefix(bytes.ToLower(raw), []byte("bcc:")) {
		return raw
	}
	rest := raw
	for len(rest) > 0 {
		idx := bytes.IndexByte(rest, '\n')
		if idx < 0 {
			return raw
		}
		rest = rest[idx+1:]
		if len(rest) == 0 || (rest[0] != ' ' && rest[0] != '\t') {
			break
		}
	}
	return rest
}

func (s *Graph) paginateSlice(w http.ResponseWriter, r *http.Request, all []map[string]any) {
	q := r.URL.Query()
	skip := 0
	if sv := q.Get("$skip"); sv != "" {
		skip, _ = strconv.Atoi(sv)
	}
	if skip > len(all) {
		skip = len(all)
	}
	rest := all[skip:]
	size := s.listPageSize
	if size <= 0 {
		size = 250
	}
	if tv := q.Get("$top"); tv != "" {
		if n, err := strconv.Atoi(tv); err == nil && n > 0 && n < size {
			size = n
		}
	}
	var next string
	if len(rest) > size {
		rest = rest[:size]
		u, _ := url.Parse(s.srv.URL + r.URL.Path)
		nq := mapsCloneQuery(q)
		nq.Set("$skip", strconv.Itoa(skip+size))
		u.RawQuery = nq.Encode()
		next = u.String()
	}
	if rest == nil {
		rest = []map[string]any{}
	}
	resp := map[string]any{"value": rest}
	if next != "" {
		resp["@odata.nextLink"] = next
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Graph) paginateDelta(w http.ResponseWriter, r *http.Request, all []map[string]any, deltaTok string) {
	q := r.URL.Query()
	skip := 0
	if sv := q.Get("$skiptoken"); sv != "" {
		skip, _ = strconv.Atoi(sv)
	}
	if skip > len(all) {
		skip = len(all)
	}
	rest := all[skip:]
	size := s.deltaPageSize
	if size <= 0 {
		size = 50
	}
	baseURL := s.srv.URL + r.URL.Path
	resp := map[string]any{}
	if len(rest) > size {
		rest = rest[:size]
		u, _ := url.Parse(baseURL)
		nq := url.Values{}
		if sel := q.Get("$select"); sel != "" {
			nq.Set("$select", sel)
		}
		nq.Set("$skiptoken", strconv.Itoa(skip+size))
		u.RawQuery = nq.Encode()
		resp["@odata.nextLink"] = u.String()
	} else {
		u, _ := url.Parse(baseURL)
		dq := url.Values{}
		if sel := q.Get("$select"); sel != "" {
			dq.Set("$select", sel)
		}
		dq.Set("$deltatoken", deltaTok)
		u.RawQuery = dq.Encode()
		resp["@odata.deltaLink"] = u.String()
	}
	if rest == nil {
		rest = []map[string]any{}
	}
	resp["value"] = rest
	writeJSON(w, http.StatusOK, resp)
}

func mapsCloneQuery(q url.Values) url.Values {
	out := make(url.Values, len(q))
	for k, vs := range q {
		out[k] = slices.Clone(vs)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeGraphError(w http.ResponseWriter, status int, code, msg, innerCode string) {
	errObj := map[string]any{
		"code":    code,
		"message": msg,
	}
	if innerCode != "" {
		errObj["innerError"] = map[string]any{"code": innerCode}
	}
	writeJSON(w, status, map[string]any{"error": errObj})
}
