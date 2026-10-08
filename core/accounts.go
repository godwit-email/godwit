package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwitmail/godwit/core/conn"
	"github.com/godwitmail/godwit/core/store"
	"github.com/zalando/go-keyring"
)

const (
	keyringService = "godwit"
	defaultSyncLim = 2000
)

var keyringSet = keyring.Set // replaced in tests

// keyringUser is the account's keychain entry name (service "godwit"). The
// install id keeps data directories from sharing entries: account ids restart at
// 1 in every database.
func (s *Service) keyringUser(id int64) string {
	return s.installID + "/account:" + strconv.FormatInt(id, 10)
}

// loadInstallID returns the data directory's random id, creating it on first use.
func loadInstallID(ctx context.Context, db *store.DB) (string, error) {
	b := make([]byte, 16)
	rand.Read(b)
	if _, err := db.Execute(ctx, "INSERT OR IGNORE INTO meta(key, value) VALUES ('install_id', ?)", hex.EncodeToString(b)); err != nil {
		return "", err
	}
	rows, err := db.Query(ctx, "SELECT value FROM meta WHERE key = 'install_id'")
	if err != nil {
		return "", err
	}
	return store.Str(rows.Values[0][0]), nil
}

func isLoopback(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

func validSecurity(s Security) bool {
	return s == SecurityTLS || s == SecurityStartTLS || s == SecurityNone
}

func (in AccountInput) validate() error {
	if _, err := mail.ParseAddress(in.Email); err != nil {
		return fmt.Errorf("invalid email address %q", in.Email)
	}
	if in.Password == "" {
		return errors.New("password is required")
	}
	for _, e := range []struct {
		name string
		host string
		port int
		sec  Security
	}{{"IMAP", in.IMAPHost, in.IMAPPort, in.IMAPSecurity}, {"SMTP", in.SMTPHost, in.SMTPPort, in.SMTPSecurity}} {
		if e.host == "" {
			return fmt.Errorf("%s host is required", e.name)
		}
		if e.port < 1 || e.port > 65535 {
			return fmt.Errorf("%s port %d is out of range", e.name, e.port)
		}
		if !validSecurity(e.sec) {
			return fmt.Errorf("%s security %q is invalid", e.name, e.sec)
		}
		if e.sec == SecurityNone && !isLoopback(e.host) {
			return fmt.Errorf("%s: plaintext connections are only allowed to localhost", e.name)
		}
	}
	return nil
}

func (in AccountInput) username() string {
	if in.Username != "" {
		return in.Username
	}
	return in.Email
}

// TestAccount validates in and checks that both servers accept the credentials.
func (s *Service) TestAccount(ctx context.Context, in AccountInput) error {
	if err := in.validate(); err != nil {
		return err
	}
	ic, err := conn.DialIMAP(ctx, conn.Endpoint{Host: in.IMAPHost, Port: in.IMAPPort, Security: in.IMAPSecurity, Username: in.username(), Password: in.Password})
	if err != nil {
		return err
	}
	ic.Close()
	sc, err := conn.DialSMTP(ctx, conn.Endpoint{Host: in.SMTPHost, Port: in.SMTPPort, Security: in.SMTPSecurity, Username: in.username(), Password: in.Password})
	if err != nil {
		return err
	}
	sc.Close()
	return nil
}

// AddAccount tests in, stores the account and saves the password in the keychain.
func (s *Service) AddAccount(ctx context.Context, in AccountInput) (Account, error) {
	if err := s.TestAccount(ctx, in); err != nil {
		return Account{}, err
	}
	if in.SyncLimit == 0 {
		in.SyncLimit = defaultSyncLim
	}
	rows, err := s.db.Query(ctx, "SELECT count(*) FROM accounts")
	if err != nil {
		return Account{}, err
	}
	isDefault := in.IsDefault || store.Int(rows.Values[0][0]) == 0
	var prevDefault int64
	if isDefault {
		prev, err := s.db.Query(ctx, "SELECT id FROM accounts WHERE is_default = 1 LIMIT 1")
		if err != nil {
			return Account{}, err
		}
		if len(prev.Values) > 0 {
			prevDefault = store.Int(prev.Values[0][0])
		}
	}

	var stmts []sqlite.Statement
	if isDefault {
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE accounts SET is_default = 0"})
	}
	stmts = append(stmts, sqlite.Statement{
		SQL: `INSERT INTO accounts(email, name, alias, signature, is_default, sync_limit, auth_type, status, username,
			imap_host, imap_port, imap_security, smtp_host, smtp_port, smtp_security, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 'password', 'ok', ?, ?, ?, ?, ?, ?, ?, ?)`,
		Args: []any{in.Email, in.Name, in.Alias, in.Signature, b2i(isDefault), in.SyncLimit, in.username(),
			in.IMAPHost, in.IMAPPort, string(in.IMAPSecurity), in.SMTPHost, in.SMTPPort, string(in.SMTPSecurity), time.Now().Unix()},
	})
	res, err := s.db.Transaction(ctx, stmts)
	if err != nil {
		return Account{}, err
	}
	id := res[len(res)-1].LastInsertID
	if err := keyringSet(keyringService, s.keyringUser(id), in.Password); err != nil {
		// Compensate on a fresh context: the caller's may already be cancelled, and a
		// row without credentials must not survive.
		undo := []sqlite.Statement{{SQL: "DELETE FROM accounts WHERE id = ?", Args: []any{id}}}
		if prevDefault != 0 {
			undo = append(undo, sqlite.Statement{SQL: "UPDATE accounts SET is_default = 1 WHERE id = ?", Args: []any{prevDefault}})
		}
		uctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, derr := s.db.Transaction(uctx, undo); derr != nil {
			err = errors.Join(err, derr)
		}
		return Account{}, fmt.Errorf("save password to keychain: %w", err)
	}
	s.startWorker(id)
	return s.account(ctx, id)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

const accountCols = `id, email, name, alias, signature, username, is_default, sync_limit, auth_type, status,
	imap_host, imap_port, imap_security, smtp_host, smtp_port, smtp_security`

func (s *Service) account(ctx context.Context, id int64) (Account, error) {
	rows, err := s.db.Query(ctx, "SELECT "+accountCols+" FROM accounts WHERE id = ?", id)
	if err != nil {
		return Account{}, err
	}
	if len(rows.Values) == 0 {
		return Account{}, fmt.Errorf("account %d not found", id)
	}
	return scanAccount(rows.Values[0]), nil
}

func scanAccount(r []any) Account {
	return Account{
		ID: store.Int(r[0]), Email: store.Str(r[1]), Name: store.Str(r[2]), Alias: store.Str(r[3]),
		Signature: store.Str(r[4]), Username: store.Str(r[5]), IsDefault: store.Int(r[6]) == 1,
		SyncLimit: int(store.Int(r[7])), AuthType: store.Str(r[8]), Status: store.Str(r[9]),
		IMAPHost: store.Str(r[10]), IMAPPort: int(store.Int(r[11])), IMAPSecurity: Security(store.Str(r[12])),
		SMTPHost: store.Str(r[13]), SMTPPort: int(store.Int(r[14])), SMTPSecurity: Security(store.Str(r[15])),
	}
}

// ListAccounts returns all accounts in creation order.
func (s *Service) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.Query(ctx, "SELECT "+accountCols+" FROM accounts ORDER BY id")
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, scanAccount(r))
	}
	return out, nil
}

// RemoveAccount deletes the account, its keychain entry and its cache directory.
func (s *Service) RemoveAccount(ctx context.Context, id int64) error {
	s.stopWorker(id)
	if _, err := s.db.Execute(ctx, "DELETE FROM accounts WHERE id = ?", id); err != nil {
		return err
	}
	if err := keyring.Delete(keyringService, s.keyringUser(id)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return os.RemoveAll(filepath.Join(s.dataDir, "cache", strconv.FormatInt(id, 10)))
}
