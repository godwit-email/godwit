package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/store"
	"github.com/zalando/go-keyring"
)

const (
	keyringService = "godwit"
	defaultSyncLim = 2000
)

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
	if in.OAuthFlowID != "" {
		if _, err := oauthConfig(in.Provider); err != nil {
			return err
		}
	} else if in.Password == "" {
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

// TestAccount runs the connection check; a failure is a *CheckError.
func (s *Service) TestAccount(ctx context.Context, in AccountInput) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	if r, err := s.check(ctx, in); err != nil {
		return &CheckError{Report: r, err: err}
	}
	return nil
}

// flowGrant returns the grant of in's finished OAuth flow without using it up.
// Its signed-in address must be in.Email, ignoring case.
func (s *Service) flowGrant(in AccountInput) (auth.Grant, error) {
	s.oauthMu.Lock()
	g, ok := s.grants[in.OAuthFlowID]
	s.oauthMu.Unlock()
	if !ok {
		return auth.Grant{}, ErrUnknownFlow
	}
	if !strings.EqualFold(g.Email, strings.TrimSpace(in.Email)) {
		return auth.Grant{}, ErrWrongAccount
	}
	return g, nil
}

// AddAccount tests in, stores the account and saves the password in the keychain.
// With OAuthFlowID it uses up the flow's grant and saves its refresh token instead.
func (s *Service) AddAccount(ctx context.Context, in AccountInput) (Account, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Account{}, err
	}
	defer end()
	if err := s.KeychainStatus(ctx); err != nil {
		return Account{}, err
	}
	authType, secret := "password", in.Password
	var g auth.Grant
	if in.OAuthFlowID != "" {
		if g, err = s.flowGrant(in); err != nil {
			return Account{}, err
		}
		if g.RefreshToken == "" {
			return Account{}, errors.New("the provider granted no refresh token")
		}
	}
	if err := s.TestAccount(ctx, in); err != nil {
		return Account{}, err
	}
	if in.OAuthFlowID != "" {
		// Peeked for the check; taken now, so one grant adds one account.
		if _, ok := s.takeGrant(in.OAuthFlowID); !ok {
			return Account{}, ErrUnknownFlow
		}
		authType, secret, in.Username = "oauth", g.RefreshToken, g.Email
	}
	if in.SyncLimit == 0 {
		in.SyncLimit = defaultSyncLim
	}
	if in.Provider == "" {
		in.Provider = "other"
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
			imap_host, imap_port, imap_security, smtp_host, smtp_port, smtp_security, created_at, provider)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'ok', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		Args: []any{in.Email, in.Name, in.Alias, in.Signature, b2i(isDefault), in.SyncLimit, authType, in.username(),
			in.IMAPHost, in.IMAPPort, string(in.IMAPSecurity), in.SMTPHost, in.SMTPPort, string(in.SMTPSecurity), time.Now().Unix(), in.Provider},
	})
	res, err := s.db.Transaction(ctx, stmts)
	if err != nil {
		return Account{}, err
	}
	id := res[len(res)-1].LastInsertID
	if err := keychainSet(s.keyringUser(id), secret); err != nil {
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
		return Account{}, fmt.Errorf("save credentials to keychain: %w", err)
	}
	if g.AccessToken != "" {
		s.tokMu.Lock()
		if s.tokens == nil {
			s.tokens = map[int64]oauthToken{}
		}
		s.tokens[id] = oauthToken{g.AccessToken, g.Expiry}
		s.tokMu.Unlock()
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
	imap_host, imap_port, imap_security, smtp_host, smtp_port, smtp_security, smtp_reauth, provider, status_detail`

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
	a := Account{
		ID: store.Int(r[0]), Email: store.Str(r[1]), Name: store.Str(r[2]), Alias: store.Str(r[3]),
		Signature: store.Str(r[4]), Username: store.Str(r[5]), IsDefault: store.Int(r[6]) == 1,
		SyncLimit: int(store.Int(r[7])), AuthType: store.Str(r[8]), Status: store.Str(r[9]),
		IMAPHost: store.Str(r[10]), IMAPPort: int(store.Int(r[11])), IMAPSecurity: Security(store.Str(r[12])),
		SMTPHost: store.Str(r[13]), SMTPPort: int(store.Int(r[14])), SMTPSecurity: Security(store.Str(r[15])),
		Provider: store.Str(r[17]), StatusDetail: store.Str(r[18]),
	}
	// SMTP refuses the password: the user is asked for it the same way. status and
	// status_detail are IMAP's and the keychain's, and they take precedence.
	if store.Int(r[16]) != 0 && a.Status == "ok" {
		a.Status, a.StatusDetail = "reauth_required", "auth_rejected"
	}
	return a
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
// It first cancels the account's explicit calls and worker and waits for them.
func (s *Service) RemoveAccount(ctx context.Context, id int64) error {
	s.callsMu.Lock()
	if s.callsClosed {
		s.callsMu.Unlock()
		return ErrClosed
	}
	if s.removing == nil {
		s.removing = map[int64]bool{}
	}
	s.removing[id] = true
	s.callsMu.Unlock()
	defer func() {
		s.callsMu.Lock()
		delete(s.removing, id)
		s.callsMu.Unlock()
	}()
	s.stopCalls(func(a int64) bool { return a == id })
	s.stopWorker(id)
	if _, err := s.db.Execute(ctx, "DELETE FROM accounts WHERE id = ?", id); err != nil {
		return err
	}
	s.tokMu.Lock()
	delete(s.tokens, id)
	s.tokMu.Unlock()
	if err := keychainDelete(s.keyringUser(id)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return os.RemoveAll(filepath.Join(s.dataDir, "cache", strconv.FormatInt(id, 10)))
}

// DetectProvider finds the provider for an email address; DNS gets 5 seconds in total.
func (s *Service) DetectProvider(ctx context.Context, email string) (auth.Provider, auth.Source, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return auth.Detect(ctx, net.DefaultResolver, email)
}

func (s *Service) ListProviders() []auth.Provider { return auth.ListProviders() }
