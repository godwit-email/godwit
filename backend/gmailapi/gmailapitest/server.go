// Package gmailapitest is an in-process fake Gmail REST API v1 server for
// backend contract tests and coretest end-to-end tests, with zero external
// network dependency.
package gmailapitest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Delivery is one message accepted by users.messages.send.
type Delivery struct {
	Rcpts []string
	Raw   []byte
}

type label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type storedMsg struct {
	id           string
	threadID     string
	raw          []byte
	labels       []string
	internalDate int64
	historyID    uint64
}

type historyEntry struct {
	id            uint64
	added         *storedMsg
	deletedID     string
	labelsAdded   []string
	labelsRemoved []string
	targetID      string
}

type throttleRule struct {
	left       int
	status     int
	retryAfter string
}

// Server is an in-process fake Gmail REST API v1 server for one account.
type Server struct {
	srv *httptest.Server

	inFlight    atomic.Int32
	maxInFlight atomic.Int32
	listReqs    atomic.Int32

	mu           sync.Mutex
	email        string
	token        string
	validToken   func(string) bool
	rejectAuth   bool
	apiDisabled  bool
	loseVerdict  bool
	throttle     throttleRule
	pageLimit    int // 0 uses query maxResults
	labels       map[string]label
	nextLabel    int
	msgs         map[string]*storedMsg
	order        []string // insertion order (oldest to newest)
	nextMsg      int
	historyID    uint64
	minHistoryID uint64
	history      []historyEntry
	refused      map[string]bool
	accepted     []Delivery
}

// Start starts a new fake Gmail API server, closed when tb ends.
func Start(tb testing.TB) *Server {
	tb.Helper()
	s := &Server{
		email:        "alice@gmail.com",
		token:        "test-oauth-token",
		labels:       map[string]label{},
		msgs:         map[string]*storedMsg{},
		historyID:    100,
		minHistoryID: 1,
		refused:      map[string]bool{},
	}
	for _, sys := range []string{"INBOX", "SENT", "DRAFT", "SPAM", "TRASH", "STARRED", "IMPORTANT", "UNREAD", "CATEGORY_PERSONAL", "CHAT"} {
		s.labels[sys] = label{ID: sys, Name: sys, Type: "system"}
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	tb.Cleanup(s.srv.Close)
	return s
}

// URL is the server's base URL (http://127.0.0.1:port).
func (s *Server) URL() string { return s.srv.URL }

// HostPort is the listener's host and port.
func (s *Server) HostPort() (string, int) {
	u := strings.TrimPrefix(s.srv.URL, "http://")
	h, p, _ := strings.Cut(u, ":")
	port, _ := strconv.Atoi(p)
	return h, port
}

// Token is the default valid Bearer token when SetTokenValidator is not set.
func (s *Server) Token() string { return s.token }

// SetEmail sets the address getProfile reports.
func (s *Server) SetEmail(email string) {
	s.mu.Lock()
	s.email = email
	s.mu.Unlock()
}

// SetTokenValidator sets a custom Bearer token check (such as testoauth's).
func (s *Server) SetTokenValidator(fn func(string) bool) {
	s.mu.Lock()
	s.validToken = fn
	s.mu.Unlock()
}

// RejectAuth makes every request fail with 401 Unauthorized.
func (s *Server) RejectAuth() {
	s.mu.Lock()
	s.rejectAuth = true
	s.mu.Unlock()
}

// SetAPIDisabled makes every request fail with 403 accessNotConfigured.
func (s *Server) SetAPIDisabled(disabled bool) {
	s.mu.Lock()
	s.apiDisabled = disabled
	s.mu.Unlock()
}

// ThrottleNext makes the next n authenticated requests return status (429 or 403
// rateLimitExceeded) with optional Retry-After header before succeeding.
func (s *Server) ThrottleNext(n, status int, retryAfter string) {
	s.mu.Lock()
	s.throttle = throttleRule{left: n, status: status, retryAfter: retryAfter}
	s.mu.Unlock()
}

// MaxInFlight returns the peak concurrent requests observed by the server.
func (s *Server) MaxInFlight() int { return int(s.maxInFlight.Load()) }

// SetPageLimit caps list/history page sizes to test pagination.
func (s *Server) SetPageLimit(n int) {
	s.mu.Lock()
	s.pageLimit = n
	s.mu.Unlock()
}

// ExpireHistory invalidates all historyIds issued so far so history.list returns 404.
func (s *Server) ExpireHistory() {
	s.mu.Lock()
	s.historyID++
	s.minHistoryID = s.historyID
	s.mu.Unlock()
}

// Refuse makes messages.send permanently reject mail addressed to rcpt.
func (s *Server) Refuse(rcpt string) {
	s.mu.Lock()
	s.refused[strings.ToLower(rcpt)] = true
	s.mu.Unlock()
}

// LoseVerdict makes the next messages.send drop the TCP connection after reading the body.
func (s *Server) LoseVerdict() {
	s.mu.Lock()
	s.loseVerdict = true
	s.mu.Unlock()
}

// Accepted returns all messages accepted by messages.send, in order.
func (s *Server) Accepted() []Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.accepted)
}

// EnsureLabel returns the label ID for name, creating a user label if needed.
func (s *Server) EnsureLabel(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureLabelLocked(name)
}

func (s *Server) ensureLabelLocked(name string) string {
	if l, ok := s.labels[name]; ok {
		return l.ID
	}
	for _, l := range s.labels {
		if l.Name == name {
			return l.ID
		}
	}
	if i := strings.LastIndex(name, "/"); i > 0 {
		s.ensureLabelLocked(name[:i])
	}
	s.nextLabel++
	id := fmt.Sprintf("Label_%d", s.nextLabel)
	s.labels[id] = label{ID: id, Name: name, Type: "user"}
	return id
}

// RenameLabel renames an existing user label while preserving its ID.
func (s *Server) RenameLabel(id, newName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.labels[id]; ok {
		l.Name = newName
		s.labels[id] = l
	}
}

// DeleteLabel deletes a user label and removes it from all messages.
func (s *Server) DeleteLabel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.labels, id)
	for _, m := range s.msgs {
		if slices.Contains(m.labels, id) {
			m.labels = slices.DeleteFunc(m.labels, func(x string) bool { return x == id })
			s.historyID++
			m.historyID = s.historyID
			s.history = append(s.history, historyEntry{id: s.historyID, targetID: m.id, labelsRemoved: []string{id}})
		}
	}
}

// AddMessage adds a message with the given label names or IDs (such as "INBOX",
// "UNREAD", "Work") and returns its Gmail message ID.
func (s *Server) AddMessage(raw []byte, labelNamesOrIDs ...string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, name := range labelNamesOrIDs {
		id := s.ensureLabelLocked(name)
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return s.insertLocked(raw, ids).id
}

// Deliver delivers raw as a new unread message into folder (creating a user label if needed).
func (s *Server) Deliver(folder string, raw []byte) string {
	return s.AddMessage(raw, folder, "UNREAD")
}

// ModifyLabels adds and removes labels (by name or ID) on msgID, as webmail would.
func (s *Server) ModifyLabels(msgID string, add, remove []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.msgs[msgID]
	if m == nil {
		return
	}
	var addIDs, remIDs []string
	for _, a := range add {
		addIDs = append(addIDs, s.ensureLabelLocked(a))
	}
	for _, r := range remove {
		if l, ok := s.labels[r]; ok {
			remIDs = append(remIDs, l.ID)
		} else {
			for _, l := range s.labels {
				if l.Name == r {
					remIDs = append(remIDs, l.ID)
				}
			}
		}
	}
	s.applyModifyLocked(m, addIDs, remIDs)
}

// DeleteMessage permanently deletes msgID on the server, recording messagesDeleted.
func (s *Server) DeleteMessage(msgID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.msgs[msgID] == nil {
		return
	}
	delete(s.msgs, msgID)
	s.order = slices.DeleteFunc(s.order, func(x string) bool { return x == msgID })
	s.historyID++
	s.history = append(s.history, historyEntry{id: s.historyID, deletedID: msgID})
}

// MessageLabels returns a copy of msgID's current label IDs, or nil if deleted.
func (s *Server) MessageLabels(msgID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.msgs[msgID]
	if m == nil {
		return nil
	}
	return slices.Clone(m.labels)
}

func (s *Server) insertLocked(raw []byte, labelIDs []string) *storedMsg {
	s.nextMsg++
	s.historyID++
	id := fmt.Sprintf("msg-%d", s.nextMsg)
	ms := time.Now().UnixMilli()
	if parsed, err := mail.ReadMessage(bytes.NewReader(raw)); err == nil {
		if dt, err := parsed.Header.Date(); err == nil {
			ms = dt.UnixMilli()
		}
	}
	m := &storedMsg{
		id:           id,
		threadID:     "thr-" + id,
		raw:          bytes.Clone(raw),
		labels:       slices.Clone(labelIDs),
		internalDate: ms,
		historyID:    s.historyID,
	}
	s.msgs[id] = m
	s.order = append(s.order, id)
	cp := *m
	cp.labels = slices.Clone(m.labels)
	s.history = append(s.history, historyEntry{id: s.historyID, added: &cp})
	return m
}

func (s *Server) applyModifyLocked(m *storedMsg, add, remove []string) {
	var added, removed []string
	if slices.Contains(add, "TRASH") || slices.Contains(add, "SPAM") {
		if !slices.Contains(remove, "INBOX") {
			remove = append(slices.Clone(remove), "INBOX")
		}
	}
	for _, r := range remove {
		if slices.Contains(m.labels, r) {
			m.labels = slices.DeleteFunc(m.labels, func(x string) bool { return x == r })
			removed = append(removed, r)
		}
	}
	for _, a := range add {
		if !slices.Contains(m.labels, a) {
			m.labels = append(m.labels, a)
			added = append(added, a)
		}
	}
	if len(added) > 0 || len(removed) > 0 {
		s.historyID++
		m.historyID = s.historyID
		s.history = append(s.history, historyEntry{
			id:            s.historyID,
			targetID:      m.id,
			labelsAdded:   added,
			labelsRemoved: removed,
		})
	}
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	cur := s.inFlight.Add(1)
	for {
		old := s.maxInFlight.Load()
		if cur <= old || s.maxInFlight.CompareAndSwap(old, cur) {
			break
		}
	}
	defer s.inFlight.Add(-1)

	if !s.authorize(w, r) {
		return
	}

	const prefix = "/gmail/v1/users/me/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeErr(w, http.StatusNotFound, "notFound", "unknown path")
		return
	}
	sub := strings.TrimPrefix(r.URL.Path, prefix)
	switch {
	case sub == "profile" && r.Method == http.MethodGet:
		s.handleProfile(w)
	case sub == "labels" && r.Method == http.MethodGet:
		s.handleLabels(w)
	case sub == "history" && r.Method == http.MethodGet:
		s.handleHistory(w, r)
	case sub == "messages" && r.Method == http.MethodGet:
		s.handleListMessages(w, r)
	case sub == "messages" && r.Method == http.MethodPost:
		s.handleInsertMessage(w, r)
	case sub == "messages/send" && r.Method == http.MethodPost:
		s.handleSendMessage(w, r)
	case strings.HasPrefix(sub, "messages/"):
		s.handleMessageOp(w, r, strings.TrimPrefix(sub, "messages/"))
	default:
		writeErr(w, http.StatusNotFound, "notFound", "unknown endpoint")
	}
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	authHdr := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(authHdr, "Bearer ")
	s.mu.Lock()
	reject := s.rejectAuth
	validFn := s.validToken
	wantTok := s.token
	disabled := s.apiDisabled
	var th throttleRule
	if !reject && ok && tok != "" && (validFn == nil && tok == wantTok || validFn != nil && validFn(tok)) && !disabled && s.throttle.left > 0 {
		th = s.throttle
		s.throttle.left--
	}
	s.mu.Unlock()

	if reject || !ok || tok == "" || (validFn == nil && tok != wantTok) || (validFn != nil && !validFn(tok)) {
		writeErr(w, http.StatusUnauthorized, "authError", "Invalid Credentials")
		return false
	}
	if disabled {
		writeErr(w, http.StatusForbidden, "accessNotConfigured", "Gmail API has not been used in project before or it is disabled.")
		return false
	}
	if th.left > 0 {
		if th.retryAfter != "" {
			w.Header().Set("Retry-After", th.retryAfter)
		}
		if th.status == http.StatusForbidden {
			writeErr(w, http.StatusForbidden, "rateLimitExceeded", "User-rate limit exceeded.")
		} else {
			writeErr(w, http.StatusTooManyRequests, "rateLimitExceeded", "Too Many Requests")
		}
		return false
	}
	return true
}

func (s *Server) handleProfile(w http.ResponseWriter) {
	s.mu.Lock()
	email := s.email
	hid := strconv.FormatUint(s.historyID, 10)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"emailAddress": email,
		"historyId":    hid,
	})
}

func (s *Server) handleLabels(w http.ResponseWriter) {
	s.mu.Lock()
	ids := slices.SortedFunc(slices.Values(s.labelIDsLocked()), strings.Compare)
	out := make([]label, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.labels[id])
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"labels": out})
}

func (s *Server) labelIDsLocked() []string {
	out := make([]string, 0, len(s.labels))
	for id := range s.labels {
		out = append(out, id)
	}
	return out
}

// ListMessageRequests returns how many messages.list HTTP calls the server handled.
func (s *Server) ListMessageRequests() int { return int(s.listReqs.Load()) }

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	s.listReqs.Add(1)
	qv := r.URL.Query()
	labelFilter := qv["labelIds"]
	incSpamTrash := qv.Get("includeSpamTrash") == "true"
	q := qv.Get("q")
	maxRes, _ := strconv.Atoi(qv.Get("maxResults"))
	if maxRes <= 0 {
		maxRes = 100
	}
	offset, _ := strconv.Atoi(qv.Get("pageToken"))

	s.mu.Lock()
	if s.pageLimit > 0 && maxRes > s.pageLimit {
		maxRes = s.pageLimit
	}
	var matched []string
	// Gmail returns messages in reverse chronological / newest-first order.
	for i := len(s.order) - 1; i >= 0; i-- {
		m := s.msgs[s.order[i]]
		if m == nil {
			continue
		}
		if !incSpamTrash && (slices.Contains(m.labels, "SPAM") || slices.Contains(m.labels, "TRASH")) {
			continue
		}
		ok := true
		for _, lf := range labelFilter {
			if !slices.Contains(m.labels, lf) {
				ok = false
				break
			}
		}
		if !ok || !matchesQuery(m, q) {
			continue
		}
		matched = append(matched, m.id)
	}
	s.mu.Unlock()

	if offset > len(matched) {
		offset = len(matched)
	}
	end := min(offset+maxRes, len(matched))
	type item struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	}
	items := make([]item, 0, end-offset)
	for _, id := range matched[offset:end] {
		items = append(items, item{ID: id, ThreadID: "thr-" + id})
	}
	next := ""
	if end < len(matched) {
		next = strconv.Itoa(end)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages":      items,
		"nextPageToken": next,
	})
}

func matchesQuery(m *storedMsg, q string) bool {
	for tok := range strings.FieldsSeq(q) {
		switch {
		case tok == "-label:DRAFT":
			if slices.Contains(m.labels, "DRAFT") {
				return false
			}
		case strings.HasPrefix(tok, "after:"):
			sec, err := strconv.ParseInt(strings.TrimPrefix(tok, "after:"), 10, 64)
			if err == nil && m.internalDate < sec*1000 {
				return false
			}
		case strings.HasPrefix(tok, "rfc822msgid:"):
			want := strings.ToLower(strings.Trim(strings.TrimPrefix(tok, "rfc822msgid:"), "<>"))
			parsed, err := mail.ReadMessage(bytes.NewReader(m.raw))
			if err != nil {
				return false
			}
			got := strings.ToLower(strings.Trim(strings.TrimSpace(parsed.Header.Get("Message-Id")), "<>"))
			if !strings.Contains(got, want) {
				return false
			}
		}
	}
	return true
}

func (s *Server) handleInsertMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Raw      string   `json:"raw"`
		LabelIDs []string `json:"labelIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalidArgument", err.Error())
		return
	}
	raw, err := decodeB64(body.Raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalidArgument", "invalid base64url raw")
		return
	}
	s.mu.Lock()
	for _, lid := range body.LabelIDs {
		if _, ok := s.labels[lid]; !ok {
			s.mu.Unlock()
			writeErr(w, http.StatusBadRequest, "invalidArgument", "Invalid label: "+lid)
			return
		}
	}
	m := s.insertLocked(raw, body.LabelIDs)
	id, tid, hid := m.id, m.threadID, strconv.FormatUint(m.historyID, 10)
	labels := slices.Clone(m.labels)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        id,
		"threadId":  tid,
		"labelIds":  labels,
		"historyId": hid,
	})
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalidArgument", err.Error())
		return
	}
	s.mu.Lock()
	lose := s.loseVerdict
	s.loseVerdict = false
	s.mu.Unlock()
	if lose {
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, herr := hj.Hijack()
			if herr == nil {
				_ = conn.Close()
				return
			}
		}
	}

	var body struct {
		Raw string `json:"raw"`
	}
	if err := json.Unmarshal(rawBody, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalidArgument", err.Error())
		return
	}
	raw, err := decodeB64(body.Raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalidArgument", "invalid base64url raw")
		return
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalidArgument", "invalid RFC 5322 message")
		return
	}
	var rcpts []string
	for _, h := range []string{"To", "Cc", "Bcc"} {
		list, _ := parsed.Header.AddressList(h)
		for _, a := range list {
			if !slices.ContainsFunc(rcpts, func(x string) bool { return strings.EqualFold(x, a.Address) }) {
				rcpts = append(rcpts, a.Address)
			}
		}
	}
	if len(rcpts) == 0 {
		writeErr(w, http.StatusBadRequest, "invalidArgument", "Recipient address required")
		return
	}

	s.mu.Lock()
	for _, rcpt := range rcpts {
		if s.refused[strings.ToLower(rcpt)] {
			s.mu.Unlock()
			writeErr(w, http.StatusBadRequest, "invalidArgument", "Recipient "+rcpt+" refused")
			return
		}
	}
	s.accepted = append(s.accepted, Delivery{Rcpts: rcpts, Raw: raw})
	m := s.insertLocked(stripBccHeader(raw), []string{"SENT"})
	id, tid, hid := m.id, m.threadID, strconv.FormatUint(m.historyID, 10)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"id":        id,
		"threadId":  tid,
		"labelIds":  []string{"SENT"},
		"historyId": hid,
	})
}

func stripBccHeader(raw []byte) []byte {
	var out bytes.Buffer
	inHeader := true
	dropFold := false
	for line := range bytes.Lines(raw) {
		if inHeader {
			if len(bytes.TrimRight(line, "\r\n")) == 0 {
				inHeader = false
				dropFold = false
				out.Write(line)
				continue
			}
			if line[0] == ' ' || line[0] == '\t' {
				if dropFold {
					continue
				}
			} else {
				name, _, _ := bytes.Cut(line, []byte(":"))
				dropFold = strings.EqualFold(strings.TrimSpace(string(name)), "Bcc")
				if dropFold {
					continue
				}
			}
		}
		out.Write(line)
	}
	return out.Bytes()
}

func (s *Server) handleMessageOp(w http.ResponseWriter, r *http.Request, tail string) {
	id, action, hasAction := strings.Cut(tail, "/")
	s.mu.Lock()
	m := s.msgs[id]
	if m == nil {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "notFound", "Requested entity was not found.")
		return
	}
	switch {
	case !hasAction && r.Method == http.MethodGet:
		format := r.URL.Query().Get("format")
		resp := map[string]any{
			"id":           m.id,
			"threadId":     m.threadID,
			"labelIds":     slices.Clone(m.labels),
			"historyId":    strconv.FormatUint(m.historyID, 10),
			"internalDate": strconv.FormatInt(m.internalDate, 10),
			"sizeEstimate": len(m.raw),
		}
		if format == "" || format == "raw" {
			resp["raw"] = base64.RawURLEncoding.EncodeToString(m.raw)
		}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, resp)

	case !hasAction && r.Method == http.MethodDelete:
		delete(s.msgs, id)
		s.order = slices.DeleteFunc(s.order, func(x string) bool { return x == id })
		s.historyID++
		s.history = append(s.history, historyEntry{id: s.historyID, deletedID: id})
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	case action == "modify" && r.Method == http.MethodPost:
		s.mu.Unlock()
		var body struct {
			AddLabelIDs    []string `json:"addLabelIds"`
			RemoveLabelIDs []string `json:"removeLabelIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalidArgument", err.Error())
			return
		}
		s.mu.Lock()
		m = s.msgs[id]
		if m == nil {
			s.mu.Unlock()
			writeErr(w, http.StatusNotFound, "notFound", "Requested entity was not found.")
			return
		}
		for _, lid := range slices.Concat(body.AddLabelIDs, body.RemoveLabelIDs) {
			if lid == "SENT" || lid == "DRAFT" {
				s.mu.Unlock()
				writeErr(w, http.StatusBadRequest, "invalidArgument", "Invalid label: "+lid)
				return
			}
			if _, ok := s.labels[lid]; !ok {
				s.mu.Unlock()
				writeErr(w, http.StatusBadRequest, "invalidArgument", "Invalid label: "+lid)
				return
			}
		}
		s.applyModifyLocked(m, body.AddLabelIDs, body.RemoveLabelIDs)
		labels := slices.Clone(m.labels)
		hid := strconv.FormatUint(m.historyID, 10)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "labelIds": labels, "historyId": hid})

	case action == "trash" && r.Method == http.MethodPost:
		s.applyModifyLocked(m, []string{"TRASH"}, []string{"INBOX"})
		labels := slices.Clone(m.labels)
		hid := strconv.FormatUint(m.historyID, 10)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "labelIds": labels, "historyId": hid})

	case action == "untrash" && r.Method == http.MethodPost:
		s.applyModifyLocked(m, nil, []string{"TRASH"})
		labels := slices.Clone(m.labels)
		hid := strconv.FormatUint(m.historyID, 10)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "labelIds": labels, "historyId": hid})

	default:
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "notFound", "unknown message action")
	}
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	qv := r.URL.Query()
	start, err := strconv.ParseUint(qv.Get("startHistoryId"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalidArgument", "invalid startHistoryId")
		return
	}
	maxRes, _ := strconv.Atoi(qv.Get("maxResults"))
	if maxRes <= 0 {
		maxRes = 100
	}
	offset, _ := strconv.Atoi(qv.Get("pageToken"))

	s.mu.Lock()
	if start < s.minHistoryID {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "notFound", "startHistoryId is too old")
		return
	}
	if s.pageLimit > 0 && maxRes > s.pageLimit {
		maxRes = s.pageLimit
	}
	var recs []map[string]any
	for _, h := range s.history {
		if h.id <= start {
			continue
		}
		rec := map[string]any{"id": strconv.FormatUint(h.id, 10)}
		if h.added != nil {
			rec["messagesAdded"] = []map[string]any{{
				"message": map[string]any{
					"id":       h.added.id,
					"threadId": h.added.threadID,
					"labelIds": h.added.labels,
				},
			}}
		}
		if h.deletedID != "" {
			rec["messagesDeleted"] = []map[string]any{{
				"message": map[string]any{"id": h.deletedID},
			}}
		}
		if len(h.labelsAdded) > 0 {
			rec["labelsAdded"] = []map[string]any{{
				"message":  map[string]any{"id": h.targetID},
				"labelIds": h.labelsAdded,
			}}
		}
		if len(h.labelsRemoved) > 0 {
			rec["labelsRemoved"] = []map[string]any{{
				"message":  map[string]any{"id": h.targetID},
				"labelIds": h.labelsRemoved,
			}}
		}
		recs = append(recs, rec)
	}
	hid := strconv.FormatUint(s.historyID, 10)
	s.mu.Unlock()

	if offset > len(recs) {
		offset = len(recs)
	}
	end := min(offset+maxRes, len(recs))
	next := ""
	if end < len(recs) {
		next = strconv.Itoa(end)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"history":       recs[offset:end],
		"historyId":     hid,
		"nextPageToken": next,
	})
}

func decodeB64(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\r', '\n', ' ', '\t', '=':
			return -1
		case '+':
			return '-'
		case '/':
			return '_'
		}
		return r
	}, s)
	return base64.RawURLEncoding.DecodeString(s)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, reason, msg string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": msg,
			"errors": []map[string]any{{
				"domain":  "global",
				"reason":  reason,
				"message": msg,
			}},
		},
	})
}
