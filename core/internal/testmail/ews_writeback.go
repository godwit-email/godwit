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
// DeleteItem and CreateItem saving a message. A move, as on Exchange, is a
// copy under a new ItemId and the original deleted.

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
