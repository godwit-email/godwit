package mailsync

import (
	"context"

	"github.com/godwit-email/godwit/backend"
)

// InboxOnly is the condition that the account bound to ? has only an inbox on
// its server (backend.IdentityInboxOnly, POP3), as its check recorded. Such an
// account keeps its mail locally (spec #627): a location gone from the server
// only loses its server_ref, moves are local, and a purge DELEs the message
// at its inbox location wherever it shows.
const InboxOnly = "EXISTS (SELECT 1 FROM accounts WHERE id = ? AND CASE WHEN json_valid(features) THEN json_extract(features, '$.Identity') END = '" + string(backend.IdentityInboxOnly) + "')"

// Originals keeps the originals of an inbox-only account's mail, which its
// server may delete: core stores them as a local archive does, by
// content_hash, and reads body, attachments and raw from them.
type Originals interface {
	Keep(hash string, raw []byte) error
	// Drop removes the originals of messages deleted for good, best effort.
	Drop(hashes []string)
}

// Core is core's part in a sync of one account: it keeps an inbox-only
// account's originals, and picks the 期限 a thread merge keeps.
type Core interface {
	Originals
	// MergedReplyDate is, of threads about to merge into one conversation,
	// the one whose thread_deadlines row the merged conversation keeps (0:
	// none has one). Which are in effect is 回复状态's to say, and only core
	// computes that.
	MergedReplyDate(ctx context.Context, threads []int64) (int64, error)
}
