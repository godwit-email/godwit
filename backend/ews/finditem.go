package ews

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"
)

// FindItem: the ids of a folder's items whose InternetMessageId is one
// value, or a page of its items, newest received first.

type findItemRequest struct {
	XMLName   xml.Name `xml:"m:FindItem"`
	Traversal string   `xml:"Traversal,attr"`
	BaseShape string   `xml:"m:ItemShape>t:BaseShape"`
	Field     fieldURI `xml:"m:Restriction>t:IsEqualTo>t:FieldURI"`
	Constant  struct {
		Value string `xml:"Value,attr"`
	} `xml:"m:Restriction>t:IsEqualTo>t:FieldURIOrConstant>t:Constant"`
	Folders []folderID `xml:"m:ParentFolderIds>x"`
}

// findPageRequest is FindItem of one IndexedPageItemView page, sorted by
// DateTimeReceived, newest first.
type findPageRequest struct {
	XMLName    xml.Name   `xml:"m:FindItem"`
	Traversal  string     `xml:"Traversal,attr"`
	BaseShape  string     `xml:"m:ItemShape>t:BaseShape"`
	Properties []fieldURI `xml:"m:ItemShape>t:AdditionalProperties>t:FieldURI"`
	View       struct {
		Max       int    `xml:"MaxEntriesReturned,attr"`
		Offset    int    `xml:"Offset,attr"`
		BasePoint string `xml:"BasePoint,attr"`
	} `xml:"m:IndexedPageItemView"`
	Order struct {
		Order string   `xml:"Order,attr"`
		Field fieldURI `xml:"t:FieldURI"`
	} `xml:"m:SortOrder>t:FieldOrder"`
	Folders []folderID `xml:"m:ParentFolderIds>x"`
}

type findItemResponse struct {
	Messages []findItemMessage `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages>FindItemResponseMessage"`
}

type findItemMessage struct {
	responseMessage
	Root struct {
		Last  bool `xml:"IncludesLastItemInRange,attr"`
		Items struct {
			Item []found `xml:",any"` // Message, MeetingRequest, ...
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Items"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RootFolder"`
}

// found is an item FindItem found.
type found struct {
	ID       id        `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
	Received time.Time `xml:"http://schemas.microsoft.com/exchange/services/2006/types DateTimeReceived"` // a page's
}

func (c *client) findItem(ctx context.Context, req any) (findItemMessage, error) {
	resp, err := call[findItemResponse](ctx, c, req)
	if err != nil {
		return findItemMessage{}, err
	}
	if len(resp.Messages) != 1 {
		return findItemMessage{}, fmt.Errorf("ews: FindItem: %d responses", len(resp.Messages))
	}
	m := resp.Messages[0]
	return m, m.err()
}

// findByMessageID returns the ids of the items in folder whose
// InternetMessageId is messageID, angle brackets included.
// shortcut: one page, the server's default of up to 1000 items; page with
// IndexedPageItemView if a folder ever holds more copies of one message.
func (c *client) findByMessageID(ctx context.Context, folder, messageID string) ([]string, error) {
	req := findItemRequest{Traversal: "Shallow", BaseShape: "IdOnly", Field: fieldURI{"message:InternetMessageId"}, Folders: []folderID{folderRef(folder)}}
	req.Constant.Value = messageID
	m, err := c.findItem(ctx, req)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, it := range m.Root.Items.Item {
		out = append(out, it.ID.ID)
	}
	return out, nil
}

// findPage returns up to max of folder's items from offset, newest received
// first, and whether they reach its last. The server may return fewer than
// max, its own limit on a page.
func (c *client) findPage(ctx context.Context, folder string, offset, max int) ([]found, bool, error) {
	req := findPageRequest{Traversal: "Shallow", BaseShape: "IdOnly", Properties: []fieldURI{{"item:DateTimeReceived"}}, Folders: []folderID{folderRef(folder)}}
	req.View.Max, req.View.Offset, req.View.BasePoint = max, offset, "Beginning"
	req.Order.Order, req.Order.Field = "Descending", fieldURI{"item:DateTimeReceived"}
	m, err := c.findItem(ctx, req)
	if err != nil {
		return nil, false, err
	}
	return m.Root.Items.Item, m.Root.Last || len(m.Root.Items.Item) == 0, nil
}
