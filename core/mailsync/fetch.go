// Package mailsync pulls folders and messages from IMAP into the store.
package mailsync

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwitmail/godwit/core/events"
	"github.com/godwitmail/godwit/core/mail"
	"github.com/godwitmail/godwit/core/store"
)

// textBatch is how many new messages share one round of body-text FETCHes.
const textBatch = 50

type account struct {
	id    int64
	email string
	limit int
}

// result collects what one Fetch changed, for the events it publishes.
type result struct {
	received, threads, changed, changedThreads []int64
	conflicts                                  []string
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
func Fetch(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64, bus *events.Bus) error {
	var r result
	defer func() {
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
	}()
	rows, err := db.Query(ctx, "SELECT email, sync_limit FROM accounts WHERE id = ?", accountID)
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		return fmt.Errorf("account %d not found", accountID)
	}
	a := &account{id: accountID, email: store.Str(rows.Values[0][0]), limit: int(store.Int(rows.Values[0][1]))}
	folders, err := syncFolders(ctx, db, c, accountID, &r)
	if err != nil {
		return err
	}
	for _, f := range folders {
		if err := syncFolder(ctx, db, c, a, f, &r); err != nil {
			return fmt.Errorf("sync %q: %w", f.name, err)
		}
	}
	return nil
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
		{SQL: "UPDATE folders SET uidvalidity = ?, last_uid = 0 WHERE id = ?", Args: []any{uidValidity, f.id}},
	}
	if _, err := db.Transaction(ctx, append(stmts, tidyThreads(accountID, tids)...)); err != nil {
		return err
	}
	r.gone(ids, tids)
	f.uidValidity, f.lastUID = uidValidity, 0
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

// fetchNew imports messages above last_uid; the first sync takes only the newest
// sync_limit (n is the mailbox's message count).
func fetchNew(ctx context.Context, db *store.DB, c *imapclient.Client, a *account, f *folder, n uint32, r *result) error {
	if n == 0 {
		return nil
	}
	var set imap.NumSet
	if f.lastUID == 0 {
		start := uint32(1)
		if a.limit > 0 && n > uint32(a.limit) {
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
	msgs, err := c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true, RFC822Size: true, Envelope: true, InternalDate: true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true}, BodySection: []*imap.FetchItemBodySection{headerSection()}}).Collect()
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
	slices.SortFunc(todo, func(x, y *imapclient.FetchMessageBuffer) int { return cmp.Compare(x.UID, y.UID) })
	twins := map[string]int{} // arrivals per Message-ID and size, for claims
	for _, m := range todo {
		twins[twinKey(m)]++
	}
	for batch := range slices.Chunk(todo, textBatch) {
		texts, err := fetchTexts(c, batch)
		if err != nil {
			return err
		}
		for _, m := range batch {
			if err := importMessage(ctx, db, a, f, m, texts[m.UID], twins[twinKey(m)], r); err != nil {
				return err
			}
		}
	}
	if last == f.lastUID {
		return nil
	}
	if _, err := db.Execute(ctx, "UPDATE folders SET last_uid = ? WHERE id = ?", last, f.id); err != nil {
		return err
	}
	f.lastUID = last
	return nil
}

func twinKey(m *imapclient.FetchMessageBuffer) string {
	if m.Envelope == nil {
		return ""
	}
	return m.Envelope.MessageID + "\x00" + strconv.FormatInt(m.RFC822Size, 10)
}

// fetchTexts downloads the displayable text parts of msgs, keyed by UID then part
// path. Messages with the same part layout share one UID FETCH.
func fetchTexts(c *imapclient.Client, msgs []*imapclient.FetchMessageBuffer) (map[imap.UID]map[string][]byte, error) {
	groups := map[string][]imap.UID{}
	layout := map[string][]mail.Attachment{}
	for _, m := range msgs {
		parts, _ := mail.ParseStructure(m.BodyStructure)
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
			secs = append(secs, &imap.FetchItemBodySection{Part: partPath(p.Part), Peek: true})
		}
		// ponytail: whole text parts are downloaded and cut to MaxBodyBytes afterwards;
		// a partial FETCH would need care with base64/QP boundaries.
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
		b, err := mail.DecodePart(texts[tp.Part], tp.Encoding, tp.Charset)
		if err != nil {
			b = texts[tp.Part] // undecodable: keep the raw text rather than nothing
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
