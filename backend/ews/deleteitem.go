package ews

import (
	"context"
	"encoding/xml"
)

// DeleteItem: an item to Deleted Items, or deleted for good.

type deleteItemRequest struct {
	XMLName    xml.Name `xml:"m:DeleteItem"`
	DeleteType string   `xml:"DeleteType,attr"` // HardDelete, SoftDelete or MoveToDeletedItems
	ItemIDs    []itemID `xml:"m:ItemIds>t:ItemId"`
}

// deleteItem deletes the item id the way how names.
func (c *client) deleteItem(ctx context.Context, id, how string) error {
	_, err := one(ctx, c, deleteItemRequest{DeleteType: how, ItemIDs: []itemID{{id}}})
	return err
}
