// Package mailsync pulls folders and messages from IMAP into the store.
package mailsync

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// One round of body-text FETCHes covers at most textBatch messages and asks for
// at most textBatchBytes (or one message's text), which bounds sync memory.
const (
	textBatch      = 50
	textBatchBytes = 32 << 20
)

// PartFetchBytes is how much of one text part a sync downloads: enough encoded
// bytes for mail.MaxBodyBytes of text even in quoted-printable. Tests lower it.
var PartFetchBytes int64 = 3 * mail.MaxBodyBytes

// BackfillPage is how many messages one FETCH of a range backfill asks for,
// which bounds its memory. Tests lower it.
var BackfillPage = 1000

// BackfillPages is how many pages a range backfill takes per sync round
// before it yields, so new mail, ops and SyncNow wait at most that long; the
// next round goes on. Tests lower it.
var BackfillPages = 2

// account is a sync's view of the account: limit > 0 keeps the newest limit
// messages per folder, else months > 0 the mail since the cutoff, else all.
type account struct {
	id     int64
	email  string
	limit  int
	months int
	budget int // range backfill pages left this round
}

// since is the SINCE date of a months range: date-only, as IMAP compares
// INTERNALDATE by day; zero for all mail.
func (a *account) since(now time.Time) time.Time {
	if a.months <= 0 {
		return time.Time{}
	}
	y, m, d := now.AddDate(0, -a.months, 0).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// result collects what one Fetch changed, for the events it publishes.
type result struct {
	received, threads, changed, changedThreads []int64
	conflicts                                  []string
	more                                       bool // a range backfill yielded with work left
}

// gone records messages about to be deleted (or moved between threads) as changed.
func (r *result) gone(ids, tids []int64) {
	r.changed = append(r.changed, ids...)
	r.changedThreads = append(r.changedThreads, tids...)
}

// merged repoints thread IDs collected so far from the merged-away threads to target.
func (r *result) merged(target int64, others []int64) {
	for _, l := range [][]int64{r.threads, r.changedThreads} {
		for i, t := range l {
			if slices.Contains(others, t) {
				l[i] = target
			}
		}
	}
	r.changedThreads = append(r.changedThreads, target)
}

// Fetch syncs every folder of the account over c: folder list, new messages
// (headers and text parts only; attachments stay lazy), threading, remote flag
// changes and expunges. Rows committed before an error are still announced.
// more reports that a range backfill used its pages for the round and has
// work left: the caller should sync again soon.
func Fetch(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64, bus *events.Bus) (more bool, err error) {
	var r result
	defer r.publish(bus, accountID)
	a, err := loadAccount(ctx, db, accountID)
	if err != nil {
		return false, err
	}
	a.budget = BackfillPages
	folders, err := syncFolders(ctx, db, c, accountID, &r)
	if err != nil {
		return false, err
	}
	for _, f := range folders {
		if err := syncFolder(ctx, db, c, a, f, &r); err != nil {
			return r.more, fmt.Errorf("sync %q: %w", f.name, err)
		}
	}
	return r.more, unclaimable(ctx, db, accountID, &r)
}

// publish announces what r collected.
func (r *result) publish(bus *events.Bus, accountID int64) {
	if len(r.received) > 0 {
		bus.Publish(events.Event{Type: events.MessageReceived, AccountID: accountID, MessageIDs: r.received, ThreadIDs: r.threads})
	}
	if len(r.changed) > 0 {
		bus.Publish(events.Event{Type: events.MessagesChanged, AccountID: accountID, MessageIDs: r.changed,
			ThreadIDs: slices.Compact(slices.Sorted(slices.Values(r.changedThreads)))})
	}
	if len(r.conflicts) > 0 {
		bus.Publish(events.Event{Type: events.SyncFailed, AccountID: accountID, Error: strings.Join(r.conflicts, "; ")})
	}
}

func loadAccount(ctx context.Context, db *store.DB, accountID int64) (*account, error) {
	rows, err := db.Query(ctx, "SELECT email, sync_limit, sync_since_months FROM accounts WHERE id = ?", accountID)
	if err != nil {
		return nil, err
	}
	if len(rows.Values) == 0 {
		return nil, fmt.Errorf("account %d not found", accountID)
	}
	return &account{id: accountID, email: store.Str(rows.Values[0][0]), limit: int(store.Int(rows.Values[0][1])),
		months: int(store.Int(rows.Values[0][2]))}, nil
}

func syncFolder(ctx context.Context, db *store.DB, c *imapclient.Client, a *account, f *folder, r *result) error {
	sel, err := c.Select(f.name, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return err
	}
	if f.uidValidity != sel.UIDValidity {
		if err := resetFolder(ctx, db, a.id, f, sel.UIDValidity, r); err != nil {
			return err
		}
	}
	if err := fetchNew(ctx, db, c, a, f, sel.NumMessages, r); err != nil {
		return err
	}
	if err := backfill(ctx, db, c, a, f, sel.NumMessages, r); err != nil {
		return err
	}
	return syncFlags(ctx, db, c, a.id, f, r)
}

// resetFolder drops everything known about the folder's old UIDs: their messages
// (attachments and FTS rows go with them), and pending ops that would act on them
// become conflicts. Cached attachment files are removed best effort after the
// commit; one that cannot be removed is no longer referenced, so the startup
// orphan sweep of the cache directory (store.CleanOrphans) collects it.
func resetFolder(ctx context.Context, db *store.DB, accountID int64, f *folder, uidValidity uint32, r *result) error {
	ids, tids, err := doomed(ctx, db, "server_folder_id = ?", f.id)
	if err != nil {
		return err
	}
	files, err := db.Query(ctx, `SELECT a.cache_path FROM attachments a JOIN messages m ON m.id = a.message_id
		WHERE m.server_folder_id = ? AND a.cache_path IS NOT NULL`, f.id)
	if err != nil {
		return err
	}
	stmts := []sqlite.Statement{
		{SQL: `UPDATE ops SET state = 'conflict' WHERE state = 'pending' AND account_id = ?
			AND message_id IN (SELECT id FROM messages WHERE server_folder_id = ?)`, Args: []any{accountID, f.id}},
		{SQL: "DELETE FROM messages WHERE server_folder_id = ?", Args: []any{f.id}},
		{SQL: "UPDATE folders SET uidvalidity = ?, last_uid = 0, synced = 0, filled_since = NULL WHERE id = ?", Args: []any{uidValidity, f.id}},
	}
	if _, err := db.Transaction(ctx, append(stmts, tidyThreads(accountID, tids)...)); err != nil {
		return err
	}
	r.gone(ids, tids)
	f.uidValidity, f.lastUID, f.synced, f.filled = uidValidity, 0, false, nil
	for _, p := range files.Values {
		os.Remove(store.Str(p[0])) // best effort, see above
	}
	return nil
}

// doomed returns the IDs and thread IDs of the messages matching where.
func doomed(ctx context.Context, db *store.DB, where string, args ...any) (ids, tids []int64, err error) {
	rows, err := db.Query(ctx, "SELECT id, thread_id FROM messages WHERE "+where, args...)
	if err != nil {
		return nil, nil, err
	}
	for _, v := range rows.Values {
		ids = append(ids, store.Int(v[0]))
		if t := store.Int(v[1]); t != 0 {
			tids = append(tids, t)
		}
	}
	return ids, tids, nil
}

func headerSection() *imap.FetchItemBodySection {
	return &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader,
		HeaderFields: []string{"REFERENCES", "IN-REPLY-TO", "REPLY-TO"}, Peek: true}
}

// fetchNew imports messages above last_uid; the first sync of a count range
// takes only the newest sync_limit (n is the mailbox's message count), and that
// of a months or all range only notes the top UID, leaving the mail to the
// backfill. What it imports into a folder whose first sync completed is new
// mail, whatever its date (a folder synced empty included); completing one
// marks it.
func fetchNew(ctx context.Context, db *store.DB, c *imapclient.Client, a *account, f *folder, n uint32, r *result) error {
	if n == 0 {
		return advance(ctx, db, f, f.lastUID)
	}
	if !f.synced && a.limit <= 0 { // a folder synced empty gets its mail as new below
		top, err := c.Fetch(imap.SeqSetNum(n), &imap.FetchOptions{UID: true}).Collect()
		if err != nil {
			return err
		}
		if len(top) == 0 {
			return fmt.Errorf("no message %d", n)
		}
		return advance(ctx, db, f, uint32(top[0].UID))
	}
	var set imap.NumSet
	if f.lastUID == 0 && a.limit > 0 {
		start := uint32(1)
		if n > uint32(a.limit) {
			start = n - uint32(a.limit) + 1
		}
		var s imap.SeqSet
		s.AddRange(start, 0)
		set = s
	} else {
		var s imap.UIDSet
		s.AddRange(imap.UID(f.lastUID+1), 0)
		set = s
	}
	msgs, err := c.Fetch(set, importItems()).Collect()
	if err != nil {
		return err
	}
	have, err := db.Query(ctx, "SELECT server_uid FROM messages WHERE server_folder_id = ? AND server_uid > ?", f.id, f.lastUID)
	if err != nil {
		return err
	}
	known := map[uint32]bool{}
	for _, v := range have.Values {
		known[uint32(store.Int(v[0]))] = true
	}
	last, todo := f.lastUID, msgs[:0]
	for _, m := range msgs {
		uid := uint32(m.UID)
		if uid <= f.lastUID { // "n:*" returns the highest old UID when nothing is new
			continue
		}
		last = max(last, uid)
		if !known[uid] {
			todo = append(todo, m)
		}
	}
	slices.SortFunc(todo, byUID)
	// A first sync, or the reimport after a UIDVALIDITY reset (which clears
	// synced), is not new mail, even if an earlier attempt stopped partway.
	if err := importAll(ctx, db, c, a, f, todo, f.synced, r); err != nil {
		return err
	}
	return advance(ctx, db, f, last)
}

// advance records a completed fetchNew: last_uid and synced in one write.
func advance(ctx context.Context, db *store.DB, f *folder, last uint32) error {
	if last == f.lastUID && f.synced {
		return nil
	}
	if _, err := db.Execute(ctx, "UPDATE folders SET last_uid = ?, synced = 1 WHERE id = ?", last, f.id); err != nil {
		return err
	}
	f.lastUID, f.synced = last, true
	return nil
}

// importItems is what a sync fetches of each message before its text parts.
func importItems() *imap.FetchOptions {
	return &imap.FetchOptions{UID: true, Flags: true, RFC822Size: true, Envelope: true, InternalDate: true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true}, BodySection: []*imap.FetchItemBodySection{headerSection()}}
}

// importAll imports msgs in the order given, fetching their text parts in
// batches; with arrivals set each inserted message logs a message.received event.
func importAll(ctx context.Context, db *store.DB, c *imapclient.Client, a *account, f *folder, msgs []*imapclient.FetchMessageBuffer, arrivals bool, r *result) error {
	twins := map[string]int{} // arrivals per Message-ID and size, for claims
	for _, m := range msgs {
		twins[twinKey(m)]++
	}
	for _, batch := range textBatches(msgs) {
		texts, err := fetchTexts(c, batch)
		if err != nil {
			return err
		}
		for _, m := range batch {
			if err := importMessage(ctx, db, a, f, m, texts[m.UID], twins[twinKey(m)], arrivals, r); err != nil {
				return err
			}
		}
	}
	return nil
}

// backfill imports older messages a lower sync_limit left out: while the folder
// holds fewer than sync_limit messages, the newest server UIDs below the oldest
// one stored, up to the shortfall (n is the mailbox's message count). Stored
// messages and pending ops are untouched; a narrower range never deletes mail.
func backfill(ctx context.Context, db *store.DB, c *imapclient.Client, a *account, f *folder, n uint32, r *result) error {
	if f.lastUID == 0 {
		return nil
	}
	if a.limit <= 0 {
		return backfillRange(ctx, db, c, a, f, r)
	}
	rows, err := db.Query(ctx, "SELECT count(*), min(server_uid) FROM messages WHERE server_folder_id = ?", f.id)
	if err != nil {
		return err
	}
	have, below := int(store.Int(rows.Values[0][0])), f.lastUID+1
	if have > 0 {
		below = uint32(store.Int(rows.Values[0][1]))
	}
	if have >= a.limit || have >= int(n) || below <= 1 {
		return nil
	}
	var older imap.UIDSet
	older.AddRange(1, imap.UID(below-1))
	found, err := c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{older}}, nil).Wait()
	if err != nil {
		return err
	}
	uids := found.AllUIDs()
	slices.Sort(uids)
	uids = uids[max(len(uids)-(a.limit-have), 0):]
	if len(uids) == 0 {
		return nil
	}
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), importItems()).Collect()
	if err != nil {
		return err
	}
	// Newest first: if this stops partway, the oldest stored UID still bounds
	// what is missing and the next round resumes below it.
	slices.SortFunc(msgs, func(x, y *imapclient.FetchMessageBuffer) int { return byUID(y, x) })
	return importAll(ctx, db, c, a, f, msgs, false, r) // older mail, not new arrivals
}

// backfillRange imports the messages up to last_uid that a months range (or
// all) covers and the folder lacks: UID SEARCH SINCE the cutoff, less what is
// stored, newest first in pages. UIDs and INTERNALDATE are not monotonic, so it
// diffs the whole window rather than paging below the oldest stored UID. A
// window filled once is not searched again until it grows (filled_since).
func backfillRange(ctx context.Context, db *store.DB, c *imapclient.Client, a *account, f *folder, r *result) error {
	since := a.since(time.Now())
	mark := int64(0)
	if !since.IsZero() {
		mark = since.Unix()
	}
	if f.filled != nil && *f.filled <= mark {
		return nil
	}
	if a.budget <= 0 {
		r.more = true // this round's pages are spent
		return nil
	}
	var upto imap.UIDSet
	upto.AddRange(1, imap.UID(f.lastUID))
	found, err := c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{upto}, Since: since}, nil).Wait()
	if err != nil {
		return err
	}
	have, err := db.Query(ctx, "SELECT server_uid FROM messages WHERE server_folder_id = ?", f.id)
	if err != nil {
		return err
	}
	known := map[imap.UID]bool{}
	for _, v := range have.Values {
		known[imap.UID(store.Int(v[0]))] = true
	}
	uids := slices.DeleteFunc(found.AllUIDs(), func(u imap.UID) bool { return known[u] })
	slices.SortFunc(uids, func(x, y imap.UID) int { return cmp.Compare(y, x) })
	for page := range slices.Chunk(uids, BackfillPage) {
		if a.budget <= 0 {
			r.more = true // filled_since stays unset: the next round searches again
			return nil
		}
		a.budget--
		msgs, err := c.Fetch(imap.UIDSetNum(page...), importItems()).Collect()
		if err != nil {
			return err
		}
		slices.SortFunc(msgs, func(x, y *imapclient.FetchMessageBuffer) int { return byUID(y, x) })
		if err := importAll(ctx, db, c, a, f, msgs, false, r); err != nil { // older mail, not new arrivals
			return err
		}
	}
	if _, err := db.Execute(ctx, "UPDATE folders SET filled_since = ? WHERE id = ?", mark, f.id); err != nil {
		return err
	}
	f.filled = &mark
	return nil
}

func byUID(x, y *imapclient.FetchMessageBuffer) int { return cmp.Compare(x.UID, y.UID) }

func twinKey(m *imapclient.FetchMessageBuffer) string {
	if m.Envelope == nil {
		return ""
	}
	return m.Envelope.MessageID + "\x00" + strconv.FormatInt(m.RFC822Size, 10)
}

// textParts returns the text parts of m that a sync downloads: in order, until
// the message used enough for MaxBodyBytes of both text and HTML. Each is
// fetched up to PartFetchBytes; toParsed marks what was left out as truncated.
func textParts(m *imapclient.FetchMessageBuffer) []mail.Attachment {
	parts, _ := mail.ParseStructure(m.BodyStructure)
	budget := 2 * PartFetchBytes
	for i, p := range parts {
		if budget <= 0 {
			return parts[:i]
		}
		budget -= partCost(p)
	}
	return parts
}

// partCost is how many bytes fetching the text part may return.
func partCost(p mail.Attachment) int64 {
	if p.Size > 0 && int64(p.Size) < PartFetchBytes {
		return int64(p.Size)
	}
	return PartFetchBytes
}

// textBatches splits msgs into rounds of at most textBatch messages and
// textBatchBytes of requested text; a message never splits.
func textBatches(msgs []*imapclient.FetchMessageBuffer) [][]*imapclient.FetchMessageBuffer {
	var out [][]*imapclient.FetchMessageBuffer
	var cur []*imapclient.FetchMessageBuffer
	var size int64
	for _, m := range msgs {
		var cost int64
		for _, p := range textParts(m) {
			cost += partCost(p)
		}
		if len(cur) > 0 && (len(cur) == textBatch || size+cost > textBatchBytes) {
			out, cur, size = append(out, cur), nil, 0
		}
		cur, size = append(cur, m), size+cost
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// fetchTexts downloads the first PartFetchBytes of each of textParts of msgs,
// keyed by UID then part path. Messages with the same parts share one UID FETCH.
func fetchTexts(c *imapclient.Client, msgs []*imapclient.FetchMessageBuffer) (map[imap.UID]map[string][]byte, error) {
	groups := map[string][]imap.UID{}
	layout := map[string][]mail.Attachment{}
	for _, m := range msgs {
		parts := textParts(m)
		if len(parts) == 0 {
			continue
		}
		var key []string
		for _, p := range parts {
			key = append(key, p.Part)
		}
		k := strings.Join(key, " ")
		groups[k] = append(groups[k], m.UID)
		layout[k] = parts
	}
	out := map[imap.UID]map[string][]byte{}
	for k, uids := range groups {
		var secs []*imap.FetchItemBodySection
		for _, p := range layout[k] {
			secs = append(secs, &imap.FetchItemBodySection{Part: partPath(p.Part), Peek: true,
				Partial: &imap.SectionPartial{Size: PartFetchBytes}})
		}
		bufs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, BodySection: secs}).Collect()
		if err != nil {
			return nil, err
		}
		for _, b := range bufs {
			got := map[string][]byte{}
			for i, s := range secs {
				got[layout[k][i].Part] = b.FindBodySection(s)
			}
			out[b.UID] = got
		}
	}
	return out, nil
}

func partPath(p string) []int {
	var out []int
	for _, s := range strings.Split(p, ".") {
		n, _ := strconv.Atoi(s)
		out = append(out, n)
	}
	return out
}

// toParsed assembles a message from ENVELOPE, the fetched header fields and the
// decoded text parts.
func toParsed(m *imapclient.FetchMessageBuffer, texts map[string][]byte) *mail.Parsed {
	p := &mail.Parsed{}
	if h, err := mail.Parse(m.FindBodySection(headerSection())); err == nil {
		p.InReplyTo, p.References, p.ReplyTo = h.InReplyTo, h.References, h.ReplyTo
	}
	if env := m.Envelope; env != nil {
		p.MessageID, p.Subject, p.Date = env.MessageID, env.Subject, env.Date
		if from := addrs(env.From); len(from) > 0 {
			p.From = from[0]
		}
		p.To, p.Cc = addrs(env.To), addrs(env.Cc)
		if p.InReplyTo == "" && len(env.InReplyTo) > 0 {
			p.InReplyTo = env.InReplyTo[0]
		}
	}
	if p.Date.IsZero() {
		p.Date = m.InternalDate
	}
	parts, atts := mail.ParseStructure(m.BodyStructure)
	p.Attachments = atts
	for _, tp := range parts {
		raw, fetched := texts[tp.Part]
		if cut := int64(tp.Size) > PartFetchBytes; cut || !fetched {
			p.Truncated = true
			if i := bytes.LastIndexByte(raw, '\n'); cut && i >= 0 {
				raw = raw[:i+1] // whole lines decode cleanly in base64 and quoted-printable
			}
		}
		b, err := mail.DecodePart(raw, tp.Encoding, tp.Charset)
		if err != nil {
			b = raw // undecodable: keep the raw text rather than nothing
		}
		dst := &p.Text
		if tp.ContentType == "text/html" {
			dst = &p.HTML
		}
		p.Truncated = appendCapped(dst, b) || p.Truncated
	}
	if p.Text == "" && p.HTML != "" {
		p.Text = mail.HTMLToText(p.HTML)
	}
	return p
}

// appendCapped appends b to *dst, cutting at a rune boundary so *dst stays within
// MaxBodyBytes. It reports whether anything was cut.
func appendCapped(dst *string, b []byte) bool {
	room := mail.MaxBodyBytes - len(*dst)
	if len(b) <= room {
		*dst += string(b)
		return false
	}
	cut := max(room, 0)
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	*dst += string(b[:cut])
	return true
}

func addrs(l []imap.Address) []string {
	var out []string
	for _, a := range l {
		switch addr := a.Addr(); {
		case addr == "": // group syntax marker
		default:
			out = append(out, mail.FormatAddress(a.Name, addr))
		}
	}
	return out
}

func flagString(fl []imap.Flag) string {
	var out []string
	for _, f := range fl {
		if f != "\\Recent" {
			out = append(out, string(f))
		}
	}
	slices.Sort(out)
	return strings.Join(out, " ")
}

func jsonList(l []string) string {
	b, _ := json.Marshal(append([]string{}, l...))
	return string(b)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
