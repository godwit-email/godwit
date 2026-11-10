package ews

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"
)

// SyncFolderItems: one page of a folder's item changes since a SyncState.

// maxChanges is MaxChangesReturned, the most the server allows.
const maxChanges = 512

type syncFolderItemsRequest struct {
	XMLName    xml.Name   `xml:"m:SyncFolderItems"`
	BaseShape  string     `xml:"m:ItemShape>t:BaseShape"`
	Properties []fieldURI `xml:"m:ItemShape>t:AdditionalProperties>t:FieldURI"`
	Folder     folderID   `xml:"m:SyncFolderId>x"`
	SyncState  string     `xml:"m:SyncState,omitempty"`
	MaxChanges int        `xml:"m:MaxChangesReturned"`
}

type syncFolderItemsResponse struct {
	Messages []syncFolderItemsMessage `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages>SyncFolderItemsResponseMessage"`
}

type syncFolderItemsMessage struct {
	responseMessage
	SyncState string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncState"`
	Last      bool   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages IncludesLastItemInRange"`
	Changes   struct {
		Change []itemChange `xml:",any"` // Create, Update, Delete or ReadFlagChange
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Changes"`
}

// itemChange is one item change: XMLName.Local says which. A Create or
// Update holds the item (Message, MeetingRequest, ...), whose ItemId Item
// reads; a Delete or ReadFlagChange holds the ItemId itself.
type itemChange struct {
	XMLName xml.Name
	Item    struct {
		ID       id        `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
		Received time.Time `xml:"http://schemas.microsoft.com/exchange/services/2006/types DateTimeReceived"` // a Create's
	} `xml:",any"`
	ItemID *id `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
}

// ref is the changed item's id.
func (ch itemChange) ref() string {
	if ch.ItemID != nil {
		return ch.ItemID.ID
	}
	return ch.Item.ID.ID
}

// syncFolderItems returns one page of the changes in folder since state ("" for every item).
func (c *client) syncFolderItems(ctx context.Context, folder, state string) (syncFolderItemsMessage, error) {
	resp, err := call[syncFolderItemsResponse](ctx, c, syncFolderItemsRequest{BaseShape: "IdOnly", Properties: []fieldURI{{"item:DateTimeReceived"}}, Folder: folderRef(folder), SyncState: state, MaxChanges: maxChanges})
	if err != nil {
		return syncFolderItemsMessage{}, err
	}
	if len(resp.Messages) != 1 {
		return syncFolderItemsMessage{}, fmt.Errorf("ews: SyncFolderItems: %d responses", len(resp.Messages))
	}
	m := resp.Messages[0]
	return m, m.err()
}
