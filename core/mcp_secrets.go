package core

import (
	"slices"

	"context"
	"errors"
	"fmt"
	"github.com/egoist/mygo/plugins/sqlite"
	"log/slog"

	"github.com/zalando/go-keyring"
)

// MCP env and header values: keychain entries and the secret set.

// Env and header values live in the keychain as <install-id>/mcp:<id>:<kind>:<key>,
// each its own owner in the secret set.
func (s *Service) mcpSecretUser(id int64, kind, key string) string {
	return s.installID + "/" + mcpSecretOwner(id, kind, key)
}

func mcpSecretOwner(id int64, kind, key string) string {
	return fmt.Sprintf("mcp:%d:%s:%s", id, kind, key)
}

// saveMCPSecrets stores the non-empty values. A new value is redacted from
// before it is written; the old one stays redacted (secretSet only grows).
func (s *Service) saveMCPSecrets(id int64, kind string, values map[string]string) error {
	for k, v := range values {
		if v == "" {
			continue
		}
		s.secrets.Add(v)
		if err := keychainSet(s.mcpSecretUser(id, kind, k), v); err != nil {
			return fmt.Errorf("save MCP %s %s: %w", kind, k, err)
		}
	}
	return nil
}

// dropMCPSecrets deletes the keys' entries (missing ones are fine) and
// returns the keys it could not delete. Their values stay redacted either way.
func (s *Service) dropMCPSecrets(id int64, kind string, keys []string) (failed []string, err error) {
	var errs []error
	for _, k := range keys {
		if err := keychainDelete(s.mcpSecretUser(id, kind, k)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			errs = append(errs, fmt.Errorf("delete MCP %s %s: %w", kind, k, err))
			failed = append(failed, k)
			continue
		}
	}
	return failed, errors.Join(errs...)
}

// listMCPKeys records the server's env and header keys that may have a
// keychain entry (the plugin secret_keys pattern: listed before a write,
// unlisted only after a delete).
func listMCPKeys(id int64, env, headers []string) sqlite.Statement {
	sorted := func(l []string) string { return mcpList(slices.Compact(slices.Sorted(slices.Values(l)))) }
	return sqlite.Statement{SQL: "UPDATE mcp_servers SET env_keys = ?, header_keys = ? WHERE id = ?",
		Args: []any{sorted(env), sorted(headers), id}}
}

// mcpValues reads the keys' values and puts them in the secret set.
func (s *Service) mcpValues(id int64, kind string, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		v, err := keychainGet(s.mcpSecretUser(id, kind, k))
		if err != nil {
			return nil, fmt.Errorf("read MCP %s %s: %w", kind, k, err)
		}
		s.secrets.Add(v)
		out[k] = v
	}
	return out, nil
}

// loadMCPSecrets puts every server's values in the secret set at startup;
// one that cannot be read is read again when its server connects.
func (s *Service) loadMCPSecrets(ctx context.Context) error {
	list, err := s.mcpRows(ctx, 0)
	if err != nil {
		return err
	}
	for _, srv := range list {
		_, err1 := s.mcpValues(srv.ID, "env", srv.EnvKeys)
		_, err2 := s.mcpValues(srv.ID, "header", srv.HeaderKeys)
		if err := errors.Join(err1, err2); err != nil {
			slog.Default().Warn("could not load an MCP server's secrets", "server", srv.ID, "err", err)
		}
	}
	return nil
}
