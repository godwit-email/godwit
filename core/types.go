package core

import "github.com/godwitmail/godwit/core/conn"

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
}

// Account is a stored account. The password lives only in the system keychain.
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
}

// Folder is a mailbox of an account. Role is inbox, sent, trash, drafts, junk,
// archive or empty.
type Folder struct {
	ID, AccountID int64
	Name, Role    string
	Unread        int
}
