// Package backend is the seam between Godwit and a mail protocol (ADR 0037).
// A protocol backend (GLOSSARY: 协议后端) speaks its protocol and hands over
// protocol-neutral change sets; storing them, claiming, orphans, merging and
// the order of a sync round stay local. This package and its backends depend
// on neither core nor store.
//
// An account has one Receiver and one Sender. A backend owns its connections:
// it dials, reconnects and keeps sessions itself, takes credentials from the
// CredentialsFunc it is given, and reports refused credentials as ErrAuthFailed.
package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrAuthFailed means the server refused the credentials. Core then refreshes
// a token and retries, or marks the account as needing reauthorization.
// Wrap it; server text is never matched.
var ErrAuthFailed = errors.New("authentication failed")

// ErrUnsupported is Watch's answer when the server cannot push: core polls at
// Description.PollInterval instead.
var ErrUnsupported = errors.New("backend: not supported")

// ErrNotFound means a server reference no longer names anything on the server.
var ErrNotFound = errors.New("backend: not found on the server")

// ErrNotSynced is Sync with SyncRequest.Older on a folder whose first sync at
// the server's current state has not completed: a full sync comes first.
var ErrNotSynced = errors.New("folder not synced yet")

// FolderError is the server's refusal of one folder in a sync round. The
// round went on with the other folders; Sync returns the refusals joined.
type FolderError struct {
	Folder FolderRef
	Err    error
}

func (e *FolderError) Error() string { return e.Err.Error() }
func (e *FolderError) Unwrap() error { return e.Err }

// ErrUncertain means the whole message reached the server but its verdict did
// not come back: it may have been accepted. Retrying could deliver it twice
// (ADR 0006).
var ErrUncertain = errors.New("backend: the server may have accepted the message")

// RejectedError is the server's refusal of a message it was given to deliver.
// The server has not accepted the message.
type RejectedError struct {
	Stage     string // the refused step, such as SMTP's "MAIL", "RCPT" or "DATA"
	Recipient string // the refused address, "" when the refusal is not about one
	Code      int    // the reply code, such as 550: 4xx refuses for now, 5xx for good
	Enhanced  string // the enhanced status code, such as "5.1.1"; "" if none
	Message   string // the server's text as it sent it
}

func (e *RejectedError) Error() string {
	at := e.Stage
	if e.Recipient != "" {
		at += " " + e.Recipient
	}
	return fmt.Sprintf("backend: the server refused %s: %d %s %s", at, e.Code, e.Enhanced, e.Message)
}

// Credentials sign in to an account: Username with either Password (IMAP
// LOGIN, SMTP AUTH, HTTP Basic) or Token (XOAUTH2, HTTP Bearer).
type Credentials struct {
	Username, Password, Token string
}

// CredentialsFunc gives the account's current credentials. A backend calls it
// for each sign-in, so a token core refreshed after ErrAuthFailed is used.
type CredentialsFunc func(ctx context.Context) (Credentials, error)

// FolderRef is an opaque reference to a folder on the server, stable across a
// rename: IMAP uses the name, other protocols the server's folder id.
type FolderRef string

// MessageRef is an opaque server location of a message within one folder
// (GLOSSARY: 服务器位置): IMAP encodes UIDVALIDITY and UID, JMAP uses the
// Email id. Only the backend that gave it reads it.
type MessageRef string

// PartRef is an opaque reference to one MIME part of a message, from Part.Ref.
type PartRef string

// Cursor is an opaque sync position, at the account or at one folder. The
// backend decides which it uses; local storage keeps it as it is given.
type Cursor string

// Location is one server location of a message.
type Location struct {
	Folder FolderRef
	Ref    MessageRef
	// ServerID is the message's identity when the account's is IdentityStable,
	// else "". A backend may use it to find the message's other locations.
	ServerID string
}

// Flags are a message's flags, written protocol-neutrally. Local storage
// converts them to its own \Seen spelling at the boundary.
type Flags struct {
	Seen, Flagged, Answered, Draft bool
	Keywords                       []string // custom keywords, as the server spells them
}

// Role is what a folder is for, as the backend recognizes it; "" for none.
type Role string

const (
	RoleInbox   Role = "inbox"
	RoleSent    Role = "sent"
	RoleDrafts  Role = "drafts"
	RoleTrash   Role = "trash"
	RoleJunk    Role = "junk"
	RoleArchive Role = "archive"
	RoleAll     Role = "all"     // every message, such as Gmail's All Mail
	RoleFlagged Role = "flagged" // a server view of flagged mail, such as Gmail's Starred
)

// Folder is a folder on the server.
type Folder struct {
	Ref    FolderRef
	Name   string    // the full name the user sees, segments joined by Delim
	Delim  string    // the hierarchy delimiter in Name, "" when flat
	Parent FolderRef // "" at the top level
	Role   Role
}

// Part is a node of a message's MIME structure.
type Part struct {
	Ref               PartRef // for Fetch; set on leaves
	ContentType       string  // lower case "type/subtype"
	Params            map[string]string
	Disposition       string // lower case, such as "attachment"; "" if none
	DispositionParams map[string]string
	ContentID         string
	Encoding          string // Content-Transfer-Encoding, lower case
	Size              int64  // encoded size in bytes
	Children          []Part // the parts of a multipart, in order
}

// Change is one entry of a change set: FolderUpdated, FolderGone,
// FolderReset, MessageAppeared, MessageGone, FlagsChanged or FolderSnapshot.
type Change interface{ change() }

// FolderUpdated reports a folder that appeared or changed (a rename keeps Ref).
type FolderUpdated struct{ Folder }

// FolderGone reports a folder deleted on the server.
type FolderGone struct{ Ref FolderRef }

// FolderReset voids every server location in the folder (IMAP's UIDVALIDITY
// changed, JMAP cannot calculate changes): local storage forgets them and
// claims the messages again as they reappear. The folder's cursor is cleared.
type FolderReset struct{ Ref FolderRef }

// MessageAppeared reports a message at a location local storage may not know.
type MessageAppeared struct {
	Location
	Flags Flags
	// Backfill is true for older mail filled in, false for a new arrival.
	// Notifications, rules and message.received follow it.
	Backfill bool
	// Content is nil when SyncRequest.Local reported ServerID as known: only
	// a new location of a stored message. It is nil too for a location Local
	// holds without a ServerID (Local.Held): the change gives it its ServerID.
	Content *Content
}

// Content is what a sync stores of a message.
type Content struct {
	Size   int64
	Date   time.Time // when the server received it
	Header []byte    // the SyncRequest.HeaderFields the message has, raw
	// Structure is the MIME structure; nil when the backend cannot give one,
	// and then Raw holds the whole message.
	Structure *Part
	Text      map[PartRef][]byte // the first SyncRequest.TextBytes of each SyncRequest.TextParts part
	Raw       []byte
}

// MessageGone reports a location gone from the server.
type MessageGone struct{ Location }

// FlagsChanged reports a message's flags as they now are.
type FlagsChanged struct {
	Location
	Flags Flags
}

// FolderSnapshot is the whole folder as the server now has it: every location
// and its flags. Local locations missing from it are gone; local flags that
// differ changed. Locations local storage does not hold are ignored.
type FolderSnapshot struct {
	Folder   FolderRef
	Messages map[MessageRef]Flags
}

func (FolderUpdated) change()   {}
func (FolderGone) change()      {}
func (FolderReset) change()     {}
func (MessageAppeared) change() {}
func (MessageGone) change()     {}
func (FlagsChanged) change()    {}
func (FolderSnapshot) change()  {}

// FolderState is what local storage keeps of a folder's sync: the backend's
// cursor and the local progress the backend reports back.
type FolderState struct {
	Cursor Cursor
	Synced bool      // a first sync at this cursor completed
	Filled time.Time // a range backfill completed down to this date; zero for none
}

// Window is how much mail the account keeps per folder: the newest Newest
// messages if Newest > 0, else the mail since Since if set, else all.
type Window struct {
	Newest int
	Since  time.Time
}

// Local answers what local storage already holds, so a backend fetches no
// more than it must. A nil Local holds nothing.
type Local interface {
	// KnownIDs reports which of ids (Location.ServerID) are stored.
	KnownIDs(ctx context.Context, ids []string) (map[string]bool, error)
	// HeldRefs reports which of refs in folder are stored.
	HeldRefs(ctx context.Context, folder FolderRef, refs []MessageRef) (map[MessageRef]bool, error)
	// Held is every location stored in folder, with its ServerID ("" when
	// none is stored).
	Held(ctx context.Context, folder FolderRef) (map[MessageRef]string, error)
	// Lost is the ServerIDs of stored messages left at no server location,
	// which local storage is about to delete as gone. A backend that still
	// finds one reports it as a MessageAppeared without content.
	Lost(ctx context.Context) ([]string, error)
}

// SyncRequest is one sync round's input.
type SyncRequest struct {
	Account Cursor                    // the account's cursor, "" at first
	Folders map[FolderRef]FolderState // the folders local storage holds
	Only    []FolderRef               // sync only these folders (a Watch wake); nil for all
	Window  Window
	// Older, with Only, fetches only up to this many messages of each folder
	// that the window left out, newest first (the user's 加载更早); more then
	// reports that the server holds still more. ErrNotSynced: a full sync of
	// the folder comes first.
	Older int
	// Pages bounds how many pages a backfill takes this round; 0 for no bound.
	Pages int
	// PageSize is how many messages one page fetches at most; 0 for the
	// backend's own.
	PageSize     int
	HeaderFields []string
	TextBytes    int64
	// TextParts picks the parts of a structure whose text a sync stores.
	TextParts func(*Part) []PartRef
	Local     Local
}

// Page is one page of a change set. Local storage writes its changes and its
// cursors in one transaction.
type Page struct {
	Changes []Change
	Account Cursor                    // the account's new cursor; "" keeps it
	Folders map[FolderRef]FolderState // the new state of the folders this page advanced
}

// ContentRequest names content to fetch: the whole message, one part, or
// some header fields.
type ContentRequest struct {
	Location
	Part         PartRef  // "" for the whole message
	HeaderFields []string // non-nil: only these header fields of the message
}

// Op is one write-back of a local change: SetFlags, Move, Copy, AddToFolder,
// RemoveFromFolder, Delete, Purge or Append.
type Op interface{ op() }

// SetFlags adds and removes flags of the message at At.
type SetFlags struct {
	At          Location
	Add, Remove Flags
}

// Move moves the message from At to folder To.
type Move struct {
	At Location
	To FolderRef
}

// Copy puts an independent copy of the message at At into folder To.
type Copy struct {
	At Location
	To FolderRef
}

// AddToFolder gives the message at At one more location, in folder To: the
// same message, not a copy (Gmail labels, JMAP mailboxIds).
type AddToFolder struct {
	At Location
	To FolderRef
}

// RemoveFromFolder takes the location At away; the message stays in its others.
type RemoveFromFolder struct{ At Location }

// Delete deletes the message at At the server's own way: to its trash where
// it has one.
type Delete struct{ At Location }

// Purge deletes the message at At for good (永久删除).
type Purge struct{ At Location }

// Append stores Raw into folder To, as with a sent copy. A backend does not
// store it again when To already holds a message with MessageID and Raw's size.
type Append struct {
	To        FolderRef
	Raw       []byte
	Flags     Flags
	Date      time.Time
	MessageID string // without angle brackets
}

func (SetFlags) op()         {}
func (Move) op()             {}
func (Copy) op()             {}
func (AddToFolder) op()      {}
func (RemoveFromFolder) op() {}
func (Delete) op()           {}
func (Purge) op()            {}
func (Append) op()           {}

// Outcome is how a write-back ended.
type Outcome string

const (
	// Done: Result.Ref is the message's new location in the target folder
	// (Move, Copy, AddToFolder, Append), "" for the others.
	Done Outcome = "done"
	// Unlocated: done, but the server did not say where the message is now.
	// Local storage claims it by Message-ID and size (ADR 0005); a round with
	// one pushes a second time.
	Unlocated Outcome = "unlocated"
	// Failed: the server refused it for good; retrying will not help.
	Failed Outcome = "failed"
	// Unsupported: this backend or server cannot do it; Result.Reason says why.
	Unsupported Outcome = "unsupported"
)

// Result is a write-back's outcome.
type Result struct {
	Outcome Outcome
	Ref     MessageRef
	Reason  string // for Failed and Unsupported: a Unsupported reason is the feature's
}

// Feature is something a backend may or may not do on an account (GLOSSARY:
// 协议能力). A difference that changes the user's data is a different
// Feature; one of precision or matching is a note on the same Feature.
type Feature string

const (
	FeatureMove             Feature = "move"
	FeatureCopy             Feature = "copy"          // an independent copy
	FeatureAddToFolder      Feature = "add_to_folder" // the same message in one more folder
	FeatureRemoveFromFolder Feature = "remove_from_folder"
	FeaturePurge            Feature = "purge"
	FeatureAppend           Feature = "append"
	FeatureSeen             Feature = "seen"
	FeatureFlagged          Feature = "flagged"
	FeatureAnswered         Feature = "answered"
	FeatureDraft            Feature = "draft"
	FeatureKeywords         Feature = "keywords"
	FeatureFolderCreate     Feature = "folder_create"
	FeatureFolderRename     Feature = "folder_rename"
	FeatureFolderDelete     Feature = "folder_delete" // the folder and the mail in it
	FeatureLabelDelete      Feature = "label_delete"  // the folder only; its mail stays
	FeatureSavesSent        Feature = "saves_sent"    // the server stores sent copies itself
	FeaturePush             Feature = "push"
)

// AllFeatures lists every Feature, in order.
var AllFeatures = []Feature{
	FeatureMove, FeatureCopy, FeatureAddToFolder, FeatureRemoveFromFolder, FeaturePurge, FeatureAppend,
	FeatureSeen, FeatureFlagged, FeatureAnswered, FeatureDraft, FeatureKeywords,
	FeatureFolderCreate, FeatureFolderRename, FeatureFolderDelete, FeatureLabelDelete,
	FeatureSavesSent, FeaturePush,
}

// Level is how far a Feature is supported.
type Level string

const (
	LevelSupported   Level = "supported"
	LevelNoted       Level = "noted" // supported, with a note
	LevelUnsupported Level = "unsupported"
)

// Support is one Feature's level. Reason is a stable key the UI and Wit
// translate, such as "imap.no_uidplus": the note for LevelNoted, why not for
// LevelUnsupported, "" for LevelSupported.
type Support struct {
	Level  Level
	Reason string
}

// Identity is what identifies a message on the account.
type Identity string

const (
	// IdentityStable: Location.ServerID is stable within the account (Gmail
	// IMAP, JMAP); one message may have several locations.
	IdentityStable Identity = "stable"
	// IdentityLocation: only the location identifies it (plain IMAP, EWS).
	IdentityLocation Identity = "location"
	// IdentityInboxOnly: only an inbox (POP3); mail gone from the server
	// stays local.
	IdentityInboxOnly Identity = "inbox_only"
)

// Description is what a backend declares of an account. The protocol fixes
// part of Features; the rest depends on the server, so core records it after
// connecting (ADR 0034's moments: the add-account check, a server change, a
// sign-in).
type Description struct {
	Features     map[Feature]Support // one entry for each of AllFeatures
	Identity     Identity
	PollInterval time.Duration // how often to sync when not woken by Watch
}

// CheckResult is the add-account check's finding.
type CheckResult struct {
	Host    string // the host the backend actually talks to, such as JMAP's session host
	Folders int
	Description
}

// Receiver is an account's receiving backend.
type Receiver interface {
	// Sync runs one sync round, handing the change set to commit page by
	// page. more reports that a backfill used its Pages with work left.
	// An error from commit stops the round and is returned.
	Sync(ctx context.Context, req SyncRequest, commit func(Page) error) (more bool, err error)
	// Fetch reads content by server reference; ErrNotFound when it is gone.
	Fetch(ctx context.Context, req ContentRequest) (io.ReadCloser, error)
	// Find lists the messages in folder whose Message-ID (without angle
	// brackets) is messageID and, when size > 0, of that size.
	Find(ctx context.Context, folder FolderRef, messageID string, size int64) ([]MessageRef, error)
	// Apply writes one Op back. An error means the outcome is not known (a
	// lost connection): the op is tried again later.
	Apply(ctx context.Context, op Op) (Result, error)
	// Watch calls wake when the server has changes, with the folders that
	// changed (nil: anywhere), until ctx ends or the connection fails.
	// ErrUnsupported: the server cannot push.
	Watch(ctx context.Context, wake func(folders []FolderRef)) error
	// Check is the add-account check.
	Check(ctx context.Context) (CheckResult, error)
	// Describe declares the account's features, identity and poll interval.
	Describe(ctx context.Context) (Description, error)
	Close() error
}

// Sender is an account's sending backend. The outbox, approvals and sending
// at most once stay in core (ADR 0003, 0006).
type Sender interface {
	// Send delivers raw to rcpts. nil: accepted. ErrUncertain: it may have
	// been. *RejectedError: refused. Any other error came before the server
	// could accept it, so retrying is safe.
	Send(ctx context.Context, from string, rcpts []string, raw []byte) error
	// SavesSent reports that the server stores a sent copy itself; else the
	// Receiver Appends one to the sent folder.
	SavesSent() bool
}
