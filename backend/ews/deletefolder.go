package ews

import (
	"context"
	"encoding/xml"
)

// DeleteFolder: a folder with its mail and subfolders.

type deleteFolderRequest struct {
	XMLName    xml.Name   `xml:"m:DeleteFolder"`
	DeleteType string     `xml:"DeleteType,attr"`
	Folders    []folderID `xml:"m:FolderIds>x"`
}

// deleteFolder deletes the folder id the way how names.
func (c *client) deleteFolder(ctx context.Context, id, how string) error {
	_, err := one(ctx, c, deleteFolderRequest{DeleteType: how, Folders: []folderID{folderRef(id)}})
	return err
}
