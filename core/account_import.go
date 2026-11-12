package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/store"
)

var (
	// ErrBadAccountExport refuses a corrupted or invalid account export file.
	ErrBadAccountExport = errors.New("the account export file is invalid")
	// ErrUnsupportedExportVersion refuses an account export file with an unknown format version.
	ErrUnsupportedExportVersion = errors.New("the account export file version is not supported")
	// ErrAccountExists refuses an imported account whose address is already used on another protocol.
	ErrAccountExists = errors.New("an account with this email address already exists")
)

// AccountImportPreview describes the accounts in an export file before ImportAccounts writes them.
type AccountImportPreview struct {
	Accounts []AccountImportEntry `json:"accounts"`
}

// AccountImportEntry is one account in an AccountImportPreview.
type AccountImportEntry struct {
	Email             string `json:"email"`
	Name              string `json:"name,omitempty"`
	ReceivingProtocol string `json:"receiving_protocol"`
	ReceivingHost     string `json:"receiving_host"`
	ReceivingPort     int    `json:"receiving_port"`
	SendingProtocol   string `json:"sending_protocol"`
	SendingHost       string `json:"sending_host"`
	SendingPort       int    `json:"sending_port"`
	ReceiveOnly       bool   `json:"receive_only,omitempty"`
	Auth              string `json:"auth"`
	// Existing marks an address already on this device or earlier in the file: ImportAccounts skips it.
	Existing bool `json:"existing,omitempty"`
	// ExistingProtocol is the local account's receiving protocol when it differs from the file's.
	ExistingProtocol string `json:"existing_protocol,omitempty"`
}

// AccountImportSkip is one address ImportAccounts skipped. ExistingProtocol is
// the local account's receiving protocol when it differs from the file's;
// empty when the address already exists with the same protocol.
type AccountImportSkip struct {
	Email            string `json:"email"`
	ExistingProtocol string `json:"existing_protocol,omitempty"`
}

// AccountImportResult reports the accounts ImportAccounts created and the addresses it skipped.
type AccountImportResult struct {
	Imported []Account           `json:"imported"`
	Skipped  []AccountImportSkip `json:"skipped"`
}

func normalizeImportEmail(raw string) (clean, norm string, err error) {
	clean = strings.TrimSpace(raw)
	addr, err := mail.ParseAddress(clean)
	if err != nil || addr.Address != clean || strings.HasSuffix(strings.ToLower(clean), "@local.invalid") {
		return "", "", fmt.Errorf("invalid email address %q", raw)
	}
	return clean, strings.ToLower(clean), nil
}

func validateExportedSlot(slot string, sv ExportedServer, http bool) error {
	host := strings.TrimSpace(sv.Host)
	if host == "" || host != sv.Host || strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("%s host %q is invalid", slot, sv.Host)
	}
	if sv.Port < 1 || sv.Port > 65535 {
		return fmt.Errorf("%s port %d is out of range", slot, sv.Port)
	}
	if !validSecurity(sv.Security) || http && sv.Security == SecurityStartTLS {
		return fmt.Errorf("%s security %q is invalid", slot, sv.Security)
	}
	if sv.Security == SecurityNone && !isLoopback(host) {
		return fmt.Errorf("%s: plaintext connections are only allowed to localhost", slot)
	}
	if sv.Path != "" && (!http || !strings.HasPrefix(sv.Path, "/") || strings.ContainsAny(sv.Path, "?# ")) {
		return fmt.Errorf("%s path %q is invalid", slot, sv.Path)
	}
	return nil
}

func cleanExportedAccount(ea ExportedAccount) (ExportedAccount, string, error) {
	email, norm, err := normalizeImportEmail(ea.Email)
	if err != nil {
		return ExportedAccount{}, "", err
	}
	ea.Email = email
	ea.Username = strings.TrimSpace(ea.Username)
	if ea.Username == "" {
		ea.Username = email
	}
	ea.Provider = strings.TrimSpace(ea.Provider)
	if ea.Provider == "local" {
		return ExportedAccount{}, "", fmt.Errorf("provider %q is reserved", ea.Provider)
	}
	if ea.Provider == "" {
		ea.Provider = "other"
	}
	ea.OAuthClientID = strings.TrimSpace(ea.OAuthClientID)

	recv := ea.Receiving
	recvHTTP := false
	switch recv.Protocol {
	case auth.IMAP, auth.POP3:
		if recv.Auth != AuthPassword && recv.Auth != AuthOAuth {
			return ExportedAccount{}, "", fmt.Errorf("receiving auth %q is not supported for %s", recv.Auth, recv.Protocol)
		}
	case auth.JMAP:
		recvHTTP = true
		if recv.Auth != AuthPassword && recv.Auth != AuthAPIToken && recv.Auth != AuthOAuth {
			return ExportedAccount{}, "", fmt.Errorf("receiving auth %q is not supported for jmap", recv.Auth)
		}
	case auth.EWS:
		recvHTTP = true
		if recv.Auth != AuthPassword {
			return ExportedAccount{}, "", fmt.Errorf("receiving auth %q is not supported for ews", recv.Auth)
		}
		if recv.Path == "" {
			return ExportedAccount{}, "", errors.New("ews path is required")
		}
	case auth.Graph, auth.GmailAPI:
		recvHTTP = true
		if recv.Auth != AuthOAuth {
			return ExportedAccount{}, "", fmt.Errorf("receiving auth %q is not supported for %s", recv.Auth, recv.Protocol)
		}
	default:
		return ExportedAccount{}, "", fmt.Errorf("receiving protocol %q is not supported", recv.Protocol)
	}
	if err := validateExportedSlot("receiving", recv, recvHTTP); err != nil {
		return ExportedAccount{}, "", err
	}
	if recv.Auth != AuthOAuth && ea.OAuthClientID != "" {
		return ExportedAccount{}, "", errors.New("oauth_client_id requires oauth auth")
	}

	send := ea.Sending
	switch recv.Protocol {
	case auth.JMAP, auth.EWS, auth.Graph, auth.GmailAPI:
		if send == (ExportedServer{}) {
			send = recv
		} else if send != recv {
			return ExportedAccount{}, "", fmt.Errorf("sending server must match receiving server for %s", recv.Protocol)
		}
	default:
		switch send.Protocol {
		case auth.SMTP:
		case auth.Graph:
			if recv.Auth != AuthOAuth {
				return ExportedAccount{}, "", errors.New("graph sending requires oauth auth")
			}
		default:
			return ExportedAccount{}, "", fmt.Errorf("sending protocol %q is not supported", send.Protocol)
		}
		if send.Auth != recv.Auth {
			return ExportedAccount{}, "", fmt.Errorf("sending auth %q does not match receiving auth %q", send.Auth, recv.Auth)
		}
		if err := validateExportedSlot("sending", send, false); err != nil {
			return ExportedAccount{}, "", err
		}
	}
	ea.Receiving, ea.Sending = recv, send

	sr := SyncRange{Limit: ea.Sync.Limit, Months: ea.Sync.Months}
	if err := checkRange(&sr); err != nil {
		return ExportedAccount{}, "", err
	}
	ea.AIInstructions = strings.TrimSpace(ea.AIInstructions)
	if utf8.RuneCountInString(ea.AIInstructions) > MaxAIInstructions {
		return ExportedAccount{}, "", fmt.Errorf("AI instructions exceed %d characters", MaxAIInstructions)
	}
	if ea.Signature != nil {
		name, err := cleanSignatureName(ea.Signature.Name)
		if err != nil {
			return ExportedAccount{}, "", err
		}
		ea.Signature = &ExportedSignature{Name: name, HTML: ea.Signature.HTML}
		if rest, ok := strings.CutPrefix(ea.Signature.HTML, SignatureHTMLPrefix); ok {
			if _, _, err := signatureAssets(0, rest, nil); err != nil {
				return ExportedAccount{}, "", err
			}
		}
	}
	return ea, norm, nil
}

type parsedImportAccount struct {
	acc  ExportedAccount
	norm string
}

func parseAccountExport(data []byte) ([]parsedImportAccount, error) {
	var env struct {
		Version  *int            `json:"version"`
		Accounts json.RawMessage `json:"accounts"`
	}
	if err := decodeSingleJSON(data, &env, false); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadAccountExport, err)
	}
	if env.Version == nil {
		return nil, fmt.Errorf("%w: missing version", ErrBadAccountExport)
	}
	if *env.Version != AccountExportVersion {
		return nil, fmt.Errorf("%w: got version %d", ErrUnsupportedExportVersion, *env.Version)
	}
	if len(env.Accounts) == 0 || bytes.Equal(bytes.TrimSpace(env.Accounts), []byte("null")) {
		return nil, fmt.Errorf("%w: missing accounts array", ErrBadAccountExport)
	}

	var doc AccountExport
	if err := decodeSingleJSON(data, &doc, true); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadAccountExport, err)
	}

	seenProto := make(map[string]string, len(doc.Accounts))
	out := make([]parsedImportAccount, 0, len(doc.Accounts))
	for i, raw := range doc.Accounts {
		clean, norm, err := cleanExportedAccount(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: account %d: %v", ErrBadAccountExport, i, err)
		}
		if prev, ok := seenProto[norm]; ok && prev != clean.Receiving.Protocol {
			return nil, fmt.Errorf("%w: duplicate email %q with conflicting protocols", ErrBadAccountExport, clean.Email)
		}
		seenProto[norm] = clean.Receiving.Protocol
		out = append(out, parsedImportAccount{acc: clean, norm: norm})
	}
	return out, nil
}

func decodeSingleJSON(data []byte, dst any, strict bool) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("unexpected trailing JSON data")
	}
	return nil
}

func (s *Service) existingAccountProtocols(ctx context.Context) (map[string]string, bool, error) {
	rows, err := s.db.Query(ctx, "SELECT email, recv_protocol, is_default FROM accounts WHERE kind != ?", KindLocalArchive)
	if err != nil {
		return nil, false, err
	}
	byEmail := make(map[string]string, len(rows.Values))
	hasDefault := false
	for _, r := range rows.Values {
		byEmail[strings.ToLower(strings.TrimSpace(store.Str(r[0])))] = store.Str(r[1])
		if store.Int(r[2]) == 1 {
			hasDefault = true
		}
	}
	return byEmail, hasDefault, nil
}

// PreviewAccountImport validates an exported accounts file and lists each
// account with the servers it will connect to and whether it already exists.
func (s *Service) PreviewAccountImport(ctx context.Context, data []byte) (AccountImportPreview, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return AccountImportPreview{}, err
	}
	defer end()

	parsed, err := parseAccountExport(data)
	if err != nil {
		return AccountImportPreview{}, err
	}
	existing, _, err := s.existingAccountProtocols(ctx)
	if err != nil {
		return AccountImportPreview{}, err
	}

	seen := make(map[string]bool, len(parsed))
	entries := make([]AccountImportEntry, 0, len(parsed))
	for _, item := range parsed {
		ea := item.acc
		already := seen[item.norm]
		other := ""
		if proto, ok := existing[item.norm]; ok {
			already = true
			if proto != ea.Receiving.Protocol {
				other = proto
			}
		}
		seen[item.norm] = true
		entries = append(entries, AccountImportEntry{
			Email:             ea.Email,
			Name:              ea.Name,
			ReceivingProtocol: ea.Receiving.Protocol,
			ReceivingHost:     ea.Receiving.Host,
			ReceivingPort:     ea.Receiving.Port,
			SendingProtocol:   ea.Sending.Protocol,
			SendingHost:       ea.Sending.Host,
			SendingPort:       ea.Sending.Port,
			ReceiveOnly:       ea.ReceiveOnly,
			Auth:              ea.Receiving.Auth,
			Existing:          already,
			ExistingProtocol:  other,
		})
	}
	return AccountImportPreview{Accounts: entries}, nil
}

// ImportAccounts validates data and imports every new account in a single
// transaction with status reauth_required (credentials_missing), without
// dialing any server. Addresses already on this device are skipped and
// reported in Skipped, with the local protocol when it differs.
func (s *Service) ImportAccounts(ctx context.Context, data []byte) (AccountImportResult, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return AccountImportResult{}, err
	}
	defer end()

	parsed, err := parseAccountExport(data)
	if err != nil {
		return AccountImportResult{}, err
	}

	s.sigMu.Lock()
	existing, hasDefault, err := s.existingAccountProtocols(ctx)
	if err != nil {
		s.sigMu.Unlock()
		return AccountImportResult{}, err
	}

	seen := make(map[string]bool, len(parsed))
	toImport := make([]ExportedAccount, 0, len(parsed))
	skipped := make([]AccountImportSkip, 0)
	for _, item := range parsed {
		ea := item.acc
		if proto, ok := existing[item.norm]; ok {
			skip := AccountImportSkip{Email: ea.Email}
			if proto != ea.Receiving.Protocol {
				skip.ExistingProtocol = proto
			}
			skipped = append(skipped, skip)
			continue
		}
		if seen[item.norm] {
			skipped = append(skipped, AccountImportSkip{Email: ea.Email})
			continue
		}
		seen[item.norm] = true
		toImport = append(toImport, ea)
	}

	if len(toImport) == 0 {
		s.sigMu.Unlock()
		return AccountImportResult{Imported: []Account{}, Skipped: skipped}, nil
	}

	defaultIdx := -1
	if !hasDefault {
		defaultIdx = 0
		for i, ea := range toImport {
			if ea.IsDefault {
				defaultIdx = i
				break
			}
		}
	}

	rows, err := s.db.Query(ctx, `SELECT max(COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'signatures'), 0),
		COALESCE((SELECT max(id) FROM signatures), 0)) + 1`)
	if err != nil {
		s.sigMu.Unlock()
		return AccountImportResult{}, err
	}
	nextSigID := store.Int(rows.Values[0][0])

	type sigKey struct{ name, html string }
	sigIDs := make(map[sigKey]int64)
	var stmts []sqlite.Statement
	wroteSigs := false
	now := time.Now().Unix()
	insertIdxs := make([]int, 0, len(toImport))

	for i, ea := range toImport {
		var sigID any
		if ea.Signature != nil {
			key := sigKey{name: ea.Signature.Name, html: ea.Signature.HTML}
			id, ok := sigIDs[key]
			if !ok {
				id = nextSigID
				nextSigID++
				sigIDs[key] = id
				wroteSigs = true
				stmts = append(stmts, sqlite.Statement{
					SQL:  "INSERT INTO signatures(id, name) VALUES (?, ?)",
					Args: []any{id, ea.Signature.Name},
				})
				w, err := s.signatureWrite(ctx, id, ea.Signature.HTML)
				if err != nil {
					s.sigMu.Unlock()
					return AccountImportResult{}, fmt.Errorf("%w: %v", ErrBadAccountExport, err)
				}
				stmts = append(stmts, w...)
			}
			sigID = id
		}

		stmts = append(stmts, sqlite.Statement{
			SQL: `INSERT INTO accounts(email, name, alias, is_default, sync_limit, sync_since_months, recv_auth, send_auth, status, status_detail, username,
				recv_protocol, recv_host, recv_port, recv_security, recv_path, send_protocol, send_host, send_port, send_security, created_at, provider, oauth_client_id, features, receive_only, ai_instructions, label_keywords, signature_id, position)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'reauth_required', 'credentials_missing', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, (SELECT COALESCE(MAX(position) + 1, 0) FROM accounts))`,
			Args: []any{
				ea.Email, ea.Name, ea.Alias, b2i(i == defaultIdx), ea.Sync.Limit, ea.Sync.Months,
				ea.Receiving.Auth, ea.Sending.Auth, ea.Username,
				ea.Receiving.Protocol, ea.Receiving.Host, ea.Receiving.Port, string(ea.Receiving.Security), ea.Receiving.Path,
				ea.Sending.Protocol, ea.Sending.Host, ea.Sending.Port, string(ea.Sending.Security),
				now, ea.Provider, ea.OAuthClientID, b2i(ea.ReceiveOnly),
				ea.AIInstructions, b2i(ea.LabelKeywords), sigID,
			},
		})
		insertIdxs = append(insertIdxs, len(stmts)-1)

		if ea.Receiving.Protocol == auth.POP3 {
			for _, f := range [][2]string{{"Trash", "trash"}, {"Archive", "archive"}, {"Sent", "sent"}} {
				stmts = append(stmts, sqlite.Statement{
					SQL:  "INSERT INTO folders(account_id, name, role, role_since) VALUES ((SELECT id FROM accounts WHERE email = ?), ?, ?, unixepoch())",
					Args: []any{ea.Email, f[0], f[1]},
				})
			}
		}
	}

	res, err := s.db.Transaction(ctx, stmts)
	s.sigMu.Unlock()
	if err != nil {
		return AccountImportResult{}, err
	}
	if wroteSigs {
		s.bus.Publish(events.Event{Type: events.SignaturesChanged})
	}

	imported := make([]Account, 0, len(insertIdxs))
	for _, idx := range insertIdxs {
		id := res[idx].LastInsertID
		s.startWorker(id)
		acc, err := s.account(ctx, id)
		if err != nil {
			return AccountImportResult{}, err
		}
		imported = append(imported, acc)
	}
	return AccountImportResult{Imported: imported, Skipped: skipped}, nil
}
