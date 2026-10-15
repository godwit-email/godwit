package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/ownerlock"
)

// SyncDevDatabase replaces dst's database with an isolated copy of src's.
// It requires both data directories to be offline and never migrates src.
func SyncDevDatabase(ctx context.Context, src, dst string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	sourceInfo, err := os.Stat(filepath.Join(src, "godwit.db"))
	if err != nil {
		return err
	}
	for _, dir := range []string{src, dst} {
		release, err := ownerlock.Acquire(dir)
		if err != nil {
			return fmt.Errorf("close Godwit using %s before syncing: %w", dir, err)
		}
		defer release()
	}
	stage, err := os.MkdirTemp(dst, ".sync-db-")
	if err != nil {
		return err
	}
	target := filepath.Join(dst, "godwit.db")
	var moved []string
	installed := false
	defer func() {
		var restoreErr error
		if !installed {
			for _, suffix := range moved {
				restoreErr = errors.Join(restoreErr, os.Rename(filepath.Join(stage, "old"+suffix), target+suffix))
			}
		}
		if restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore dev database from %s: %w", stage, restoreErr))
			return // Keep any backups that could not be restored.
		}
		os.RemoveAll(stage)
	}()
	for _, name := range []string{"godwit.db", "godwit.db-wal"} {
		if err := copyDatabaseFile(filepath.Join(src, name), filepath.Join(stage, name)); err != nil {
			if name == "godwit.db-wal" && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
	}
	// Migrate only the disposable copy; never open production with store.Open.
	path := filepath.Join(stage, "godwit.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		return err
	}
	// Files and keychain entries are not copied; never reuse production or stale dev references.
	_, err = db.Transaction(ctx, []sqlite.Statement{
		{SQL: "DELETE FROM meta WHERE key = 'install_id'"},
		{SQL: "UPDATE attachments SET cache_path = NULL"},
		{SQL: "UPDATE draft_attachments SET path = NULL"},
		{SQL: "UPDATE outbox SET raw_path = NULL"},
	})
	if err == nil {
		err = sealDatabase(ctx, db.DB)
	}
	if err = errors.Join(err, db.Close()); err != nil {
		return err
	}
	// Move old files aside without opening them: even corrupt dev data can be replaced.
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		fi, err := os.Lstat(target + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() || os.SameFile(sourceInfo, fi) {
			return fmt.Errorf("dev database files must be separate regular files: %s", target+suffix)
		}
		if err := os.Rename(target+suffix, filepath.Join(stage, "old"+suffix)); err != nil {
			return err
		}
		moved = append(moved, suffix)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(path, target); err != nil {
		return err
	}
	installed = true
	return nil
}

func sealDatabase(ctx context.Context, db *sqlite.DB) error {
	rows, err := db.Query(ctx, "PRAGMA quick_check")
	if err != nil {
		return err
	}
	if len(rows.Values) != 1 || store.Str(rows.Values[0][0]) != "ok" {
		return errors.New("database integrity check failed")
	}
	rows, err = db.Query(ctx, "PRAGMA journal_mode = DELETE")
	if err != nil {
		return err
	}
	if len(rows.Values) != 1 || store.Str(rows.Values[0][0]) != "delete" {
		return errors.New("could not checkpoint database WAL")
	}
	return nil
}

func copyDatabaseFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	return errors.Join(err, out.Close())
}
