package testmail

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The fake EWS server's write-back operations: MoveItem, CopyItem,
// DeleteItem, CreateItem saving a message, CreateFolder, UpdateFolder and
// DeleteFolder. A move, as on Exchange, is a copy under a new ItemId and
// the original deleted.

// OmitNewItemIds makes MoveItem and CopyItem answer without the new
// ItemIds, as Exchange does for a move to another mailbox.
func (s *EWS) OmitNewItemIds() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.omitNewIDs = true
}

// Items is the ItemIds of the mail in the folder at path, in order.
func (s *EWS) Items(path string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, it := range s.path(path, false).items {
		if !it.gone {
			out = append(out, it.id)
		}
	}
	return out
}

// Unsent reports that the item id was saved as a draft.
func (s *EWS) Unsent(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.findItem(id)
	return it != nil && it.unsent
}

// put files a copy of it into f under a new ItemId.
func (s *EWS) put(f *ewsFolder, it ewsItem) *ewsItem {
	it.id, it.changed, it.readOnly = s.newID("AAMkItem"), changed{}, false
	it.created = s.touch(&it.changed)
	f.items = append(f.items, &it)
	return &it
}

// distinguishedFolder is the live folder whose DistinguishedFolderId is d.
func (s *EWS) distinguishedFolder(d string) *ewsFolder {
	for _, f := range s.folders {
		if f.distinguished == d && !f.gone {
			return f
		}
	}
	return nil
}

// target is the folder a request names first, by FolderId or
// DistinguishedFolderId; nil if there is none.
func (s *EWS) target(req ewsRequest) *ewsFolder {
	switch {
	case len(req.folderIDs) > 0:
		return s.byID(req.folderIDs[0])
	case len(req.distinguished) > 0:
		return s.distinguishedFolder(req.distinguished[0])
	}
	return nil
}

// moveItem moves or copies the items to the ToFolderId.
func (s *EWS) moveItem(w http.ResponseWriter, req ewsRequest) {
	to := s.target(req)
	var msgs strings.Builder
	for _, id := range req.itemIDs {
		it := s.findItem(id)
		switch {
		case it == nil:
			msgs.WriteString(message(req.op, "ErrorItemNotFound", "<m:Items/>"))
			continue
		case to == nil:
			msgs.WriteString(message(req.op, "ErrorFolderNotFound", "<m:Items/>"))
			continue
		}
		moved := s.put(to, *it)
		if req.op == "MoveItem" {
			it.gone = true
			s.touch(&it.changed)
		}
		items := "<m:Items/>"
		if req.text["ReturnNewItemIds"] == "true" && !s.omitNewIDs {
			items = fmt.Sprintf(`<m:Items><t:Message><t:ItemId Id="%s" ChangeKey="CQAAAA=="/></t:Message></m:Items>`, moved.id)
		}
		msgs.WriteString(message(req.op, "", items))
	}
	s.respond(w, styleDefaultNS, req.op, "<m:ResponseMessages>"+msgs.String()+"</m:ResponseMessages>")
}

// deleteItem deletes the items for good (HardDelete) or moves them to
// Deleted Items (MoveToDeletedItems; for good from there).
func (s *EWS) deleteItem(w http.ResponseWriter, req ewsRequest) {
	how := req.text["DeleteType"]
	if how != "HardDelete" && how != "MoveToDeletedItems" {
		s.t.Errorf("fake EWS: DeleteItem DeleteType %q", how)
	}
	trash := s.distinguishedFolder("deleteditems")
	var msgs strings.Builder
	for _, id := range req.itemIDs {
		it := s.findItem(id)
		if it == nil {
			msgs.WriteString(message("DeleteItem", "ErrorItemNotFound", ""))
			continue
		}
		if how == "MoveToDeletedItems" && !slices.Contains(trash.items, it) {
			s.put(trash, *it)
		}
		it.gone = true
		s.touch(&it.changed)
		msgs.WriteString(message("DeleteItem", "", ""))
	}
	s.respond(w, styleUsual, "DeleteItem", "<m:ResponseMessages>"+msgs.String()+"</m:ResponseMessages>")
}

// saveItem saves a message from its MIME content into the SavedItemFolderId
// (CreateItem SaveOnly), with the flags its properties set: PR_MESSAGE_FLAGS
// (0x0E07) read and unsent, Flag, PidTagLastVerbExecuted (0x1081) and
// PR_MESSAGE_DELIVERY_TIME (0x0E06).
func (s *EWS) saveItem(w http.ResponseWriter, req ewsRequest) {
	reply := func(msg string) {
		s.respond(w, styleEnvelopeDecl, "CreateItem", "<m:ResponseMessages>"+msg+"</m:ResponseMessages>")
	}
	if d := req.text["MessageDisposition"]; d != "SaveOnly" {
		s.t.Errorf("fake EWS: CreateItem MessageDisposition %q", d)
		reply(message("CreateItem", "ErrorInvalidRequest", ""))
		return
	}
	if req.elements["Flag"] && req.text["FlagStatus"] == "" {
		s.t.Errorf("fake EWS: CreateItem with a Flag without its FlagStatus")
	}
	f := s.target(req)
	raw, err := base64.StdEncoding.DecodeString(req.text["MimeContent"])
	switch {
	case err != nil:
		s.t.Errorf("fake EWS: CreateItem MimeContent: %v", err)
		reply(message("CreateItem", "ErrorMimeContentInvalid", ""))
		return
	case f == nil:
		reply(message("CreateItem", "ErrorFolderNotFound", ""))
		return
	}
	it := ewsItem{raw: raw, received: time.Now().UTC().Truncate(time.Second), flagged: req.text["FlagStatus"] == "Flagged"}
	if fl, ok := req.extended["0x0E07"]; ok {
		n, _ := strconv.Atoi(fl)
		it.read, it.unsent = n&1 != 0, n&8 != 0
	}
	it.verb, _ = strconv.Atoi(req.extended["0x1081"])
	if at, err := time.Parse(time.RFC3339, req.extended["0x0E06"]); err == nil {
		it.received = at
	}
	saved := s.put(f, it)
	reply(message("CreateItem", "", fmt.Sprintf(`<m:Items><t:Message><t:ItemId Id="%s" ChangeKey="CQAAAA=="/></t:Message></m:Items>`, saved.id)))
}

// createFolder creates a folder named DisplayName under the ParentFolderId.
func (s *EWS) createFolder(w http.ResponseWriter, req ewsRequest) {
	reply := func(msg string) {
		s.respond(w, styleUsual, "CreateFolder", "<m:ResponseMessages>"+msg+"</m:ResponseMessages>")
	}
	parent, name := s.target(req), req.text["DisplayName"]
	if parent == nil {
		reply(message("CreateFolder", "ErrorParentFolderNotFound", ""))
		return
	}
	for _, f := range s.folders {
		if f.parent == parent.id && f.name == name && !f.gone {
			reply(message("CreateFolder", "ErrorFolderExists", ""))
			return
		}
	}
	f := s.folder(parent.id, name, "", req.text["FolderClass"])
	reply(message("CreateFolder", "", fmt.Sprintf(`<m:Folders><t:Folder><t:FolderId Id="%s" ChangeKey="AQAAAA=="/></t:Folder></m:Folders>`, f.id)))
}

// updateFolder sets a folder's DisplayName, the one field it knows.
func (s *EWS) updateFolder(w http.ResponseWriter, req ewsRequest) {
	reply := func(msg string) {
		s.respond(w, styleDefaultNS, "UpdateFolder", "<m:ResponseMessages>"+msg+"</m:ResponseMessages>")
	}
	if !slices.Equal(req.fieldURIs, []string{"folder:DisplayName"}) || !req.elements["SetFolderField"] {
		s.t.Errorf("fake EWS: UpdateFolder of %v", req.fieldURIs)
		reply(message("UpdateFolder", "ErrorInvalidPropertySet", ""))
		return
	}
	f := s.target(req)
	if f == nil {
		reply(message("UpdateFolder", "ErrorFolderNotFound", ""))
		return
	}
	f.name = req.text["DisplayName"]
	s.touch(&f.changed)
	reply(message("UpdateFolder", "", fmt.Sprintf(`<m:Folders><t:Folder><t:FolderId Id="%s" ChangeKey="AQAAAA=="/></t:Folder></m:Folders>`, f.id)))
}

// deleteFolder deletes a folder for good (HardDelete) or moves it to
// Deleted Items with its mail and subfolders (MoveToDeletedItems).
func (s *EWS) deleteFolder(w http.ResponseWriter, req ewsRequest) {
	how := req.text["DeleteType"]
	if how != "HardDelete" && how != "MoveToDeletedItems" {
		s.t.Errorf("fake EWS: DeleteFolder DeleteType %q", how)
	}
	f := s.target(req)
	switch {
	case f == nil:
		s.respond(w, styleEnvelopeDecl, "DeleteFolder", "<m:ResponseMessages>"+message("DeleteFolder", "ErrorFolderNotFound", "")+"</m:ResponseMessages>")
		return
	case how == "HardDelete":
		f.gone = true
	default:
		f.parent = s.distinguishedFolder("deleteditems").id
	}
	s.touch(&f.changed)
	s.respond(w, styleEnvelopeDecl, "DeleteFolder", "<m:ResponseMessages>"+message("DeleteFolder", "", "")+"</m:ResponseMessages>")
}
