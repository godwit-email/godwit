package ews

import (
	"context"
	"encoding/xml"
)

// UpdateFolder: a folder's display name; its FolderId stays.

type updateFolderRequest struct {
	XMLName xml.Name `xml:"m:UpdateFolder"`
	Folder  folderID `xml:"m:FolderChanges>t:FolderChange>x"`
	Field   fieldURI `xml:"m:FolderChanges>t:FolderChange>t:Updates>t:SetFolderField>t:FieldURI"`
	Name    string   `xml:"m:FolderChanges>t:FolderChange>t:Updates>t:SetFolderField>t:Folder>t:DisplayName"`
}

// renameFolder sets the folder id's display name to name.
func (c *client) renameFolder(ctx context.Context, id, name string) error {
	_, err := one(ctx, c, updateFolderRequest{Folder: folderRef(id), Field: fieldURI{"folder:DisplayName"}, Name: name})
	return err
}
