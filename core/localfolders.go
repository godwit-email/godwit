package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/godwit-email/godwit/core/store"
)

// Local folders are the folders of a local archive (ADR 0036): they exist
// only here, so they are made, renamed and deleted without a server.

var (
	// ErrNotLocalArchive is a local folder call on a mail account's folder:
	// those folders are the server's.
	ErrNotLocalArchive = errors.New("only a local archive has local folders")
	// ErrRoleFolder is a rename or delete of a local archive's inbox or trash.
	ErrRoleFolder = errors.New("the inbox and trash cannot be renamed or deleted")
	// ErrFolderNotEmpty is a delete of a local folder that still holds mail.
	ErrFolderNotEmpty = errors.New("the folder is not empty")
)

// CreateLocalFolder adds a folder named name to the local archive accountID.
func (s *Service) CreateLocalFolder(ctx context.Context, accountID int64, name string) (Folder, error) {
	name, err := localFolderName(name)
	if err != nil {
		return Folder{}, err
	}
	if local, err := s.localArchive(ctx, accountID); !local || err != nil {
		return Folder{}, cmp.Or(err, ErrNotLocalArchive)
	}
	s.editMu.Lock()
	defer s.editMu.Unlock()
	if err := s.freeFolderName(ctx, accountID, name); err != nil {
		return Folder{}, err
	}
	res, err := s.db.Execute(ctx, "INSERT INTO folders(account_id, name) VALUES (?, ?)", accountID, name)
	if err != nil {
		return Folder{}, err
	}
	return Folder{ID: res.LastInsertID, AccountID: accountID, Name: name}, nil
}

// RenameLocalFolder renames a local archive's folder other than its inbox and trash.
func (s *Service) RenameLocalFolder(ctx context.Context, folderID int64, name string) error {
	name, err := localFolderName(name)
	if err != nil {
		return err
	}
	s.editMu.Lock()
	defer s.editMu.Unlock()
	acc, err := s.localFolder(ctx, folderID)
	if err != nil {
		return err
	}
	if err := s.freeFolderName(ctx, acc, name); err != nil {
		return err
	}
	_, err = s.db.Execute(ctx, "UPDATE folders SET name = ? WHERE id = ?", name, folderID)
	return err
}

// DeleteLocalFolder deletes an empty folder of a local archive other than its
// inbox and trash. A folder still holding mail is refused with
// ErrFolderNotEmpty: its mail moves out first.
func (s *Service) DeleteLocalFolder(ctx context.Context, folderID int64) error {
	s.editMu.Lock()
	defer s.editMu.Unlock()
	if _, err := s.localFolder(ctx, folderID); err != nil {
		return err
	}
	// Any location counts, shown or not: deleting the folder would cascade it.
	rows, err := s.db.Query(ctx, "SELECT EXISTS(SELECT 1 FROM message_locations WHERE folder_id = ?)", folderID)
	if err != nil {
		return err
	}
	if store.Int(rows.Values[0][0]) != 0 {
		return ErrFolderNotEmpty
	}
	_, err = s.db.Execute(ctx, "DELETE FROM folders WHERE id = ?", folderID)
	return err
}

// localFolder returns the account of a local archive's folder that is
// neither its inbox nor its trash.
func (s *Service) localFolder(ctx context.Context, folderID int64) (int64, error) {
	rows, err := s.db.Query(ctx, "SELECT f.account_id, f.role, a.kind FROM folders f JOIN accounts a ON a.id = f.account_id WHERE f.id = ?", folderID)
	if err != nil {
		return 0, err
	}
	if len(rows.Values) == 0 {
		return 0, fmt.Errorf("folder %d not found", folderID)
	}
	r := rows.Values[0]
	if store.Str(r[2]) != KindLocalArchive {
		return 0, ErrNotLocalArchive
	}
	if store.Str(r[1]) != "" {
		return 0, ErrRoleFolder
	}
	return store.Int(r[0]), nil
}

func localFolderName(name string) (string, error) {
	if name = strings.TrimSpace(name); name == "" {
		return "", errors.New("a folder needs a name")
	}
	return name, nil
}

func (s *Service) freeFolderName(ctx context.Context, accountID int64, name string) error {
	rows, err := s.db.Query(ctx, "SELECT 1 FROM folders WHERE account_id = ? AND name = ?", accountID, name)
	if err != nil {
		return err
	}
	if len(rows.Values) > 0 {
		return fmt.Errorf("a folder named %q already exists", name)
	}
	return nil
}
