package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/ownerlock"
)

// Leave committed data in a WAL, as after an unclean shutdown.
func syncFixture(t *testing.T, dir, value string) {
	t.Helper()
	seed := filepath.Join(t.TempDir(), "godwit.db")
	db, err := sqlite.Open(t.Context(), seed, sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, sql := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0"} {
		if _, err := db.Query(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	var stmts []sqlite.Statement
	for _, migration := range store.Migrations[:3] {
		for _, sql := range migration {
			stmts = append(stmts, sqlite.Statement{SQL: sql})
		}
	}
	stmts = append(stmts,
		sqlite.Statement{SQL: "PRAGMA user_version=3"},
		sqlite.Statement{SQL: "INSERT INTO meta VALUES ('install_id', ?)", Args: []any{value}},
		sqlite.Statement{SQL: "INSERT INTO accounts (id, email) VALUES (1, 'user@example.com')"},
		sqlite.Statement{SQL: "INSERT INTO folders (id, account_id, name) VALUES (1, 1, 'INBOX')"},
		sqlite.Statement{SQL: "INSERT INTO messages (id, account_id, folder_id) VALUES (1, 1, 1)"},
		sqlite.Statement{SQL: "INSERT INTO attachments (message_id, cache_path) VALUES (1, ?)", Args: []any{filepath.Join(dir, "cache", "attachment")}},
		sqlite.Statement{SQL: "INSERT INTO drafts (id, account_id, subject) VALUES (1, 1, ?)", Args: []any{value}},
		sqlite.Statement{SQL: "INSERT INTO draft_attachments (draft_id, path) VALUES (1, ?)", Args: []any{filepath.Join(dir, "drafts", "1", "attachment")}},
		sqlite.Statement{SQL: "INSERT INTO outbox (account_id, raw_path, state) VALUES (1, ?, 'queued')", Args: []any{filepath.Join(dir, "outbox", "mail.eml")}},
	)
	if _, err := db.Transaction(t.Context(), stmts); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		b, err := os.ReadFile(seed + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "godwit.db")+suffix, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDevSyncDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("USERPROFILE", home)
	t.Setenv("MYGO_ENV", "production")
	src, err := defaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYGO_ENV", "")
	dst := src + "-dev"
	syncFixture(t, src, "production")
	syncFixture(t, dst, "old dev")
	before := map[string][]byte{}
	for _, suffix := range []string{"", "-wal"} {
		before[suffix], err = os.ReadFile(filepath.Join(src, "godwit.db") + suffix)
		if err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"dev-sync-db"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("sync exit %d: %s", code, &stderr)
	}
	for suffix, want := range before {
		got, err := os.ReadFile(filepath.Join(src, "godwit.db") + suffix)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("production database %q changed: %v", suffix, err)
		}
	}
	db, err := sqlite.Open(t.Context(), filepath.Join(dst, "godwit.db"), sqlite.OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tc := range []struct {
		sql  string
		want any
	}{
		{"SELECT subject FROM drafts", "production"},
		{"SELECT count(*) FROM meta WHERE key='install_id'", int64(0)},
		{"SELECT cache_path FROM attachments", nil},
		{"SELECT path FROM draft_attachments", nil},
		{"SELECT raw_path FROM outbox", nil},
		{"PRAGMA user_version", int64(len(store.Migrations))},
	} {
		rows, err := db.Query(t.Context(), tc.sql)
		if err != nil || len(rows.Values) != 1 || rows.Values[0][0] != tc.want {
			t.Errorf("%s = %v, %v; want %v", tc.sql, rows.Values, err, tc.want)
		}
	}
}

func TestSyncDatabaseRefusesBusyOrInvalidSource(t *testing.T) {
	for _, failure := range []string{"production busy", "dev busy", "missing", "corrupt", "shared file", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			src, dst := filepath.Join(t.TempDir(), "prod"), filepath.Join(t.TempDir(), "dev")
			syncFixture(t, src, "production")
			syncFixture(t, dst, "keep dev")
			ctx := t.Context()
			switch failure {
			case "production busy", "dev busy":
				busy := src
				if failure == "dev busy" {
					busy = dst
				}
				release, err := ownerlock.Acquire(busy)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "missing":
				if err := os.RemoveAll(src); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.Remove(filepath.Join(src, "godwit.db-wal")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(src, "godwit.db"), []byte("invalid SQLite"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "shared file":
				if err := os.Remove(filepath.Join(dst, "godwit.db")); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(filepath.Join(src, "godwit.db"), filepath.Join(dst, "godwit.db")); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before := map[string][]byte{}
			for _, suffix := range []string{"", "-wal"} {
				b, err := os.ReadFile(filepath.Join(dst, "godwit.db") + suffix)
				if err != nil {
					t.Fatal(err)
				}
				before[suffix] = b
			}
			if err := core.SyncDevDatabase(ctx, src, dst); err == nil {
				t.Fatal("sync succeeded")
			}
			for suffix, want := range before {
				got, err := os.ReadFile(filepath.Join(dst, "godwit.db") + suffix)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("failed sync changed dev %q: %v", suffix, err)
				}
			}
		})
	}
}

func TestSyncDatabaseFreshDev(t *testing.T) {
	src, dst := filepath.Join(t.TempDir(), "prod"), filepath.Join(t.TempDir(), "dev")
	syncFixture(t, src, "production")
	// A clean shutdown leaves no WAL to copy.
	db, err := sqlite.Open(t.Context(), filepath.Join(src, "godwit.db"), sqlite.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Query(t.Context(), "PRAGMA journal_mode=DELETE"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src, "godwit.db-wal")); !os.IsNotExist(err) {
		t.Fatalf("fixture still has a WAL: %v", err)
	}
	if err := core.SyncDevDatabase(t.Context(), src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "godwit.db")); err != nil {
		t.Fatal(err)
	}
	release, err := ownerlock.Acquire(dst)
	if err != nil {
		t.Fatalf("sync did not release the dev lock: %v", err)
	}
	release()
}

func TestDevSyncDBUsage(t *testing.T) {
	for _, tc := range []struct {
		env  string
		args []string
		code int
	}{
		{"production", nil, 2},
		{"", []string{"unexpected"}, 2},
		{"", []string{"--data-dir", "unused"}, 2},
		{"", []string{"--help"}, 0},
	} {
		t.Setenv("MYGO_ENV", tc.env)
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), append([]string{"dev-sync-db"}, tc.args...), nil, &stdout, &stderr); code != tc.code {
			t.Errorf("env %q, args %v: exit %d, want %d: %s", tc.env, tc.args, code, tc.code, &stderr)
		}
	}
}
