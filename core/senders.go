package core

import (
	"cmp"
	"context"
	"net/mail"
	"slices"
	"strings"
	"time"

	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
)

// Sender is one address's mail, as the senders view lists it: Unread and
// Inbox count within Messages; ListMessage is its newest message with a
// List-Unsubscribe header (0 none), in ListThread.
type Sender struct {
	Name, Address           string
	Messages, Unread, Inbox int
	Last                    time.Time
	ListMessage, ListThread int64
}

// SenderArchive is what ArchiveSender moved, each message with the folder
// it left and the one it went to, and how many it left in the inbox.
type SenderArchive struct {
	Moved   []ArchivedMessage
	Skipped int
}

// ArchivedMessage is a message ArchiveSender moved From a folder To another.
type ArchivedMessage struct{ ID, From, To int64 }

// roleFolderIDs are every account's folders of roles, one per role as
// RoleFolder resolves it (a folder detected with a role the user moved
// elsewhere is not one).
func (s *Service) roleFolderIDs(ctx context.Context, roles ...string) ([]any, error) {
	accs, err := s.db.Query(ctx, "SELECT id FROM accounts")
	if err != nil {
		return nil, err
	}
	var ids []any
	for _, r := range accs.Values {
		for _, role := range roles {
			id, _, err := mailsync.RoleFolder(ctx, s.db, store.Int(r[0]), role)
			if err != nil {
				return nil, err
			}
			if id != 0 {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}

// Senders counts every sender's synced mail outside Sent, Drafts, Trash and
// Junk, the user's own accounts left out, most mail first.
func (s *Service) Senders(ctx context.Context) ([]Sender, error) {
	own, err := s.db.Query(ctx, "SELECT email FROM accounts")
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{}
	for _, r := range own.Values {
		skip[gomail.AddrKey(store.Str(r[0]))] = true
	}
	left, err := s.roleFolderIDs(ctx, "sent", "drafts", "trash", "junk")
	if err != nil {
		return nil, err
	}
	from := " FROM messages m JOIN folders f ON f.id = m.folder_id WHERE m.folder_id NOT IN (" + placeholders(len(left)) + ")"
	// shortcut: groups every sender in Go on each load; keep a senders table if large stores get slow.
	rows, err := s.db.Query(ctx, `SELECT m.from_addr, count(*), sum(instr(m.flags, '\Seen') = 0), sum(f.role = 'inbox'), max(m.date)`+
		from+" GROUP BY m.from_addr ORDER BY 5, 1", left...)
	if err != nil {
		return nil, err
	}
	// A bare column beside max() comes from the row holding the max.
	lists, err := s.db.Query(ctx, "SELECT m.from_addr, max(m.date), m.id, m.thread_id"+from+
		" AND m.list_unsubscribe <> '' GROUP BY m.from_addr", left...)
	if err != nil {
		return nil, err
	}
	byAddr := map[string]*Sender{}
	for _, r := range rows.Values {
		h := store.Str(r[0])
		addr := gomail.AddrKey(h)
		if addr == "" || skip[addr] {
			continue
		}
		c := byAddr[addr]
		if c == nil {
			c = &Sender{Address: addr}
			byAddr[addr] = c
		}
		if a, err := mail.ParseAddress(h); err == nil && a.Name != "" {
			c.Name = a.Name // rows come oldest first: the latest name wins
		}
		c.Messages += int(store.Int(r[1]))
		c.Unread += int(store.Int(r[2]))
		c.Inbox += int(store.Int(r[3]))
		c.Last = time.Unix(store.Int(r[4]), 0)
	}
	listDate := map[string]int64{}
	for _, r := range lists.Values {
		addr := gomail.AddrKey(store.Str(r[0]))
		c := byAddr[addr]
		if c == nil || store.Int(r[1]) < listDate[addr] {
			continue
		}
		listDate[addr], c.ListMessage, c.ListThread = store.Int(r[1]), store.Int(r[2]), store.Int(r[3])
	}
	out := make([]Sender, 0, len(byAddr))
	for _, c := range byAddr {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b Sender) int {
		return cmp.Or(cmp.Compare(b.Messages, a.Messages), b.Last.Compare(a.Last), strings.Compare(a.Address, b.Address))
	})
	return out, nil
}

// ArchiveSender moves the inbox mail from addr (a From header or its
// address) to each account's archive, except mail in a thread the user
// starred or replied to (a message \Flagged, \Answered or in Sent). An
// account without an archive folder fails before anything moves.
func (s *Service) ArchiveSender(ctx context.Context, addr string) (SenderArchive, error) {
	var res SenderArchive
	q, err := s.withSenders(ctx, SearchQuery{FromAddr: gomail.AddrKey(addr)})
	if err != nil || len(q.fromHeaders) == 0 {
		return res, err
	}
	sent, err := s.roleFolderIDs(ctx, "sent")
	if err != nil {
		return res, err
	}
	args := sent
	for _, h := range q.fromHeaders {
		args = append(args, h)
	}
	kept := `instr(t.flags, '\Flagged') > 0 OR instr(t.flags, '\Answered') > 0 OR t.folder_id IN (` + placeholders(len(sent)) + `)`
	rows, err := s.db.Query(ctx, `SELECT m.id, m.account_id, m.folder_id, EXISTS (SELECT 1 FROM messages t WHERE (t.id = m.id OR t.thread_id = m.thread_id) AND (`+kept+`))
		FROM messages m WHERE m.folder_id IN (SELECT id FROM folders WHERE role = 'inbox') AND m.from_addr IN (`+
		placeholders(len(q.fromHeaders))+") ORDER BY m.id", args...)
	if err != nil {
		return res, err
	}
	byAcc, archive := map[int64][]ArchivedMessage{}, map[int64]int64{}
	for _, r := range rows.Values {
		if store.Int(r[3]) != 0 {
			res.Skipped++
			continue
		}
		acc := store.Int(r[1])
		if _, ok := archive[acc]; !ok {
			if archive[acc], err = s.roleFolder(ctx, acc, "archive"); err != nil {
				return res, err
			}
		}
		byAcc[acc] = append(byAcc[acc], ArchivedMessage{ID: store.Int(r[0]), From: store.Int(r[2]), To: archive[acc]})
	}
	for acc, ms := range byAcc {
		for c := range slices.Chunk(ms, 200) {
			if err := s.Move(ctx, archivedIDs(c), archive[acc]); err != nil {
				return res, err
			}
			res.Moved = append(res.Moved, c...)
		}
	}
	return res, nil
}

// UndoArchive moves a's messages back to the folders they left, those still
// where it put them; ones moved since or gone stay.
func (s *Service) UndoArchive(ctx context.Context, a SenderArchive) error {
	back := map[ArchivedMessage][]any{} // by From and To, ID 0
	for _, m := range a.Moved {
		back[ArchivedMessage{From: m.From, To: m.To}] = append(back[ArchivedMessage{From: m.From, To: m.To}], m.ID)
	}
	for k, ids := range back {
		for c := range slices.Chunk(ids, 200) {
			rows, err := s.db.Query(ctx, "SELECT id FROM messages WHERE folder_id = ? AND id IN ("+placeholders(len(c))+")", append([]any{k.To}, c...)...)
			if err != nil {
				return err
			}
			var still []int64
			for _, r := range rows.Values {
				still = append(still, store.Int(r[0]))
			}
			if err := s.Move(ctx, still, k.From); err != nil {
				return err
			}
		}
	}
	return nil
}

func archivedIDs(ms []ArchivedMessage) []int64 {
	ids := make([]int64, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	return ids
}
