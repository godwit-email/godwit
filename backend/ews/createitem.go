package ews

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"time"

	"github.com/godwit-email/godwit/backend"
)

// CreateItem with SaveOnly: a MIME message saved into a folder with its
// flags, sending nothing. (Sending is createitem_send.go.)

type createItemRequest struct {
	XMLName     xml.Name           `xml:"m:CreateItem"`
	Disposition string             `xml:"MessageDisposition,attr"`
	SavedIn     folderID           `xml:"m:SavedItemFolderId>x"`
	MIME        string             `xml:"m:Items>t:Message>t:MimeContent"` // base64
	Extended    []extendedProperty `xml:"m:Items>t:Message>t:ExtendedProperty"`
	Flag        *flag              `xml:"m:Items>t:Message>t:Flag"` // nil: none, as an empty Flag fails the schema
}

type flag struct {
	Status string `xml:"t:FlagStatus"` // NotFlagged, Flagged or Complete
}

// extendedProperty is a MAPI property set on an item.
type extendedProperty struct {
	URI   extendedFieldURI `xml:"t:ExtendedFieldURI"`
	Value string           `xml:"t:Value"`
}

// The MAPI properties a saved message's flags are set with.
const (
	messageFlags    = "0x0E07" // PR_MESSAGE_FLAGS, settable only as the item is created
	msgflagRead     = 0x1
	msgflagUnsent   = 0x8      // a draft; without it the message is mail sent or received
	deliveryTime    = "0x0E06" // PR_MESSAGE_DELIVERY_TIME, read as DateTimeReceived
	repliedToSender = 102      // lastVerb's value for a reply
)

// createItem saves raw into folder with flags (read, flagged, answered, a
// draft), received at date unless it is zero, and returns its ItemId.
func (c *client) createItem(ctx context.Context, folder string, raw []byte, flags backend.Flags, date time.Time) (string, error) {
	mf := 0
	if flags.Seen {
		mf |= msgflagRead
	}
	if flags.Draft {
		mf |= msgflagUnsent
	}
	req := createItemRequest{Disposition: "SaveOnly", SavedIn: folderRef(folder), MIME: base64.StdEncoding.EncodeToString(raw),
		Extended: []extendedProperty{{extendedFieldURI{messageFlags, "Integer"}, fmt.Sprint(mf)}}}
	if flags.Answered {
		req.Extended = append(req.Extended, extendedProperty{extendedFieldURI{fmt.Sprintf("0x%x", lastVerb), "Integer"}, fmt.Sprint(repliedToSender)})
	}
	if !date.IsZero() {
		req.Extended = append(req.Extended, extendedProperty{extendedFieldURI{deliveryTime, "SystemTime"}, date.UTC().Format(time.RFC3339)})
	}
	if flags.Flagged {
		req.Flag = &flag{"Flagged"}
	}
	return one(ctx, c, req)
}
