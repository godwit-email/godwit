package core

import (
	"cmp"
	"context"
	"encoding/json"
	"net/mail"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
)

// Contact is someone the user exchanged synced mail with, by address
// (gomail.AddrKey) across accounts; the user's own accounts are none. Name is
// the display name their mail used most (a notifier's own, not whoever
// triggered its latest), else the one the user wrote to them.
// Received counts their mail outside Sent, Drafts, Trash and Junk, Sent the
// user's mail in Sent to or copying them, Messages both. Unread and Inbox
// count within Received; ListMessage is their newest received message with a
// List-Unsubscribe header (0 none); Blocked is whether they are a blocked
// sender.
type Contact struct {
	Name, Address                 string
	Messages                      int
	Last                          time.Time
	Received, Sent, Unread, Inbox int
	ListMessage                   int64
	Blocked                       bool
}

// ContactDetail is what ArchiveSender would do with a contact's inbox mail
// now: move Archivable messages, leave Kept in the inbox.
type ContactDetail struct {
	Archivable, Kept int
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

// Contacts lists everyone the user exchanged mail with, most mail first.
func (s *Service) Contacts(ctx context.Context) ([]Contact, error) {
	all, _, err := s.contacts(ctx, 0)
	return all, err
}

// contacts gathers the contacts of accountID's mail (0: every account's),
// most mail first, with every name each used, oldest first.
func (s *Service) contacts(ctx context.Context, accountID int64) ([]Contact, map[string][]string, error) {
	own, err := s.db.Query(ctx, "SELECT email FROM accounts")
	if err != nil {
		return nil, nil, err
	}
	skip := map[string]bool{}
	for _, r := range own.Values {
		skip[gomail.AddrKey(store.Str(r[0]))] = true
	}
	blocked, err := s.BlockedSenders(ctx)
	if err != nil {
		return nil, nil, err
	}
	left, err := s.roleFolderIDs(ctx, "sent", "drafts", "trash", "junk")
	if err != nil {
		return nil, nil, err
	}
	sent, err := s.roleFolderIDs(ctx, "sent")
	if err != nil {
		return nil, nil, err
	}
	acc, accArgs := "", []any(nil)
	if accountID != 0 {
		acc, accArgs = " AND m.account_id = ?", []any{accountID}
	}
	from := " FROM messages m JOIN folders f ON f.id = m.folder_id WHERE m.folder_id NOT IN (" + placeholders(len(left)) + ")" + acc
	args := append(slices.Clone(left), accArgs...)
	// shortcut: gathers every contact in Go on each call; keep a contacts table if large stores get slow.
	rows, err := s.db.Query(ctx, `SELECT m.from_addr, count(*), sum(instr(m.flags, '\Seen') = 0), sum(f.role = 'inbox'), max(m.date)`+
		from+" GROUP BY m.from_addr ORDER BY 5, 1", args...)
	if err != nil {
		return nil, nil, err
	}
	// A bare column beside max() comes from the row holding the max.
	lists, err := s.db.Query(ctx, "SELECT m.from_addr, max(m.date), m.id"+from+" AND m.list_unsubscribe <> '' GROUP BY m.from_addr", args...)
	if err != nil {
		return nil, nil, err
	}
	out, err := s.db.Query(ctx, "SELECT m.to_addrs, m.cc_addrs, m.date FROM messages m WHERE m.folder_id IN ("+placeholders(len(sent))+")"+acc+" ORDER BY m.date",
		append(slices.Clone(sent), accArgs...)...)
	if err != nil {
		return nil, nil, err
	}
	byAddr, names := map[string]*Contact{}, map[string][]string{}
	// how often and how lately each name came, by address: in their mail, and in the user's to them
	type use struct{ n, last int64 }
	theirs, ours := map[string]map[string]use{}, map[string]map[string]use{}
	tally := func(m map[string]map[string]use, addr, h string, n, date int64) {
		a, err := mail.ParseAddress(h)
		if err != nil || a.Name == "" {
			return
		}
		if m[addr] == nil {
			m[addr] = map[string]use{}
		}
		u := m[addr][a.Name]
		m[addr][a.Name] = use{u.n + n, max(u.last, date)}
	}
	get := func(h string) *Contact {
		addr := gomail.AddrKey(h)
		if addr == "" || skip[addr] {
			return nil
		}
		c := byAddr[addr]
		if c == nil {
			c = &Contact{Address: addr, Blocked: slices.Contains(blocked, addr)}
			byAddr[addr] = c
		}
		if a, err := mail.ParseAddress(h); err == nil && a.Name != "" {
			if !slices.Contains(names[addr], a.Name) {
				names[addr] = append(names[addr], a.Name)
			}
		}
		return c
	}
	for _, r := range rows.Values {
		h := store.Str(r[0])
		c := get(h)
		if c == nil {
			continue
		}
		tally(theirs, c.Address, h, store.Int(r[1]), store.Int(r[4]))
		c.Received += int(store.Int(r[1]))
		c.Unread += int(store.Int(r[2]))
		c.Inbox += int(store.Int(r[3]))
		c.Last = time.Unix(store.Int(r[4]), 0)
	}
	listDate := map[string]int64{}
	for _, r := range lists.Values {
		addr := gomail.AddrKey(store.Str(r[0]))
		if c := byAddr[addr]; c != nil && store.Int(r[1]) >= listDate[addr] {
			listDate[addr], c.ListMessage = store.Int(r[1]), store.Int(r[2])
		}
	}
	for _, r := range out.Values {
		seen := map[*Contact]bool{}
		for _, h := range slices.Concat(jsonList(store.Str(r[0])), jsonList(store.Str(r[1]))) {
			c := get(h)
			if c == nil || seen[c] {
				continue
			}
			seen[c] = true
			c.Sent++
			tally(ours, c.Address, h, 1, store.Int(r[2]))
			if d := time.Unix(store.Int(r[2]), 0); d.After(c.Last) {
				c.Last = d
			}
		}
	}
	all := make([]Contact, 0, len(byAddr))
	for _, c := range byAddr {
		c.Messages = c.Received + c.Sent
		// the name used most, the latest of a tie; one never heard from goes by the name the user wrote
		var best use
		used := theirs[c.Address]
		if len(used) == 0 {
			used = ours[c.Address]
		}
		for name, u := range used {
			if u.n > best.n || u.n == best.n && (u.last > best.last || u.last == best.last && name < c.Name) {
				c.Name, best = name, u
			}
		}
		all = append(all, *c)
	}
	slices.SortFunc(all, func(a, b Contact) int {
		return cmp.Or(cmp.Compare(b.Messages, a.Messages), b.Last.Compare(a.Last), strings.Compare(a.Address, b.Address))
	})
	return all, names, nil
}

// ContactDetail is what ArchiveSender would do with addr's inbox mail now.
func (s *Service) ContactDetail(ctx context.Context, addr string) (ContactDetail, error) {
	var d ContactDetail
	plan, err := s.archivePlan(ctx, gomail.AddrKey(addr))
	if err != nil {
		return d, err
	}
	for _, ms := range plan.byAcc {
		d.Archivable += len(ms)
	}
	d.Kept = plan.kept
	return d, nil
}

// ContactMail is a page of the mail between the user and addr, newest
// first: theirs as Contact.Received counts it and the user's to them as
// Contact.Sent does, so a contact's pages hold Contact.Messages in all.
// cursor "" starts; NextCursor is "" past the last page. A page may hold
// fewer than limit with more to come.
func (s *Service) ContactMail(ctx context.Context, addr, cursor string, limit int) (MessagePage, error) {
	var page MessagePage
	key := gomail.AddrKey(addr)
	q, err := s.withSenders(ctx, SearchQuery{FromAddr: key})
	if err != nil {
		return page, err
	}
	left, err := s.roleFolderIDs(ctx, "sent", "drafts", "trash", "junk")
	if err != nil {
		return page, err
	}
	sent, err := s.roleFolderIDs(ctx, "sent")
	if err != nil {
		return page, err
	}
	headers, _ := json.Marshal(append([]string{}, q.fromHeaders...)) // [] when none: theirs is no mail
	cond := "(m.from_addr IN (SELECT value FROM json_each(?)) AND m.folder_id NOT IN (" + placeholders(len(left)) + ")) OR (m.folder_id IN (" + placeholders(len(sent)) + ")"
	args := slices.Concat([]any{string(headers)}, left, sent)
	// Narrowed by an ASCII domain, which LIKE folds and JSON never escapes
	// (a local part's & is \u0026 in the stored list); AddrKey decides below.
	if at := strings.LastIndexByte(key, '@'); at >= 0 && strings.IndexFunc(key[at:], func(r rune) bool { return r >= utf8.RuneSelf }) < 0 {
		cond += ` AND (m.to_addrs LIKE ? ESCAPE '\' OR m.cc_addrs LIKE ? ESCAPE '\')`
		args = append(args, likeArg(key[at:]), likeArg(key[at:]))
	}
	cond = "(" + cond + "))"
	isSent := func(m MessageSummary) bool { return slices.Contains(sent, any(m.FolderID)) }
	limit = clampLimit(limit)
	for len(page.Items) == 0 { // a batch of only near misses reads on
		c, a := cond, args
		if cursor != "" {
			d, id, err := decodeCursor(cursor)
			if err != nil {
				return page, err
			}
			c, a = c+" AND (m.date < ? OR (m.date = ? AND m.id < ?))", append(slices.Clone(args), d, d, id)
		}
		rows, err := s.db.Query(ctx, "SELECT "+msgCols+" FROM messages m WHERE "+c+" ORDER BY m.date DESC, m.id DESC LIMIT ?", append(a, limit+1)...)
		if err != nil {
			return page, err
		}
		for i, r := range rows.Values[:min(len(rows.Values), limit)] {
			m := scanSummary(r)
			if !isSent(m) || slices.ContainsFunc(slices.Concat(m.To, m.Cc), func(h string) bool { return gomail.AddrKey(h) == key }) {
				page.Items = append(page.Items, m)
			}
			if i == limit-1 {
				cursor = encodeCursor(m.Date.Unix(), m.ID)
			}
		}
		if len(rows.Values) <= limit {
			page.NextCursor = "" // a batch of near misses before may have set it
			return page, nil
		}
		page.NextCursor = cursor
	}
	return page, nil
}

// archiving is the inbox mail from a contact that ArchiveSender would move,
// by account (To not set yet), how much it would keep, and the From headers
// the contact's mail carries.
type archiving struct {
	byAcc   map[int64][]ArchivedMessage
	kept    int
	headers []string
}

// archivePlan is what archiving addr's inbox mail would move: all of it but
// mail in a thread the user starred or replied to (a message \Flagged,
// \Answered or in Sent).
func (s *Service) archivePlan(ctx context.Context, addr string) (archiving, error) {
	p := archiving{byAcc: map[int64][]ArchivedMessage{}}
	q, err := s.withSenders(ctx, SearchQuery{FromAddr: addr})
	if err != nil || len(q.fromHeaders) == 0 {
		return p, err
	}
	p.headers = q.fromHeaders
	sent, err := s.roleFolderIDs(ctx, "sent")
	if err != nil {
		return p, err
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
		return p, err
	}
	for _, r := range rows.Values {
		if store.Int(r[3]) != 0 {
			p.kept++
			continue
		}
		acc := store.Int(r[1])
		p.byAcc[acc] = append(p.byAcc[acc], ArchivedMessage{ID: store.Int(r[0]), From: store.Int(r[2])})
	}
	return p, nil
}

// ArchiveSender moves the inbox mail from addr (a From header or its
// address) to each account's archive, as archivePlan says. An account
// without an archive folder fails before anything moves.
func (s *Service) ArchiveSender(ctx context.Context, addr string) (SenderArchive, error) {
	var res SenderArchive
	p, err := s.archivePlan(ctx, gomail.AddrKey(addr))
	if err != nil {
		return res, err
	}
	res.Skipped = p.kept
	archive := map[int64]int64{}
	for acc := range p.byAcc {
		if archive[acc], err = s.roleFolder(ctx, acc, "archive"); err != nil {
			return res, err
		}
	}
	for acc, ms := range p.byAcc {
		for i := range ms {
			ms[i].To = archive[acc]
		}
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
// where it put them; ones moved since or gone stay. The check and the move
// hold the edit lock together, so a move elsewhere meanwhile wins.
func (s *Service) UndoArchive(ctx context.Context, a SenderArchive) error {
	s.editMu.Lock()
	defer s.editMu.Unlock()
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
			acc, ts, err := s.targets(ctx, still)
			if err != nil {
				return err
			}
			if err := s.move(ctx, acc, ts, k.From); err != nil {
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
