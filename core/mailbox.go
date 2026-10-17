package core

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
)

// Folder roles in Godwit: sent is where the outbox files sent mail; trash, junk
// and archive are where Delete and MoveToRole move mail; drafts only picks the
// server folder the local drafts view stands in for (drafts stay local, nothing
// is appended there). ListFolders shows each role on the folder holding it.

// PurgeDays are the purge periods offered: never, a day, a week and a month.
var PurgeDays = []int{0, 1, 7, 30}

// Purge is an account's junk or trash purge. Days 0 is never. Blocked is set
// while the server lacks UIDPLUS, so the purge is skipped.
type Purge struct {
	Days    int
	Blocked bool
}

// SetRoleFolder gives role (drafts, sent, junk, trash or archive) to the folder;
// 0 leaves the account without a folder of the role. The choice survives sync;
// once the folder is gone the role follows detection again. INBOX takes no role,
// and a folder holding another role cannot take this one (junk and trash would
// purge its mail): that role moves elsewhere first.
func (s *Service) SetRoleFolder(ctx context.Context, accountID int64, role string, folderID int64) error {
	if !slices.Contains(mailsync.Roles, role) {
		return fmt.Errorf("invalid role %q", role)
	}
	s.editMu.Lock()
	defer s.editMu.Unlock()
	stmts := []sqlite.Statement{{SQL: "UPDATE folders SET role_override = '' WHERE account_id = ? AND role_override = ?", Args: []any{accountID, role}}}
	if folderID == 0 {
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE folders SET role_override = 'none' WHERE account_id = ? AND role IN (?, ?) AND role_override = ''",
			Args: []any{accountID, role, mailsync.AltRole(role)}})
	} else {
		rows, err := s.db.Query(ctx, "SELECT role FROM folders WHERE id = ? AND account_id = ?", folderID, accountID)
		if err != nil {
			return err
		}
		if len(rows.Values) == 0 {
			return fmt.Errorf("folder %d not found in account %d", folderID, accountID)
		}
		if store.Str(rows.Values[0][0]) == "inbox" {
			return errors.New("the inbox cannot take another role")
		}
		for _, other := range mailsync.Roles { // a folder holds one role: junk or trash never purges sent mail
			if other == role {
				continue
			}
			id, _, err := mailsync.RoleFolder(ctx, s.db, accountID, other)
			if err != nil {
				return err
			}
			if id == folderID {
				return fmt.Errorf("the %s folder cannot be the %s folder", other, role)
			}
		}
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE folders SET role_override = ? WHERE id = ?", Args: []any{role, folderID}})
	}
	_, err := s.db.Transaction(ctx, stmts)
	return err
}

// Purges returns the account's junk and trash purges by role.
func (s *Service) Purges(ctx context.Context, accountID int64) (map[string]Purge, error) {
	rows, err := s.db.Query(ctx, "SELECT role, days, blocked FROM purge_policies WHERE account_id = ?", accountID)
	if err != nil {
		return nil, err
	}
	out := map[string]Purge{}
	for _, role := range mailsync.PurgeRoles {
		out[role] = Purge{}
	}
	for _, r := range rows.Values {
		out[store.Str(r[0])] = Purge{Days: int(store.Int(r[1])), Blocked: store.Int(r[2]) != 0}
	}
	return out, nil
}

// SetPurge sets how many days mail stays in the account's junk or trash folder
// before it is expunged on the server; 0 is never. Any change restarts the
// clock: only mail that stays that long from now on is purged.
func (s *Service) SetPurge(ctx context.Context, accountID int64, role string, days int) error {
	if !slices.Contains(mailsync.PurgeRoles, role) {
		return fmt.Errorf("invalid purge role %q", role)
	}
	if !slices.Contains(PurgeDays, days) {
		return fmt.Errorf("invalid purge period %d", days)
	}
	folder, _, err := mailsync.RoleFolder(ctx, s.db, accountID, role)
	if err != nil {
		return err
	}
	var f any
	if folder != 0 {
		f = folder
	}
	_, err = s.db.Execute(ctx, `INSERT INTO purge_policies(account_id, role, days, since, folder_id) VALUES (?, ?, ?, unixepoch(), ?)
		ON CONFLICT(account_id, role) DO UPDATE SET days = excluded.days, since = excluded.since, folder_id = excluded.folder_id`,
		accountID, role, days, f)
	return err
}
