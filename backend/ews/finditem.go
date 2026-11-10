package ews

import (
	"context"
	"encoding/xml"
	"fmt"
)

// FindItem: the ids of a folder's items whose InternetMessageId is one value.

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

type findItemResponse struct {
	Messages []findItemMessage `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages>FindItemResponseMessage"`
}

type findItemMessage struct {
	responseMessage
	Root struct {
		Items struct {
			Item []struct {
				ID id `xml:"http://schemas.microsoft.com/exchange/services/2006/types ItemId"`
			} `xml:",any"` // Message, MeetingRequest, ...
		} `xml:"http://schemas.microsoft.com/exchange/services/2006/types Items"`
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages RootFolder"`
}

// findByMessageID returns the ids of the items in folder whose
// InternetMessageId is messageID, angle brackets included.
// shortcut: one page, the server's default of up to 1000 items; page with
// IndexedPageItemView if a folder ever holds more copies of one message.
func (c *client) findByMessageID(ctx context.Context, folder, messageID string) ([]string, error) {
	req := findItemRequest{Traversal: "Shallow", BaseShape: "IdOnly", Field: fieldURI{"message:InternetMessageId"}, Folders: []folderID{folderRef(folder)}}
	req.Constant.Value = messageID
	resp, err := call[findItemResponse](ctx, c, req)
	if err != nil {
		return nil, err
	}
	if len(resp.Messages) != 1 {
		return nil, fmt.Errorf("ews: FindItem: %d responses", len(resp.Messages))
	}
	m := resp.Messages[0]
	if err := m.err(); err != nil {
		return nil, err
	}
	var out []string
	for _, it := range m.Root.Items.Item {
		out = append(out, it.ID.ID)
	}
	return out, nil
}
