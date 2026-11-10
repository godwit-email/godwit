package ews

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
)

// CreateItem: send a MIME message, the server filing its copy in Sent Items.

type createItemRequest struct {
	XMLName     xml.Name `xml:"m:CreateItem"`
	Disposition string   `xml:"MessageDisposition,attr"`
	SavedIn     folderID `xml:"m:SavedItemFolderId>t:DistinguishedFolderId"`
	MIME        string   `xml:"m:Items>t:Message>t:MimeContent"` // base64
	Bcc         []string `xml:"m:Items>t:Message>t:BccRecipients>t:Mailbox>t:EmailAddress"`
}

type createItemResponse struct {
	Messages []responseMessage `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages>CreateItemResponseMessage"`
}

// sendMIME sends raw with SendAndSaveCopy, to its To and Cc and to bcc.
func (c *client) sendMIME(ctx context.Context, raw []byte, bcc []string) error {
	resp, err := call[createItemResponse](ctx, c, createItemRequest{Disposition: "SendAndSaveCopy",
		SavedIn: distinguished("sentitems"), MIME: base64.StdEncoding.EncodeToString(raw), Bcc: bcc})
	if err != nil {
		return err
	}
	if len(resp.Messages) != 1 {
		return fmt.Errorf("%w: %d responses to one message", errNoVerdict, len(resp.Messages))
	}
	return resp.Messages[0].err()
}

// errNoVerdict is an answer that does not say whether the message was sent.
var errNoVerdict = errors.New("ews: CreateItem answered without a verdict")
