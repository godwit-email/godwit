package core

import "github.com/godwit-email/godwit/core/conn"

// Security is the transport protection of an IMAP or SMTP connection.
type Security = conn.Security

const (
	SecurityTLS      = conn.TLS
	SecurityStartTLS = conn.StartTLS
	SecurityNone     = conn.None
)

// AccountInput is what the user supplies to add or test an account.
type AccountInput struct {
	Email, Name, Alias, Signature, Username, Password string
	IsDefault                                         bool
	SyncLimit                                         int
	IMAPHost                                          string
	IMAPPort                                          int
	IMAPSecurity                                      Security
	SMTPHost                                          string
	SMTPPort                                          int
	SMTPSecurity                                      Security
	Provider                                          string // auth.Provider ID; empty is stored as other
}

// Account is a stored account. The password lives only in the system keychain.
// Status is ok; reauth_required when the password is missing from the keychain
// (StatusDetail credentials_missing) or the IMAP or SMTP server refuses it
// (auth_rejected); or error when the keychain is unavailable (keychain_unavailable).
type Account struct {
	ID                                      int64
	Email, Name, Alias, Signature, Username string
	IsDefault                               bool
	SyncLimit                               int
	AuthType, Status                        string
	IMAPHost                                string
	IMAPPort                                int
	IMAPSecurity                            Security
	SMTPHost                                string
	SMTPPort                                int
	SMTPSecurity                            Security
	Provider, StatusDetail                  string
}

// Folder is a mailbox of an account. Role is inbox, sent, trash, drafts, junk,
// archive or empty.
type Folder struct {
	ID, AccountID int64
	Name, Role    string
	Unread        int
}
