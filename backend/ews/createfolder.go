package ews

import (
	"context"
	"encoding/xml"
)

// CreateFolder: a mail folder under another.

type createFolderRequest struct {
	XMLName xml.Name `xml:"m:CreateFolder"`
	Parent  folderID `xml:"m:ParentFolderId>x"`
	Class   string   `xml:"m:Folders>t:Folder>t:FolderClass"`
	Name    string   `xml:"m:Folders>t:Folder>t:DisplayName"`
}

// createFolder creates a mail folder named name under parent and returns its FolderId.
func (c *client) createFolder(ctx context.Context, parent folderID, name string) (string, error) {
	return one(ctx, c, createFolderRequest{Parent: parent, Class: "IPF.Note", Name: name})
}
