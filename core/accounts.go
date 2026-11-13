package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mailsync"
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

// sendUser is the keychain entry of the sending slot's own password, when it
// has one; else it signs in with the receiving slot's (keyringUser).
func (s *Service) sendUser(id int64) string { return s.keyringUser(id) + "/send" }

// deleteCredentials removes the account's keychain entries; missing ones are fine.
func (s *Service) deleteCredentials(id int64) error {
	var errs []error
	for _, u := range []string{s.keyringUser(id), s.oauthClientUser(id), s.sendUser(id)} {
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

// ErrJMAPOff refuses to add a JMAP account while Settings.ExperimentalJMAP is off.
var ErrJMAPOff = errors.New("JMAP is experimental: turn it on in settings to add a JMAP account")

// ErrEWSOff refuses to add an EWS account while Settings.ExperimentalEWS is off.
var ErrEWSOff = errors.New("EWS is experimental: turn it on in settings to add an Exchange (EWS) account")

// errPOP3Off refuses to add a POP3 account while Settings.ExperimentalPOP3 is
// off. It has no wire code: TestWireCodesPinned pins the codes, and the UI
// hides POP3 while the setting is off, so only a program can meet it.
var errPOP3Off = errors.New("POP3 is experimental: turn it on in settings to add a POP3 account")

// ErrGraphOff refuses to add a Microsoft Graph account while Settings.ExperimentalGraph is off.
var ErrGraphOff = errors.New("Microsoft Graph is experimental: turn it on in settings to add an Outlook (Graph) account")

var errGmailAPIOff = errors.New("Gmail API is experimental: turn it on in settings to add a Gmail API account")

var errGmailAPINeedsOwnClient = errors.New("Gmail API requires your own Google Cloud OAuth client")

// allowProtocol sets in.allowed: in's receiving protocol is not
// experimental, or its setting is on.
func (s *Service) allowProtocol(ctx context.Context, in *AccountInput) error {
	fillGmailAPISlot(in)
	p := receivingProtocol(in.receiving())
	if in.allowed = p.setting == nil; in.allowed {
		return nil
	}
	st, err := s.loadSettings(ctx)
	in.allowed = p.setting(st)
	return err
}

func fillGmailAPISlot(in *AccountInput) {
	if in.receiving() != auth.GmailAPI {
		return
	}
	if in.Receiving.Host == "" {
		in.Receiving = Server{Protocol: auth.GmailAPI, Host: "gmail.googleapis.com", Port: 443, Security: SecurityTLS, Auth: in.Receiving.Auth}
	}
	if in.Sending.Host == "" && in.Sending.Protocol == "" {
		in.Sending = in.Receiving
	}
}

func validGmailAPIGrant(g oauthGrant) error {
	if g.provider != "gmail" {
		return ErrProviderMismatch
	}
	pub, _ := auth.ByID("gmail")
	if g.client.ID == "" || pub.OAuth != nil && pub.OAuth.Public != nil && g.client.ID == pub.OAuth.Public.ID {
		return errGmailAPINeedsOwnClient
	}
	return nil
}

// receiving is the receiving slot's protocol, "" read as IMAP.
func (in AccountInput) receiving() string {
	if in.Receiving.Protocol == "" {
		return auth.IMAP
	}
	return in.Receiving.Protocol
}

// sending is the protocol a new or changed account sends with: EWS through
// EWS, JMAP through JMAP, Gmail API through Gmail API, any other first through SMTP (Graph is stored only
// after SMTP refused AUTH).
func (in AccountInput) sending() string {
	return receivingProtocol(in.receiving()).sends
}

func (in AccountInput) validate() error {
	if _, err := mail.ParseAddress(in.Email); err != nil {
		return fmt.Errorf("invalid email address %q", in.Email)
	}
	fillGmailAPISlot(&in)
	recv := in.receiving()
	sendSlot := in.Sending
	if recv == auth.Graph && sendSlot == (Server{Protocol: sendSlot.Protocol}) {
		sendSlot = in.Receiving
		sendSlot.Protocol = auth.Graph
	}
	for _, e := range []struct {
		slot, name string
		ok         bool
		Server
	}{{"receiving", strings.ToUpper(recv), protocols[recv].receiver != nil && (!protocols[recv].checkRefuses || in.allowed), in.Receiving},
		// Graph is not chosen but stored after SMTP refused AUTH; a stored
		// account's servers come back with it.
		{"sending", strings.ToUpper(in.sending()), sendSlot.Protocol == "" || sendSlot.Protocol == in.sending() ||
			sendSlot.Protocol == auth.Graph && !sendsViaReceiving(recv), sendSlot}} {
		http := e.name == "JMAP" || e.name == "EWS" || e.name == "GRAPH" || e.name == "GMAILAPI"
		if !e.ok {
			return fmt.Errorf("%s protocol %q is not supported", e.slot, e.Protocol)
		}
		if e.slot == "sending" && http && (e.Server == (Server{}) || e.Server == (Server{Protocol: recv})) {
			continue // JMAP and EWS send through the receiving server
		}
		if bare := e.Server; e.name == "EWS" && (bare.Auth == "" || bare.Auth == AuthPassword) {
			if bare.Auth = ""; bare == (Server{Protocol: e.Protocol}) {
				continue // Autodiscover finds it
			}
		}
		if e.Host == "" {
			return fmt.Errorf("%s host is required", e.name)
		}
		if e.Port < 1 || e.Port > 65535 {
			return fmt.Errorf("%s port %d is out of range", e.name, e.Port)
		}
		if !validSecurity(e.Security) || http && e.Security == SecurityStartTLS {
			return fmt.Errorf("%s security %q is invalid", e.name, e.Security)
		}
		if e.name == "GMAILAPI" {
			if !(e.Auth == AuthOAuth || e.Auth == "" && in.OAuthFlowID != "") {
				return fmt.Errorf("%s sign-in %q is not supported", e.name, e.Auth)
			}
		} else if !(e.Auth == "" || e.Auth == AuthPassword && e.name != "GRAPH" || e.Auth == AuthOAuth && e.name != "EWS" || e.Auth == AuthAPIToken && e.name == "JMAP") {
			return fmt.Errorf("%s sign-in %q is not supported", e.name, e.Auth)
		}
		if e.Path != "" && (!http || !strings.HasPrefix(e.Path, "/") || strings.ContainsAny(e.Path, "?# ")) ||
			e.Path == "" && e.name == "EWS" { // the EWS URL's
			return fmt.Errorf("%s path %q is invalid", e.name, e.Path)
		}
		if e.Security == SecurityNone && !isLoopback(e.Host) {
			return fmt.Errorf("%s: plaintext connections are only allowed to localhost", e.name)
		}
	}
	return nil
}

// slotAuth is how slot signs in: AuthPassword when it does not say.
func slotAuth(slot Server) string {
	if slot.Auth == "" {
		return AuthPassword
	}
	return slot.Auth
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
	if err := s.allowProtocol(ctx, &in); err != nil {
		return err
	}
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
	secret := in.Password
	var g oauthGrant
	if in.OAuthFlowID != "" {
		if g, err = s.flowGrant(in); err != nil {
			return Account{}, err
		}
		if g.RefreshToken == "" {
			return Account{}, errors.New("the provider granted no refresh token")
		}
	} else if in.Receiving.Auth == AuthOAuth || in.Sending.Auth == AuthOAuth {
		return Account{}, errors.New("an OAuth sign-in needs an OAuth flow")
	}
	// Before the check, and before the grant is taken (only peeked above): this refusal leaves it unused.
	if rows, err := s.db.Query(ctx, "SELECT 1 FROM accounts WHERE email = ?", in.Email); err != nil {
		return Account{}, err
	} else if len(rows.Values) > 0 {
		return Account{}, ErrAccountExists
	}
	recvAuth, sendAuth := slotAuth(in.Receiving), slotAuth(in.Sending)
	if err := s.allowProtocol(ctx, &in); err != nil {
		return Account{}, err
	}
	if !in.allowed {
		return Account{}, receivingProtocol(in.receiving()).off
	}
	if in.receiving() == auth.GmailAPI {
		if in.OAuthFlowID == "" {
			return Account{}, errors.New("Gmail API requires an OAuth sign-in")
		}
		if err := validGmailAPIGrant(g); err != nil {
			return Account{}, err
		}
	}
	checked, checkErr := s.check(ctx, in, "")
	receiveOnly := in.ReceiveOnly && checked.OnlySendingFailed()
	if checkErr != nil && !receiveOnly {
		return Account{}, &CheckError{Report: checked, err: checkErr}
	}
	if checked.URL != "" { // found by Autodiscover
		in.Receiving, _ = ewsSlot(checked.URL)
	}
	if sendsViaReceiving(in.receiving()) {
		in.Sending = in.Receiving // it sends through the receiving server
		sendAuth = slotAuth(in.Sending)
	}
	if in.OAuthFlowID != "" {
		// Peeked for the check; taken now, so one grant adds one account.
		if _, ok := s.takeGrant(in.OAuthFlowID); !ok {
			return Account{}, ErrUnknownFlow
		}
		secret, in.Username, in.Provider = g.RefreshToken, g.Email, g.provider
		recvAuth, sendAuth, in.SendPassword = AuthOAuth, AuthOAuth, ""
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
	rows, err := s.db.Query(ctx, "SELECT count(*) FROM accounts WHERE kind != ?", KindLocalArchive) // a local archive is never the default
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
		SQL: `INSERT INTO accounts(email, name, alias, is_default, sync_limit, sync_since_months, recv_auth, send_auth, status, username,
			recv_protocol, recv_host, recv_port, recv_security, recv_path, send_protocol, send_host, send_port, send_security, created_at, provider, oauth_client_id, features, receive_only, position)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'ok', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (` + nextListPosition + `))`,
		Args: []any{in.Email, in.Name, in.Alias, b2i(isDefault), sync.Limit, sync.Months, recvAuth, sendAuth, in.username(),
			in.receiving(), in.Receiving.Host, in.Receiving.Port, string(in.Receiving.Security), in.Receiving.Path, in.sending(), in.Sending.Host, in.Sending.Port, string(in.Sending.Security), time.Now().Unix(), in.Provider, in.oauthClientID, encodeFeatures(checked.desc), b2i(receiveOnly)},
	})
	inserted := len(stmts) - 1
	if in.receiving() == auth.POP3 { // the server has only an inbox: trash, archive and sent are local (no server_ref)
		for _, f := range [][2]string{{"Trash", "trash"}, {"Archive", "archive"}, {"Sent", "sent"}} {
			stmts = append(stmts, sqlite.Statement{SQL: "INSERT INTO folders(account_id, name, role, role_since) VALUES ((SELECT id FROM accounts WHERE email = ?), ?, ?, unixepoch())",
				Args: []any{in.Email, f[0], f[1]}})
		}
	}
	res, err := s.db.Transaction(ctx, stmts)
	if err != nil {
		return Account{}, err
	}
	id := res[inserted].LastInsertID
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
	if in.SendPassword != "" {
		if err := keychainSet(s.sendUser(id), in.SendPassword); err != nil {
			if kerr := s.deleteCredentials(id); kerr != nil {
				err = errors.Join(err, kerr)
			}
			return Account{}, fmt.Errorf("save the sending password to keychain: %w", undo(err))
		}
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
	if g.AccessToken != "" && in.receiving() != auth.Graph {
		s.cacheToken(id, g.Grant)
	}
	s.startWorker(id)
	return s.account(ctx, id)
}

// AddLocalArchive adds a local archive account (KindLocalArchive) named name,
// with a local inbox and trash. It is never the default and has no worker.
func (s *Service) AddLocalArchive(ctx context.Context, name string) (Account, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Account{}, err
	}
	defer end()
	if name = strings.TrimSpace(name); name == "" {
		return Account{}, errors.New("a local archive needs a name")
	}
	// accounts.email is NOT NULL UNIQUE; .invalid matches no real mail.
	email := "archive-" + strings.ToLower(rand.Text()) + "@local.invalid"
	acc := "(SELECT id FROM accounts WHERE email = ?)"
	res, err := s.db.Transaction(ctx, []sqlite.Statement{{
		SQL: `INSERT INTO accounts(email, name, recv_auth, send_auth, status, username, created_at, provider, features, recv_protocol, send_protocol, kind, position)
			VALUES (?, ?, 'none', 'none', 'ok', '', ?, 'local', '', '', '', ?, (` + nextListPosition + `))`,
		Args: []any{email, name, time.Now().Unix(), KindLocalArchive},
	},
		{SQL: "INSERT INTO folders(account_id, name, role, role_since) VALUES (" + acc + ", 'INBOX', 'inbox', unixepoch())", Args: []any{email}},
		{SQL: "INSERT INTO folders(account_id, name, role, role_since) VALUES (" + acc + ", 'Trash', 'trash', unixepoch())", Args: []any{email}},
	})
	if err != nil {
		return Account{}, err
	}
	return s.account(ctx, res[0].LastInsertID)
}

// ErrLocalArchive is a server, sending or sync call on a local archive
// account, which has none of these.
var ErrLocalArchive = errors.New("a local archive has no mail server")

// ErrReceiveOnly is a send, or a sender choice, from a receive-only account
// (Account.ReceiveOnly), or a reply with no other account that sends.
var ErrReceiveOnly = errors.New("the account only receives mail")

// localArchive reports whether account id is a local archive.
func (s *Service) localArchive(ctx context.Context, id int64) (bool, error) {
	rows, err := s.db.Query(ctx, "SELECT kind FROM accounts WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	if len(rows.Values) == 0 {
		return false, fmt.Errorf("account %d: %w", id, ErrAccountNotFound)
	}
	return store.Str(rows.Values[0][0]) == KindLocalArchive, nil
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

const accountCols = `id, email, name, alias, COALESCE((SELECT html FROM signatures WHERE id = signature_id), ''), username, is_default, sync_limit, recv_auth, status,
	recv_host, recv_port, recv_security, send_host, send_port, send_security, smtp_reauth, provider, status_detail,
	sync_since_months, (SELECT count(*) FROM folders WHERE account_id = accounts.id), signature_id, icon, ai_instructions, oauth_client_id, label_keywords, kind, receive_only,
	recv_protocol, send_protocol, features, recv_path, send_auth, position`

// ErrAccountNotFound is returned for an account id that does not exist.
var ErrAccountNotFound = errors.New("account not found")

// ErrAccountExists refuses to add an address that is already an account: sign
// it in again (Reauthorize, UpdatePassword) instead.
var ErrAccountExists = errors.New("this address is already an account")

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
		Receiving: Server{store.Str(r[28]), store.Str(r[10]), int(store.Int(r[11])), Security(store.Str(r[12])), store.Str(r[31]), store.Str(r[8])},
		Sending:   Server{store.Str(r[29]), store.Str(r[13]), int(store.Int(r[14])), Security(store.Str(r[15])), "", store.Str(r[32])},
		Provider:  store.Str(r[17]), StatusDetail: store.Str(r[18]), SignatureID: store.Int(r[21]), Icon: store.Str(r[22]),
		AIInstructions: store.Str(r[23]), OAuthClientID: store.Str(r[24]), LabelKeywords: store.Int(r[25]) == 1, Kind: store.Str(r[26]),
		ReceiveOnly: store.Int(r[27]) == 1, Position: int(store.Int(r[33])),
	}
	if sendsViaReceiving(a.Receiving.Protocol) {
		// EWS, JMAP, Graph and Gmail API send through the receiving server, whose
		// path and sign-in live on the receiving slot (this also migrates older
		// JMAP accounts whose send_protocol was stored as smtp).
		a.Sending = a.Receiving
	}
	if d, ok := decodeFeatures(r[30]); ok {
		a.Features = d.Features
		if a.Receiving.Protocol == auth.JMAP {
			if s, ok := a.Features[backend.FeatureSavesSent]; ok && s.Reason == "jmap.sent_via_smtp" {
				a.Features[backend.FeatureSavesSent] = backend.Support{Level: backend.LevelSupported}
			}
		}
	}
	// SMTP refuses the password: the user is asked for it the same way. status and
	// status_detail are IMAP's and the keychain's, and they take precedence.
	if store.Int(r[16]) != 0 && a.Status == "ok" {
		a.Status, a.StatusDetail, a.SendRefused = "reauth_required", "auth_rejected", true
	}
	return a
}

// nextListPosition is the position after the accounts list's last row: mail
// accounts and standalone address books share one sequence (ReorderAccountEntries).
const nextListPosition = `SELECT COALESCE(MAX(p) + 1, 0) FROM (SELECT position AS p FROM accounts UNION ALL SELECT sort_order FROM address_books WHERE account_id IS NULL)`

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
// Standalone address books keep their places; ReorderAccountEntries orders both.
func (s *Service) ReorderAccounts(ctx context.Context, ids []int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT id, position FROM accounts ORDER BY id")
	if err != nil {
		return err
	}
	have := make([]int64, len(rows.Values))
	slots := make([]int64, len(rows.Values)) // the accounts' places in the order shared with standalone address books
	for i, r := range rows.Values {
		have[i], slots[i] = store.Int(r[0]), store.Int(r[1])
	}
	want := slices.Clone(ids)
	slices.Sort(want)
	if !slices.Equal(have, want) {
		return errors.New("the new order must name every account once")
	}
	slices.Sort(slots)
	stmts := make([]sqlite.Statement, len(ids))
	for i, id := range ids {
		stmts[i] = sqlite.Statement{SQL: "UPDATE accounts SET position = ? WHERE id = ?", Args: []any{slots[i], id}}
	}
	_, err = s.db.Transaction(ctx, stmts)
	return err
}

// RemoveAccount deletes the account, its keychain entry, its cache directory
// and, for a local archive, its originals (ADR 0036).
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
	linked, err := s.db.Query(ctx, "SELECT id FROM address_books WHERE account_id = ?", id)
	if err != nil {
		return err
	}
	if _, err := s.db.DurableExecute(ctx, "DELETE FROM accounts WHERE id = ?", id); err != nil { // cascades to its address book
		return err
	}
	var abErrs []error
	for _, r := range linked.Values {
		abErrs = append(abErrs, s.forgetAddressBook(store.Int(r[0])))
	}
	if len(linked.Values) > 0 {
		s.notifyAddressBooksChanged(ctx)
	}
	s.tokMu.Lock()
	delete(s.tokens, id)
	s.tokMu.Unlock()
	s.credMu.Lock()
	delete(s.credVer, id)
	s.credMu.Unlock()
	// The files go even when the keychain fails: with the row gone, nothing
	// else would ever remove them.
	return errors.Join(append(abErrs, s.deleteCredentials(id),
		os.RemoveAll(filepath.Join(s.dataDir, "cache", strconv.FormatInt(id, 10))),
		store.RemoveAll(filepath.Join(s.dataDir, "archive", strconv.FormatInt(id, 10))))...)
}

// DetectProvider finds the provider for an email address; DNS gets 5 seconds in total.
func (s *Service) DetectProvider(ctx context.Context, email string) (auth.Provider, auth.Source, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return auth.Detect(ctx, net.DefaultResolver, email)
}

func (s *Service) ListProviders() []auth.Provider { return auth.ListProviders() }

var errNotPassword = errors.New("the account does not sign in with a password")

// UpdatePassword checks password (an API token for a receiving slot that signs
// in with one) against the account's servers and, if both accept it, saves it
// as the receiving slot's and clears reauth_required and a paused outbox; the
// worker resumes and the outbox sends in its next round. A sending slot with
// a password of its own keeps it (UpdateSendPassword). A failed check is a
// *CheckError and leaves the keychain unchanged; for a receive-only account
// only the receiving step must pass, and a passing sending step makes it send
// again. A JMAP API token whose session may not send is saved too, and the
// account becomes receive-only.
func (s *Service) UpdatePassword(ctx context.Context, id int64, password string) error {
	return s.updatePassword(ctx, id, password, false)
}

// UpdateSendPassword is UpdatePassword for the sending slot's own password:
// the one SMTP signs in with when it differs from the receiving slot's. The
// receiving slot keeps its secret.
func (s *Service) UpdateSendPassword(ctx context.Context, id int64, password string) error {
	return s.updatePassword(ctx, id, password, true)
}

func (s *Service) updatePassword(ctx context.Context, id int64, password string, send bool) error {
	ctx, end, err := s.begin(ctx, id)
	if err != nil {
		return err
	}
	defer end()
	acc, err := s.account(ctx, id)
	if err != nil {
		return err
	}
	if acc.Kind == KindLocalArchive || acc.Receiving.Auth == AuthOAuth {
		return errNotPassword
	}
	in, user := serverInput(acc.Email, acc.servers()), s.keyringUser(id)
	if send { // checked with the receiving slot's stored secret
		in.Password, _, _, err = s.credentials(ctx, acc, "")
		in.SendPassword, user = password, s.sendUser(id)
	} else {
		in.Password = password
		in.SendPassword, err = s.sendSecret(ctx, acc)
	}
	if err != nil {
		return err
	}
	r, err := s.check(ctx, in, "")
	// An imported account never signed in has no sending password of its own
	// yet: one SMTP refuses is asked for apart (smtp_reauth, UpdateSendPassword),
	// receiving signing in with this one now.
	sendApart := err != nil && !send && acc.StatusDetail == "credentials_missing" && in.SendPassword == "" && r.OnlySendingFailed()
	// A receive-only account needs receiving only; so does a JMAP sign-in that may not send.
	if err != nil && !sendApart && !((acc.ReceiveOnly || acc.Receiving.Protocol == auth.JMAP) && r.OnlySendingFailed()) {
		return &CheckError{Report: r, err: err}
	}
	// A refusal of the old password still in flight is stale after credentialsUpdated.
	if err := keychainSet(user, password); err != nil {
		return fmt.Errorf("save credentials to keychain: %w", err)
	}
	if !send {
		if _, err := s.db.Execute(ctx, "UPDATE accounts SET receive_only = ?, features = COALESCE(?, features) WHERE id = ?", b2i(err != nil && !sendApart), encodeFeatures(r.desc), id); err != nil {
			return err
		}
	} else if acc.ReceiveOnly != (err != nil) {
		if _, err := s.db.Execute(ctx, "UPDATE accounts SET receive_only = ? WHERE id = ?", b2i(err != nil), id); err != nil {
			return err
		}
	}
	if err := s.credentialsUpdated(ctx, id, nil); err != nil || !sendApart {
		return err
	}
	_, err = s.db.Execute(context.WithoutCancel(ctx), "UPDATE accounts SET smtp_reauth = 1 WHERE id = ?", id)
	return err
}

// Reauthorize gives an OAuth account the tokens of a finished flow, which must
// have signed in to the account's address with its provider (ErrWrongAccount,
// ErrProviderMismatch; the old tokens stay), and the flow's OAuth client, which
// may be another one than before (a rotated secret). It clears reauth_required
// and a paused outbox; the worker resumes. A receive-only account sends again
// if the check passes with the new sign-in (RecheckSending).
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
	if acc.Receiving.Protocol == auth.GmailAPI {
		if err := validGmailAPIGrant(g); err != nil {
			return err
		}
	}
	// No refresh with the old refresh token may finish after the swap.
	sem := s.refreshLock(id)
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	release := sync.OnceFunc(func() { <-sem })
	defer release()
	if _, ok := s.takeGrant(flowID); !ok {
		return ErrUnknownFlow
	}
	// The new refresh token refreshes only with the client that granted it, and
	// the old one only with the old client: a failure below puts that back.
	old := OAuthClient{ID: acc.OAuthClientID}
	if old.ID != "" {
		// A lost secret (credentials_missing) stays lost on a rollback; this sign-in is its fix.
		if old.Secret, err = keychainGet(s.oauthClientUser(id)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
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
	var cached *auth.Grant
	if acc.Receiving.Protocol != auth.Graph {
		cached = &g.Grant
	}
	if err := s.credentialsUpdated(ctx, id, cached); err != nil {
		return err
	}
	if acc.ReceiveOnly { // the new sign-in may send: a failing check keeps it receive-only
		release() // the check may refresh the token
		// Reread: the check refreshes with the OAuth client just saved.
		if acc, err = s.account(ctx, id); err == nil {
			_, err = s.recheckSending(ctx, acc)
		}
		if err != nil {
			slog.Warn("could not recheck sending", "account", id, "err", err)
		}
	}
	return nil
}

// saveClient makes c the account's OAuth client: its ID on the account and its
// secret in the keychain; with no secret (the provider's own client, or one
// whose secret was lost) the keychain keeps none.
func (s *Service) saveClient(ctx context.Context, id int64, c OAuthClient) error {
	var err error
	if c.Secret != "" {
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
	return ServerSettings{a.Username, a.Receiving, a.Sending}
}

// serverInput is the AccountInput that checks email's account on ss.
func serverInput(email string, ss ServerSettings) AccountInput {
	return AccountInput{Email: email, Username: ss.Username, Receiving: ss.Receiving, Sending: ss.Sending, allowed: true} // a stored account's
}

// UpdateAccount writes p's fields in one transaction. New Servers are first
// checked with the stored credentials (a failure is a *CheckError and nothing is
// saved; a receive-only account's failing SMTP step alone still saves them and
// keeps it receive-only, a passing one makes it send again). IsDefault true makes the account the only default; false fails on the
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
	if acc.Kind == KindLocalArchive && (p.Servers != nil || p.Sync != nil || p.IsDefault != nil && *p.IsDefault) {
		return Account{}, ErrLocalArchive
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
	if p.LabelKeywords != nil {
		set("label_keywords", b2i(*p.LabelKeywords))
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
		if in.Receiving.Protocol == "" {
			in.Receiving.Protocol = acc.Receiving.Protocol
		}
		if in.Receiving.Protocol != acc.Receiving.Protocol {
			return Account{}, errors.New("an account's receiving protocol cannot change: add the account again")
		}
		if sendsViaReceiving(in.receiving()) {
			in.Sending = in.Receiving // the same endpoint; the stored sending slot has no path
		}
		in.Receiving.Auth, in.Sending.Auth = acc.Receiving.Auth, acc.Sending.Auth // the sign-in stays
		r, err := s.checkStored(ctx, acc, &in)
		var ce *CheckError
		if err != nil && !errors.As(err, &ce) {
			return Account{}, err
		}
		// A receive-only account may change servers while sending still fails; a
		// passing sending step makes it send again.
		if err != nil && !(acc.ReceiveOnly && r.OnlySendingFailed()) {
			return Account{}, err
		}
		if r.URL != "" { // found by Autodiscover
			in.Receiving, _ = ewsSlot(r.URL)
			in.Sending = in.Receiving
		}
		set("receive_only", b2i(err != nil))
		set("username", in.username())
		set("recv_host", in.Receiving.Host)
		set("features", encodeFeatures(r.desc))
		set("recv_port", in.Receiving.Port)
		set("recv_security", string(in.Receiving.Security))
		set("recv_path", in.Receiving.Path)
		set("send_host", in.Sending.Host)
		set("send_port", in.Sending.Port)
		set("send_security", string(in.Sending.Security))
		set("send_protocol", in.sending()) // new servers: the next send tries SMTP before Graph again (EWS stays EWS)
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
	if p.Servers != nil && (p.Servers.Receiving.Host != acc.Receiving.Host || p.Servers.Receiving.Port != acc.Receiving.Port ||
		p.Servers.Receiving.Path != acc.Receiving.Path || serverInput(acc.Email, *p.Servers).username() != acc.Username) {
		// Another mailbox: its server locations are not the old one's even where
		// they read the same. They and the cursors are forgotten, as a
		// FolderReset of every folder would, so downloads find no location,
		// queued changes are conflicts and the next round syncs every folder
		// from the start: a message it finds by identity (Gmail, JMAP) keeps
		// its row, the rest is gone at the round's end.
		stmts = append(stmts,
			sqlite.Statement{SQL: `UPDATE ops SET state = 'conflict', last_error = 'the account moved to another server, so a change queued for the old one was not applied'
				WHERE account_id = ? AND state IN ('pending', 'awaiting_claim')`, Args: []any{id}},
			sqlite.Statement{SQL: "UPDATE accounts SET cursor = NULL WHERE id = ?", Args: []any{id}},
			sqlite.Statement{SQL: "UPDATE folders SET cursor = NULL, synced = 0, filled_since = NULL WHERE account_id = ?", Args: []any{id}},
			// An inbox-only account (POP3) keeps its mail where it shows: it may exist nowhere else.
			sqlite.Statement{SQL: "UPDATE message_locations SET server_ref = NULL WHERE message_id IN (SELECT id FROM messages WHERE account_id = ?) AND " + mailsync.InboxOnly, Args: []any{id, id}},
			sqlite.Statement{SQL: "DELETE FROM message_locations WHERE message_id IN (SELECT id FROM messages WHERE account_id = ?) AND (server_ref IS NOT NULL OR shown = 0 OR NOT " + mailsync.InboxOnly + ")", Args: []any{id, id}})
		unlock, err := s.lockAccount(ctx, id) // no sync or download is using the old locations meanwhile
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
	if p.LabelKeywords != nil && *p.LabelKeywords && !acc.LabelKeywords { // the keywords already synced show at once
		if err := s.importKeywordLabels(ctx, id); err != nil {
			slog.Warn("keyword labels not imported", "account", id, "err", err)
		}
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
