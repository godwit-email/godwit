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
	Sync                                              *SyncRange // nil: the newest 2000 per folder
	IMAPHost                                          string
	IMAPPort                                          int
	IMAPSecurity                                      Security
	SMTPHost                                          string
	SMTPPort                                          int
	SMTPSecurity                                      Security
	Provider                                          string // auth.Provider ID; empty is stored as other
	// OAuthFlowID names a finished AwaitOAuth flow: the account signs in with its
	// tokens instead of Password, and Email must be the signed-in address.
	OAuthFlowID string
}

// Account is a stored account. The password lives only in the system keychain.
// Status is ok; reauth_required when the password is missing from the keychain
// (StatusDetail credentials_missing) or the IMAP or SMTP server refuses it
// (auth_rejected) or the provider revoked an OAuth grant (token_revoked); or
// error when the keychain is unavailable (keychain_unavailable). An OAuth
// account's keychain entry holds its refresh token; access tokens stay in memory.
type Account struct {
	ID                                      int64
	Email, Name, Alias, Signature, Username string // Signature is SignatureID's (Signature.HTML), "" for none
	SignatureID                             int64  // its default signature, 0 none
	Icon                                    string // '' the provider's mark, label:<name>, brand:<provider id> or image:<n> (AccountIcon)
	IsDefault                               bool
	Sync                                    SyncRange
	Folders                                 int // folders synced so far: 0 before the first sync
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
	Delim         string // the server's hierarchy delimiter in Name, "" when flat
	Unread        int
}

// ServerSettings replaces an account's username and IMAP and SMTP servers. An
// empty Username means the email address.
type ServerSettings struct {
	Username     string
	IMAPHost     string
	IMAPPort     int
	IMAPSecurity Security
	SMTPHost     string
	SMTPPort     int
	SMTPSecurity Security
}

// SyncRange is how much of each folder a sync fetches: the newest Limit
// messages, the mail of the last Months months, or (both 0) all of it.
// Narrowing it only stops fetching older mail; nothing stored is deleted.
type SyncRange struct{ Limit, Months int }

// AccountPatch changes the account settings that are not nil. Signature
// rewrites the account's default signature (for every account using it),
// making one named as the account if it has none; SignatureID picks it, 0
// for none.
type AccountPatch struct {
	Name, Alias, Signature *string
	SignatureID            *int64
	IsDefault              *bool
	Sync                   *SyncRange
	Servers                *ServerSettings
	Icon                   *string // '', label:<name> or brand:<provider id>
	IconImage              []byte  // an uploaded png, jpeg, gif, webp or svg: Icon becomes image:<n>
}

// AccountIcon is an account's uploaded icon image.
type AccountIcon struct {
	ContentType string
	Data        []byte
}
