package ews

import (
	"context"
	"encoding/xml"
	"fmt"
)

// SyncFolderHierarchy: the mailbox's folders, or their changes since a SyncState.

type syncFolderHierarchyRequest struct {
	XMLName   xml.Name `xml:"m:SyncFolderHierarchy"`
	BaseShape string   `xml:"m:FolderShape>t:BaseShape"`
	SyncState string   `xml:"m:SyncState,omitempty"`
}

type syncFolderHierarchyResponse struct {
	Messages []syncFolderHierarchyMessage `xml:"http://schemas.microsoft.com/exchange/services/2006/messages ResponseMessages>SyncFolderHierarchyResponseMessage"`
}

type syncFolderHierarchyMessage struct {
	responseMessage
	SyncState string `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncState"`
	Last      bool   `xml:"http://schemas.microsoft.com/exchange/services/2006/messages IncludesLastFolderInRange"`
	Changes   struct {
		Change []folderChange `xml:",any"` // Create, Update or Delete
	} `xml:"http://schemas.microsoft.com/exchange/services/2006/messages Changes"`
}

// folderChange is one hierarchy change. Folder is set only for a mail
// folder (a t:Folder; calendars, contacts and the like have their own
// element); FolderID only for a Delete.
type folderChange struct {
	XMLName  xml.Name
	Folder   *folder `xml:"http://schemas.microsoft.com/exchange/services/2006/types Folder"`
	FolderID *id     `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
}

type folder struct {
	ID          id     `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderId"`
	Parent      id     `xml:"http://schemas.microsoft.com/exchange/services/2006/types ParentFolderId"`
	Class       string `xml:"http://schemas.microsoft.com/exchange/services/2006/types FolderClass"`
	DisplayName string `xml:"http://schemas.microsoft.com/exchange/services/2006/types DisplayName"`
}

// syncFolderHierarchy returns the changes since state ("" for every folder)
// and the new state, following the server's pages to the last.
func (c *client) syncFolderHierarchy(ctx context.Context, state string) ([]folderChange, string, error) {
	var out []folderChange
	for {
		resp, err := call[syncFolderHierarchyResponse](ctx, c, syncFolderHierarchyRequest{BaseShape: "AllProperties", SyncState: state})
		if err != nil {
			return nil, "", err
		}
		if len(resp.Messages) != 1 {
			return nil, "", fmt.Errorf("ews: SyncFolderHierarchy: %d responses", len(resp.Messages))
		}
		m := resp.Messages[0]
		if err := m.err(); err != nil {
			return nil, "", err
		}
		if m.Last {
			return append(out, m.Changes.Change...), m.SyncState, nil
		}
		if m.SyncState == state {
			return nil, "", fmt.Errorf("ews: SyncFolderHierarchy: more to come at the same SyncState")
		}
		out, state = append(out, m.Changes.Change...), m.SyncState
	}
}
