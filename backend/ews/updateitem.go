package ews

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"

	"github.com/godwit-email/godwit/backend"
)

// UpdateItem: setting and clearing an item's properties. Writes overwrite
// whatever the item has now (AlwaysOverwrite), so no ChangeKey is kept, and
// marking a message read never sends a read receipt.

type updateItemRequest struct {
	XMLName              xml.Name       `xml:"m:UpdateItem"`
	Disposition          string         `xml:"MessageDisposition,attr"`
	Conflict             string         `xml:"ConflictResolution,attr"`
	SuppressReadReceipts bool           `xml:"SuppressReadReceipts,attr"`
	Changes              []updateChange `xml:"m:ItemChanges>t:ItemChange"`
}

type updateChange struct {
	ItemID  itemID       `xml:"t:ItemId"`
	Updates []itemUpdate `xml:"t:Updates>x"`
}

// itemUpdate is a t:SetItemField or a t:DeleteItemField of one property.
type itemUpdate struct {
	XMLName  xml.Name
	Field    *fieldURI         `xml:"t:FieldURI"`
	Extended *extendedFieldURI `xml:"t:ExtendedFieldURI"`
	Message  *updateMessage    `xml:"t:Message"` // the new value, for SetItemField
}

// updateMessage holds the one property a SetItemField sets.
type updateMessage struct {
	IsRead   *bool             `xml:"t:IsRead"`
	Flag     *flag             `xml:"t:Flag"`
	Extended *extendedProperty `xml:"t:ExtendedProperty"`
}

type updateItemResponse struct {
	Messages []struct {
		responseMessage
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages>UpdateItemResponseMessage"`
}

// flagUpdates are the updates that add and remove flags: read (IsRead),
// flagged (FlagStatus) and answered (PidTagLastVerbExecuted, written as a
// reply to the sender). Draft is read-only on Exchange and keywords are not
// kept, so neither is written.
func flagUpdates(add, remove backend.Flags) []itemUpdate {
	set := func(m updateMessage) itemUpdate {
		u := itemUpdate{XMLName: xml.Name{Local: "t:SetItemField"}, Message: &m}
		switch {
		case m.IsRead != nil:
			u.Field = &fieldURI{"message:IsRead"}
		case m.Flag != nil:
			u.Field = &fieldURI{"item:Flag"}
		default:
			u.Extended = &m.Extended.URI
		}
		return u
	}
	verb := extendedFieldURI{Tag: fmt.Sprintf("0x%x", lastVerb), Type: "Integer"}
	var us []itemUpdate
	if add.Seen || remove.Seen {
		us = append(us, set(updateMessage{IsRead: &add.Seen}))
	}
	if add.Flagged {
		us = append(us, set(updateMessage{Flag: &flag{"Flagged"}}))
	} else if remove.Flagged {
		us = append(us, set(updateMessage{Flag: &flag{"NotFlagged"}}))
	}
	if add.Answered {
		us = append(us, set(updateMessage{Extended: &extendedProperty{URI: verb, Value: strconv.Itoa(replyToSender)}}))
	} else if remove.Answered {
		us = append(us, itemUpdate{XMLName: xml.Name{Local: "t:DeleteItemField"}, Extended: &verb})
	}
	return us
}

// replyToSender is the PidTagLastVerbExecuted value written for answered.
const replyToSender = 102

// updateItem applies us to the item id.
func (c *client) updateItem(ctx context.Context, id string, us []itemUpdate) error {
	req := updateItemRequest{Disposition: "SaveOnly", Conflict: "AlwaysOverwrite", SuppressReadReceipts: true,
		Changes: []updateChange{{ItemID: itemID{id}, Updates: us}}}
	resp, err := call[updateItemResponse](ctx, c, req)
	if err != nil {
		return err
	}
	if len(resp.Messages) != 1 {
		return fmt.Errorf("ews: UpdateItem: %d responses", len(resp.Messages))
	}
	return resp.Messages[0].err()
}
