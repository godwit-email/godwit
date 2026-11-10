package ews

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"time"
)

// GetItem: items by id, with their whole MIME message.

type getItemRequest struct {
	XMLName    xml.Name   `xml:"m:GetItem"`
	BaseShape  string     `xml:"m:ItemShape>t:BaseShape"`
	MIME       bool       `xml:"m:ItemShape>t:IncludeMimeContent"`
	Properties []fieldURI `xml:"m:ItemShape>t:AdditionalProperties>t:FieldURI"`
	ItemIDs    []itemID   `xml:"m:ItemIds>t:ItemId"`
}

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
}

// fetched is one GetItem result: the item, or the server's refusal of it.
type fetched struct {
	item
	raw []byte // the decoded MIME message
	err error
}

// getItems fetches ids with their MIME content, one result per id in order.
func (c *client) getItems(ctx context.Context, ids []string) ([]fetched, error) {
	req := getItemRequest{BaseShape: "IdOnly", MIME: true,
		Properties: []fieldURI{{"item:DateTimeReceived"}, {"message:IsRead"}}}
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
			if out[i].raw, err = base64.StdEncoding.DecodeString(out[i].MIME); err != nil {
				out[i].err = fmt.Errorf("ews: GetItem: MimeContent of %s: %w", ids[i], err)
			}
		}
	}
	return out, nil
}
