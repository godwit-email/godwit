// Package store is Godwit's SQLite storage layer.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	if err := db.init(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) init(ctx context.Context) error {
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
	pragmas = append(pragmas, "PRAGMA foreign_keys = ON", "PRAGMA journal_mode = WAL")
	for _, p := range pragmas {
		if _, err := db.Query(ctx, p); err != nil {
			return err
		}
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
	return nil
}
