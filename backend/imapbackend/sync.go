package imapbackend

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/backend"
)

// pageSize is how many messages one page fetches when SyncRequest.PageSize is 0.
const pageSize = 1000

// folder is a folder in a sync round: its name (its FolderRef) and the state
// its cursor encodes.
type folder struct {
	name           string
	role           backend.Role
	cursor         backend.Cursor
	validity, last uint32
	synced         bool
	filled         time.Time
}

func newFolder(name string, role backend.Role, st backend.FolderState) *folder {
	f := &folder{name: name, role: role, cursor: st.Cursor, synced: st.Synced, filled: st.Filled}
	f.validity, f.last = Decode(string(st.Cursor))
	return f
}

func (f *folder) fref() backend.FolderRef { return backend.FolderRef(f.name) }

func (f *folder) ref(uid uint32) backend.MessageRef {
	return backend.MessageRef(Encode(f.validity, uid))
}

func (f *folder) state() backend.FolderState {
	return backend.FolderState{Cursor: backend.Cursor(Encode(f.validity, f.last)), Synced: f.synced, Filled: f.filled}
}

// syncer is one Sync call.
type syncer struct {
	c       *imapclient.Client
	req     backend.SyncRequest
	commit  func(backend.Page) error
	gmail   bool
	section *imap.FetchItemBodySection
	page    int
	budget  int // range backfill pages left this round
	more    bool
}

// Sync runs one sync round: the folder list, then each folder's new mail,
// backfill and flags snapshot, in LIST order. A folder the server refuses is
// skipped, so it cannot hold back the others' arrivals round after round; its
// refusal comes back as a backend.FolderError, and a broken connection still
// stops the round.
func (r *Receiver) Sync(ctx context.Context, req backend.SyncRequest, commit func(backend.Page) error) (bool, error) {
	c, err := r.client(ctx)
	if err != nil {
		return false, err
	}
	s := &syncer{c: c, req: req, commit: commit, gmail: c.Caps().Has(GmailFeature), page: cmp.Or(req.PageSize, pageSize), budget: math.MaxInt,
		section: &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: req.HeaderFields, Peek: true}}
	if req.Pages > 0 {
		s.budget = req.Pages
	}
	if req.Older > 0 {
		return s.older(ctx)
	}
	folders, err := s.folders(ctx)
	if err != nil {
		return false, err
	}
	if s.gmail {
		if err := s.identify(ctx, folders); err != nil {
			return false, err
		}
	}
	var refused error
	for _, f := range folders {
		if req.Only != nil && !slices.Contains(req.Only, f.fref()) {
			continue
		}
		if err := s.folder(ctx, f); err != nil {
			err = fmt.Errorf("sync %q: %w", f.name, err)
			var no *imap.Error
			if !errors.As(err, &no) || ctx.Err() != nil {
				return s.more, err
			}
			refused = errors.Join(refused, &backend.FolderError{Folder: f.fref(), Err: err})
		}
	}
	if refused != nil { // a skipped folder may yet hold a message reported lost
		return s.more, refused
	}
	if s.gmail && req.Only == nil {
		return s.more, s.keepInAllMail(ctx, folders)
	}
	return s.more, nil
}

// folders reports the server's selectable folders and those gone, in one
// page, and returns them in LIST order. An empty LIST reports nothing.
func (s *syncer) folders(ctx context.Context) ([]*folder, error) {
	list, err := s.c.List("", "*", nil).Collect()
	if err != nil {
		return nil, err
	}
	var page backend.Page
	var out []*folder
	listed := map[backend.FolderRef]bool{}
	rs := Roles(list)
	for i, d := range list {
		if !Selectable(d) {
			continue
		}
		f := folderOf(d, rs[i])
		listed[f.Ref] = true
		page.Changes = append(page.Changes, backend.FolderUpdated{Folder: f})
		out = append(out, newFolder(d.Mailbox, rs[i], s.req.Folders[f.Ref]))
	}
	if len(out) == 0 {
		return nil, nil
	}
	for _, ref := range slices.Sorted(func(yield func(backend.FolderRef) bool) {
		for ref := range s.req.Folders {
			if !listed[ref] && !yield(ref) {
				return
			}
		}
	}) {
		page.Changes = append(page.Changes, backend.FolderGone{Ref: ref})
	}
	return out, s.commit(page)
}

func (s *syncer) folder(ctx context.Context, f *folder) error {
	sel, err := s.c.Select(f.name, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return err
	}
	if f.validity != sel.UIDValidity {
		reset := f.cursor != "" // a folder new to local storage has nothing to void
		f.validity, f.last, f.synced, f.filled = sel.UIDValidity, 0, false, time.Time{}
		if reset {
			if err := s.commit(backend.Page{Changes: []backend.Change{backend.FolderReset{Ref: f.fref()}},
				Folders: map[backend.FolderRef]backend.FolderState{f.fref(): f.state()}}); err != nil {
				return err
			}
		}
	}
	if err := s.fetchNew(ctx, f, sel.NumMessages); err != nil {
		return err
	}
	if err := s.backfill(ctx, f, sel.NumMessages); err != nil {
		return err
	}
	return s.snapshot(f)
}

// put commits changes and, when not nil, the folder's state.
func (s *syncer) put(f *folder, changes []backend.Change, state bool) error {
	p := backend.Page{Changes: changes}
	if state {
		p.Folders = map[backend.FolderRef]backend.FolderState{f.fref(): f.state()}
	}
	return s.commit(p)
}

// fetchNew reports messages above the cursor's last UID; the first sync of a
// count window takes only the newest Window.Newest (n is the mailbox's
// message count), and that of a months or all window only notes the top UID,
// leaving the mail to the backfill. What it reports in a folder whose first
// sync completed is new mail, whatever its date (a folder synced empty
// included); completing one marks it synced.
func (s *syncer) fetchNew(ctx context.Context, f *folder, n uint32) error {
	limit := s.req.Window.Newest
	if n == 0 {
		return s.advance(f, f.last, nil)
	}
	if !f.synced && limit <= 0 { // a folder synced empty gets its mail as new below
		top, err := s.c.Fetch(imap.SeqSetNum(n), &imap.FetchOptions{UID: true}).Collect()
		if err != nil {
			return err
		}
		if len(top) == 0 {
			return fmt.Errorf("no message %d", n)
		}
		return s.advance(f, uint32(top[0].UID), nil)
	}
	// A first sync, or the reimport after a UIDVALIDITY reset (which clears
	// synced), is not new mail, even if an earlier attempt stopped partway.
	backfill := !f.synced
	if f.last == 0 && limit > 0 {
		start := uint32(1)
		if n > uint32(limit) {
			start = n - uint32(limit) + 1
		}
		var set imap.SeqSet
		set.AddRange(start, 0)
		return s.importAbove(ctx, f, set, backfill, true)
	}
	// A page at a time: a folder that gains 50,000 messages at once (a bulk
	// move in webmail) never sits in memory whole, and a dropped connection
	// keeps the pages already reported.
	var above imap.UIDSet
	above.AddRange(imap.UID(f.last+1), 0)
	found, err := s.c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{above}}, nil).Wait()
	if err != nil {
		return err
	}
	uids := slices.DeleteFunc(found.AllUIDs(), func(u imap.UID) bool { return uint32(u) <= f.last }) // "n:*" includes n when nothing is new
	slices.Sort(uids)
	for page := range slices.Chunk(uids, s.page) {
		if err := s.importAbove(ctx, f, imap.UIDSetNum(page...), backfill, false); err != nil {
			return err
		}
	}
	return s.advance(f, f.last, nil)
}

// advance records a completed fetchNew, with changes: the cursor and synced in one page.
func (s *syncer) advance(f *folder, last uint32, changes []backend.Change) error {
	if last == f.last && f.synced && len(changes) == 0 {
		return nil
	}
	f.last, f.synced = last, true
	return s.put(f, changes, true)
}

// importAbove reports the messages of set above the last UID that local
// storage lacks, in UID order, and moves the cursor to the highest UID set
// has; done also marks the folder synced.
func (s *syncer) importAbove(ctx context.Context, f *folder, set imap.NumSet, backfill, done bool) error {
	msgs, top, located, err := s.fetchImport(ctx, f, set, backfill)
	if err != nil {
		return err
	}
	var refs []backend.MessageRef
	for _, m := range msgs {
		refs = append(refs, f.ref(uint32(m.UID)))
	}
	held, err := s.heldRefs(ctx, f, refs)
	if err != nil {
		return err
	}
	todo := msgs[:0]
	for _, m := range msgs {
		if uint32(m.UID) > f.last && !held[f.ref(uint32(m.UID))] {
			todo = append(todo, m)
		}
	}
	slices.SortFunc(todo, byUID)
	last := max(f.last, top)
	return s.appear(f, todo, backfill, located, func() {
		f.last = last
		f.synced = f.synced || done
	})
}

func (s *syncer) heldRefs(ctx context.Context, f *folder, refs []backend.MessageRef) (map[backend.MessageRef]bool, error) {
	if s.req.Local == nil || len(refs) == 0 {
		return map[backend.MessageRef]bool{}, nil
	}
	return s.req.Local.HeldRefs(ctx, f.fref(), refs)
}

func (s *syncer) held(ctx context.Context, f *folder) (map[backend.MessageRef]string, error) {
	if s.req.Local == nil {
		return map[backend.MessageRef]string{}, nil
	}
	return s.req.Local.Held(ctx, f.fref())
}

// importItems is what a sync fetches of each message before its text parts,
// with X-GM-MSGID only from a Gmail server: others refuse the item.
func (s *syncer) importItems() *imap.FetchOptions {
	return &imap.FetchOptions{UID: true, Flags: true, RFC822Size: true, InternalDate: true, GmailMsgID: s.gmail,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true}, BodySection: []*imap.FetchItemBodySection{s.section}}
}

// fetchImport fetches the messages of set to report in f, and the highest UID
// in set. From Gmail it first fetches only UID, FLAGS and X-GM-MSGID: a
// message local storage holds already, from another folder, is reported in f
// without content (located), and only the others are fetched whole (ADR 0034).
func (s *syncer) fetchImport(ctx context.Context, f *folder, set imap.NumSet, backfill bool) (msgs []*imapclient.FetchMessageBuffer, top uint32, located []backend.Change, err error) {
	opts := s.importItems()
	if s.gmail {
		opts = &imap.FetchOptions{UID: true, Flags: true, GmailMsgID: true}
	}
	if msgs, err = s.c.Fetch(set, opts).Collect(); err != nil {
		return nil, 0, nil, err
	}
	for _, m := range msgs {
		top = max(top, uint32(m.UID))
	}
	if !s.gmail {
		return msgs, top, nil, nil
	}
	var refs []backend.MessageRef
	var ids []string
	for _, m := range msgs {
		refs = append(refs, f.ref(uint32(m.UID)))
	}
	held, err := s.heldRefs(ctx, f, refs)
	if err != nil {
		return nil, 0, nil, err
	}
	for _, m := range msgs {
		if m.GmailMsgID != 0 && !held[f.ref(uint32(m.UID))] {
			ids = append(ids, gmailID(m))
		}
	}
	known := map[string]bool{}
	if s.req.Local != nil && len(ids) > 0 {
		if known, err = s.req.Local.KnownIDs(ctx, ids); err != nil {
			return nil, 0, nil, err
		}
	}
	var rest []imap.UID
	for _, m := range msgs {
		switch {
		case held[f.ref(uint32(m.UID))]:
		case m.GmailMsgID != 0 && known[gmailID(m)]:
			located = append(located, backend.MessageAppeared{Location: backend.Location{Folder: f.fref(), Ref: f.ref(uint32(m.UID)), ServerID: gmailID(m)},
				Flags: flagsOf(m.Flags), Backfill: backfill})
		default:
			rest = append(rest, m.UID)
		}
	}
	if len(rest) == 0 {
		return nil, top, located, nil
	}
	msgs, err = s.c.Fetch(imap.UIDSetNum(rest...), s.importItems()).Collect()
	return msgs, top, located, err
}

// gmailID is m's X-GM-MSGID as a ServerID: decimal text, "" when not fetched.
func gmailID(m *imapclient.FetchMessageBuffer) string {
	if m.GmailMsgID == 0 {
		return ""
	}
	return strconv.FormatUint(m.GmailMsgID, 10)
}

// appear reports located and msgs, in the order given, with the text parts the
// request picks, a page per text batch. advance, if not nil, moves f's state
// before the last page, which carries it.
func (s *syncer) appear(f *folder, msgs []*imapclient.FetchMessageBuffer, backfill bool, located []backend.Change, advance func()) error {
	if err := fillStructures(s.c, msgs); err != nil {
		return err
	}
	limit := s.req.TextBytes
	all := make([]fetched, len(msgs))
	for i, m := range msgs {
		all[i] = fetched{m: m, part: partOf(m.BodyStructure)}
		if all[i].part != nil && s.req.TextParts != nil {
			all[i].texts = s.req.TextParts(all[i].part)
			sz := map[backend.PartRef]int64{}
			sizes(all[i].part, sz)
			for _, p := range all[i].texts {
				all[i].cost += partCost(sz[p], limit)
			}
		}
	}
	batches := textBatches(all)
	if len(batches) == 0 {
		batches = [][]fetched{nil}
	}
	for i, batch := range batches {
		texts, err := fetchTexts(s.c, batch, limit)
		if err != nil {
			return err
		}
		changes := located
		located = nil
		for _, m := range batch {
			changes = append(changes, backend.MessageAppeared{
				Location: backend.Location{Folder: f.fref(), Ref: f.ref(uint32(m.m.UID)), ServerID: gmailID(m.m)},
				Flags:    flagsOf(m.m.Flags), Backfill: backfill,
				Content: &backend.Content{Size: m.m.RFC822Size, Date: m.m.InternalDate, Header: m.m.FindBodySection(s.section),
					Structure: m.part, Text: texts[m.m.UID]},
			})
		}
		last := i == len(batches)-1 && advance != nil
		if last {
			advance()
		}
		if len(changes) > 0 || last {
			if err := s.put(f, changes, last); err != nil {
				return err
			}
		}
	}
	return nil
}

func byUID(x, y *imapclient.FetchMessageBuffer) int { return cmp.Compare(x.UID, y.UID) }

// backfill reports older messages a lower Window.Newest left out: while the
// folder holds fewer than it, the newest server UIDs below the oldest one
// held, up to the shortfall (n is the mailbox's message count). A narrower
// window never deletes mail. A count pages only below the oldest held UID:
// after a months window left gaps above it (old-dated mail with high UIDs),
// those stay unfilled, which Older and an all window still reach.
func (s *syncer) backfill(ctx context.Context, f *folder, n uint32) error {
	limit := s.req.Window.Newest
	if f.last == 0 {
		return nil
	}
	if limit <= 0 {
		return s.backfillRange(ctx, f)
	}
	held, err := s.held(ctx, f)
	if err != nil {
		return err
	}
	have, below := len(held), f.last+1
	for ref := range held {
		_, uid := Decode(string(ref))
		below = min(below, uid)
	}
	if have >= limit || have >= int(n) || below <= 1 {
		return nil
	}
	var older imap.UIDSet
	older.AddRange(1, imap.UID(below-1))
	found, err := s.c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{older}}, nil).Wait()
	if err != nil {
		return err
	}
	uids := found.AllUIDs()
	slices.Sort(uids)
	uids = uids[max(len(uids)-(limit-have), 0):]
	if len(uids) == 0 {
		return nil
	}
	msgs, _, located, err := s.fetchImport(ctx, f, imap.UIDSetNum(uids...), true)
	if err != nil {
		return err
	}
	// Newest first: if this stops partway, the oldest held UID still bounds
	// what is missing and the next round resumes below it.
	slices.SortFunc(msgs, func(x, y *imapclient.FetchMessageBuffer) int { return byUID(y, x) })
	return s.appear(f, msgs, true, located, nil)
}

// backfillRange reports the messages up to the last UID that a months window
// (or all) covers and the folder lacks: UID SEARCH SINCE the cutoff, less what
// is held, newest first in pages. UIDs and INTERNALDATE are not monotonic, so
// it diffs the whole window rather than paging below the oldest held UID. A
// window filled once is not searched again until it grows (Filled).
func (s *syncer) backfillRange(ctx context.Context, f *folder) error {
	since := s.req.Window.Since
	mark := time.Unix(0, 0)
	if !since.IsZero() {
		mark = since
	}
	if !f.filled.IsZero() && !f.filled.After(mark) {
		return nil
	}
	if s.budget <= 0 {
		s.more = true // this round's pages are spent
		return nil
	}
	var upto imap.UIDSet
	upto.AddRange(1, imap.UID(f.last))
	found, err := s.c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{upto}, Since: since}, nil).Wait()
	if err != nil {
		return err
	}
	held, err := s.held(ctx, f)
	if err != nil {
		return err
	}
	uids := slices.DeleteFunc(found.AllUIDs(), func(u imap.UID) bool { _, ok := held[f.ref(uint32(u))]; return ok })
	slices.SortFunc(uids, func(x, y imap.UID) int { return cmp.Compare(y, x) })
	for page := range slices.Chunk(uids, s.page) {
		if s.budget <= 0 {
			s.more = true // Filled stays: the next round searches again
			return nil
		}
		s.budget--
		msgs, _, located, err := s.fetchImport(ctx, f, imap.UIDSetNum(page...), true)
		if err != nil {
			return err
		}
		slices.SortFunc(msgs, func(x, y *imapclient.FetchMessageBuffer) int { return byUID(y, x) })
		if err := s.appear(f, msgs, true, located, nil); err != nil {
			return err
		}
	}
	f.filled = mark
	return s.put(f, nil, true)
}

// snapshot reports the flags of 1:last UID, which local storage diffs for
// changed flags and expunged locations. Nothing unless the FETCH completed:
// a missing UID might only mean a dropped connection.
// shortcut: refetches every FLAGS each round, a cost that grows with the
// folder; switch to CONDSTORE/QRESYNC (FlagsChanged, MessageGone) when large
// folders make rounds slow or the traffic shows up in error reports.
func (s *syncer) snapshot(f *folder) error {
	if f.last == 0 {
		return nil
	}
	var set imap.UIDSet
	set.AddRange(1, imap.UID(f.last))
	bufs, err := s.c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		return err
	}
	snap := backend.FolderSnapshot{Folder: f.fref(), Messages: make(map[backend.MessageRef]backend.Flags, len(bufs))}
	for _, b := range bufs {
		snap.Messages[f.ref(uint32(b.UID))] = flagsOf(b.Flags)
	}
	return s.put(f, []backend.Change{snap}, false)
}

// identify gives Gmail X-GM-MSGIDs to the locations local storage holds
// without one, stored before a sync fetched them (ADR 0034), folder by
// folder, before any folder syncs. A folder recreated since is left to its
// reset.
func (s *syncer) identify(ctx context.Context, folders []*folder) error {
	for _, f := range folders {
		held, err := s.held(ctx, f)
		if err != nil {
			return err
		}
		var uids []imap.UID
		for ref, id := range held {
			if v, uid := Decode(string(ref)); id == "" && v == f.validity && f.cursor != "" {
				uids = append(uids, imap.UID(uid))
			}
		}
		if len(uids) == 0 {
			continue
		}
		sel, err := s.c.Select(f.name, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			return err
		}
		if sel.UIDValidity != f.validity {
			continue
		}
		msgs, err := s.c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, Flags: true, GmailMsgID: true}).Collect()
		if err != nil {
			return err
		}
		var changes []backend.Change
		for _, m := range msgs {
			if m.GmailMsgID != 0 {
				changes = append(changes, backend.MessageAppeared{Location: backend.Location{Folder: f.fref(), Ref: f.ref(uint32(m.UID)), ServerID: gmailID(m)},
					Flags: flagsOf(m.Flags), Backfill: true})
			}
		}
		if len(changes) > 0 {
			if err := s.commit(backend.Page{Changes: changes}); err != nil {
				return err
			}
		}
	}
	return nil
}

// keepInAllMail reports the lost messages Gmail's All Mail still has: a
// count window may never have cached their UID there, so a complete round
// alone does not prove a message is gone.
func (s *syncer) keepInAllMail(ctx context.Context, folders []*folder) error {
	i := slices.IndexFunc(folders, func(f *folder) bool { return f.role == backend.RoleAll })
	if i < 0 || s.req.Local == nil {
		return nil
	}
	lost, err := s.req.Local.Lost(ctx)
	if err != nil || len(lost) == 0 {
		return err
	}
	all := folders[i]
	sel, err := s.c.Select(all.name, nil).Wait()
	if err != nil {
		return err
	}
	var uids []imap.UID
	ids := map[imap.UID]string{}
	for _, id := range lost {
		gid, _ := strconv.ParseUint(id, 10, 64)
		found, err := s.c.UIDSearch(&imap.SearchCriteria{GmailMsgID: []uint64{gid}}, nil).Wait()
		if err != nil {
			return err
		}
		if uid := single(found.All); uid != 0 {
			uids, ids[imap.UID(uid)] = append(uids, imap.UID(uid)), id
		}
	}
	if len(uids) == 0 {
		return nil
	}
	msgs, err := s.c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, Flags: true}).Collect()
	if err != nil {
		return err
	}
	var changes []backend.Change
	for _, m := range msgs {
		changes = append(changes, backend.MessageAppeared{Location: backend.Location{Folder: all.fref(),
			Ref: backend.MessageRef(Encode(sel.UIDValidity, uint32(m.UID))), ServerID: ids[m.UID]}, Flags: flagsOf(m.Flags), Backfill: true})
	}
	return s.commit(backend.Page{Changes: changes})
}

// single returns the only UID of s, or 0.
func single(s imap.NumSet) uint32 {
	u, _ := s.(imap.UIDSet)
	if n, ok := u.Nums(); ok && len(n) == 1 {
		return uint32(n[0])
	}
	return 0
}

// older reports one page of each Only folder's mail that the window left out:
// the newest server UIDs up to the last UID that local storage lacks.
func (s *syncer) older(ctx context.Context) (more bool, err error) {
	for _, ref := range s.req.Only {
		f := newFolder(string(ref), "", s.req.Folders[ref])
		m, err := s.olderIn(ctx, f)
		if err != nil {
			return false, err
		}
		more = more || m
	}
	return more, nil
}

func (s *syncer) olderIn(ctx context.Context, f *folder) (bool, error) {
	sel, err := s.c.Select(f.name, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return false, err
	}
	if !f.synced || f.validity != sel.UIDValidity {
		return false, backend.ErrNotSynced
	}
	if f.last == 0 { // synced empty: nothing older
		return false, nil
	}
	held, err := s.held(ctx, f)
	if err != nil {
		return false, err
	}
	known := map[imap.UID]bool{}
	for ref := range held {
		_, uid := Decode(string(ref))
		known[imap.UID(uid)] = true
	}
	// Down from the newest by sequence number, a window of UIDs at a time,
	// until a page (and one more, for more) the folder lacks: no UID SEARCH
	// of the whole folder per page.
	var uids []imap.UID
	window := uint32(5 * s.req.Older)
	for hi := sel.NumMessages; hi > 0 && len(uids) <= s.req.Older; {
		lo := max(1, int64(hi)-int64(window)+1)
		var seq imap.SeqSet
		seq.AddRange(uint32(lo), hi)
		got, err := s.c.Fetch(seq, &imap.FetchOptions{UID: true}).Collect()
		if err != nil {
			return false, err
		}
		slices.SortFunc(got, func(x, y *imapclient.FetchMessageBuffer) int { return byUID(y, x) })
		for _, m := range got {
			if m.UID <= imap.UID(f.last) && !known[m.UID] {
				known[m.UID] = true // once, should an expunge shift a window
				uids = append(uids, m.UID)
			}
		}
		hi = uint32(lo) - 1
	}
	if len(uids) == 0 {
		return false, nil
	}
	msgs, _, located, err := s.fetchImport(ctx, f, imap.UIDSetNum(uids[:min(len(uids), s.req.Older)]...), true)
	if err != nil {
		return false, err
	}
	slices.SortFunc(msgs, func(x, y *imapclient.FetchMessageBuffer) int { return byUID(y, x) })
	return len(uids) > s.req.Older, s.appear(f, msgs, true, located, nil)
}
