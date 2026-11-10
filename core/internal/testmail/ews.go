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
	"testing"
	"time"
)

// EWS is a running fake EWS server (Exchange Web Services) with one mailbox,
// alice/secret over HTTP Basic, that starts with the distinguished mail
// folders and a calendar. It speaks GetFolder, SyncFolderHierarchy,
// SyncFolderItems, GetItem and CreateItem sending a message; an operation it
// does not know fails the test.
// Its responses spell the namespace prefixes differently from operation to
// operation, as servers do. Hooks (RejectAuth, SetVersion, Refuse,
// LoseVerdict) inject faults.
type EWS struct {
	t   testing.TB
	srv *httptest.Server

	mu           sync.Mutex
	major, minor int // ServerVersionInfo
	rejected     bool
	folders      []*ewsFolder // the root first
	next         int          // the last id given
	sent         []EWSSent
	refused      map[string]bool // recipients refused
	lose         bool            // drop the next CreateItem's answer
	rejectSend   bool            // refuse the next CreateItem's credentials
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
}

type ewsItem struct {
	id       string
	raw      []byte
	received time.Time
	read     bool
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

// Deliver puts raw into the folder at path, as mail arriving, and returns
// its ItemId. Path segments name folders from the root ("Inbox/Projects"),
// created if need be; "INBOX" is the inbox.
func (s *EWS) Deliver(path string, raw []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.folders[0]
	if path == "INBOX" {
		path = "Inbox"
	}
	for _, name := range strings.Split(path, "/") {
		var child *ewsFolder
		for _, c := range s.folders {
			if c.parent == f.id && c.name == name {
				child = c
			}
		}
		if child == nil {
			child = s.folder(f.id, name, "", "IPF.Note")
		}
		f = child
	}
	it := &ewsItem{id: s.newID("AAMkItem"), raw: raw, received: time.Now().UTC().Truncate(time.Second)}
	f.items = append(f.items, it)
	return it.id
}

func (s *EWS) newID(prefix string) string {
	s.next++
	return prefix + strconv.Itoa(s.next) + "=="
}

func (s *EWS) folder(parent, name, distinguished, class string) *ewsFolder {
	f := &ewsFolder{id: s.newID("AAMkFolder"), parent: parent, name: name, distinguished: distinguished, class: class}
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
}

// readRequest reads a request by namespace, as a server does: a client's
// element in the wrong namespace is not what it meant.
func readRequest(body []byte) (ewsRequest, error) {
	r := ewsRequest{text: map[string]string{}}
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
	switch req.op {
	case "GetFolder":
		s.getFolder(w, req)
	case "SyncFolderHierarchy":
		s.syncFolderHierarchy(w)
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

// message is one response message of op: Success with inner, or Error with code.
func message(op, code, inner string) string {
	if code != "" {
		return fmt.Sprintf(`<m:%sResponseMessage ResponseClass="Error"><m:MessageText>%s</m:MessageText><m:ResponseCode>%s</m:ResponseCode><m:DescriptiveLinkKey>0</m:DescriptiveLinkKey></m:%sResponseMessage>`,
			op, code, code, op)
	}
	return fmt.Sprintf(`<m:%sResponseMessage ResponseClass="Success"><m:ResponseCode>NoError</m:ResponseCode>%s</m:%sResponseMessage>`, op, inner, op)
}

func (s *EWS) byID(id string) *ewsFolder {
	for _, f := range s.folders {
		if f.id == id {
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
			if f.distinguished == d {
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

// syncFolderHierarchy answers every folder under the root as created: it
// keeps no hierarchy state.
func (s *EWS) syncFolderHierarchy(w http.ResponseWriter) {
	var changes strings.Builder
	for _, f := range s.folders[1:] {
		changes.WriteString("<t:Create>" + folderXML(f) + "</t:Create>")
	}
	s.respond(w, styleEnvelopeDecl, "SyncFolderHierarchy", "<m:ResponseMessages>"+message("SyncFolderHierarchy", "",
		"<m:SyncState>H1</m:SyncState><m:IncludesLastFolderInRange>true</m:IncludesLastFolderInRange><m:Changes>"+changes.String()+"</m:Changes>")+"</m:ResponseMessages>")
}

// syncFolderItems pages through a folder's items in the order they came; a
// SyncState is how many were given.
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
	from := 0
	if st, ok := req.text["SyncState"]; ok {
		n, err := strconv.Atoi(strings.TrimPrefix(st, "I"))
		if err != nil || n > len(f.items) {
			reply(message("SyncFolderItems", "ErrorInvalidSyncStateData", ""))
			return
		}
		from = n
	}
	maxChanges, _ := strconv.Atoi(req.text["MaxChangesReturned"])
	if maxChanges < 1 || maxChanges > 512 {
		s.t.Errorf("fake EWS: MaxChangesReturned %q", req.text["MaxChangesReturned"])
		maxChanges = 512
	}
	to := min(from+maxChanges, len(f.items))
	var changes strings.Builder
	for _, it := range f.items[from:to] {
		fmt.Fprintf(&changes, `<t:Create><t:Message><t:ItemId Id="%s" ChangeKey="CQAAAA=="/></t:Message></t:Create>`, it.id)
	}
	reply(message("SyncFolderItems", "", fmt.Sprintf("<m:SyncState>I%d</m:SyncState><m:IncludesLastItemInRange>%t</m:IncludesLastItemInRange><m:Changes>%s</m:Changes>",
		to, to == len(f.items), changes.String())))
}

func (s *EWS) getItem(w http.ResponseWriter, req ewsRequest) {
	if req.text["IncludeMimeContent"] != "true" {
		s.t.Errorf("fake EWS: GetItem without IncludeMimeContent")
	}
	var msgs strings.Builder
	for _, id := range req.itemIDs {
		var found *ewsItem
		for _, f := range s.folders {
			for _, it := range f.items {
				if it.id == id {
					found = it
				}
			}
		}
		if found == nil {
			msgs.WriteString(message("GetItem", "ErrorItemNotFound", ""))
			continue
		}
		msgs.WriteString(message("GetItem", "", fmt.Sprintf(`<m:Items><t:Message><t:MimeContent CharacterSet="UTF-8">%s</t:MimeContent><t:ItemId Id="%s" ChangeKey="CQAAAA=="/>`+
			`<t:DateTimeReceived>%s</t:DateTimeReceived><t:IsRead>%t</t:IsRead></t:Message></m:Items>`,
			base64.StdEncoding.EncodeToString(found.raw), found.id, found.received.Format(time.RFC3339), found.read)))
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
			f.items = append(f.items, &ewsItem{id: s.newID("AAMkItem"), raw: raw, received: time.Now().UTC().Truncate(time.Second), read: true})
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
