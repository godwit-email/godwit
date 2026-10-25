// Package store is Godwit's SQLite storage layer.
package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/plugins/sqlite"
)

// ErrNewerDatabase is returned when the database was written by a newer Godwit.
var ErrNewerDatabase = errors.New("store: database schema is newer than this version")

// DB wraps a mygo sqlite connection.
type DB struct{ *sqlite.DB }

// Open opens (creating if needed) the database at path and applies pending migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	raw, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		return nil, err
	}
	db := &DB{raw}
	if err := db.init(ctx, path); err != nil {
		raw.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) init(ctx context.Context, path string) error {
	rows, err := db.Query(ctx, "PRAGMA user_version")
	if err != nil {
		return err
	}
	cur := int(Int(rows.Values[0][0]))
	if cur > len(Migrations) {
		return fmt.Errorf("%w (have %d, know %d)", ErrNewerDatabase, cur, len(Migrations))
	}
	// Tests open a new database per case; FULL would fsync every migration.
	var pragmas []string
	if testing.Testing() {
		pragmas = append(pragmas, "PRAGMA synchronous = OFF")
	}
	// Only a new database takes auto_vacuum as set, and only before WAL
	// writes its header; an older one converts with VACUUM below.
	pragmas = append(pragmas, "PRAGMA auto_vacuum = INCREMENTAL", "PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL")
	for _, p := range pragmas {
		if _, err := db.Query(ctx, p); err != nil {
			return err
		}
	}
	if cur > 0 && cur < PiMessagesVersion {
		backup(ctx, db, path, cur)
	}
	for v := cur + 1; v <= len(Migrations); v++ {
		var stmts []sqlite.Statement
		for _, s := range Migrations[v-1] {
			stmts = append(stmts, sqlite.Statement{SQL: s})
		}
		stmts = append(stmts, sqlite.Statement{SQL: fmt.Sprintf("PRAGMA user_version = %d", v)})
		if _, err := db.Transaction(ctx, stmts); err != nil {
			return fmt.Errorf("store: migration %d: %w", v, err)
		}
	}
	dropBackups(path, cur)
	return db.convertAutoVacuum(ctx)
}

// backup copies the database at path, at schema version cur, to
// path.bak-v<cur> before a migration older versions cannot open; it is
// best effort (no room: no backup) and dropped a version later (dropBackups).
func backup(ctx context.Context, db *DB, path string, cur int) {
	// The copy is the main file alone: the WAL is folded in and emptied first.
	if _, err := db.Query(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		slog.Default().Warn("could not back the database up", "err", err)
		return
	}
	dst := fmt.Sprintf("%s.bak-v%d", path, cur)
	if _, err := os.Stat(dst); err == nil {
		return // an earlier attempt's: kept as it is
	}
	if err := copyFile(path, dst); err != nil {
		os.Remove(dst) // what this attempt wrote of it
		slog.Default().Warn("could not back the database up", "err", err)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// dropBackups removes path.bak-v<n> backups once a start begins past n+1
// (cur, the version before migrating): a version later than the migration
// they were taken for has run. Not by the version just migrated to: one
// start that migrates past n+1 would drop the backup it just took.
func dropBackups(path string, cur int) {
	olds, _ := filepath.Glob(path + ".bak-v*")
	for _, f := range olds {
		var n int
		if _, err := fmt.Sscanf(strings.TrimPrefix(f, path+".bak-v"), "%d", &n); err == nil && cur > n+1 {
			os.Remove(f)
		}
	}
}

// convertAutoVacuum turns incremental auto_vacuum on for a database made
// before it was set: a VACUUM, which needs up to twice the file's size free;
// one that fails (no room, say) leaves the database as it was until the
// next open tries again.
func (db *DB) convertAutoVacuum(ctx context.Context) error {
	rows, err := db.Query(ctx, "PRAGMA auto_vacuum")
	if err != nil {
		return err
	}
	if Int(rows.Values[0][0]) == autoVacuumIncremental {
		return nil
	}
	if _, err := db.Query(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		return err
	}
	if _, err := db.Execute(ctx, "VACUUM"); err != nil {
		slog.Default().Warn("could not convert the database to incremental vacuum", "err", err)
	}
	return nil
}

const autoVacuumIncremental = 2

// Reclaim thresholds, as Codex's: free pages are given back once they are at
// least reclaimMin and reclaimShare of the file, down to what keeps
// reclaimKeep (or reclaimKeepShare) free for reuse, reclaimBatch pages a time.
const (
	reclaimMin       = 64 << 20
	reclaimShare     = 0.25
	reclaimKeep      = 16 << 20
	reclaimKeepShare = 0.10
	reclaimBatch     = 64
)

// Reclaim gives free pages back to the file system, a batch at a time, when
// enough of the file is free; it stops at ctx's end.
func (db *DB) Reclaim(ctx context.Context) error {
	rows, err := db.Query(ctx, "SELECT (SELECT page_size FROM pragma_page_size), (SELECT freelist_count FROM pragma_freelist_count), (SELECT page_count FROM pragma_page_count)")
	if err != nil {
		return err
	}
	size, free, total := Int(rows.Values[0][0]), Int(rows.Values[0][1]), Int(rows.Values[0][2])
	if size == 0 || total == 0 || free*size < reclaimMin || float64(free)/float64(total) < reclaimShare {
		return nil
	}
	keep := max(reclaimKeep/size, int64(float64(total)*reclaimKeepShare))
	for n := free - keep; n > 0 && ctx.Err() == nil; n -= reclaimBatch {
		if _, err := db.Query(ctx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", min(n, reclaimBatch))); err != nil {
			return err
		}
	}
	_, err = db.Query(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
	return err
}
