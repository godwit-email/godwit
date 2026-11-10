package ews

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"strconv"
	"time"

	"github.com/godwit-email/godwit/backend"
)

// GetItem: items by id, with their flags and, when asked, their whole MIME message.

type getItemRequest struct {
	XMLName    xml.Name           `xml:"m:GetItem"`
	BaseShape  string             `xml:"m:ItemShape>t:BaseShape"`
	MIME       bool               `xml:"m:ItemShape>t:IncludeMimeContent"`
	Properties []fieldURI         `xml:"m:ItemShape>t:AdditionalProperties>t:FieldURI"`
	Extended   []extendedFieldURI `xml:"m:ItemShape>t:AdditionalProperties>t:ExtendedFieldURI"`
	ItemIDs    []itemID           `xml:"m:ItemIds>t:ItemId"`
}

// extendedFieldURI is a MAPI property path in AdditionalProperties.
type extendedFieldURI struct {
	Tag  string `xml:"PropertyTag,attr"`
	Type string `xml:"PropertyType,attr"`
}

// lastVerb is PidTagLastVerbExecuted: 102 is a reply to the sender, 103 to all.
const lastVerb = 0x1081

type getItemResponse struct {
	Messages []getItemMessage `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages>GetItemResponseMessage"`
}

type getItemMessage struct {
	responseMessage
	Items struct {
		Item []item `xml:",any"` // Message, MeetingRequest, ...
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Items"`
}

type item struct {
	ID       id        `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	MIME     string    `xml:"http://schemas.microsoft.com/exchange/services/2006/types MimeContent"` // base64
	Received time.Time `xml:"http://schemas.microsoft.com/exchange/services/2006/types DateTimeReceived"`
	IsRead   bool      `xml:"http://schemas.microsoft.com/exchange/services/2006/types IsRead"`
	Flag     struct {
		Status string `xml:"http://schemas.microsoft.com/exchange/services/2006/types FlagStatus"` // NotFlagged, Flagged or Complete
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Flag"`
	Extended []struct {
		URI struct {
			Tag string `xml:"PropertyTag,attr"`
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types ExtendedFieldURI"`
		Value string `xml:"http://schemas.microsoft.com/exchange/services/2006/types Value"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/types ExtendedProperty"`
}

// flags is the item's flags: read, flagged (a completed flag is not), and
// answered when its last verb was a reply.
func (it item) flags() backend.Flags {
	f := backend.Flags{Seen: it.IsRead, Flagged: it.Flag.Status == "Flagged"}
	for _, e := range it.Extended {
		if tag, err := strconv.ParseInt(e.URI.Tag, 0, 32); err == nil && tag == lastVerb {
			f.Answered = e.Value == "102" || e.Value == "103"
		}
	}
	return f
}

// fetched is one GetItem result: the item, or the server's refusal of it.
type fetched struct {
	item
	raw []byte // the decoded MIME message, when asked for
	err error
}

// getItems fetches ids with their flags and, if mime, their MIME content:
// one result per id, in order.
func (c *client) getItems(ctx context.Context, ids []string, mime bool) ([]fetched, error) {
	req := getItemRequest{BaseShape: "IdOnly", MIME: mime,
		Properties: []fieldURI{{"item:DateTimeReceived"}, {"message:IsRead"}, {"item:Flag"}},
		Extended:   []extendedFieldURI{{Tag: fmt.Sprintf("0x%x", lastVerb), Type: "Integer"}}}
	for _, i := range ids {
		req.ItemIDs = append(req.ItemIDs, itemID{i})
	}
	resp, err := call[getItemResponse](ctx, c, req)
	if err != nil {
		return nil, err
	}
	if len(resp.Messages) != len(ids) {
		return nil, fmt.Errorf("ews: GetItem: %d responses to %d items", len(resp.Messages), len(ids))
	}
	out := make([]fetched, len(ids))
	for i, m := range resp.Messages {
		switch {
		case m.err() != nil:
			out[i].err = m.err()
		case len(m.Items.Item) == 0:
			out[i].err = fmt.Errorf("ews: GetItem: no item for %s", ids[i])
		default:
			out[i].item = m.Items.Item[0]
			if !mime {
				continue
			}
			if out[i].raw, err = base64.StdEncoding.DecodeString(out[i].MIME); err != nil {
				out[i].err = fmt.Errorf("ews: GetItem: MimeContent of %s: %w", ids[i], err)
			}
		}
	}
	return out, nil
}
