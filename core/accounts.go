package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/conn"
	"github.com/godwit-email/godwit/core/events"
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

// oauthClientUser is the keychain entry of the account's own OAuth client secret.
func (s *Service) oauthClientUser(id int64) string { return s.keyringUser(id) + "/oauth-client" }

// deleteCredentials removes the account's keychain entries; missing ones are fine.
func (s *Service) deleteCredentials(id int64) error {
	var errs []error
	for _, u := range []string{s.keyringUser(id), s.oauthClientUser(id)} {
		if err := keychainDelete(u); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
	if r, err := s.check(ctx, in, ""); err != nil {
		return &CheckError{Report: r, err: err}
	}
	return nil
}

// flowGrant returns the grant of in's finished OAuth flow without using it up.
// Its signed-in address must be in.Email (when set), ignoring case, and
// in.Provider, if set, the provider the flow began with.
func (s *Service) flowGrant(in AccountInput) (oauthGrant, error) {
	s.oauthMu.Lock()
	g, ok := s.grants[in.OAuthFlowID]
	s.oauthMu.Unlock()
	switch {
	case !ok:
		return oauthGrant{}, ErrUnknownFlow
	case in.Provider != "" && in.Provider != g.provider:
		return oauthGrant{}, ErrProviderMismatch
	case in.Email != "" && !strings.EqualFold(g.Email, strings.TrimSpace(in.Email)):
		return oauthGrant{}, ErrWrongAccount
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
	if err := checkRange(in.Sync); err != nil {
		return Account{}, err
	}
	if err := s.KeychainStatus(ctx); err != nil {
		return Account{}, err
	}
	authType, secret := "password", in.Password
	var g oauthGrant
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
		authType, secret, in.Username, in.Provider = "oauth", g.RefreshToken, g.Email, g.provider
		in.oauthClientID = g.client.ID
	}
	if rest, ok := strings.CutPrefix(in.Signature, SignatureHTMLPrefix); ok {
		if _, _, err := signatureAssets(0, rest, nil); err != nil { // its images, before anything is saved
			return Account{}, err
		}
	}
	sync := SyncRange{Limit: defaultSyncLim}
	if in.Sync != nil {
		sync = *in.Sync
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
		SQL: `INSERT INTO accounts(email, name, alias, is_default, sync_limit, sync_since_months, auth_type, status, username,
			imap_host, imap_port, imap_security, smtp_host, smtp_port, smtp_security, created_at, provider, oauth_client_id, position)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'ok', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(position) + 1, 0) FROM accounts))`,
		Args: []any{in.Email, in.Name, in.Alias, b2i(isDefault), sync.Limit, sync.Months, authType, in.username(),
			in.IMAPHost, in.IMAPPort, string(in.IMAPSecurity), in.SMTPHost, in.SMTPPort, string(in.SMTPSecurity), time.Now().Unix(), in.Provider, in.oauthClientID},
	})
	res, err := s.db.Transaction(ctx, stmts)
	if err != nil {
		return Account{}, err
	}
	id := res[len(res)-1].LastInsertID
	// undo takes the new account back out (a fresh context: the caller's may
	// already be cancelled, and a half-made account must not survive), the
	// previous default made the default again.
	undo := func(err error) error {
		uctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var sigID int64 // the signature made for it (in.Signature), if it got that far
		if rows, qerr := s.db.Query(uctx, "SELECT signature_id FROM accounts WHERE id = ?", id); qerr == nil && len(rows.Values) > 0 {
			sigID = store.Int(rows.Values[0][0])
		}
		stmts := []sqlite.Statement{{SQL: "DELETE FROM accounts WHERE id = ?", Args: []any{id}}}
		if prevDefault != 0 {
			stmts = append(stmts, sqlite.Statement{SQL: "UPDATE accounts SET is_default = 1 WHERE id = ?", Args: []any{prevDefault}})
		}
		if _, derr := s.db.Transaction(uctx, stmts); derr != nil {
			return errors.Join(err, derr)
		}
		if sigID != 0 { // apart: the account goes even if this cannot
			if _, derr := s.db.Transaction(uctx, []sqlite.Statement{
				{SQL: "DELETE FROM signatures WHERE id = ?", Args: []any{sigID}}, unusedSignatureImages}); derr != nil {
				err = errors.Join(err, derr)
			}
		}
		return err
	}
	if err := keychainSet(s.keyringUser(id), secret); err != nil {
		return Account{}, fmt.Errorf("save credentials to keychain: %w", undo(err))
	}
	if g.client.ID != "" {
		if err := keychainSet(s.oauthClientUser(id), g.client.Secret); err != nil {
			if kerr := s.deleteCredentials(id); kerr != nil {
				err = errors.Join(err, kerr)
			}
			return Account{}, fmt.Errorf("save the OAuth client secret to keychain: %w", undo(err))
		}
	}
	if in.Signature != "" { // its images checked above: only the database can fail it now
		if _, err := s.UpdateAccount(ctx, id, AccountPatch{Signature: &in.Signature}); err != nil {
			if kerr := s.deleteCredentials(id); kerr != nil {
				err = errors.Join(err, kerr)
			}
			return Account{}, fmt.Errorf("save the signature: %w", undo(err))
		}
	}
	if g.AccessToken != "" {
		s.cacheToken(id, g.Grant)
	}
	s.startWorker(id)
	return s.account(ctx, id)
}

// checkRange accepts a Limit of 1–100000, 1–120 Months, or neither (all).
func checkRange(r *SyncRange) error {
	if r != nil && (r.Limit < 0 || r.Limit > 100000 || r.Months < 0 || r.Months > 120 || r.Limit > 0 && r.Months > 0) {
		return fmt.Errorf("sync range %+v is not a limit of 1–100000, 1–120 months or all", *r)
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

const accountCols = `id, email, name, alias, COALESCE((SELECT html FROM signatures WHERE id = signature_id), ''), username, is_default, sync_limit, auth_type, status,
	imap_host, imap_port, imap_security, smtp_host, smtp_port, smtp_security, smtp_reauth, provider, status_detail,
	sync_since_months, (SELECT count(*) FROM folders WHERE account_id = accounts.id), signature_id, icon, ai_instructions, oauth_client_id`

// ErrAccountNotFound is returned for an account id that does not exist.
var ErrAccountNotFound = errors.New("account not found")

func (s *Service) account(ctx context.Context, id int64) (Account, error) {
	rows, err := s.db.Query(ctx, "SELECT "+accountCols+" FROM accounts WHERE id = ?", id)
	if err != nil {
		return Account{}, err
	}
	if len(rows.Values) == 0 {
		return Account{}, fmt.Errorf("account %d: %w", id, ErrAccountNotFound)
	}
	return scanAccount(rows.Values[0]), nil
}

func scanAccount(r []any) Account {
	a := Account{
		ID: store.Int(r[0]), Email: store.Str(r[1]), Name: store.Str(r[2]), Alias: store.Str(r[3]),
		Signature: store.Str(r[4]), Username: store.Str(r[5]), IsDefault: store.Int(r[6]) == 1,
		Sync: SyncRange{Limit: int(store.Int(r[7])), Months: int(store.Int(r[19]))}, Folders: int(store.Int(r[20])), AuthType: store.Str(r[8]), Status: store.Str(r[9]),
		IMAPHost: store.Str(r[10]), IMAPPort: int(store.Int(r[11])), IMAPSecurity: Security(store.Str(r[12])),
		SMTPHost: store.Str(r[13]), SMTPPort: int(store.Int(r[14])), SMTPSecurity: Security(store.Str(r[15])),
		Provider: store.Str(r[17]), StatusDetail: store.Str(r[18]), SignatureID: store.Int(r[21]), Icon: store.Str(r[22]),
		AIInstructions: store.Str(r[23]), OAuthClientID: store.Str(r[24]),
	}
	// SMTP refuses the password: the user is asked for it the same way. status and
	// status_detail are IMAP's and the keychain's, and they take precedence.
	if store.Int(r[16]) != 0 && a.Status == "ok" {
		a.Status, a.StatusDetail = "reauth_required", "auth_rejected"
	}
	return a
}

// ListAccounts returns all accounts in their order (ReorderAccounts), new ones last.
func (s *Service) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.Query(ctx, "SELECT "+accountCols+" FROM accounts ORDER BY position, id")
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(rows.Values))
	for _, r := range rows.Values {
		out = append(out, scanAccount(r))
	}
	return out, nil
}

// ReorderAccounts puts the accounts in the order of ids, which names every account once.
// An account added meanwhile fails the check or, added after it, goes last.
func (s *Service) ReorderAccounts(ctx context.Context, ids []int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT id FROM accounts ORDER BY id")
	if err != nil {
		return err
	}
	have := make([]int64, len(rows.Values))
	for i, r := range rows.Values {
		have[i] = store.Int(r[0])
	}
	want := slices.Clone(ids)
	slices.Sort(want)
	if !slices.Equal(have, want) {
		return errors.New("the new order must name every account once")
	}
	stmts := make([]sqlite.Statement, len(ids))
	for i, id := range ids {
		stmts[i] = sqlite.Statement{SQL: "UPDATE accounts SET position = ? WHERE id = ?", Args: []any{i, id}}
	}
	_, err = s.db.Transaction(ctx, stmts)
	return err
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
	s.credMu.Lock()
	delete(s.credVer, id)
	s.credMu.Unlock()
	if err := s.deleteCredentials(id); err != nil {
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

var errNotPassword = errors.New("the account does not sign in with a password")

// UpdatePassword checks password against the account's servers and, if both
// accept it, saves it and clears reauth_required and a paused outbox; the worker
// resumes and the outbox sends in its next round. A failed check is a
// *CheckError and leaves the keychain unchanged.
func (s *Service) UpdatePassword(ctx context.Context, id int64, password string) error {
	ctx, end, err := s.begin(ctx, id)
	if err != nil {
		return err
	}
	defer end()
	acc, err := s.account(ctx, id)
	if err != nil {
		return err
	}
	if acc.AuthType != "password" {
		return errNotPassword
	}
	in := serverInput(acc.Email, acc.servers())
	in.Password = password
	if r, err := s.check(ctx, in, ""); err != nil {
		return &CheckError{Report: r, err: err}
	}
	// A refusal of the old password still in flight is stale after credentialsUpdated.
	if err := keychainSet(s.keyringUser(id), password); err != nil {
		return fmt.Errorf("save credentials to keychain: %w", err)
	}
	return s.credentialsUpdated(ctx, id, nil)
}

// Reauthorize gives an OAuth account the tokens of a finished flow, which must
// have signed in to the account's address with its provider (ErrWrongAccount,
// ErrProviderMismatch; the old tokens stay), and the flow's OAuth client, which
// may be another one than before (a rotated secret). It clears reauth_required
// and a paused outbox; the worker resumes.
func (s *Service) Reauthorize(ctx context.Context, id int64, flowID string) error {
	ctx, end, err := s.begin(ctx, id)
	if err != nil {
		return err
	}
	defer end()
	acc, err := s.account(ctx, id)
	if err != nil {
		return err
	}
	if acc.AuthType != "oauth" {
		return fmt.Errorf("account %d does not sign in with OAuth", id)
	}
	g, err := s.flowGrant(AccountInput{OAuthFlowID: flowID, Email: acc.Email, Provider: acc.Provider})
	if err != nil {
		return err
	}
	if g.RefreshToken == "" {
		return errors.New("the provider granted no refresh token")
	}
	// No refresh with the old refresh token may finish after the swap.
	sem := s.refreshLock(id)
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if _, ok := s.takeGrant(flowID); !ok {
		return ErrUnknownFlow
	}
	// The new refresh token refreshes only with the client that granted it, and
	// the old one only with the old client: a failure below puts that back.
	old := OAuthClient{ID: acc.OAuthClientID}
	if old.ID != "" {
		if old.Secret, err = keychainGet(s.oauthClientUser(id)); err != nil {
			return fmt.Errorf("read the OAuth client secret from the keychain: %w", err)
		}
	}
	if err := s.saveClient(ctx, id, g.client); err != nil {
		return errors.Join(err, s.saveClient(context.WithoutCancel(ctx), id, old))
	}
	if err := keychainSet(s.keyringUser(id), g.RefreshToken); err != nil {
		err = fmt.Errorf("save credentials to keychain: %w", err)
		return errors.Join(err, s.saveClient(context.WithoutCancel(ctx), id, old))
	}
	return s.credentialsUpdated(ctx, id, &g.Grant)
}

// saveClient makes c the account's OAuth client: its secret in the keychain,
// or none for the provider's own client, and its ID on the account.
func (s *Service) saveClient(ctx context.Context, id int64, c OAuthClient) error {
	var err error
	if c.ID != "" {
		err = keychainSet(s.oauthClientUser(id), c.Secret)
	} else if err = keychainDelete(s.oauthClientUser(id)); errors.Is(err, keyring.ErrNotFound) {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("save the OAuth client secret to keychain: %w", err)
	}
	_, err = s.db.Execute(ctx, "UPDATE accounts SET oauth_client_id = ? WHERE id = ?", c.ID, id)
	return err
}

func (a Account) servers() ServerSettings {
	return ServerSettings{a.Username, a.IMAPHost, a.IMAPPort, a.IMAPSecurity, a.SMTPHost, a.SMTPPort, a.SMTPSecurity}
}

// serverInput is the AccountInput that checks email's account on ss.
func serverInput(email string, ss ServerSettings) AccountInput {
	return AccountInput{Email: email, Username: ss.Username, IMAPHost: ss.IMAPHost, IMAPPort: ss.IMAPPort,
		IMAPSecurity: ss.IMAPSecurity, SMTPHost: ss.SMTPHost, SMTPPort: ss.SMTPPort, SMTPSecurity: ss.SMTPSecurity}
}

// UpdateAccount writes p's fields in one transaction. New Servers are first
// checked with the stored credentials (a failure is a *CheckError and nothing is
// saved). IsDefault true makes the account the only default; false fails on the
// default account, as one account is always the default. Sync is a Limit of
// 1–100000, Months of 1–120, or neither (all).
func (s *Service) UpdateAccount(ctx context.Context, id int64, p AccountPatch) (Account, error) {
	ctx, end, err := s.begin(ctx, id)
	if err != nil {
		return Account{}, err
	}
	defer end()
	acc, err := s.account(ctx, id)
	if err != nil {
		return Account{}, err
	}
	if err := checkRange(p.Sync); err != nil {
		return Account{}, err
	}
	if p.IsDefault != nil && !*p.IsDefault && acc.IsDefault {
		return Account{}, errors.New("make another account the default instead")
	}
	var cols []string
	var args []any
	set := func(col string, v any) { cols, args = append(cols, col+" = ?"), append(args, v) }
	if p.Name != nil {
		set("name", *p.Name)
	}
	if p.Alias != nil {
		set("alias", *p.Alias)
	}
	if p.AIInstructions != nil {
		v := strings.TrimSpace(*p.AIInstructions)
		if utf8.RuneCountInString(v) > MaxAIInstructions {
			return Account{}, fmt.Errorf("AI instructions have at most %d characters", MaxAIInstructions)
		}
		set("ai_instructions", v)
	}
	var iconStmts []sqlite.Statement
	switch {
	case p.IconImage != nil:
		ct, err := accountIconType(p.IconImage)
		if err != nil {
			return Account{}, err
		}
		iconStmts = append(iconStmts, sqlite.Statement{SQL: "INSERT OR REPLACE INTO account_icons(account_id, content_type, data) VALUES (?, ?, ?)",
			Args: []any{id, ct, p.IconImage}})
		set("icon", "image:"+strconv.FormatInt(time.Now().UnixNano(), 36)) // a new key per upload: the GUI caches by it
	case p.Icon != nil:
		if !validAccountIcon(*p.Icon) {
			return Account{}, fmt.Errorf("invalid account icon %q", *p.Icon)
		}
		iconStmts = append(iconStmts, sqlite.Statement{SQL: "DELETE FROM account_icons WHERE account_id = ?", Args: []any{id}})
		set("icon", *p.Icon)
	}
	var images []sqlite.Statement
	if p.Signature != nil || p.SignatureID != nil {
		s.sigMu.Lock()
		defer s.sigMu.Unlock()
		if images, err = s.accountSignature(ctx, acc, p); err != nil {
			return Account{}, err
		}
	}
	if p.Sync != nil {
		set("sync_limit", p.Sync.Limit)
		set("sync_since_months", p.Sync.Months)
	}
	if p.Servers != nil {
		in := serverInput(acc.Email, *p.Servers)
		pw, tok, _, err := s.credentials(ctx, acc, "")
		if err != nil {
			return Account{}, err
		}
		in.Password = pw
		r, err := s.check(ctx, in, tok)
		if tok != "" && errors.Is(err, conn.ErrAuthFailed) { // the cached token may be stale
			if _, tok, _, err = s.credentials(ctx, acc, tok); err != nil {
				return Account{}, err
			}
			r, err = s.check(ctx, in, tok)
		}
		if err != nil {
			return Account{}, &CheckError{Report: r, err: err}
		}
		set("username", in.username())
		set("imap_host", in.IMAPHost)
		set("imap_port", in.IMAPPort)
		set("imap_security", string(in.IMAPSecurity))
		set("smtp_host", in.SMTPHost)
		set("smtp_port", in.SMTPPort)
		set("smtp_security", string(in.SMTPSecurity))
		// The checked settings work: the user just fixed the sign-in.
		set("status", "ok")
		set("status_detail", "")
		set("smtp_reauth", 0)
	}
	var stmts []sqlite.Statement
	if p.IsDefault != nil && *p.IsDefault {
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE accounts SET is_default = 0 WHERE id <> ?", Args: []any{id}})
		set("is_default", 1)
	}
	stmts = append(stmts, images...)
	stmts = append(stmts, iconStmts...)
	if len(cols) > 0 {
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE accounts SET " + strings.Join(cols, ", ") + " WHERE id = ?",
			Args: append(args, id)})
	}
	if p.Servers != nil && (p.Servers.IMAPHost != acc.IMAPHost || p.Servers.IMAPPort != acc.IMAPPort ||
		serverInput(acc.Email, *p.Servers).username() != acc.Username) {
		// Another mailbox: its UIDs are not the old one's even where UIDVALIDITY
		// matches. No real UIDVALIDITY is 0, so pushes of queued ops and downloads
		// see a recreated folder and the next fetch re-imports every folder.
		stmts = append(stmts,
			sqlite.Statement{SQL: "UPDATE folders SET uidvalidity = 0 WHERE account_id = ?", Args: []any{id}},
			sqlite.Statement{SQL: "UPDATE messages SET server_uidvalidity = 0 WHERE account_id = ? AND server_uid IS NOT NULL", Args: []any{id}})
		unlock, err := s.lockAccount(ctx, id) // no sync or download is using the old UIDs meanwhile
		if err != nil {
			return Account{}, err
		}
		defer unlock()
	}
	if p.Servers != nil {
		// Under credMu, like credentialsUpdated: refusals by the old servers still
		// in flight are stale and cannot undo the cleared status.
		s.tokMu.Lock()
		s.credMu.Lock()
		s.newCredentials(id, nil)
		_, err = s.db.Transaction(ctx, stmts)
		s.credMu.Unlock()
		s.tokMu.Unlock()
	} else if len(stmts) > 0 {
		_, err = s.db.Transaction(ctx, stmts)
	}
	if err != nil {
		return Account{}, err
	}
	if p.Servers != nil || p.Sync != nil {
		s.wakeWorker(id) // the next round dials the new servers or backfills
	}
	if len(images) > 0 {
		s.bus.Publish(events.Event{Type: events.SignaturesChanged})
	}
	return s.account(ctx, id)
}

// AccountIconMax is the most bytes an uploaded account icon may have.
const AccountIconMax = 5 << 20

// ErrAccountIcon is an icon that is not png, jpeg, gif, webp or svg, or is over AccountIconMax.
var ErrAccountIcon = errors.New("an account icon must be a png, jpeg, gif, webp or svg of at most 5 MB")

// accountIconType is data's sniffed type, or ErrAccountIcon.
func accountIconType(data []byte) (string, error) {
	if len(data) == 0 || len(data) > AccountIconMax {
		return "", ErrAccountIcon
	}
	if ct := http.DetectContentType(data); signatureImageTypes[ct] {
		return ct, nil
	}
	if bytes.Contains(data[:min(len(data), 1024)], []byte("<svg")) { // http sniffs svg as text
		return "image/svg+xml", nil
	}
	return "", ErrAccountIcon
}

// AccountIcon is the image of an account whose Icon is image:<n>.
func (s *Service) AccountIcon(ctx context.Context, id int64) (AccountIcon, error) {
	rows, err := s.db.Query(ctx, "SELECT content_type, data FROM account_icons WHERE account_id = ?", id)
	if err != nil {
		return AccountIcon{}, err
	}
	if len(rows.Values) == 0 {
		return AccountIcon{}, fmt.Errorf("account %d icon: %w", id, ErrAccountNotFound)
	}
	r := rows.Values[0]
	data, _ := r[1].([]byte)
	return AccountIcon{ContentType: store.Str(r[0]), Data: data}, nil
}

// validAccountIcon is whether icon may be set as is: ”, label:<name> or brand:<id>.
func validAccountIcon(icon string) bool {
	if name, ok := strings.CutPrefix(icon, "label:"); ok {
		return slices.Contains(LabelIcons, name)
	}
	if id, ok := strings.CutPrefix(icon, "brand:"); ok { // the GUI's marks, more than the providers': it falls back on unknown ones
		return id != "" && strings.Trim(id, "abcdefghijklmnopqrstuvwxyz0123456789-") == ""
	}
	return icon == ""
}
