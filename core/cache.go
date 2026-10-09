package core

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

func (s *Service) cacheDir(accountID int64) string {
	return filepath.Join(s.dataDir, "cache", strconv.FormatInt(accountID, 10))
}

// AttachmentCacheUsage is the bytes the account's downloaded attachments take on disk.
func (s *Service) AttachmentCacheUsage(ctx context.Context, accountID int64) (int64, error) {
	var n int64
	err := filepath.WalkDir(s.cacheDir(accountID), func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err == nil {
			n += info.Size()
		}
		return err
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return n, err
}

// ClearAttachmentCache deletes the account's downloaded attachments; opening
// one downloads it again. Other accounts' caches stay.
func (s *Service) ClearAttachmentCache(ctx context.Context, accountID int64) error {
	ctx, end, err := s.begin(ctx, accountID)
	if err != nil {
		return err
	}
	defer end()
	unlock, err := s.lockAccount(ctx, accountID) // no download writes a file meanwhile
	if err != nil {
		return err
	}
	defer unlock()
	// Records first: a file that cannot be removed is then merely unreferenced.
	if _, err := s.db.Execute(ctx, `UPDATE attachments SET cache_path = NULL
		WHERE cache_path IS NOT NULL AND message_id IN (SELECT id FROM messages WHERE account_id = ?)`, accountID); err != nil {
		return err
	}
	return os.RemoveAll(s.cacheDir(accountID))
}
