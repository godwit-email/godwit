package mailsync

import "github.com/godwit-email/godwit/backend"

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
