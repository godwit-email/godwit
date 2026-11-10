package core

import (
	"context"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/conn"
)

// Security is the transport protection of a connection to a server.
type Security = conn.Security

const (
	SecurityTLS      = conn.TLS
	SecurityStartTLS = conn.StartTLS
	SecurityNone     = conn.None
)

// AccountInput is what the user supplies to add or test an account.
type AccountInput struct {
	Email, Name, Alias, Signature, Username, Password string // Password: Receiving.Auth's secret, an API token for AuthAPIToken
	// SendPassword is the sending server's own password; "" signs in to it
	// with Password, which an API token cannot.
	SendPassword       string
	IsDefault          bool
	Sync               *SyncRange // nil: the newest 2000 per folder
	Receiving, Sending Server
	Provider           string // auth.Provider ID; empty is stored as other
	// OAuthFlowID names a finished AwaitOAuth flow: the account signs in with its
	// tokens instead of Password, and Email must be the signed-in address.
	OAuthFlowID string
	// ReceiveOnly adds the account although its SMTP check fails (the settings
	// and IMAP steps must pass); it then only receives mail. A passing SMTP
	// check adds it as usual.
	ReceiveOnly   bool
	oauthClientID string // the flow's own client, set by AddAccount
	// graph checks a stored OAuth account can send through Microsoft Graph, for
	// a check whose SMTP AUTH is disabled; a flow's check makes its own.
	graph     func(context.Context) error
	allowPOP3 bool // a POP3 receiving slot is accepted: a stored account's, or ExperimentalPOP3 is on
}

// Account is a stored account. The password lives only in the system keychain.
// Status is ok; reauth_required when the password is missing from the keychain
// (StatusDetail credentials_missing) or the IMAP or SMTP server refuses it
// (auth_rejected) or the provider revoked an OAuth grant (token_revoked) or
// refuses the user's own OAuth client (client_rejected); or
// error when the keychain is unavailable (keychain_unavailable). An OAuth
// account's keychain entry holds its refresh token; access tokens stay in memory.
type Account struct {
	ID                                      int64
	Email, Name, Alias, Signature, Username string // Signature is SignatureID's (Signature.HTML), "" for none
	SignatureID                             int64  // its default signature, 0 none
	Icon                                    string // '' the provider's mark, label:<name>, brand:<provider id> or image:<n> (AccountIcon)
	AIInstructions                          string // the user's facts and guidelines for Wit drafting from this account
	IsDefault                               bool
	LabelKeywords                           bool // labels and the server's IMAP keywords follow each other (ADR 0035)
	Sync                                    SyncRange
	Folders                                 int // folders synced so far: 0 before the first sync
	AuthType, Status                        string
	SendRefused                             bool   // the reauth_required is only SMTP's (or Graph's) refusal: receiving signs in
	Receiving, Sending                      Server // a local archive's are zero
	Provider, StatusDetail                  string
	OAuthClientID                           string // the user's own OAuth client (OAuthClient), "" the provider's
	Kind                                    string // "" a mail account, KindLocalArchive
	ReceiveOnly                             bool   // added without working SMTP: it syncs but never sends (ErrReceiveOnly)
	// Features is what its receiving server can do (协议能力), recorded at
	// the add-account check, a server change and each sign-in (ADR 0034);
	// nil before the first. See Unsupported.
	Features map[backend.Feature]backend.Support
}

// KindLocalArchive is an account with no servers or credentials: it never
// syncs and never sends, and its folders are only local (ADR 0036). Its Email
// is a unique placeholder, never a sender address.
const KindLocalArchive = "local_archive"

// Folder is a mailbox of an account. Role is inbox, sent, trash, drafts, junk,
// archive or empty.
type Folder struct {
	ID, AccountID int64
	Name, Role    string
	Delim         string // the server's hierarchy delimiter in Name, "" when flat
	Unread        int
	Total         int
}

// ServerSettings replaces an account's username and servers. An empty
// Username means the email address.
type ServerSettings struct {
	Username           string
	Receiving, Sending Server
}

// Server is one of an account's two server slots, receiving and sending: a
// protocol and where its server is. Receiving is auth.IMAP, auth.JMAP
// (only with Settings.ExperimentalJMAP to add one) or auth.POP3 (only with
// Settings.ExperimentalPOP3) and sending auth.SMTP;
// an empty Protocol in input is auth.IMAP and auth.SMTP.
type Server struct {
	Protocol string
	Host     string
	Port     int
	Security Security // JMAP: SecurityTLS is https, SecurityNone http (localhost only)
	// Path is JMAP's session resource on Host, as a session URL typed in
	// gives it; "" discovers it at /.well-known/jmap. Other protocols have none.
	Path string
	// Auth is how the slot signs in: AuthPassword ("" in input), AuthAPIToken
	// (JMAP receiving only) or AuthOAuth, which only an OAuth flow sets.
	Auth string
}

// How a server slot signs in (Server.Auth).
const (
	AuthPassword = "password"  // a password: IMAP LOGIN, SMTP AUTH, HTTP Basic
	AuthAPIToken = "api_token" // an API token sent as HTTP Bearer (JMAP)
	AuthOAuth    = "oauth"     // an OAuth access token
)

// SyncRange is how much of each folder a sync fetches: the newest Limit
// messages, the mail of the last Months months, or (both 0) all of it.
// Narrowing it only stops fetching older mail; nothing stored is deleted.
type SyncRange struct{ Limit, Months int }

// MaxAIInstructions is the most runes an account's AI instructions have:
// every draft from the account sends them to the model.
const MaxAIInstructions = 2000

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
	AIInstructions         *string // trimmed, at most MaxAIInstructions runes
	LabelKeywords          *bool   // turning it on imports the server's keywords at once
}

// AccountIcon is an account's uploaded icon image.
type AccountIcon struct {
	ContentType string
	Data        []byte
}
