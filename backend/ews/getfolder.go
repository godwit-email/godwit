package ews

import (
	"context"
	"encoding/xml"
	"fmt"
)

// GetFolder: the ids of folders named by DistinguishedFolderId.

type getFolderRequest struct {
	XMLName   xml.Name   `xml:"m:GetFolder"`
	BaseShape string     `xml:"m:FolderShape>t:BaseShape"`
	FolderIDs []folderID `xml:"m:FolderIds>x"` // each element is named by its XMLName
}

type getFolderResponse struct {
	Messages []getFolderMessage `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages>GetFolderResponseMessage"`
}

type getFolderMessage struct {
	responseMessage
	Folders struct {
		Folder []struct {
			ID id `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
		} `xml:",any"` // Folder, CalendarFolder, ...
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Folders"`
}

// getFolders returns the id of each distinguished folder in names, in order;
// "" for one the server refused (an Error response, such as no archive folder).
func (c *client) getFolders(ctx context.Context, names []string) ([]string, error) {
	req := getFolderRequest{BaseShape: "IdOnly"}
	for _, n := range names {
		req.FolderIDs = append(req.FolderIDs, distinguished(n))
	}
	resp, err := call[getFolderResponse](ctx, c, req)
	if err != nil {
		return nil, err
	}
	if len(resp.Messages) != len(names) {
		return nil, fmt.Errorf("ews: GetFolder: %d responses to %d folders", len(resp.Messages), len(names))
	}
	out := make([]string, len(names))
	for i, m := range resp.Messages {
		if m.err() == nil && len(m.Folders.Folder) > 0 {
			out[i] = m.Folders.Folder[0].ID.ID
		}
	}
	return out, nil
}
