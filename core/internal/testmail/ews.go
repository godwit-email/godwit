package testmail

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
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

// EWS is a running fake EWS server (Exchange Web Services) with one mailbox,
// alice/secret over HTTP Basic, that starts with the distinguished mail
// folders and a calendar. It speaks GetFolder, SyncFolderHierarchy,
// SyncFolderItems, GetItem, FindItem and CreateItem sending a message; an
// operation it does not know fails the test. Its responses spell the
// namespace prefixes differently from operation to operation, as servers do.
// Hooks change the mailbox as another client would (MarkRead, MarkFlagged,
// MarkAnswered, Remove, RenameFolder, RemoveFolder) or inject faults
// (RejectAuth, SetVersion, Reset, Busy, Refuse, LoseVerdict,
// RejectNextSendAuth).
//
// Every change takes the next sequence number, and deleted items and
// folders stay as tombstones, so a SyncState, the sequence number it was
// given at, says what changed since.
type EWS struct {
	t   testing.TB
	srv *httptest.Server

	mu           sync.Mutex
	major, minor int // ServerVersionInfo
	rejected     bool
	busy         int           // requests still to refuse with ErrorServerBusy
	backOff      time.Duration // the BackOffMilliseconds they carry
	busyFault    bool          // as a SOAP fault (HTTP 500), else a response message (HTTP 503)
	requests     int           // signed-in requests answered

	latency          atomic.Int64 // time.Duration each request takes before being read
	inFlight, maxFly atomic.Int32 // requests being answered now, and the most at once
	folders          []*ewsFolder // the root first; deleted ones too, gone
	next             int          // the last id given
	seq              int          // the last change's sequence number
	sent             []EWSSent
	refused          map[string]bool // recipients refused
	lose             bool            // drop the next CreateItem's answer
	rejectSend       bool            // refuse the next CreateItem's credentials
}

// EWSSent is a message the server sent: its recipients and MIME as it got them.
type EWSSent struct {
	Rcpts []string
	Raw   []byte
}

type ewsFolder struct {
	id, parent, name string
	class            string // FolderClass; "IPF.Appointment" is a calendar
	distinguished    string // its DistinguishedFolderId, "" for none
	items            []*ewsItem
	epoch            int // Reset voids the SyncStates given in an older one
	changed              // in the hierarchy
}

type ewsItem struct {
	id       string
	raw      []byte
	received time.Time
	read     bool
	flagged  bool
	verb     int  // PidTagLastVerbExecuted, 0 for none
	readOnly bool // its last change was to read alone: a ReadFlagChange
	changed
}

// changed is when a folder or item was created and last changed, and
// whether it is gone.
type changed struct {
	created, modseq int
	gone            bool
}

const (
	nsSOAP     = "http://schemas.xmlsoap.org/soap/envelope/"
	nsTypes    = "http://schemas.microsoft.com/exchange/services/2006/types"
	nsMessages = "http://schemas.microsoft.com/exchange/services/2006/messages"
)

// StartEWS starts an EWS server answering as Exchange 2019, closed when t ends.
func StartEWS(t testing.TB) *EWS {
	s := &EWS{t: t, major: 15, minor: 2, refused: map[string]bool{}}
	root := s.folder("", "", "msgfolderroot", "")
	for _, f := range [][3]string{{"Inbox", "inbox", "IPF.Note"}, {"Sent Items", "sentitems", "IPF.Note"}, {"Drafts", "drafts", "IPF.Note"},
		{"Deleted Items", "deleteditems", "IPF.Note"}, {"Junk Email", "junkemail", "IPF.Note"}, {"Archive", "archive", "IPF.Note"},
		{"Calendar", "calendar", "IPF.Appointment"}} {
		s.folder(root.id, f[0], f[1], f[2])
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the EWS endpoint.
func (s *EWS) URL() string { return s.srv.URL + "/EWS/Exchange.asmx" }

// Password is the password a client should use: "secret", or a wrong one
// after RejectAuth.
func (s *EWS) Password() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rejected {
		return "wrong"
	}
	return "secret"
}

// RejectAuth makes Password wrong from now on, so the server refuses it.
func (s *EWS) RejectAuth() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected = true
}

// SetVersion makes the server say it is Exchange major.minor (15.0 is 2013,
// 15.1 2016, 15.2 2019).
func (s *EWS) SetVersion(major, minor int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.major, s.minor = major, minor
}

// Sent is every message the server has sent, in order.
func (s *EWS) Sent() []EWSSent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

// Refuse makes the server refuse every message to rcpt.
func (s *EWS) Refuse(rcpt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refused[strings.ToLower(rcpt)] = true
}

// LoseVerdict makes the server read the whole next CreateItem, send it, and
// close the connection without answering.
func (s *EWS) LoseVerdict() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lose = true
}

// RejectNextSendAuth makes the server refuse the credentials of the next
// CreateItem only, as when the password changes between a sync's sign-in
// and its send.
func (s *EWS) RejectNextSendAuth() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectSend = true
}

// Count is how many items the top-level folder name holds.
func (s *EWS) Count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.folders {
		if f.parent == s.folders[0].id && f.name == name {
			return len(f.items)
		}
	}
	return -1
}

// Busy makes the server refuse the next n requests with ErrorServerBusy
// asking to wait backOff, as a SOAP fault with HTTP 500 when fault, else as
// the operation's response message with HTTP 503.
func (s *EWS) Busy(n int, backOff time.Duration, fault bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy, s.backOff, s.busyFault = n, backOff, fault
}

// Requests is how many signed-in requests the server has answered.
func (s *EWS) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// SetLatency makes each request take d before the server answers it, so
// requests sent at once overlap.
func (s *EWS) SetLatency(d time.Duration) { s.latency.Store(int64(d)) }

// MaxConcurrent is the most requests the server has had in hand at once.
func (s *EWS) MaxConcurrent() int { return int(s.maxFly.Load()) }

// Deliver puts raw into the folder at path, as mail arriving, and returns
// its ItemId. Path segments name folders from the root ("Inbox/Projects"),
// created if need be; "INBOX" is the inbox.
func (s *EWS) Deliver(path string, raw []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.path(path, true)
	it := &ewsItem{id: s.newID("AAMkItem"), raw: raw, received: time.Now().UTC().Truncate(time.Second)}
	it.created = s.touch(&it.changed)
	f.items = append(f.items, it)
	return it.id
}

// MarkRead marks the item id read or unread.
func (s *EWS) MarkRead(id string, read bool) {
	s.change(id, true, func(it *ewsItem) { it.read = read })
}

// MarkFlagged flags or unflags the item id.
func (s *EWS) MarkFlagged(id string, flagged bool) {
	s.change(id, false, func(it *ewsItem) { it.flagged = flagged })
}

// MarkAnswered records a reply to the item id (PidTagLastVerbExecuted 102).
func (s *EWS) MarkAnswered(id string) {
	s.change(id, false, func(it *ewsItem) { it.verb = 102 })
}

// Remove deletes the item id for good.
func (s *EWS) Remove(id string) {
	s.change(id, false, func(it *ewsItem) { it.gone = true })
}

// RenameFolder renames the folder at path to name; its FolderId stays.
func (s *EWS) RenameFolder(path, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.path(path, false)
	f.name = name
	s.touch(&f.changed)
}

// RemoveFolder deletes the folder at path with its mail.
func (s *EWS) RemoveFolder(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.path(path, false)
	f.gone = true
	s.touch(&f.changed)
}

// Reset voids every SyncState given for the folder at path, as a server
// that lost its synchronization state does: the next SyncFolderItems from
// one is ErrorInvalidSyncStateData. ItemIds stay, as on Exchange.
func (s *EWS) Reset(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path(path, false).epoch++
}

// change changes the item id with do, as another client would.
func (s *EWS) change(id string, readOnly bool, do func(*ewsItem)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.findItem(id)
	if it == nil {
		s.t.Fatalf("fake EWS: no item %s", id)
	}
	do(it)
	it.readOnly = readOnly
	s.touch(&it.changed)
}

// path is the folder at path ("INBOX" is the inbox), created if create; it
// fails the test if there is none.
func (s *EWS) path(path string, create bool) *ewsFolder {
	f := s.folders[0]
	if path == "INBOX" {
		path = "Inbox"
	}
	for _, name := range strings.Split(path, "/") {
		var child *ewsFolder
		for _, c := range s.folders {
			if c.parent == f.id && c.name == name && !c.gone {
				child = c
			}
		}
		if child == nil && !create {
			s.t.Fatalf("fake EWS: no folder %q", path)
		}
		if child == nil {
			child = s.folder(f.id, name, "", "IPF.Note")
		}
		f = child
	}
	return f
}

// touch gives c the next sequence number and returns it.
func (s *EWS) touch(c *changed) int {
	s.seq++
	c.modseq = s.seq
	return s.seq
}

func (s *EWS) findItem(id string) *ewsItem {
	for _, f := range s.folders {
		for _, it := range f.items {
			if it.id == id && !it.gone && !f.gone {
				return it
			}
		}
	}
	return nil
}

func (s *EWS) newID(prefix string) string {
	s.next++
	return prefix + strconv.Itoa(s.next) + "=="
}

func (s *EWS) folder(parent, name, distinguished, class string) *ewsFolder {
	f := &ewsFolder{id: s.newID("AAMkFolder"), parent: parent, name: name, distinguished: distinguished, class: class}
	f.created = s.touch(&f.changed)
	s.folders = append(s.folders, f)
	return f
}

// ewsRequest is what the server reads of a request: the operation and the
// few values its operations take, wherever they are in it.
type ewsRequest struct {
	op            string
	version       string            // RequestServerVersion's
	distinguished []string          // DistinguishedFolderId Ids, in order
	folderIDs     []string          // FolderId Ids
	itemIDs       []string          // ItemId Ids
	addresses     []string          // EmailAddress texts, in order
	text          map[string]string // the text of SyncState, MaxChangesReturned, IncludeMimeContent, MimeContent; the operation's attributes
	fieldURIs     []string          // FieldURI FieldURIs, in order
	constants     []string          // Constant Values, in order
	elements      map[string]bool   // the local names of its elements
}

// readRequest reads a request by namespace, as a server does: a client's
// element in the wrong namespace is not what it meant.
func readRequest(body []byte) (ewsRequest, error) {
	r := ewsRequest{text: map[string]string{}, elements: map[string]bool{}}
	d := xml.NewDecoder(bytes.NewReader(body))
	var inBody bool
	var cur string
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return r, nil
		}
		if err != nil {
			return r, err
		}
		switch el := tok.(type) {
		case xml.StartElement:
			cur = el.Name.Local
			r.elements[cur] = true
			attr := func(n string) string {
				for _, a := range el.Attr {
					if a.Name.Local == n {
						return a.Value
					}
				}
				return ""
			}
			switch {
			case el.Name.Space == nsSOAP && cur == "Body":
				inBody = true
			case inBody && r.op == "":
				if el.Name.Space != nsMessages {
					return r, fmt.Errorf("operation %s in namespace %q", cur, el.Name.Space)
				}
				r.op = cur
				for _, a := range el.Attr {
					r.text[a.Name.Local] = a.Value
				}
			case cur == "RequestServerVersion" && el.Name.Space == nsTypes:
				r.version = attr("Version")
			case cur == "FieldURI":
				r.fieldURIs = append(r.fieldURIs, attr("FieldURI"))
			case cur == "Constant":
				r.constants = append(r.constants, attr("Value"))
			case cur == "DistinguishedFolderId" || cur == "FolderId" || cur == "ItemId":
				if el.Name.Space != nsTypes {
					return r, fmt.Errorf("%s in namespace %q", cur, el.Name.Space)
				}
				switch cur {
				case "DistinguishedFolderId":
					r.distinguished = append(r.distinguished, attr("Id"))
				case "FolderId":
					r.folderIDs = append(r.folderIDs, attr("Id"))
				default:
					r.itemIDs = append(r.itemIDs, attr("Id"))
				}
			}
		case xml.CharData:
			if t := strings.TrimSpace(string(el)); t != "" {
				r.text[cur] = t
				if cur == "EmailAddress" {
					r.addresses = append(r.addresses, t)
				}
			}
		}
	}
}

func (s *EWS) serve(w http.ResponseWriter, r *http.Request) {
	n := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for m := s.maxFly.Load(); n > m && !s.maxFly.CompareAndSwap(m, n); m = s.maxFly.Load() {
	}
	time.Sleep(time.Duration(s.latency.Load()))
	user, pass, ok := r.BasicAuth()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ok || user != "alice" || pass != "secret" {
		w.Header().Set("WWW-Authenticate", `Basic realm="mail.example"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	req, err := readRequest(body)
	if err == nil && req.version != "Exchange2016" {
		err = fmt.Errorf("RequestServerVersion %q, want Exchange2016", req.version)
	}
	if err != nil {
		s.t.Errorf("fake EWS: %v in %s", err, body)
		s.fault(w, "ErrorSchemaValidation", err.Error())
		return
	}
	s.requests++
	if s.busy > 0 {
		s.busy--
		s.serverBusy(w, req.op)
		return
	}
	switch req.op {
	case "GetFolder":
		s.getFolder(w, req)
	case "SyncFolderHierarchy":
		s.syncFolderHierarchy(w, req)
	case "SyncFolderItems":
		s.syncFolderItems(w, req)
	case "GetItem":
		s.getItem(w, req)
	case "CreateItem":
		if s.rejectSend {
			s.rejectSend = false
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.createItem(w, req)
	case "FindItem":
		s.findItems(w, req)
	default:
		s.t.Errorf("fake EWS: unknown operation %s", req.op)
		s.fault(w, "ErrorInvalidRequest", "unknown operation "+req.op)
	}
}

func esc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// versionHeader is the ServerVersionInfo header, with the types namespace
// bound to prefix p (declared here when decl).
func (s *EWS) versionHeader(p string, decl bool) string {
	ns := ""
	if decl {
		ns = fmt.Sprintf(` xmlns:%s="%s"`, p, nsTypes)
	}
	return fmt.Sprintf(`<%s:ServerVersionInfo%s MajorVersion="%d" MinorVersion="%d" MajorBuildNumber="1544" MinorBuildNumber="11"/>`, p, ns, s.major, s.minor)
}

// The response styles: which prefixes stand for which namespace and where
// they are declared. Each operation answers in its own, so a client reading
// by prefix fails somewhere.
const (
	// s: envelope; m: and t: declared on the response element (Exchange's usual).
	styleUsual = iota
	// soap: envelope declaring m: and t: itself: the response element relies on its ancestor.
	styleEnvelopeDecl
	// the messages namespace as the default one, types as t: (Microsoft's documentation examples).
	styleDefaultNS
)

// respond writes a response whose body element is <op>Response holding
// inner, written with prefixes m: (messages) and t: (types).
func (s *EWS) respond(w http.ResponseWriter, style int, op, inner string) {
	var doc string
	switch style {
	case styleEnvelopeDecl:
		doc = fmt.Sprintf(`<soap:Envelope xmlns:soap="%s" xmlns:m="%s" xmlns:t="%s"><soap:Header>%s</soap:Header><soap:Body><m:%sResponse>%s</m:%sResponse></soap:Body></soap:Envelope>`,
			nsSOAP, nsMessages, nsTypes, s.versionHeader("t", false), op, inner, op)
	case styleDefaultNS:
		inner = strings.NewReplacer("<m:", "<", "</m:", "</").Replace(inner)
		doc = fmt.Sprintf(`<s:Envelope xmlns:s="%s"><s:Header>%s</s:Header><s:Body><%sResponse xmlns="%s" xmlns:t="%s">%s</%sResponse></s:Body></s:Envelope>`,
			nsSOAP, s.versionHeader("h", true), op, nsMessages, nsTypes, inner, op)
	default:
		doc = fmt.Sprintf(`<s:Envelope xmlns:s="%s"><s:Header>%s</s:Header><s:Body><m:%sResponse xmlns:m="%s" xmlns:t="%s">%s</m:%sResponse></s:Body></s:Envelope>`,
			nsSOAP, s.versionHeader("h", true), op, nsMessages, nsTypes, inner, op)
	}
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	io.WriteString(w, xml.Header+doc)
}

func (s *EWS) fault(w http.ResponseWriter, code, text string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	fmt.Fprintf(w, `%s<s:Envelope xmlns:s="%s"><s:Body><s:Fault><faultcode xmlns:a="%s">a:%s</faultcode><faultstring xml:lang="en-US">%s</faultstring>`+
		`<detail><e:ResponseCode xmlns:e="%s">%s</e:ResponseCode></detail></s:Fault></s:Body></s:Envelope>`,
		xml.Header, nsSOAP, nsTypes, code, esc(text), nsTypes, code)
}

// serverBusy refuses a request of op as Exchange's throttling does, in the
// two shapes it uses (Throttling in Exchange, tables 3 and 4).
func (s *EWS) serverBusy(w http.ResponseWriter, op string) {
	const text = "The server cannot service this request right now. Try again later."
	ms := s.backOff.Milliseconds()
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	if s.busyFault {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `%s<s:Envelope xmlns:s="%s"><s:Body><s:Fault><faultcode xmlns:a="%s">a:ErrorServerBusy</faultcode><faultstring xml:lang="en-US">%s</faultstring>`+
			`<detail><e:ResponseCode xmlns:e="%s">ErrorServerBusy</e:ResponseCode><e:Message xmlns:e="%s">%s</e:Message>`+
			`<t:MessageXml xmlns:t="%s"><t:Value Name="BackOffMilliseconds">%d</t:Value></t:MessageXml></detail></s:Fault></s:Body></s:Envelope>`,
			xml.Header, nsSOAP, nsTypes, text, nsTypes, nsTypes, text, nsTypes, ms)
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	s.respond(w, styleUsual, op, fmt.Sprintf(`<m:ResponseMessages><m:%sResponseMessage ResponseClass="Error"><m:MessageText>%s</m:MessageText><m:ResponseCode>ErrorServerBusy</m:ResponseCode>`+
		`<m:DescriptiveLinkKey>0</m:DescriptiveLinkKey><m:MessageXml><t:Value Name="BackOffMilliseconds">%d</t:Value></m:MessageXml></m:%sResponseMessage></m:ResponseMessages>`,
		op, text, ms, op))
}

// message is one response message of op: Success with inner, or Error with
// code followed by inner (what the operation still gives on that error).
func message(op, code, inner string) string {
	if code != "" {
		return fmt.Sprintf(`<m:%sResponseMessage ResponseClass="Error"><m:MessageText>%s</m:MessageText><m:ResponseCode>%s</m:ResponseCode><m:DescriptiveLinkKey>0</m:DescriptiveLinkKey>%s</m:%sResponseMessage>`,
			op, code, code, inner, op)
	}
	return fmt.Sprintf(`<m:%sResponseMessage ResponseClass="Success"><m:ResponseCode>NoError</m:ResponseCode>%s</m:%sResponseMessage>`, op, inner, op)
}

// byID is the live folder id, nil if there is none.
func (s *EWS) byID(id string) *ewsFolder {
	for _, f := range s.folders {
		if f.id == id && !f.gone {
			return f
		}
	}
	return nil
}

// folderXML is f as SyncFolderHierarchy and GetFolder give it: a calendar
// is a t:CalendarFolder.
func folderXML(f *ewsFolder) string {
	el := "Folder"
	if f.class == "IPF.Appointment" {
		el = "CalendarFolder"
	}
	return fmt.Sprintf(`<t:%s><t:FolderId Id="%s" ChangeKey="AQAAAA=="/><t:ParentFolderId Id="%s" ChangeKey="AQAAAA=="/><t:FolderClass>%s</t:FolderClass><t:DisplayName>%s</t:DisplayName><t:TotalCount>%d</t:TotalCount><t:ChildFolderCount>0</t:ChildFolderCount><t:UnreadCount>0</t:UnreadCount></t:%s>`,
		el, f.id, f.parent, f.class, esc(f.name), len(f.items), el)
}

func (s *EWS) getFolder(w http.ResponseWriter, req ewsRequest) {
	var msgs strings.Builder
	for _, d := range req.distinguished {
		var found *ewsFolder
		for _, f := range s.folders {
			if f.distinguished == d && !f.gone {
				found = f
			}
		}
		if found == nil {
			msgs.WriteString(message("GetFolder", "ErrorFolderNotFound", ""))
			continue
		}
		msgs.WriteString(message("GetFolder", "", "<m:Folders>"+folderXML(found)+"</m:Folders>"))
	}
	s.respond(w, styleUsual, "GetFolder", "<m:ResponseMessages>"+msgs.String()+"</m:ResponseMessages>")
}

// syncFolderHierarchy answers the folder changes since the SyncState, "H"
// and the sequence number it was given at; every folder without one.
func (s *EWS) syncFolderHierarchy(w http.ResponseWriter, req ewsRequest) {
	reply := func(msg string) {
		s.respond(w, styleEnvelopeDecl, "SyncFolderHierarchy", "<m:ResponseMessages>"+msg+"</m:ResponseMessages>")
	}
	since := 0
	if st, ok := req.text["SyncState"]; ok {
		n, err := strconv.Atoi(strings.TrimPrefix(st, "H"))
		if err != nil || !strings.HasPrefix(st, "H") || n > s.seq {
			reply(message("SyncFolderHierarchy", "ErrorInvalidSyncStateData", ""))
			return
		}
		since = n
	}
	var changes strings.Builder
	for _, f := range s.folders[1:] {
		switch {
		case f.modseq <= since || f.gone && f.created > since:
		case f.gone:
			fmt.Fprintf(&changes, `<t:Delete><t:FolderId Id="%s" ChangeKey="AQAAAA=="/></t:Delete>`, f.id)
		case f.created > since:
			changes.WriteString("<t:Create>" + folderXML(f) + "</t:Create>")
		default:
			changes.WriteString("<t:Update>" + folderXML(f) + "</t:Update>")
		}
	}
	reply(message("SyncFolderHierarchy", "", fmt.Sprintf("<m:SyncState>H%d</m:SyncState><m:IncludesLastFolderInRange>true</m:IncludesLastFolderInRange><m:Changes>%s</m:Changes>",
		s.seq, changes.String())))
}

// syncFolderItems pages through a folder's item changes in the order they
// were made. A SyncState is "I", the folder's epoch, "." and the sequence
// number it was given at; every item without one.
func (s *EWS) syncFolderItems(w http.ResponseWriter, req ewsRequest) {
	reply := func(msg string) {
		s.respond(w, styleDefaultNS, "SyncFolderItems", "<m:ResponseMessages>"+msg+"</m:ResponseMessages>")
	}
	var f *ewsFolder
	if len(req.folderIDs) == 1 {
		f = s.byID(req.folderIDs[0])
	}
	if f == nil {
		reply(message("SyncFolderItems", "ErrorFolderNotFound", ""))
		return
	}
	since := 0
	if st, ok := req.text["SyncState"]; ok {
		epoch, n, _ := strings.Cut(strings.TrimPrefix(st, "I"), ".")
		e, err1 := strconv.Atoi(epoch)
		m, err2 := strconv.Atoi(n)
		if err1 != nil || err2 != nil || e != f.epoch || m > s.seq {
			// as Exchange answers it: an empty SyncState, and the last of the range
			reply(message("SyncFolderItems", "ErrorInvalidSyncStateData", "<m:SyncState/><m:IncludesLastItemInRange>true</m:IncludesLastItemInRange>"))
			return
		}
		since = m
	}
	maxChanges, _ := strconv.Atoi(req.text["MaxChangesReturned"])
	if maxChanges < 1 || maxChanges > 512 {
		s.t.Errorf("fake EWS: MaxChangesReturned %q", req.text["MaxChangesReturned"])
		maxChanges = 512
	}
	var todo []*ewsItem
	for _, it := range f.items {
		if it.modseq > since && !(it.gone && it.created > since) {
			todo = append(todo, it)
		}
	}
	slices.SortFunc(todo, func(a, b *ewsItem) int { return a.modseq - b.modseq })
	state, last := s.seq, true
	if len(todo) > maxChanges {
		todo, state, last = todo[:maxChanges], todo[maxChanges-1].modseq, false
	}
	var changes strings.Builder
	for _, it := range todo {
		switch {
		case it.gone:
			fmt.Fprintf(&changes, `<t:Delete><t:ItemId Id="%s" ChangeKey="CQAAAA=="/></t:Delete>`, it.id)
		case it.created > since:
			fmt.Fprintf(&changes, `<t:Create><t:Message><t:ItemId Id="%s" ChangeKey="CQAAAA=="/></t:Message></t:Create>`, it.id)
		case it.readOnly:
			fmt.Fprintf(&changes, `<t:ReadFlagChange><t:ItemId Id="%s" ChangeKey="CQAAAA=="/><t:IsRead>%t</t:IsRead></t:ReadFlagChange>`, it.id, it.read)
		default:
			fmt.Fprintf(&changes, `<t:Update><t:Message><t:ItemId Id="%s" ChangeKey="CQAAAA=="/></t:Message></t:Update>`, it.id)
		}
	}
	reply(message("SyncFolderItems", "", fmt.Sprintf("<m:SyncState>I%d.%d</m:SyncState><m:IncludesLastItemInRange>%t</m:IncludesLastItemInRange><m:Changes>%s</m:Changes>",
		f.epoch, state, last, changes.String())))
}

// getItem answers the items with their flags, and their MIME content when
// asked.
func (s *EWS) getItem(w http.ResponseWriter, req ewsRequest) {
	var msgs strings.Builder
	for _, id := range req.itemIDs {
		it := s.findItem(id)
		if it == nil {
			msgs.WriteString(message("GetItem", "ErrorItemNotFound", ""))
			continue
		}
		mime := ""
		if req.text["IncludeMimeContent"] == "true" {
			mime = `<t:MimeContent CharacterSet="UTF-8">` + base64.StdEncoding.EncodeToString(it.raw) + `</t:MimeContent>`
		}
		flag, verb := "NotFlagged", ""
		if it.flagged {
			flag = "Flagged"
		}
		if it.verb != 0 {
			verb = fmt.Sprintf(`<t:ExtendedProperty><t:ExtendedFieldURI PropertyTag="0x1081" PropertyType="Integer"/><t:Value>%d</t:Value></t:ExtendedProperty>`, it.verb)
		}
		msgs.WriteString(message("GetItem", "", fmt.Sprintf(`<m:Items><t:Message>%s<t:ItemId Id="%s" ChangeKey="CQAAAA=="/>`+
			`<t:DateTimeReceived>%s</t:DateTimeReceived>%s<t:IsRead>%t</t:IsRead><t:Flag><t:FlagStatus>%s</t:FlagStatus></t:Flag></t:Message></m:Items>`,
			mime, it.id, it.received.Format(time.RFC3339), verb, it.read, flag)))
	}
	s.respond(w, styleEnvelopeDecl, "GetItem", "<m:ResponseMessages>"+msgs.String()+"</m:ResponseMessages>")
}

// createItem sends the message in MimeContent, as SendAndSaveCopy into Sent
// Items does: to the MIME's To and Cc and to BccRecipients. It does not read
// a Bcc header in the MIME, so a client relying on Exchange to do so fails.
func (s *EWS) createItem(w http.ResponseWriter, req ewsRequest) {
	reply := func(msg string) {
		s.respond(w, styleDefaultNS, "CreateItem", "<m:ResponseMessages>"+msg+"</m:ResponseMessages>")
	}
	if d := req.text["MessageDisposition"]; d != "SendAndSaveCopy" || !slices.Equal(req.distinguished, []string{"sentitems"}) {
		s.t.Errorf("fake EWS: CreateItem %s into %v, want SendAndSaveCopy into sentitems", d, req.distinguished)
		reply(message("CreateItem", "ErrorInvalidRequest", ""))
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.text["MimeContent"])
	m, merr := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil || merr != nil {
		s.t.Errorf("fake EWS: CreateItem MimeContent: %v %v", err, merr)
		reply(message("CreateItem", "ErrorMimeContentInvalid", ""))
		return
	}
	var rcpts []string
	for _, h := range []string{"To", "Cc"} {
		list, _ := m.Header.AddressList(h)
		for _, a := range list {
			rcpts = append(rcpts, a.Address)
		}
	}
	rcpts = append(rcpts, req.addresses...)
	for _, r := range rcpts {
		if s.refused[strings.ToLower(r)] {
			reply(message("CreateItem", "ErrorInvalidRecipients", ""))
			return
		}
	}
	s.sent = append(s.sent, EWSSent{Rcpts: rcpts, Raw: raw})
	for _, f := range s.folders {
		if f.distinguished == "sentitems" {
			it := &ewsItem{id: s.newID("AAMkItem"), raw: raw, received: time.Now().UTC().Truncate(time.Second), read: true}
			it.created = s.touch(&it.changed)
			f.items = append(f.items, it)
		}
	}
	if s.lose { // sent, but the client never hears so
		s.lose = false
		c, _, _ := http.NewResponseController(w).Hijack()
		c.Close()
		return
	}
	reply(message("CreateItem", "", "<m:Items/>"))
}

// findItems answers the items of one folder whose InternetMessageId equals
// a constant: the only restriction it knows, so a query written otherwise
// fails the test rather than matching by accident.
func (s *EWS) findItems(w http.ResponseWriter, req ewsRequest) {
	reply := func(msg string) {
		s.respond(w, styleUsual, "FindItem", "<m:ResponseMessages>"+msg+"</m:ResponseMessages>")
	}
	if !req.elements["IsEqualTo"] || !slices.Equal(req.fieldURIs, []string{"message:InternetMessageId"}) || len(req.constants) != 1 {
		s.t.Errorf("fake EWS: FindItem restriction %v %v", req.fieldURIs, req.constants)
		reply(message("FindItem", "ErrorInvalidRestriction", ""))
		return
	}
	var f *ewsFolder
	if len(req.folderIDs) == 1 {
		f = s.byID(req.folderIDs[0])
	}
	if f == nil {
		reply(message("FindItem", "ErrorFolderNotFound", ""))
		return
	}
	var items strings.Builder
	n := 0
	for _, it := range f.items {
		m, err := mail.ReadMessage(bytes.NewReader(it.raw))
		if it.gone || err != nil || strings.TrimSpace(m.Header.Get("Message-Id")) != req.constants[0] {
			continue
		}
		n++
		fmt.Fprintf(&items, `<t:Message><t:ItemId Id="%s" ChangeKey="CQAAAA=="/></t:Message>`, it.id)
	}
	reply(message("FindItem", "", fmt.Sprintf(`<m:RootFolder TotalItemsInView="%d" IncludesLastItemInRange="true"><t:Items>%s</t:Items></m:RootFolder>`, n, items.String())))
}
