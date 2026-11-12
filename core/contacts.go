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

// ContactAddress is one normalized email address belonging to a Contact.
type ContactAddress struct {
	Address string
	Pref    bool
	Blocked bool
}

// Contact is someone the user exchanged synced mail with or keeps in an
// enabled CardDAV address book, merged across shared addresses (gomail.AddrKey);
// the user's own accounts are excluded. Name comes from the highest-priority
// card when non-empty, else the display name their mail used most, else the one
// the user wrote to them. Subtitle joins the highest-priority card's ORG and
// TITLE with " · ". Addresses lists every address of the contact, preferred and
// recent-mail first, with Address equal to Addresses[0].Address.
// Received counts their mail outside Sent, Drafts, Trash and Junk, Sent the
// user's mail in Sent to or copying them (once per message), Messages both.
// Unread and Inbox count within Received; Latest is their newest received
// message (0 none), ListMessage the newest with a List-Unsubscribe header (0
// none); Blocked is whether all of their addresses are blocked senders.
type Contact struct {
	Name, Subtitle, Address       string
	Addresses                     []ContactAddress
	SearchTerms                   []string
	FromAddressBook               bool
	Messages                      int
	Last                          time.Time
	Received, Sent, Unread, Inbox int
	Latest, ListMessage           int64
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

// Contacts lists everyone the user exchanged mail with and enabled address-book
// contacts, contacts with mail first (most mail first) followed by zero-mail contacts.
func (s *Service) Contacts(ctx context.Context) ([]Contact, error) {
	return s.contacts(ctx, 0)
}

// ContactMatch is a lightweight, read-only contact summary without binary avatars.
type ContactMatch struct {
	Name, Subtitle, Address string
	Addresses               []string
}

// SearchContacts searches local mail-derived and cached address-book contacts
// without touching the network or returning binary avatars.
func (s *Service) SearchContacts(ctx context.Context, query string, limit int) ([]ContactMatch, error) {
	g, err := s.findContacts(ctx, FindQuery{
		SearchQuery: SearchQuery{Text: query},
		Kind:        "contact",
		Limit:       limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]ContactMatch, len(g.Items))
	for i, c := range g.Items {
		addrs := make([]string, len(c.Addresses))
		for j, ea := range c.Addresses {
			addrs[j] = ea.Address
		}
		out[i] = ContactMatch{
			Name:      c.Name,
			Subtitle:  c.Subtitle,
			Address:   c.Address,
			Addresses: addrs,
		}
	}
	return out, nil
}

// ContactCompletion is one single-address recipient completion candidate.
type ContactCompletion struct {
	Name, Subtitle, Address string
}

// CompleteContacts expands matching contacts into single-address candidates for
// recipient completion, with preferred and recent-mail addresses first. A blank
// query returns no candidates.
func (s *Service) CompleteContacts(ctx context.Context, query string, limit int) ([]ContactCompletion, error) {
	text := strings.ToLower(strings.TrimSpace(query))
	if text == "" {
		return []ContactCompletion{}, nil
	}
	all, err := s.contacts(ctx, 0)
	if err != nil {
		return nil, err
	}
	all = slices.DeleteFunc(all, func(c Contact) bool {
		return !contactMatchesPerson(c, text) &&
			!slices.ContainsFunc(c.Addresses, func(ea ContactAddress) bool { return strings.Contains(ea.Address, text) })
	})
	slices.SortStableFunc(all, compareSearchedContacts)
	capN := clampLimit(limit)
	out := make([]ContactCompletion, 0, min(len(all), capN))
	for _, c := range all {
		personHit := contactMatchesPerson(c, text)
		for _, ea := range c.Addresses {
			if !personHit && !strings.Contains(ea.Address, text) {
				continue
			}
			out = append(out, ContactCompletion{
				Name:     c.Name,
				Subtitle: c.Subtitle,
				Address:  ea.Address,
			})
			if len(out) >= capN {
				return out, nil
			}
		}
	}
	return out, nil
}

type cardEmailRow struct {
	addrKey string
	pref    bool
}

type cardComponent struct {
	name     string
	subtitle string
	addrs    []string
	prefs    map[string]bool
	terms    []string
	cardIDs  []int64
}

func formatCardSubtitle(org, title string) string {
	if org != "" && title != "" {
		return org + " · " + title
	}
	return cmp.Or(org, title)
}

func addUniqueTerm(dst *[]string, s string) {
	if s != "" && !slices.Contains(*dst, s) {
		*dst = append(*dst, s)
	}
}

func (s *Service) ownAddrSet(ctx context.Context) (map[string]bool, error) {
	own, err := s.db.Query(ctx, "SELECT email FROM accounts")
	if err != nil {
		return nil, err
	}
	skip := make(map[string]bool, len(own.Values))
	for _, r := range own.Values {
		if k := gomail.AddrKey(store.Str(r[0])); k != "" {
			skip[k] = true
		}
	}
	return skip, nil
}

// enabledCardComponents loads enabled address-book cards in priority order,
// strips own-account addresses (ignoring cards with zero non-own addresses),
// and merges cards sharing any non-own AddrKey into connected components.
func (s *Service) enabledCardComponents(ctx context.Context, skip map[string]bool) ([]cardComponent, map[string]int, error) {
	cardRows, err := s.db.Query(ctx, `SELECT c.id, c.fn, c.org, c.title
		FROM address_book_cards c
		JOIN address_book_collections col ON col.id = c.collection_id AND col.enabled = 1
		JOIN address_books ab ON ab.id = col.address_book_id
		ORDER BY ab.sort_order ASC, ab.id ASC, col.id ASC, c.href ASC`)
	if err != nil || len(cardRows.Values) == 0 {
		return nil, nil, err
	}
	emailRows, err := s.db.Query(ctx, `SELECT e.card_id, e.addr_key, e.pref
		FROM address_book_emails e
		JOIN address_book_cards c ON c.id = e.card_id
		JOIN address_book_collections col ON col.id = c.collection_id AND col.enabled = 1
		ORDER BY e.card_id ASC, e.pos ASC`)
	if err != nil {
		return nil, nil, err
	}
	byCard := map[int64][]cardEmailRow{}
	for _, r := range emailRows.Values {
		key := store.Str(r[1])
		if key == "" || skip[key] {
			continue
		}
		cid := store.Int(r[0])
		byCard[cid] = append(byCard[cid], cardEmailRow{addrKey: key, pref: store.Int(r[2]) != 0})
	}

	type cardEntry struct {
		id             int64
		fn, org, title string
		emails         []cardEmailRow
	}
	var valid []cardEntry
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if p := parent[x]; p != "" && p != x {
			parent[x] = find(p)
			return parent[x]
		}
		return x
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	for _, r := range cardRows.Values {
		cid := store.Int(r[0])
		ems := byCard[cid]
		if len(ems) == 0 {
			continue
		}
		for _, em := range ems {
			if parent[em.addrKey] == "" {
				parent[em.addrKey] = em.addrKey
			}
		}
		first := ems[0].addrKey
		for _, em := range ems[1:] {
			union(first, em.addrKey)
		}
		valid = append(valid, cardEntry{
			id:     cid,
			fn:     store.Str(r[1]),
			org:    store.Str(r[2]),
			title:  store.Str(r[3]),
			emails: ems,
		})
	}
	if len(valid) == 0 {
		return nil, nil, nil
	}

	rootIdx := map[string]int{}
	var comps []cardComponent
	addrToComp := map[string]int{}
	hasPref := map[int]bool{}

	for _, ce := range valid {
		root := find(ce.emails[0].addrKey)
		idx, ok := rootIdx[root]
		if !ok {
			idx = len(comps)
			rootIdx[root] = idx
			comps = append(comps, cardComponent{prefs: map[string]bool{}})
		}
		comp := &comps[idx]
		comp.cardIDs = append(comp.cardIDs, ce.id)
		if comp.name == "" && ce.fn != "" {
			comp.name = ce.fn
		}
		sub := formatCardSubtitle(ce.org, ce.title)
		if comp.subtitle == "" && sub != "" {
			comp.subtitle = sub
		}
		addUniqueTerm(&comp.terms, ce.fn)
		addUniqueTerm(&comp.terms, ce.org)
		addUniqueTerm(&comp.terms, ce.title)
		addUniqueTerm(&comp.terms, sub)

		cardHasPref := slices.ContainsFunc(ce.emails, func(e cardEmailRow) bool { return e.pref })
		takePref := !hasPref[idx] && cardHasPref
		if takePref {
			hasPref[idx] = true
		}
		for _, em := range ce.emails {
			if _, seen := addrToComp[em.addrKey]; !seen {
				addrToComp[em.addrKey] = idx
				comp.addrs = append(comp.addrs, em.addrKey)
			}
			if takePref && em.pref {
				comp.prefs[em.addrKey] = true
			}
		}
	}
	return comps, addrToComp, nil
}

// contacts gathers the contacts of accountID's mail (0: every account's plus
// zero-mail enabled address-book contacts).
func (s *Service) contacts(ctx context.Context, accountID int64) ([]Contact, error) {
	skip, err := s.ownAddrSet(ctx)
	if err != nil {
		return nil, err
	}
	blocked, err := s.BlockedSenders(ctx)
	if err != nil {
		return nil, err
	}
	blockedSet := make(map[string]bool, len(blocked))
	for _, b := range blocked {
		blockedSet[b] = true
	}
	comps, _, err := s.enabledCardComponents(ctx, skip)
	if err != nil {
		return nil, err
	}
	left, err := s.roleFolderIDs(ctx, "sent", "drafts", "trash", "junk")
	if err != nil {
		return nil, err
	}
	sent, err := s.roleFolderIDs(ctx, "sent")
	if err != nil {
		return nil, err
	}
	acc, accArgs := "", []any(nil)
	if accountID != 0 {
		acc, accArgs = " AND m.account_id = ?", []any{accountID}
	}
	from := " FROM messages m WHERE " + shownFolder + " IS NOT NULL AND NOT " + shownIn("("+placeholders(len(left))+")") + acc
	args := append(slices.Clone(left), accArgs...)
	// shortcut: gathers every contact in Go on each call; keep a contacts table if large stores get slow.
	// A bare column beside max() (m.id here and below) comes from the row holding the max.
	rows, err := s.db.Query(ctx, `SELECT m.from_addr, count(*), sum(instr(m.flags, '\Seen') = 0), sum(`+shownIn("(SELECT id FROM folders WHERE role = 'inbox')")+`), max(m.date), m.id`+
		from+" GROUP BY m.from_addr ORDER BY 5, 1", args...)
	if err != nil {
		return nil, err
	}
	lists, err := s.db.Query(ctx, "SELECT m.from_addr, max(m.date), m.id"+from+" AND m.list_unsubscribe <> '' GROUP BY m.from_addr", args...)
	if err != nil {
		return nil, err
	}
	out, err := s.db.Query(ctx, "SELECT m.to_addrs, m.cc_addrs, m.date FROM messages m WHERE "+shownIn("("+placeholders(len(sent))+")")+acc+" ORDER BY m.date",
		append(slices.Clone(sent), accArgs...)...)
	if err != nil {
		return nil, err
	}

	// how often and how lately each name came, by contact: in their mail, and in the user's to them
	type use struct{ n, last int64 }
	type contactBuild struct {
		cardName, subtitle            string
		fromBook                      bool
		addrs                         []string
		prefs                         map[string]bool
		terms                         []string
		theirs, ours                  map[string]use
		Received, Sent, Unread, Inbox int
		Last                          time.Time
		Latest, ListMessage           int64
		latestDate, listDate          int64
		hasLatest, hasList            bool
	}

	var builds []*contactBuild
	byAddr := map[string]*contactBuild{}
	for _, comp := range comps {
		cb := &contactBuild{
			cardName: comp.name,
			subtitle: comp.subtitle,
			fromBook: true,
			addrs:    slices.Clone(comp.addrs),
			prefs:    comp.prefs,
			terms:    slices.Clone(comp.terms),
			theirs:   map[string]use{},
			ours:     map[string]use{},
		}
		builds = append(builds, cb)
		for _, a := range comp.addrs {
			byAddr[a] = cb
		}
	}

	tally := func(m map[string]use, h string, n, date int64) {
		a, err := mail.ParseAddress(h)
		if err != nil || a.Name == "" {
			return
		}
		u := m[a.Name]
		m[a.Name] = use{u.n + n, max(u.last, date)}
	}
	get := func(h string) (string, *contactBuild) {
		addr := gomail.AddrKey(h)
		if addr == "" || skip[addr] {
			return "", nil
		}
		cb := byAddr[addr]
		if cb == nil {
			cb = &contactBuild{
				addrs:  []string{addr},
				theirs: map[string]use{},
				ours:   map[string]use{},
			}
			builds = append(builds, cb)
			byAddr[addr] = cb
		}
		if a, err := mail.ParseAddress(h); err == nil && a.Name != "" {
			addUniqueTerm(&cb.terms, a.Name)
		}
		return addr, cb
	}

	addrLast := map[string]time.Time{}
	touchAddr := func(addr string, d time.Time) {
		if d.After(addrLast[addr]) {
			addrLast[addr] = d
		}
	}

	for _, r := range rows.Values {
		h := store.Str(r[0])
		addr, cb := get(h)
		if cb == nil {
			continue
		}
		ts := store.Int(r[4])
		d := time.Unix(ts, 0)
		touchAddr(addr, d)
		tally(cb.theirs, h, store.Int(r[1]), ts)
		cb.Received += int(store.Int(r[1]))
		cb.Unread += int(store.Int(r[2]))
		cb.Inbox += int(store.Int(r[3]))
		if d.After(cb.Last) {
			cb.Last = d
		}
		msgID := store.Int(r[5])
		if !cb.hasLatest || ts > cb.latestDate || (ts == cb.latestDate && msgID > cb.Latest) {
			cb.hasLatest, cb.latestDate, cb.Latest = true, ts, msgID
		}
	}
	for _, r := range lists.Values {
		addr := gomail.AddrKey(store.Str(r[0]))
		cb := byAddr[addr]
		if cb == nil {
			continue
		}
		ts, msgID := store.Int(r[1]), store.Int(r[2])
		if !cb.hasList || ts > cb.listDate || (ts == cb.listDate && msgID > cb.ListMessage) {
			cb.hasList, cb.listDate, cb.ListMessage = true, ts, msgID
		}
	}
	for _, r := range out.Values {
		seenAddr := map[string]bool{}
		seenCB := map[*contactBuild]bool{}
		ts := store.Int(r[2])
		d := time.Unix(ts, 0)
		for _, h := range slices.Concat(jsonList(store.Str(r[0])), jsonList(store.Str(r[1]))) {
			addr, cb := get(h)
			if cb == nil {
				continue
			}
			if !seenAddr[addr] {
				seenAddr[addr] = true
				touchAddr(addr, d)
				tally(cb.ours, h, 1, ts)
			}
			if !seenCB[cb] {
				seenCB[cb] = true
				cb.Sent++
				if d.After(cb.Last) {
					cb.Last = d
				}
			}
		}
	}

	all := make([]Contact, 0, len(builds))
	for _, cb := range builds {
		msgs := cb.Received + cb.Sent
		if accountID != 0 && msgs == 0 {
			continue
		}
		addrs := make([]ContactAddress, len(cb.addrs))
		allBlocked := len(cb.addrs) > 0
		for i, a := range cb.addrs {
			blk := blockedSet[a]
			if !blk {
				allBlocked = false
			}
			addrs[i] = ContactAddress{Address: a, Pref: cb.prefs[a], Blocked: blk}
		}
		slices.SortFunc(addrs, func(a, b ContactAddress) int {
			if a.Pref != b.Pref {
				if a.Pref {
					return -1
				}
				return 1
			}
			al, bl := addrLast[a.Address], addrLast[b.Address]
			if al.IsZero() != bl.IsZero() {
				if !al.IsZero() {
					return -1
				}
				return 1
			}
			return cmp.Or(bl.Compare(al), strings.Compare(a.Address, b.Address))
		})

		name := cb.cardName
		if name == "" {
			var best use
			used := cb.theirs
			if len(used) == 0 {
				used = cb.ours
			}
			for n, u := range used {
				if u.n > best.n || u.n == best.n && (u.last > best.last || u.last == best.last && n < name) {
					name, best = n, u
				}
			}
		}
		nameLower, subLower := strings.ToLower(name), strings.ToLower(cb.subtitle)
		var searchTerms []string
		for _, t := range cb.terms {
			tl := strings.ToLower(t)
			if t != "" && !strings.Contains(nameLower, tl) && !strings.Contains(subLower, tl) {
				addUniqueTerm(&searchTerms, t)
			}
		}

		primary := addrs[0].Address
		all = append(all, Contact{
			Name:            name,
			Subtitle:        cb.subtitle,
			Address:         primary,
			Addresses:       addrs,
			SearchTerms:     searchTerms,
			FromAddressBook: cb.fromBook,
			Messages:        msgs,
			Last:            cb.Last,
			Received:        cb.Received,
			Sent:            cb.Sent,
			Unread:          cb.Unread,
			Inbox:           cb.Inbox,
			Latest:          cb.Latest,
			ListMessage:     cb.ListMessage,
			Blocked:         allBlocked,
		})
	}
	slices.SortFunc(all, func(a, b Contact) int {
		if (a.Messages > 0) != (b.Messages > 0) {
			if a.Messages > 0 {
				return -1
			}
			return 1
		}
		if a.Messages == 0 {
			ak := cmp.Or(strings.ToLower(a.Name), a.Address)
			bk := cmp.Or(strings.ToLower(b.Name), b.Address)
			return cmp.Or(strings.Compare(ak, bk), strings.Compare(a.Address, b.Address))
		}
		return cmp.Or(cmp.Compare(b.Messages, a.Messages), b.Last.Compare(a.Last), strings.Compare(a.Address, b.Address))
	})
	return all, nil
}

// contactAddrKeys resolves addr to every AddrKey in its merged Contact
// component (or just addr's own AddrKey when not in an enabled card component).
func (s *Service) contactAddrKeys(ctx context.Context, addr string) ([]string, error) {
	key := gomail.AddrKey(addr)
	if key == "" {
		return nil, nil
	}
	skip, err := s.ownAddrSet(ctx)
	if err != nil {
		return nil, err
	}
	if !skip[key] {
		comps, addrToComp, err := s.enabledCardComponents(ctx, skip)
		if err != nil {
			return nil, err
		}
		if idx, ok := addrToComp[key]; ok {
			return comps[idx].addrs, nil
		}
	}
	return []string{key}, nil
}

func asciiDomains(keys []string) ([]string, bool) {
	var domains []string
	for _, k := range keys {
		at := strings.LastIndexByte(k, '@')
		if at < 0 || strings.IndexFunc(k[at:], func(r rune) bool { return r >= utf8.RuneSelf }) >= 0 {
			return nil, false
		}
		dom := k[at:]
		if !slices.Contains(domains, dom) {
			domains = append(domains, dom)
		}
	}
	return domains, len(domains) > 0
}

func (s *Service) fromHeadersForAddrs(ctx context.Context, keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	keySet := make(map[string]bool, len(keys))
	for _, k := range keys {
		keySet[k] = true
	}
	cond, args := "1", []any(nil)
	if doms, ok := asciiDomains(keys); ok {
		parts := make([]string, len(doms))
		args = make([]any, len(doms))
		for i, d := range doms {
			parts[i] = `from_addr LIKE ? ESCAPE '\'`
			args[i] = likeArg(d)
		}
		cond = "(" + strings.Join(parts, " OR ") + ")"
	}
	rows, err := s.db.Query(ctx, "SELECT DISTINCT from_addr FROM messages WHERE "+cond, args...)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, r := range rows.Values {
		if h := store.Str(r[0]); keySet[gomail.AddrKey(h)] {
			out = append(out, h)
		}
	}
	return out, nil
}

// ContactDetail is what ArchiveSender would do with addr's inbox mail now.
func (s *Service) ContactDetail(ctx context.Context, addr string) (ContactDetail, error) {
	var d ContactDetail
	plan, err := s.archivePlan(ctx, addr)
	if err != nil {
		return d, err
	}
	for _, ms := range plan.byAcc {
		d.Archivable += len(ms)
	}
	d.Kept = plan.kept
	return d, nil
}

// ContactMail is a page of the mail between the user and addr (covering all
// addresses of addr's merged contact), newest first: theirs as Contact.Received
// counts it and the user's to them as Contact.Sent does, so a contact's pages
// hold Contact.Messages in all. cursor "" starts; NextCursor is "" past the
// last page. A page may hold fewer than limit with more to come.
func (s *Service) ContactMail(ctx context.Context, addr, cursor string, limit int) (MessagePage, error) {
	var page MessagePage
	keys, err := s.contactAddrKeys(ctx, addr)
	if err != nil || len(keys) == 0 {
		return page, err
	}
	keySet := make(map[string]bool, len(keys))
	for _, k := range keys {
		keySet[k] = true
	}
	fromHeaders, err := s.fromHeadersForAddrs(ctx, keys)
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
	headers, _ := json.Marshal(append([]string{}, fromHeaders...)) // [] when none: theirs is no mail
	cond := "(m.from_addr IN (SELECT value FROM json_each(?)) AND NOT " + shownIn("("+placeholders(len(left))+")") + ") OR (" + shownIn("("+placeholders(len(sent))+")")
	args := slices.Concat([]any{string(headers)}, left, sent)
	// Narrowed by ASCII domains, which LIKE folds and JSON never escapes
	// (a local part's ampersand is & in the stored list); AddrKey decides below.
	if doms, ok := asciiDomains(keys); ok {
		parts := make([]string, 0, len(doms)*2)
		for _, dom := range doms {
			parts = append(parts, `m.to_addrs LIKE ? ESCAPE '\'`, `m.cc_addrs LIKE ? ESCAPE '\'`)
			args = append(args, likeArg(dom), likeArg(dom))
		}
		cond += " AND (" + strings.Join(parts, " OR ") + ")"
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
			if !isSent(m) || slices.ContainsFunc(slices.Concat(m.To, m.Cc), func(h string) bool { return keySet[gomail.AddrKey(h)] }) {
				page.Items = append(page.Items, m)
			}
			if i == limit-1 {
				cursor = encodeCursor(m.Date.Unix(), m.ID)
			}
		}
		if len(rows.Values) <= limit {
			page.NextCursor = "" // a batch of near misses before may have set it
			break
		}
		page.NextCursor = cursor
	}
	if err := s.fillFromNames(ctx, page.Items); err != nil {
		return MessagePage{}, err
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

// archivePlan is what archiving addr's inbox mail would move: all of it across
// addr's merged contact but mail in a thread the user starred or replied to (a
// message \Flagged, \Answered or in Sent).
func (s *Service) archivePlan(ctx context.Context, addr string) (archiving, error) {
	p := archiving{byAcc: map[int64][]ArchivedMessage{}}
	keys, err := s.contactAddrKeys(ctx, addr)
	if err != nil || len(keys) == 0 {
		return p, err
	}
	fromHeaders, err := s.fromHeadersForAddrs(ctx, keys)
	if err != nil || len(fromHeaders) == 0 {
		return p, err
	}
	p.headers = fromHeaders
	sent, err := s.roleFolderIDs(ctx, "sent")
	if err != nil {
		return p, err
	}
	args := append(sent, KindLocalArchive)
	for _, h := range fromHeaders {
		args = append(args, h)
	}
	kept := `instr(t.flags, '\Flagged') > 0 OR instr(t.flags, '\Answered') > 0
		OR EXISTS (SELECT 1 FROM message_locations tl WHERE tl.message_id = t.id AND tl.shown AND tl.folder_id IN (` + placeholders(len(sent)) + `))`
	rows, err := s.db.Query(ctx, `SELECT m.id, m.account_id, `+shownFolder+`, EXISTS (SELECT 1 FROM messages t WHERE (t.id = m.id OR t.thread_id = m.thread_id) AND (`+kept+`))
		FROM messages m WHERE `+shownIn("(SELECT id FROM folders WHERE role = 'inbox')")+`
		AND m.account_id NOT IN (SELECT id FROM accounts WHERE kind = ?) AND m.from_addr IN (`+
		placeholders(len(fromHeaders))+") ORDER BY m.id", args...)
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

// ArchiveSender moves the inbox mail from addr (covering all addresses of
// addr's merged contact) to each account's archive, as archivePlan says. An
// account without an archive folder fails before anything moves. The plan and
// the moves hold the edit lock together, so a change meanwhile is planned with.
func (s *Service) ArchiveSender(ctx context.Context, addr string) (SenderArchive, error) {
	var res SenderArchive
	s.editMu.Lock()
	defer s.editMu.Unlock()
	p, err := s.archivePlan(ctx, addr)
	if err != nil {
		return res, err
	}
	res.Skipped = p.kept
	archive := map[int64]int64{}
	for acc := range p.byAcc {
		if _, archive[acc], err = s.archiveTargets(ctx, acc, nil, 0); err != nil {
			return res, err
		}
	}
	for acc, ms := range p.byAcc {
		for i := range ms {
			ms[i].To = archive[acc]
		}
		for c := range slices.Chunk(ms, 200) {
			acc, ts, err := s.targets(ctx, archivedIDs(c))
			if err != nil {
				return res, err
			}
			if err := s.move(ctx, acc, ts, 0, archive[acc]); err != nil {
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
	ids, err := s.resolve(ctx, archivedIDs(a.Moved))
	if err != nil {
		return err
	}
	back := map[ArchivedMessage][]any{} // by From and To, ID 0
	for i, m := range a.Moved {
		back[ArchivedMessage{From: m.From, To: m.To}] = append(back[ArchivedMessage{From: m.From, To: m.To}], ids[i])
	}
	for k, ids := range back {
		for c := range slices.Chunk(ids, 200) {
			where, args := shownIn("(?)"), []any{k.To}
			if k.To == 0 {
				where, args = "NOT "+shownIn("(?)"), []any{k.From}
			}
			rows, err := s.db.Query(ctx, "SELECT m.id FROM messages m WHERE "+where+" AND m.id IN ("+placeholders(len(c))+")", append(args, c...)...)
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
			stmts, err := s.archiveUndoWrites(ctx, acc, ts, k.To, k.From)
			if err != nil {
				return err
			}
			if err := s.apply(ctx, acc, ts, stmts); err != nil {
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
