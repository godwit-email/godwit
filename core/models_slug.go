package core

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/store"
)

// slugify lowercases name, keeps ASCII letters and digits and turns every
// other run into one "-"; "service" when nothing is left.
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	if b.Len() == 0 {
		return "service"
	}
	return b.String()
}

type slugRow struct {
	id         int64
	name, slug string
}

func (s *Service) slugRows(ctx context.Context) ([]slugRow, error) {
	rows, err := s.db.Query(ctx, "SELECT id, name, slug FROM model_services ORDER BY id")
	if err != nil {
		return nil, err
	}
	out := make([]slugRow, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, slugRow{store.Int(r[0]), store.Str(r[1]), store.Str(r[2])})
	}
	return out, nil
}

// aliases returns every service's old names (model_service_aliases).
func (s *Service) aliases(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, "SELECT name FROM model_service_aliases")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, r := range rows.Values {
		out[store.Str(r[0])] = true
	}
	return out, nil
}

// pickSlug is slugify(name), suffixed -2, -3… while it equals any service's
// slug, another service's name or an old name.
func pickSlug(name string, exceptID int64, rows []slugRow, aliases map[string]bool) string {
	base := slugify(name)
	taken := func(c string) bool {
		for _, r := range rows {
			if r.slug == c || r.id != exceptID && r.name == c {
				return true
			}
		}
		return aliases[c]
	}
	c := base
	for n := 2; taken(c); n++ {
		c = base + "-" + strconv.Itoa(n)
	}
	return c
}

func (s *Service) newSlug(ctx context.Context, name string, exceptID int64) (string, error) {
	rows, err := s.slugRows(ctx)
	if err != nil {
		return "", err
	}
	aliases, err := s.aliases(ctx)
	if err != nil {
		return "", err
	}
	return pickSlug(name, exceptID, rows, aliases), nil
}

// backfillSlugs gives the services an older Godwit stored a slug, and rewrites the
// "name/model" references to them (DefaultModel, runs, conversations) to
// "slug/model", all in one transaction. A name that already is a slug, and is
// no other service's name, is kept, so its references stay as they are.
func (s *Service) backfillSlugs(ctx context.Context) error {
	rows, err := s.slugRows(ctx)
	if err != nil {
		return err
	}
	aliases, err := s.aliases(ctx)
	if err != nil {
		return err
	}
	var stmts []sqlite.Statement
	renamed := map[string]string{} // name → slug, where they differ
	for i, r := range rows {
		if r.slug != "" {
			continue
		}
		slug := pickSlug(r.name, r.id, rows, aliases) // the name itself when it is a free slug
		rows[i].slug = slug
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE model_services SET slug = ? WHERE id = ?", Args: []any{slug, r.id}})
		if slug != r.name {
			renamed[r.name] = slug
		}
	}
	if len(stmts) == 0 {
		return nil
	}
	if len(renamed) > 0 {
		st, err := s.db.Query(ctx, "SELECT value FROM settings WHERE key = 'app'")
		if err != nil {
			return err
		}
		if len(st.Values) > 0 {
			var fields map[string]json.RawMessage // every field kept, unknown ones included
			if err := json.Unmarshal([]byte(store.Str(st.Values[0][0])), &fields); err != nil {
				return err
			}
			var ref string
			if json.Unmarshal(fields["DefaultModel"], &ref) == nil {
				if svc, model, ok := strings.Cut(ref, "/"); ok && renamed[svc] != "" {
					fields["DefaultModel"], _ = json.Marshal(renamed[svc] + "/" + model)
					b, err := json.Marshal(fields)
					if err != nil {
						return err
					}
					stmts = append(stmts, sqlite.Statement{SQL: "UPDATE settings SET value = ? WHERE key = 'app'", Args: []any{string(b)}})
				}
			}
		}
	}
	// A slug is never another service's name, so one rewrite never feeds another.
	for name, slug := range renamed {
		prefix := name + "/"
		n := utf8.RuneCountInString(prefix) // substr counts characters
		for _, table := range []string{"runs", "conversations"} {
			stmts = append(stmts, sqlite.Statement{SQL: "UPDATE " + table + " SET model = ? || substr(model, ?) WHERE substr(model, 1, ?) = ?",
				Args: []any{slug + "/", n + 1, n, prefix}})
		}
	}
	_, err = s.db.Transaction(ctx, stmts)
	return err
}

// captureAliases records, once (settings key model_aliases_v1), every
// service's name as its alias: skill files may say "name/model", and names no
// longer resolve. A name that is a slug or already an alias is skipped. It
// runs after backfillSlugs, in its own transaction, until it has run.
func (s *Service) captureAliases(ctx context.Context) error {
	done, err := s.db.Query(ctx, "SELECT 1 FROM settings WHERE key = 'model_aliases_v1'")
	if err != nil || len(done.Values) > 0 {
		return err
	}
	_, err = s.db.Transaction(ctx, []sqlite.Statement{
		{SQL: `INSERT OR IGNORE INTO model_service_aliases(name, service_id) SELECT name, id FROM model_services m
			WHERE NOT EXISTS (SELECT 1 FROM model_services o WHERE o.slug = m.name)`},
		{SQL: "INSERT INTO settings(key, value) VALUES ('model_aliases_v1', 'done')"},
	})
	return err
}
