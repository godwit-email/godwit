package ews

import (
	"context"
	"encoding/xml"
)

// MoveItem and CopyItem, one shape: an item into another folder, the
// answer naming the new item's ItemId (a move changes it on Exchange).

type moveItemRequest struct {
	XMLName xml.Name // m:MoveItem or m:CopyItem
	To      folderID `xml:"m:ToFolderId>x"`
	ItemIDs []itemID `xml:"m:ItemIds>t:ItemId"`
	NewIDs  bool     `xml:"m:ReturnNewItemIds"`
}

// moveItem moves the item id into folder, or copies it there if copy, and
// returns the new item's ItemId: "" when the server did not say.
func (c *client) moveItem(ctx context.Context, id, folder string, copy bool) (string, error) {
	op := "m:MoveItem"
	if copy {
		op = "m:CopyItem"
	}
	return one(ctx, c, moveItemRequest{XMLName: xml.Name{Local: op}, To: folderRef(folder), ItemIDs: []itemID{{id}}, NewIDs: true})
}
