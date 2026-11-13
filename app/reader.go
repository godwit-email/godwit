package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"maps"
	"math"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

// reader shows the thread the route names, or only its message when the
// route names one: older read messages fold to a line, unread ones and the
// newest open. It is the pane, which stays, and the session it shows,
// which a switch replaces whole (on). All its state lives on the UI thread.
type reader struct {
	*readerPane
	*session
}

// readerPane is what the reading pane keeps whichever thread it shows.
type readerPane struct {
	p *mailPage

	read latest // the thread's read: the one shown again, or the one to switch to
	// next is the thread being read to replace the one shown (switching):
	// what shows stays until it is read, rather than a blank pane between
	// (#667). Zero while none is.
	next [2]int64
	// prev is the thread shown before the switch, still drawn while the
	// new one's newest body builds unseen under it, until its page reports
	// its height (waiting) or prevStop's time is up: no blank body between
	// (#667). Nil while none is.
	prev     *session
	prevStop func()
	visits   uint64 // the sessions it has shown, numbering each (session.visit)

	ai        map[int64]*msgAI         // by message; kept across switches so a summary runs once
	skills    fetch[[]core.SkillInfo]  // the skill menu: skills on messages but summarize and translate
	actions   fetch[[]core.ActionInfo] // and plugin actions on messages
	barTop    float32                  // the floating toolbar's height as the last frame laid it out: the messages start under it
	barBottom float32                  // the reply bar's, likewise: the messages end above it
	pane      ui.Rect                  // the messages' scroll box as the last frame laid it out, in the window: the bars float at its top and bottom

	getThread  func(ctx context.Context, id int64) (core.Thread, error)                       // tests wrap it
	getMessage func(ctx context.Context, id int64, remote bool) (core.Message, error)         // tests wrap it
	getAvatar  func(context.Context, int64, bool) (core.Avatar, error)                        // tests wrap it
	runSkill   func(ctx context.Context, name string, t core.Target) (<-chan ai.Event, error) // tests wrap it
	cardsFn    func(ctx context.Context, messageID int64) ([]core.Card, error)                // tests wrap it
	tagsFn     func(ctx context.Context, messageID int64) ([]core.Tag, error)
	quickFn    func(ctx context.Context, messageID int64) ([]core.QuickReply, error) // tests wrap it
	gistFn     func(ctx context.Context, messageID int64) (string, error)            // tests wrap it
	skillsFn   func(ctx context.Context) ([]core.SkillInfo, error)                   // tests wrap it
	actionsFn  func(ctx context.Context, kind string) ([]core.ActionInfo, error)     // tests wrap it
}

// session is the thread, or message, the pane shows: from the route naming
// it until another replaces it. Dropping it drops all of it, and what was
// started for it lands nowhere once it is closed or no longer the one shown
// (reader.has).
type session struct {
	closed bool   // replaced or gone: its pages are closed
	visit  uint64 // its number in the pane: what outlives it tells it by this, not holding it

	key [2]int64        // the route's ThreadID and MessageID shown
	why core.ViewReason // why it is in the view shown
	// entered is whether the thread's parts were drawn: a part that comes
	// after enters with its motion; the thread itself comes at once (#667).
	entered bool
	loaded  bool
	subject string
	msgs    []core.MessageSummary // oldest first
	// threadIDs are every message of the thread, msgs or not (a route to one
	// message shows only it, and a message in several folders shows once):
	// what label changes apply to.
	threadIDs    []int64
	thread       core.Thread         // as last read: its copies read with what shows
	labels       []core.AppliedLabel // the thread's
	open         map[int64]bool      // messages shown in full
	focus        int64               // the message last expanded, or whose page took ⌘S/⌘P: what save and print act on while it is open
	historyShown bool                // the thread's history (its messages before the newest it keeps in sight) shown: a history card clicked
	historyOpen  map[int64]bool      // folded messages shown though the history is collapsed: ones the reader folded
	revealID     int64               // a message to scroll to once it is laid out (a digest's source); 0 for none
	bodies       map[int64]*msgBody
	scroll       ui.ScrollState // the messages' scrolling: the wheel and touchpad over a body's page scroll it (ScrollToContainer)

	aiCtx          context.Context    // its AI work; nil until some starts, cancelled as it goes (stopAI)
	aiStop         context.CancelFunc // cancels aiCtx
	egress         egressAsk          // the action waiting for egress approval
	egressDlg      bool               // its dialog shows
	unsubAsk       int64              // the message whose one-click unsubscribe waits to be confirmed
	unsubDlg       bool               // its dialog shows
	attConflict    attConflictAsk     // batch attachment save waiting on same-name conflict choice
	attConflictDlg bool               // its dialog shows
	detailsOpen    bool               // the recipients' details popover shows
	detailsID      int64              // which message's details popover is open
	replyAsk       string             // the reply bar's question to Wit, until it is asked
	copiedCard     string             // the card button that just copied (message/plugin/button), shown as Copied for a moment
}

// newSession starts the session of thread k.
func (p *readerPane) newSession(k [2]int64) *session {
	p.visits++
	return &session{visit: p.visits, key: k, open: map[int64]bool{}, historyOpen: map[int64]bool{}, bodies: map[int64]*msgBody{}}
}

// close closes the session's pages and drops what it has under way.
func (s *session) close() {
	s.closed = true
	for _, b := range s.bodies {
		b.drop()
	}
}

// on is a reader of r's pane showing s: the thread kept from before a
// switch, for its drawing and its pages' calls.
func (r *reader) on(s *session) *reader { return &reader{r.readerPane, s} }

// has reports whether s is the session r shows and is still open: what was
// started for it lands only then.
func (r *reader) has(s *session) bool { return r.session == s && !s.closed }

// msgBody is a message read in full, for its header, notices and
// attachments; its body shows in a page of its own (Platform.NewBodyView)
// while the message is expanded.
type msgBody struct {
	msg   core.Message
	cut   bool   // the page's body is only the start (ReaderBody.Cut, told by BodyBuilt)
	read  latest // reading it; a read again (a same-message folder move) drops the one before
	ready bool
	err   error // the last read, or making the page, failed; a retry reads again

	view        ui.NativeWebView // the body's page; nil while folded, not read yet, or when it could not be made
	close       func()           // closes view: unbinds and destroys it
	height      int              // the page's last reported height, CSS px; 0 until it reports
	glass       [2]int           // what the bars cover of the page, as last told it (BodyGlass); -1 to tell it again
	translation *aiRun           // the translated text currently displayed by the page

	// The remote images: allow is the user's 加载图片, which every page
	// made for the message is told (Platform.AllowImages); remote that the
	// page's body has them (BodyBuilt), which hides the notice; allowErr
	// that reading it so failed (BodyFailed), which offers a retry;
	// trusted that core loads them at once (ImagesAllowed): no notice.
	allow    bool
	remote   bool
	allowErr error
	trusted  bool
	// imgFailed is the images the proxy could not get (BodyImagesFailed); a
	// retry reloads the body. Offered only while no remote image is blocked,
	// as the retry is Allow, which lets them load.
	imgFailed int

	timing bodyTiming // how long the body took to show (GODWIT_READER_TIMING)

	unsub     core.Unsubscribe // how it unsubscribes, by its headers; none until asked
	unsubbing bool             // its unsubscribe is under way
	attSel    map[int64]bool   // selected attachment IDs for batch save

	senderAvatar
}

func newReader(p *mailPage) *reader {
	r := &reader{readerPane: &readerPane{p: p, ai: map[int64]*msgAI{},
		getThread: p.a.svc.GetThread, getMessage: p.a.svc.GetMessageLazyImages, getAvatar: p.a.svc.SenderAvatar, runSkill: p.a.svc.RunSkill,
		cardsFn: p.a.svc.PluginCards, tagsFn: p.a.svc.PluginTags, quickFn: p.a.svc.QuickReplies, gistFn: p.a.svc.MessageGist, skillsFn: p.a.svc.ListSkills, actionsFn: p.a.svc.ListActions}}
	r.session = r.newSession([2]int64{})
	return r
}

func isSeen(m core.MessageSummary) bool { return slices.Contains(m.Flags, `\Seen`) }

// sync follows the route: another thread or message is read afresh; with
// reread the one shown is read again (new messages, flags), keeping what
// is open.
func (r *reader) sync(reread bool) {
	a := r.p.a
	k := [2]int64{a.route.ThreadID, a.route.MessageID}
	if a.route.Page != PageMail {
		k = [2]int64{}
	}
	switch {
	case k == r.key && r.next != [2]int64{}: // back to the one shown before the switch was read
		r.next = [2]int64{}
		r.read.Stop() // drops the switch's read
		if !reread {
			return
		}
	case k != r.key && (k[0] == 0 || !r.loaded):
		r.next = [2]int64{}
		r.dropPrev()
		r.reset(k)
	case k != r.key:
		// Another thread: the one shown stays until it is read, and is
		// replaced at once then (load). A switch while one is under way
		// replaces it: its read is dropped.
		if k != r.next || reread {
			r.next = k
			r.load(k)
		}
		return
	case !reread:
		return
	}
	if k[0] != 0 {
		r.load(k)
	}
	r.loadWhy()
}

// reset closes the session shown and starts thread k's, at its top.
func (r *reader) reset(k [2]int64) {
	r.stopAI()
	r.session.close()
	r.session = r.newSession(k)
}

// prevWait is how long the thread before a switch stays at most, for a
// page that never reports its height.
const prevWait = time.Second

// retire starts thread k in place of the one shown, which stays drawn as
// prev, its pages open, until k's newest body has its height. A switch
// while one is under way keeps the prev shown and closes the unseen pages.
func (r *reader) retire(k [2]int64) {
	if r.prev == nil {
		r.stopAI()
		r.prev, r.session = r.session, r.newSession(k)

	} else {
		r.reset(k)
	}
	if r.prevStop != nil {
		r.prevStop()
	}
	r.prevStop = r.p.a.after(prevWait, r.dropPrev)
}

// dropPrev closes the thread kept from before a switch, if any.
func (r *reader) dropPrev() {
	if r.prevStop != nil {
		r.prevStop()
		r.prevStop = nil
	}
	if r.prev == nil {
		return
	}
	r.prev.close() // what it still had under way is dropped, not applied to a session no one sees
	r.prev = nil
}

// waiting is whether the thread is read and its newest body's page is still
// to report its height: the thread before stays drawn till then.
func (r *reader) waiting() bool {
	if !r.loaded || len(r.msgs) == 0 {
		return false
	}
	b := r.bodies[r.msgs[len(r.msgs)-1].ID]
	return b != nil && b.err == nil && b.height == 0
}

// load reads thread k: the one shown again, or the one to switch to
// (r.next), which replaces what shows once read.
func (r *reader) load(k [2]int64) {
	a, s, get := r.p.a, r.session, r.getThread
	route := k
	var th core.Thread
	var labels map[int64][]core.AppliedLabel
	r.read.Run(a, func(ctx context.Context) (err error) {
		if th, err = get(ctx, k[0]); err != nil {
			return err
		}
		if k[1] != 0 && !slices.ContainsFunc(th.Messages, func(m core.MessageSummary) bool { return m.ID == k[1] }) {
			m, err := a.svc.GetMessageLazyImages(ctx, k[1], false)
			if err != nil {
				return err
			}
			k[1] = m.ID // merged into another message since the route was set (ADR 0034)
		}
		labels, err = a.svc.ThreadLabels(ctx, []int64{k[0]})
		return err
	}, func(err error) {
		if !r.has(s) {
			return
		}
		if switching := r.next == route; switching { // what showed stays drawn till this one's body is sized
			r.next = [2]int64{}
			r.retire(route)
			defer r.loadWhy()
		}
		if err != nil {
			a.toastErr(err)
			return
		}
		r.threadIDs, r.labels, r.thread = nil, labels[k[0]], th
		for _, m := range th.Messages {
			r.threadIDs = append(r.threadIDs, m.ID)
		}
		msgs := th.Messages
		if k[1] != 0 {
			msgs = slices.DeleteFunc(slices.Clone(msgs), func(m core.MessageSummary) bool { return m.ID != k[1] })
		}
		type prevState struct {
			folder int64
			sec    gomail.SecurityStatus
		}
		known := map[int64]prevState{} // messages shown before keep their state
		for _, m := range r.msgs {
			known[m.ID] = prevState{folder: m.FolderID, sec: m.Security.Status}
		}
		r.loaded, r.subject, r.msgs = true, th.Subject, msgs
		for id, b := range r.bodies { // gone from the thread: its page goes too
			if !slices.ContainsFunc(msgs, func(m core.MessageSummary) bool { return m.ID == id }) {
				b.drop()
				delete(r.open, id)
				delete(r.bodies, id)
			}
		}
		var unread []core.MessageSummary
		for i, m := range msgs {
			if prev, ok := known[m.ID]; ok {
				if b := r.bodies[m.ID]; b != nil {
					b.msg.FromName = m.FromName
					if prev.folder != m.FolderID {
						b.msg.MessageSummary = m
						b.trusted = false
						if !b.avatarAllow || b.avatarSource == "carddav" {
							b.forgetAvatar(true) // read again with the body
						}
						b.read.Stop()
						r.loadBody(m.ID)
					} else if prev.sec != m.Security.Status {
						b.msg.Security = m.Security
						r.loadAvatar(m.ID, b.avatarAllow)
					}
				}
				continue
			}
			if !isSeen(m) {
				unread = append(unread, m)
			}
			if i == len(msgs)-1 { // the newest alone opens; unread older ones fold, read as the thread opens
				r.expand(m.ID)
			}
		}
		r.markRead(unread)
		r.startAI()
	})
}

// markRead flags msgs \Seen: one call per account, chained after the
// account's earlier writes.
func (r *reader) markRead(msgs []core.MessageSummary) {
	a := r.p.a
	byAcc := map[int64][]int64{}
	for _, m := range msgs {
		acc := r.p.accountOf(m.FolderID)
		byAcc[acc] = append(byAcc[acc], m.ID)
	}
	for _, acc := range slices.Sorted(maps.Keys(byAcc)) {
		ids := byAcc[acc]
		a.writes.Chain(fmt.Sprintf("account:%d", acc), func(ctx context.Context) error {
			return a.svc.SetFlags(ctx, ids, []string{`\Seen`}, nil)
		}, a.toastErr)
	}
	for i, m := range r.msgs {
		if !isSeen(m) && slices.ContainsFunc(msgs, func(u core.MessageSummary) bool { return u.ID == m.ID }) {
			r.msgs[i].Flags = append(slices.Clone(m.Flags), `\Seen`)
		}
	}
}

// expand shows a message in full, reading it unless it was read already
// (then its page is made again), and starts its AI.
func (r *reader) expand(id int64) {
	r.open[id] = true
	r.startFor(id)
	if b := r.bodies[id]; b == nil || b.err != nil {
		r.loadBody(id)
	} else {
		if b.avatarPending() {
			r.loadAvatar(id, b.avatarAllow)
		}
		r.openView(id)
	}
}

// toggle folds or expands a message; expanding an unread one marks it read.
// Folding closes its page.
func (r *reader) toggle(id int64) {
	if r.open[id] {
		delete(r.open, id)
		r.historyOpen[id] = true // folded in sight: not into the history beside it
		if b := r.bodies[id]; b != nil {
			b.closeView()
		}
		return
	}
	r.expand(id)
	r.focus = id
	for _, m := range r.msgs {
		if m.ID == id && !isSeen(m) {
			r.markRead([]core.MessageSummary{m})
		}
	}
}

// loadBody reads the message off the UI thread (remote images blocked:
// its page asks for them itself), then makes its page.
func (r *reader) loadBody(id int64) {
	a, s, get := r.p.a, r.session, r.getMessage
	b := r.bodies[id]
	if b == nil {
		b = &msgBody{}
		r.bodies[id] = b
	}
	if b.read.loading {
		return
	}
	b.err = nil
	b.timing.begin()
	var m core.Message
	var trusted bool
	b.read.Run(a, func(ctx context.Context) (err error) {
		if m, err = get(ctx, id, false); err != nil {
			return err
		}
		trusted, err = a.svc.ImagesAllowed(ctx, id)
		return err
	}, func(err error) {
		if !r.has(s) {
			return
		}
		if err != nil {
			b.err = err
			return
		}
		b.msg, b.ready, b.trusted = m, true, trusted
		b.timing.readDone()
		b.avatarBlocked = !trusted
		r.openView(id)
		r.loadAvatar(id, b.avatarAllow)
		r.loadUnsubscribe(id)
	})
}

// loadUnsubscribe asks how message id unsubscribes after its body shows:
// an older message's headers may be fetched from its server first. Until
// it answers, or when it fails, there is no 退订.
func (r *reader) loadUnsubscribe(id int64) {
	a, s, b := r.p.a, r.session, r.bodies[id]
	if b == nil { // opening its page may already have switched the reader away
		return
	}
	var u core.Unsubscribe
	b.read.Also(a, func(ctx context.Context) (err error) { // dropped with the body's read
		u, err = a.svc.UnsubscribeMethod(ctx, id)
		return err
	}, func(err error) {
		if err == nil && r.has(s) {
			b.unsub = u
		}
	})
}

// openView makes message id's page, if at this moment its body is read,
// of the thread shown, expanded, and without one. NewBodyView may run the
// event loop, so the reader may have moved on by its return: then the page
// is closed at once. A page of a message whose images the user allowed is
// told so.
func (r *reader) openView(id int64) {
	b := r.bodies[id]
	if b == nil || !b.ready || b.err != nil || !r.open[id] || b.view != nil {
		return
	}
	if pb := r.prev; pb != nil && pb.bodies[id] != nil && pb.bodies[id].view != nil {
		r.dropPrev() // a message has one page: a second would share, and outlive, the first's grants
	}
	s, made := r.session, time.Now()
	v, closeView, err := r.p.a.plat.NewBodyView(id)
	if !r.has(s) || r.bodies[id] != b || !r.open[id] || b.view != nil || r.p.a.ctx.Err() != nil {
		if err == nil {
			closeView()
		}
		return
	}
	if err != nil {
		b.err = err
		return
	}
	b.view, b.close = v, closeView
	b.timing.viewMade(made)
	if b.allow {
		r.p.a.plat.AllowImages(id)
	}
}

// drop closes b's page and drops its reads: it leaves the session.
func (b *msgBody) drop() {
	b.read.Stop()
	b.stopAvatar()
	b.closeView()
}

// closeView closes b's page, if it has one; what the page told goes with

// it (the user's 加载图片 stays).
func (b *msgBody) closeView() {
	if b.close != nil {
		b.close()
	}
	b.view, b.close, b.remote, b.allowErr = nil, nil, false, nil
	b.translation = nil
}

// closeViews closes every page (the App closing).
func (r *reader) closeViews() {
	r.forgetAvatars(true)
	r.dropPrev()
	for _, b := range r.bodies {
		b.closeView()
	}
}

// shown is the body whose page is v, if v is message's current page.
func (r *reader) shown(v ui.NativeWebView, message int64) *msgBody {
	if b := r.bodies[message]; b != nil && b.view != nil && b.view == v {
		return b
	}
	return nil
}

// focusView makes the message whose page is v, if shown, the one save and
// print act on, and reports whether it was.
func (r *reader) focusView(v ui.NativeWebView) bool {
	for id, b := range r.bodies {
		if b.view != nil && b.view == v && r.open[id] {
			r.focus = id
			return true
		}
	}
	return false
}

// current reports whether v is the page of a body shown.
func (r *reader) current(v ui.NativeWebView) bool {
	for id, b := range r.bodies {
		if b.view != nil && b.view == v {
			return r.open[id]
		}
	}
	return false
}

// accountOf is the account of a folder; 0 when the folder is unknown.
func (p *mailPage) accountOf(folderID int64) int64 {
	f, _ := p.folder(folderID)
	return f.AccountID
}

// folderAccount looks up the account of a folder in the Service.
func folderAccount(ctx context.Context, svc Service, folderID int64) (int64, error) {
	accs, err := svc.ListAccounts(ctx)
	if err != nil {
		return 0, err
	}
	for _, acc := range accs {
		fs, err := svc.ListFolders(ctx, acc.ID)
		if err != nil {
			return 0, err
		}
		if slices.ContainsFunc(fs, func(f core.Folder) bool { return f.ID == folderID }) {
			return acc.ID, nil
		}
	}
	return 0, fmt.Errorf("folder %d not found", folderID)
}

// last is the newest message shown: the one the AI buttons act on.
func (r *reader) last() (core.MessageSummary, bool) {
	if len(r.msgs) == 0 {
		return core.MessageSummary{}, false
	}
	return r.msgs[len(r.msgs)-1], true
}

// replyTarget is the message shown that replies act on: the thread's, or the
// newest when that one is not shown, as for a message read alone.
func (r *reader) replyTarget() (core.MessageSummary, bool) {
	return replyTarget(r.msgs, r.thread.ReplyTarget)
}

// replyTarget is the message with the ID target among msgs, else the newest.
func replyTarget(msgs []core.MessageSummary, target int64) (core.MessageSummary, bool) {
	if len(msgs) == 0 {
		return core.MessageSummary{}, false
	}
	if i := slices.IndexFunc(msgs, func(m core.MessageSummary) bool { return m.ID == target }); i >= 0 {
		return msgs[i], true
	}
	return msgs[len(msgs)-1], true
}

// target is the message save and print act on: the focused one while it is
// expanded, else the newest expanded, else the newest.
func (r *reader) target() (core.MessageSummary, bool) {
	var open []core.MessageSummary
	for _, m := range r.msgs {
		if r.focus == m.ID && r.open[m.ID] {
			return m, true
		}
		if r.open[m.ID] {
			open = append(open, m)
		}
	}
	if len(open) > 0 {
		return open[len(open)-1], true
	}
	return r.last()
}

// account is the account of the message shown; 0 when none is.
func (r *reader) account() int64 {
	m, ok := r.last()
	if !ok {
		return 0
	}
	return r.p.accountOf(m.FolderID)
}

// items are what the toolbar's actions apply to: the thread, or the message.
func (r *reader) items() []mailItem {
	if r.key[1] != 0 {
		if m, ok := r.last(); ok {
			return []mailItem{messageItem(m)}
		}
		return nil
	}
	return []mailItem{{ID: r.key[0], Thread: true, Count: len(r.msgs)}}
}

// act applies a list action to what is shown; the ones that take it out of
// the view close it, except Delete in Trash, which first asks.
func (r *reader) act(op string) {
	r.p.batch(op, r.items())
	if removes(op) && !r.p.purgeAsk {
		rt := &r.p.a.route
		rt.ThreadID, rt.MessageID = 0, 0
		r.sync(false)
	}
}

// draft starts a reply, reply-all or forward (see draftSource) and opens it
// in a compose window, even when the reader has moved on since: the window
// does not cover it.
func (r *reader) draft(mode string) {
	if m, ok := draftSource(r.msgs, r.thread.ReplyTarget, mode); ok {
		r.p.a.draft(m, r.account(), mode)
	}
}

// draftSource is the message of a thread a draft of mode starts from: a reply
// answers target; a forward takes the newest, so forwarding the user's own
// forward again keeps the note and attachments added to it.
func draftSource(msgs []core.MessageSummary, target int64, mode string) (core.MessageSummary, bool) {
	if mode == "forward" {
		target = 0
	}
	return replyTarget(msgs, target)
}

// draft starts a reply, reply-all or forward of m from account acc (0 to
// look it up by m's folder) and opens it in a compose window. A local
// archive's mail is answered from the default account that sends (core picks
// it): the compose window's From picks another.
func (a *App) draft(m core.MessageSummary, acc int64, mode string) {
	var id int64
	a.do(func(ctx context.Context) (err error) {
		if acc == 0 { // the folders have not loaded yet
			if acc, err = folderAccount(ctx, a.svc, m.FolderID); err != nil {
				return err
			}
		}
		d, err := a.svc.NewDraft(ctx, acc, mode, m.ID)
		id = d.ID
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		a.plat.OpenCompose(id)
	})
}

// cspHead keeps a page from loading anything remote or running scripts.
const cspHead = `<!doctype html><html><head><meta charset="utf-8">` +
	`<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'">`

// viewInBrowser writes the message's sanitized HTML (remote images blocked)
// to a temporary file under a CSP and opens it in the browser. Close removes it.
func (r *reader) viewInBrowser(id int64) {
	a := r.p.a
	plat := a.plat
	a.do(func(ctx context.Context) error {
		m, err := a.svc.GetMessage(ctx, id, false)
		if err != nil {
			return err
		}
		body := m.HTML
		if body == "" {
			body = "<pre>" + html.EscapeString(m.Text) + "</pre>"
		}
		dir, err := a.tempDir(plat.TempDir())
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(dir, "godwit-*.html")
		if err != nil {
			return err
		}
		_, err = io.WriteString(f, cspHead+"<title>"+html.EscapeString(m.Subject)+"</title></head><body>"+body+"</body></html>")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		return plat.OpenURL(fileURL(f.Name()))
	}, a.toastErr)
}

// fileURL is a file: URL of an absolute path.
func fileURL(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/… on Windows
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// openAttachment copies the attachment, under its own name, to a new
// directory in the platform's temporary one, and opens it with its
// application: the cache's file has no extension to choose one by. Close
// removes the copies.
func (a *App) openAttachment(att core.AttachmentInfo) {
	plat := a.plat
	a.do(func(ctx context.Context) error {
		src, _, err := a.svc.GetAttachment(ctx, att.ID)
		if err != nil {
			return err
		}
		dir, err := a.tempDir(plat.TempDir())
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, core.FileName(att.Filename, "attachment"))
		if err := copyFile(src, dst); err != nil {
			return err
		}
		return plat.OpenPath(dst)
	}, a.toastErr)
}

// saveAttachment copies the attachment where the user chooses.
func (a *App) saveAttachment(att core.AttachmentInfo) {
	plat := a.plat
	a.do(func(ctx context.Context) error {
		dst, err := plat.SaveFile(core.FileName(att.Filename, "attachment"))
		if err != nil || dst == "" {
			return err
		}
		src, _, err := a.svc.GetAttachment(ctx, att.ID)
		if err != nil {
			return err
		}
		return copyFile(src, dst)
	}, a.toastErr)
}

// copyFile copies an attachment out of the cache, marked as from the internet.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	_ = markFromInternet(dst) // best effort, as browsers do: a volume without the mark (FAT) still gets the file
	return nil
}

type attConflictMode int

const (
	attConflictKeepBoth attConflictMode = iota
	attConflictReplace
	attConflictSkip
)

type attConflictAsk struct {
	msgID     int64
	dir       string
	items     []core.AttachmentInfo
	conflicts []string
	existing  map[string]bool
}

const maxAttachmentNumbered = 1000

// copyFileNew copies src into dir under name (or "name (n)" past taken names),
// never replacing an existing file (O_EXCL). When skipIfExists is true and name
// itself is already taken on disk, it skips rather than numbering.
func copyFileNew(src, dir, name string, skipIfExists bool) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	for n := 1; n <= maxAttachmentNumbered; n++ {
		dst := filepath.Join(dir, core.NumberedName(name, n))
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
		if errors.Is(err, fs.ErrExist) {
			if n == 1 && skipIfExists {
				return "", nil
			}
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = io.Copy(out, in)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dst)
			return "", err
		}
		_ = markFromInternet(dst)
		return dst, nil
	}
	return "", fmt.Errorf("%s: no free name", name)
}

// replaceFile writes src to a temporary file in dst's directory and renames it
// over dst only after the copy succeeds, so a failed write leaves dst intact
// and a symlink or read-only file at dst is replaced rather than followed.
func replaceFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), ".godwit-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if st, lerr := os.Lstat(dst); lerr == nil {
		if st.Mode().IsRegular() {
			_ = os.Chmod(tmp, st.Mode().Perm()|0o600)
		}
		if runtime.GOOS == "windows" && !st.IsDir() {
			_ = os.Chmod(dst, 0o666)
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	_ = markFromInternet(dst)
	return nil
}

func (a *App) writeAttachmentsToDir(ctx context.Context, dir string, items []core.AttachmentInfo, mode attConflictMode, existing map[string]bool) (int, error) {
	var ids []int64
	for _, att := range items {
		name := core.FileName(att.Filename, "attachment")
		if mode == attConflictSkip && existing[name] {
			continue
		}
		ids = append(ids, att.ID)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	saved := 0
	var written []os.FileInfo
	sameWritten := func(name string) bool {
		st, err := os.Lstat(filepath.Join(dir, name))
		return err == nil && slices.ContainsFunc(written, func(w os.FileInfo) bool { return os.SameFile(w, st) })
	}
	recordWritten := func(dst string) {
		if st, err := os.Lstat(dst); err == nil {
			written = append(written, st)
		}
	}
	err := a.svc.FetchAttachments(ctx, ids, func(src string, info core.AttachmentInfo) error {
		name := core.FileName(info.Filename, "attachment")
		if mode == attConflictReplace && existing[name] && !sameWritten(name) {
			dst := filepath.Join(dir, name)
			if err := replaceFile(src, dst); err != nil {
				return err
			}
			recordWritten(dst)
			saved++
			return nil
		}
		dst, err := copyFileNew(src, dir, name, mode == attConflictSkip && !sameWritten(name))
		if err != nil {
			return err
		}
		if dst != "" {
			recordWritten(dst)
			saved++
		}
		return nil
	})
	return saved, err
}

func (r *reader) finishAttachmentsSaved(msgID int64, saved int, err error) {
	a := r.p.a
	if err != nil {
		a.toastErr(err)
		return
	}
	if b := r.bodies[msgID]; b != nil {
		clear(b.attSel)
	}
	if saved > 0 {
		a.toastAs(kit.ToastSuccess, i18n.Plural("reader.attachments.saved", saved))
	}
}

// saveAttachments asks for a target directory and writes items there, prompting
// first when any target filename already exists in that directory.
func (r *reader) saveAttachments(msgID int64, items []core.AttachmentInfo) {
	if len(items) == 0 {
		return
	}
	items = slices.Clone(items)
	a, plat, s := r.p.a, r.p.a.plat, r.session
	var (
		dir       string
		conflicts []string
		existing  map[string]bool
		saved     int
	)
	a.do(func(ctx context.Context) error {
		var err error
		dir, err = plat.OpenDirectory()
		if err != nil || dir == "" {
			return err
		}
		existing = map[string]bool{}
		var conflictFiles []os.FileInfo
		for _, att := range items {
			name := core.FileName(att.Filename, "attachment")
			st, err := os.Lstat(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			existing[name] = true
			if !slices.ContainsFunc(conflictFiles, func(f os.FileInfo) bool { return os.SameFile(f, st) }) {
				conflictFiles = append(conflictFiles, st)
				conflicts = append(conflicts, name)
			}
		}
		if len(conflicts) > 0 {
			return nil
		}
		saved, err = a.writeAttachmentsToDir(ctx, dir, items, attConflictKeepBoth, nil)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		if dir == "" {
			return
		}
		if len(conflicts) > 0 {
			if !r.has(s) {
				return
			}
			r.attConflict = attConflictAsk{
				msgID:     msgID,
				dir:       dir,
				items:     items,
				conflicts: conflicts,
				existing:  existing,
			}
			r.attConflictDlg = true
			return
		}
		r.finishAttachmentsSaved(msgID, saved, nil)
	})
}

func (r *reader) resolveAttachmentConflict(mode attConflictMode) {
	ask := r.attConflict
	r.attConflict, r.attConflictDlg = attConflictAsk{}, false
	if len(ask.items) == 0 || ask.dir == "" {
		return
	}
	a := r.p.a
	var saved int
	a.do(func(ctx context.Context) (err error) {
		saved, err = a.writeAttachmentsToDir(ctx, ask.dir, ask.items, mode, ask.existing)
		return err
	}, func(err error) {
		r.finishAttachmentsSaved(ask.msgID, saved, err)
	})
}

// saveMail saves the target message to an .eml file.
func (r *reader) saveMail() {
	m, ok := r.target()
	if !ok {
		return
	}
	a := r.p.a
	plat := a.plat
	saved := false
	a.do(func(ctx context.Context) error {
		stem := []rune(m.Subject)
		stem = stem[:min(len(stem), 100)] // file systems cap names near 255 bytes
		dst, err := plat.SaveFile(core.FileName(string(stem), "email") + ".eml")
		if err != nil || dst == "" {
			return err
		}
		raw, err := rawMessage(ctx, a.svc, m.ID)
		if err != nil {
			// Offline: a simplified copy built from what is stored, its HTML
			// body only (attachments are dropped). Build encodes the headers.
			html, herr := a.svc.StoredHTML(ctx, m.ID)
			if herr != nil {
				return err
			}
			if raw, _, err = gomail.Build(gomail.Outgoing{From: m.From, To: m.To, Cc: m.Cc, Subject: m.Subject, HTML: html}, m.Date); err != nil {
				return err
			}
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			return err
		}
		saved = true
		return nil
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
		} else if saved {
			a.toast(i18n.T("reader.saved"))
		}
	})
}

// printMail prints the target message's body page.
func (r *reader) printMail() {
	if m, ok := r.target(); ok {
		r.p.a.plat.Print(m.ID)
	}
}

// view is the reading pane: the toolbar over the subject and the messages;
// while a switch's body builds, the thread before it, the new body under.
func (r *reader) view(c *ui.Context) {
	if r.prev != nil && !r.waiting() {
		r.dropPrev()
	}
	if r.prev != nil {
		r.on(r.prev).draw(c, r.session)
		return
	}
	r.draw(c, nil)
}

// unseenBody lays out the newest body at the width it will show, unseen:
// faded out, it is placed but cuts no hole in the window, so its page
// builds and reports its height under the thread shown.
func (s *session) unseenBody(c *ui.Context) {
	if b := s.bodies[s.msgs[len(s.msgs)-1].ID]; b != nil && b.view != nil {
		kit.WebView(c, b.view).FillWidth().Height(c.Theme().Space(6)).Opacity(0)
	}
}

// draw is view's pane of r, under it next's unseen body when not nil.
func (r *reader) draw(c *ui.Context, next *session) {
	t, th := theme.Of(c), c.Theme()
	col := ui.Column(c).Grow(1).Shrink(1)
	if r.key[0] == 0 { // nothing chosen: the bird
		col.Center().Children(func() {
			ui.Icon(c, icons.Bird).Size(theme.Watermark, theme.Watermark).TextColor(t.Border)
		})
		return
	}
	if !r.loaded || len(r.msgs) == 0 {
		return
	}
	if next != nil {
		// Kept on screen only while next's body builds: what it would start
		// (a dialog, a read) would outlive it. Its pages still take clicks
		// (readerOf).
		col.IgnoreInput(true)
	} else {
		if c.Shortcut(ui.Cmd, ui.KeyS) {
			r.saveMail()
		}
		if c.Shortcut(ui.Cmd, ui.KeyP) {
			r.printMail()
		}
	}
	// The toolbar and the reply bar float over the messages, which scroll
	// under them: one sheet, the bars frosted over it.
	col.Children(func() {
		// next's unseen body, placed first so it stacks under r's pages, in a
		// slot always there: what follows keeps its place in the tree, or it
		// would go and come again with its motion (a grey block, a fade).
		ui.Column(c).Absolute().Left(th.Space(7)).Right(th.Space(7)).Top(0).Children(func() {
			if next != nil {
				next.unseenBody(c)
			}
		})
		r.pane = ui.Scroll(c).Grow(1).TrackScroll(&r.scroll).ScrollbarInsets(r.barTop, 0, r.barBottom).Children(func() {
			ui.Column(c).FillWidth().Padding(r.barTop+th.Space(5), th.Space(7), r.barBottom+th.Space(5)).Children(func() {
				ui.Column(c).FillWidth().Gap(th.Space(3.5)).Children(func() {
					subject := cmp.Or(r.subject, i18n.T("mail.no_subject"))
					r.column(c).Gap(th.Space(2)).Children(func() {
						// Plugins' tags follow the subject, so a glance at it is enough.
						ui.Row(c).FillWidth().Wrap().AlignItems(ui.Center).Gap(th.Space(1.5)).Children(func() {
							tx := kit.Text(c, subject, t.Text.X2l).FontWeight(600).LineHeight(theme.LineSubject).Shrink(1).Selectable() // copied as often as read
							if !r.hasTags() {
								// shortcut: MyGo measures large semibold text too narrow in a row, so it wraps early; with no tag to follow it, take the row. Drop it once MyGo measures it right.
								tx.Grow(1)
							}
							r.tagChips(c)
						})
						r.labelRow(c)
					})
					r.viewBand(c)
					if len(r.msgs) > 1 { // a thread's summary is the thread's: under its subject, not in a message
						if m, ok := r.last(); ok {
							r.column(c).Children(func() { r.summaryPanel(c, m) })
						}
					}
					r.renderMessages(c)
					r.viewWhyLine(c)
				})
			})
		}).LayoutBounds()
		r.barTop = r.measured(c, r.toolbar(c).Absolute().Left(0).Top(0).Right(0), r.barTop)
		r.barBottom = r.measured(c, r.replyBar(c).Absolute().Left(0).Right(0).Bottom(0), r.barBottom)
	})
}

// glass tells b's page how much of it the bars cover, box being where it
// shows this frame: the page blurs there itself, as the bars' blur cannot
// see a web view.
func (r *reader) glass(b *msgBody, box ui.Rect) {
	cover := func(px float32) int { return int(math.Ceil(float64(max(0, min(px, box.H))))) }
	g := [2]int{cover(r.pane.Y + r.barTop - box.Y), cover(box.Y + box.H - (r.pane.Y + r.pane.H - r.barBottom))}
	if g != b.glass && b.view != nil {
		b.glass = g
		r.p.a.plat.BodyGlass(b.view, g[0], g[1])
	}
}

// measured is e's height as the last frame laid it out, asking for another
// frame when it differs from was: what floats over the messages keeps them
// clear of itself.
func (r *reader) measured(c *ui.Context, e ui.Element, was float32) float32 {
	h := e.LayoutBounds().H
	if h != was {
		c.Invalidate()
	}
	return h
}

// toolbar: the reply group and the archive group as icon buttons, a menu
// of the rarer save and print, then the AI buttons, on a frosted bar the
// messages scroll under.
func (r *reader) toolbar(c *ui.Context) ui.Element {
	t, th := theme.Of(c), c.Theme()
	return ui.Row(c).Wrap().Gap(th.Space(0.5)).AlignItems(ui.Center).Padding(th.Space(2), th.Space(5)).Material(kit.Bar(t.Background)).BorderWidth(0, 0, theme.Hairline, 0).BorderColor(t.Border).Children(func() {
		kit.ButtonGroup(c,
			kit.GroupItem{Icon: "reply", Tip: i18n.T("reader.reply"), OnClick: func() { r.draft("reply") }},
			kit.GroupItem{Icon: "reply-all", Tip: i18n.T("reader.reply_all"), OnClick: func() { r.draft("reply_all") }},
			kit.GroupItem{Icon: "forward", Tip: i18n.T("reader.forward"), OnClick: func() { r.draft("forward") }},
		)
		starIcon, starTip := "star", i18n.T("mail.action.star")
		if r.starred() {
			starIcon, starTip = "star-fill", i18n.T("mail.action.unstar")
		}
		// item is act's button, greyed out with the reason as its tip when the server cannot.
		item := func(icon, tip, act string) kit.GroupItem {
			if why := r.p.cannot(r.items(), act); why != "" {
				return kit.GroupItem{Icon: icon, Tip: why, Disabled: true}
			}
			return kit.GroupItem{Icon: icon, Tip: tip, OnClick: func() { r.act(act) }}
		}
		file := []kit.GroupItem{item(starIcon, starTip, "toggle_star")}
		role := r.p.currentFolderRole()
		if role != "inbox" {
			file = append(file, item("inbox", i18n.T("mail.action.inbox"), "inbox"))
		}
		if role != "archive" {
			file = append(file, item("archive", i18n.T("mail.action.archive"), "archive"))
		}
		if role != "junk" {
			file = append(file, item("spam", i18n.T("mail.action.junk"), "junk"))
		}
		file = append(file, item("trash-2", i18n.T("mail.action.delete"), "delete"))
		kit.GroupDivider(c).Margin(0, th.Space(1.5))
		kit.ButtonGroup(c, file...)
		kit.IconButton(c, "ellipsis", i18n.T("reader.more"), kit.ButtonOpts{Variant: kit.Ghost}).TextColor(t.Foreground).Menu(func(m *ui.Menu) {
			if m.Item(i18n.T("reader.save")).Icon(menuIcon("save")).Chosen() {
				r.saveMail()
			}
			if m.Item(i18n.T("reader.print")).Icon(menuIcon("printer")).Chosen() {
				r.printMail()
			}
		})
		ui.Spacer(c)
		ui.Row(c).Wrap().Gap(th.Space(1)).AlignItems(ui.Center).Children(func() { r.aiTools(c) })
	})
}

// starred reports whether all the toolbar acts on is starred: toggle_star
// unstars only then, else it stars the rest.
func (r *reader) starred() bool {
	msgs := r.msgs
	if r.key[1] != 0 && len(msgs) > 0 { // a message's reader acts on it alone
		msgs = msgs[len(msgs)-1:]
	}
	return len(msgs) > 0 && !slices.ContainsFunc(msgs, func(m core.MessageSummary) bool {
		return !slices.Contains(m.Flags, `\Flagged`)
	})
}

// renderMessages renders a thread's messages. A thread of more than
// Settings.ThreadCollapseThreshold messages keeps its newest that many in
// sight; of its history before them, the open ones show and each run of two
// or more folded collapses into a card. A card shows the whole history,
// under one card that collapses it again, open ones and all. A message the
// reader folds stays in sight (historyOpen), not taken into the history.
func (r *reader) renderMessages(c *ui.Context) {
	th, st := c.Theme(), r.p.a.settings
	history := 0 // the messages before the newest kept in sight
	if st.ThreadCollapse {
		history = max(0, len(r.msgs)-st.ThreadCollapseThreshold)
	}
	hidden := func(i int) bool { return i < history && !r.historyShown && !r.historyOpen[r.msgs[i].ID] }
	n := 0 // what was rendered: a hairline parts each from the one before
	enter := func(m ui.ElementTransition) ui.ElementTransition {
		if !r.entered {
			m.Enter = nil
		}
		return m
	}
	defer func() { r.entered = true }()
	// part renders one part of the thread under key, with motion; a hairline
	// keeps to the message after it (sep).
	part := func(sep, key any, motion ui.ElementTransition, f func()) ui.Element {
		motion = enter(motion)
		if n > 0 {
			r.column(c).Key(sep).Transition(enter(threadMotion)).Children(func() { kit.Separator(c, false) })
		}
		n++
		// Clipped, a little larger than its content for the hovers that reach
		// past it (a folded row's, the sender's): it grows and shrinks as the
		// message opens, folds or its body reports.
		// Stretched, not FillWidth: its negative margins widen it, keeping its content where the subject is.
		return ui.Column(c).AlignSelf(ui.Stretch).Margin(-th.Space(1), -th.Space(2)).Padding(th.Space(1), th.Space(2)).Clip().Key(key).Transition(motion).Children(f)
	}
	if history > 0 && r.historyShown {
		part(threadSep{0}, threadHistory{0}, threadMotion, func() { r.historyToggle(c, 0, history) })
	}
	for i := 0; i < len(r.msgs); {
		m := r.msgs[i]
		if r.open[m.ID] {
			r.revealAt(c, m.ID, part(threadSep{m.ID}, threadPart{m.ID}, openMotion, func() { r.message(c, m) }))
			i++
			continue
		}
		j := i + 1
		for hidden(i) && j < len(r.msgs) && !r.open[r.msgs[j].ID] && hidden(j) {
			j++
		}
		if n := j - i; n >= 2 {
			part(threadSep{-m.ID}, threadHistory{m.ID}, threadMotion, func() { r.historyToggle(c, n, history) })
		} else { // in sight, or alone in the history: a card would hide no line
			part(threadSep{m.ID}, threadPart{m.ID}, threadMotion, func() { r.folded(c, m) })
		}
		i = j
	}
}

// revealAt scrolls the messages to message id's top, under the toolbar, once
// e, its part, has been laid out, when it is the one to reveal.
func (r *reader) revealAt(c *ui.Context, id int64, e ui.Element) {
	if id != r.revealID {
		return
	}
	b := e.LayoutBounds()
	if b.H == 0 { // not laid out yet: the next frame
		c.Invalidate()
		return
	}
	r.scroll.Y = max(0, r.scroll.Y+b.Y-r.pane.Y-r.barTop)
	r.revealID = 0
}

// The keys of a thread's parts (renderMessages): a message, open or folded
// (one element, so it resizes rather than one form going as the other
// comes), a history card (by the first message of its run; 0 the one that
// collapses the history), and the hairline before a message (before a
// card: its first message's, negated).
type (
	threadPart    struct{ id int64 }
	threadHistory struct{ first int64 }
	threadSep     struct{ id int64 }
)

// threadMotion is how a thread's parts come, go, move and resize.
var threadMotion = ui.ElementTransition{
	Duration: theme.MotionBase,
	Enter:    &ui.Motion{Collapse: true},
	Exit:     &ui.Motion{Collapse: true},
}

// nameMotion moves a message's sender name from its folded row to its
// open header, and back.
func nameMotion(id int64) ui.ElementTransition {
	return ui.ElementTransition{Duration: theme.MotionBase, Match: senderName{id}}
}

type senderName struct{ id int64 }

// openMotion is an open message's: no Exit, whose copy of the last frame
// cannot hold its body's native page (it showed as a grey block).
var openMotion = ui.ElementTransition{Duration: theme.MotionBase, Enter: &ui.Motion{Collapse: true}}

// historyToggle is a history card: one that shows the history, hiding a
// run of n of its messages, or with n 0 the one that collapses the history
// (its first messages of the thread), folding the open ones too.
func (r *reader) historyToggle(c *ui.Context, n, history int) {
	t, th := theme.Of(c), c.Theme()
	label, rot := i18n.Tf("reader.thread.expand", n), float32(0)
	if n == 0 {
		label, rot = i18n.T("reader.thread.collapse"), 180
	}
	b := kit.Pressable(c, kit.PressableOpts{OnClick: func() {
		r.historyShown = n > 0
		if n > 0 {
			return
		}
		clear(r.historyOpen)
		for _, m := range r.msgs[:history] {
			if r.open[m.ID] {
				delete(r.open, m.ID)
				if b := r.bodies[m.ID]; b != nil {
					b.closeView()
				}
			}
		}
	}}).
		FillWidth().MaxWidth(r.p.a.readerMaxWidth()).AlignSelf(ui.Center).Gap(th.Space(2.5)).Padding(th.Space(1.5), 0).Justify(ui.Center)
	b.Children(func() {
		ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted).Rotate(rot)
		kit.Text(c, label, t.Text.Sm).FontWeight(500).TextColor(t.Foreground)
	})
}

// folded is a message as one line: avatar, sender, the start of its text,
// time, and the chevron that expands it.
func (r *reader) folded(c *ui.Context, m core.MessageSummary) {
	t, th := theme.Of(c), c.Theme()
	name, _ := senderOf(m)
	avatar := r.shownAvatar(m) // else its initial
	// Its hover reaches past the column, so the avatar keeps the open messages' edge yet clears the hover's.
	r.column(c).Children(func() {
		kit.Pressable(c, kit.PressableOpts{OnClick: func() { r.toggle(m.ID) }}).AlignSelf(ui.Stretch).Margin(0, -th.Space(2)).
			AlignItems(ui.Center).Gap(th.Space(2.5)).Padding(th.Space(1.5), th.Space(2)).Children(func() {
			kit.AvatarImage(c, name, avatar)
			ui.Text(c, name).FontWeight(500).SingleLine().Shrink(0).Transition(nameMotion(m.ID))
			if r.isBlocked(m.From) {
				kit.Badge(c, i18n.T("reader.blocked_badge"))
			}
			r.securityBadge(c, m.ID, m.Security)
			if g := r.gist(m.ID); g != "" { // a step darker than the text it stands for, and marked the AI's
				ui.Row(c).Grow(1).Shrink(1).Gap(th.Space(1)).AlignItems(ui.Center).Children(func() {
					ui.Text(c, g).TextColor(t.Foreground.Alpha(0.7)).SingleLine().Shrink(1)
					mark := ui.Box(c).Shrink(0).Label(i18n.T("reader.gist")).Children(func() {
						ui.Icon(c, icons.Get("sparkles")).Size(theme.IconXs, theme.IconXs).TextColor(t.Idle)
					})
					kit.TooltipLabelled(c, mark, i18n.T("reader.gist"))
				})
			} else {
				ui.Text(c, m.Snippet).TextColor(t.Muted).SingleLine().Grow(1).Shrink(1)
			}
			kit.Text(c, listTime(r.p.a.clock.Now(), m.Date), t.Text.Sm).TextColor(t.Muted).Shrink(0)
			// in a box as wide as an open message's fold button: the chevrons line up
			ui.Box(c).Size(th.Space(7), th.Space(7)).Center().Shrink(0).Children(func() {
				ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
			})
		})
	})
}

func (r *reader) inJunk(m core.MessageSummary) bool {
	if r.p == nil || r.p.a == nil || r.p.a.mail == nil {
		return false
	}
	if f, ok := findFolder(r.p.a.mail.folders, m.FolderID); ok && f.Role == "junk" {
		return true
	}
	for _, fid := range m.FolderIDs {
		if f, ok := findFolder(r.p.a.mail.folders, fid); ok && f.Role == "junk" {
			return true
		}
	}
	return false
}

// shownAvatar is an avatar the thread already shows for m's sender: m's,
// read when it was open, else one of the sender's other messages'. Only
// those: an avatar loads as its message allows (loadAvatar).
func (r *reader) shownAvatar(m core.MessageSummary) ui.ImageSource {
	if r.inJunk(m) {
		return nil
	}
	fraud := m.Security.Status == gomail.SecurityFraud
	if b := r.bodies[m.ID]; b != nil && b.avatar != nil {
		if fraud && (b.avatarSource == "carddav" || !b.avatarAllow) {
			return nil
		}
		return b.avatar
	}
	_, addr := sender(m.From)
	if addr == "" {
		return nil
	}
	for _, b := range r.bodies {
		if _, from := sender(b.msg.From); b.avatar != nil && !b.msg.InJunk && strings.EqualFold(from, addr) {
			if fraud && (b.avatarSource == "carddav" || !b.avatarAllow) {
				continue
			}
			return b.avatar
		}
	}
	return nil
}

// sender splits a From into a name to show and the address.
func sender(from string) (name, addr string) {
	if a, err := mail.ParseAddress(from); err == nil {
		return cmp.Or(a.Name, a.Address), a.Address
	}
	return from, ""
}

// senderOf returns m's display name (falling back to m.FromName when From is a
// bare address) and parsed address without mutating m.From.
func senderOf(m core.MessageSummary) (name, addr string) {
	name, addr = sender(m.From)
	return cmp.Or(m.FromName, name), addr
}

// recipients is the header's line of who a message went to: the user's
// address that got it (its sidebar name, else "me" with one account) and how
// many others, To and Cc together; when the user is only Cc'd, To's first and
// the user under Cc; when neither (Bcc, a list), To's first; with no To or Cc,
// the account.
func (r *reader) recipients(m core.MessageSummary) string {
	accounts := r.p.a.accounts
	mine := func(addr string) bool {
		return slices.ContainsFunc(accounts, func(a core.Account) bool { return strings.EqualFold(a.Email, addr) })
	}
	me := func(addr string) string {
		if alias := r.alias(addr); alias != "" {
			return alias
		}
		if len(accounts) <= 1 {
			return i18n.T("reader.me")
		}
		return addr
	}
	type who struct{ name, addr string }
	parse := func(list []string) []who {
		var out []who
		for _, s := range list {
			name, addr := sender(s)
			out = append(out, who{name, addr})
		}
		return out
	}
	to, cc := parse(m.To), parse(m.Cc)
	more := func(first string, others int) string {
		if others == 0 {
			return first
		}
		return i18n.Tf("reader.recipients_more", first, others)
	}
	isMine := func(w who) bool { return mine(w.addr) }
	if i := slices.IndexFunc(to, isMine); i >= 0 {
		return i18n.Tf("reader.to", more(me(to[i].addr), len(to)+len(cc)-1))
	}
	if i := slices.IndexFunc(cc, isMine); i >= 0 {
		line := i18n.Tf("reader.cc", more(me(cc[i].addr), len(cc)-1))
		if len(to) > 0 {
			line = i18n.Tf("reader.to", more(to[0].name, len(to)-1)) + " · " + line
		}
		return line
	}
	switch {
	case len(to) > 0:
		return i18n.Tf("reader.to", more(to[0].name, len(to)+len(cc)-1))
	case len(cc) > 0:
		return i18n.Tf("reader.cc", more(cc[0].name, len(cc)-1))
	}
	return i18n.Tf("reader.to", me(r.accountOf(m)))
}

// alias is the sidebar name of the user's account at addr; "" when unset or not the user's.
func (r *reader) alias(addr string) string {
	for _, acc := range r.p.a.accounts {
		if strings.EqualFold(acc.Email, addr) {
			return acc.Alias
		}
	}
	return ""
}

// accountOf is the address of the account message m is in.
func (r *reader) accountOf(m core.MessageSummary) string {
	if f, ok := r.p.folder(m.FolderID); ok {
		for _, acc := range r.p.a.accounts {
			if acc.ID == f.AccountID {
				return acc.Email
			}
		}
	}
	return ""
}

// recipientsLine is recipients, which opens the message's details: every
// address, its date and the account it is in.
func (r *reader) recipientsLine(c *ui.Context, m core.MessageSummary, b *msgBody) {
	t, th := theme.Of(c), c.Theme()
	text := r.recipients(m)
	open := r.detailsOpen && r.detailsID == m.ID
	// Its hover clears the text by its padding; the margin keeps the text where it was.
	e := kit.Pressable(c, kit.PressableOpts{OnClick: func() { r.detailsOpen, r.detailsID = !open, m.ID }}).
		Gap(th.Space(0.5)).AlignItems(ui.Center).Padding(0, th.Space(1)).Margin(0, -th.Space(1)).Radius(t.Radius.Sm).
		Label(text).Expanded(open).Shrink(1)
	fg := t.Muted
	if open {
		e.Background(t.Hover)
		fg = t.Foreground
	}
	e.Children(func() {
		kit.Text(c, text, t.Text.Sm).TextColor(fg).Shrink(1)
		chev := ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconXs, theme.IconXs).TextColor(t.Muted).Shrink(0)
		if open {
			chev.Rotate(180)
		}
	})
	if open {
		r.details(c, e, m, b)
	}
}

// details lists who sent message m and to whom, its reply-to, date and,
// with more than one account, the account; the values select to copy.
func (r *reader) details(c *ui.Context, anchor ui.Element, m core.MessageSummary, b *msgBody) {
	t, th := theme.Of(c), c.Theme()
	now := r.p.a.clock.Now()
	var replyTo []string
	if b != nil && b.msg.ReplyTo != "" {
		replyTo = splitAddresses(b.msg.ReplyTo)
	}
	date := m.Date.In(now.Location())
	kit.PopoverAt(c, anchor, &r.detailsOpen, ui.AnchorBottomLeft, ui.AnchorTopLeft, theme.PopupOffset, func() {
		cut := map[string]bool{} // values cut to fit, by their text
		// A value cut to fit copies whole: a selection there holds only what shows.
		copyWhole := func(parts []ui.SelectedText) transfer.Data {
			var out []string
			for _, p := range parts {
				if cut[p.Text] {
					out = append(out, p.Text)
				} else {
					out = append(out, string([]rune(p.Text)[p.Start:p.End]))
				}
			}
			return transfer.TextData(strings.Join(out, "\n"))
		}
		ui.Column(c).Width(theme.DetailsWidth).Gap(th.Space(2)).Label(i18n.T("reader.details")).Selectable().CopyData(copyWhole).Children(func() {
			row := func(key int, label, fallbackName string, values []string) {
				if len(values) == 0 {
					return
				}
				ui.Row(c.Key(key)).Gap(th.Space(3)).AlignItems(ui.Start).Children(func() {
					kit.Text(c, label, t.Text.Sm).TextColor(t.Muted).Width(theme.DetailsLabel).Shrink(0).Unselectable()
					ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1)).Children(func() {
						for i, v := range values {
							r.detailsValue(c.Key(i), v, fallbackName, cut)
						}
					})
				})
			}
			row(0, i18n.T("reader.details.from"), m.FromName, []string{m.From})
			row(1, i18n.T("reader.details.to"), "", m.To)
			row(2, i18n.T("reader.details.cc"), "", m.Cc)
			row(3, i18n.T("reader.details.reply_to"), "", replyTo)
			row(4, i18n.T("reader.details.date"), "", []string{i18n.FullDate(date) + " " + i18n.T(fmt.Sprintf("mail.weekday.%d", date.Weekday())) + " " + date.Format("15:04")})
			if acc := r.accountOf(m); acc != "" && len(r.p.a.accounts) > 1 {
				row(5, i18n.T("reader.details.account"), "", []string{acc})
			}
		})
	})
}

// detailsValue is a value of details: an address as Name <address>, the
// address quieter, anything else (the date) as it is. One line, cut to fit
// (noted in cut), whole on hover.
func (r *reader) detailsValue(c *ui.Context, raw, fallbackName string, cut map[string]bool) {
	t, th := theme.Of(c), c.Theme()
	name, addr := sender(raw)
	if fallbackName != "" {
		name = fallbackName
	}
	spans := []ui.Span{{Text: name}}
	if addr != "" && addr != name {
		spans = append(spans, ui.Span{Text: " <" + addr + ">", Color: t.Muted})
	}
	e := ui.RichText(c, spans...).FontSize(t.Text.Sm).LineHeight(t.Text.Line(t.Text.Sm)).SingleLine()
	text := ui.ShapeRichText(spans, ui.Font{Size: t.Text.Sm}, 0)
	if w, _ := text.Size(); w > theme.DetailsWidth-theme.DetailsLabel-th.Space(3) {
		var b strings.Builder
		for _, sp := range spans {
			b.WriteString(sp.Text)
		}
		cut[b.String()] = true
		kit.Tooltip(c, e, b.String())
	}
}

// splitAddresses is a header's list of addresses, or the header as one when
// it does not parse.
func splitAddresses(header string) []string {
	list, err := mail.ParseAddressList(header)
	if err != nil {
		return []string{header}
	}
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.String()
	}
	return out
}

// message is a message in full: who, to whom (with the remote images that
// wait) and when, the fold button when the thread has others, the AI area
// and plugin cards, the body (or its translation) and the attachments.
func (r *reader) message(c *ui.Context, m core.MessageSummary) {
	t, th := theme.Of(c), c.Theme()
	b := r.bodies[m.ID]
	var sec gomail.SecurityReport
	if b != nil && b.ready {
		sec = b.msg.Security
	} else {
		sec = gomail.EvaluateSecurity(m.From, "")
	}
	if gomail.SecurityRank(m.Security.Status) > gomail.SecurityRank(sec.Status) { // the listed warning, before the body loads
		sec = m.Security
	}
	ui.Column(c).FillWidth().Gap(th.Space(3.5)).Children(func() {
		name, addr := senderOf(m)
		head := r.column(c).Gap(th.Space(3.5))
		var bodyReady bool
		head.Children(func() {
			// The sender, its avatar or name, opens its actions (block, copy,
			// mail from them); it no longer folds.
			target := cmp.Or(addr, m.From)
			senderActions := func(e ui.Element) {
				if target != "" {
					e.Menu(func(menu *ui.Menu) { r.senderMenu(c, menu, addr, target, m.ID, sec) })
				}
			}
			ui.Row(c).Gap(th.Space(2.5)).AlignItems(ui.Center).Children(func() {
				// At its folded row's place (its padding), so it stays still as the message opens.
				avatar := kit.Pressable(c, kit.PressableOpts{}).Shrink(0).Radius(t.Radius.Lg).Label(target).
					AlignSelf(ui.Start).Margin(th.Space(1.5), 0, 0, 0)
				senderActions(avatar)
				avatar.Children(func() {
					// Until the avatar is fetched or refused, a pulse, or in a thread what
					// its folded row showed (no grey flash as it opens); then the image or
					// the initial.
					switch loading := b == nil || b.err == nil && !b.ready || b.avatarRun.loading; {

					case loading && len(r.msgs) == 1:
						kit.Skeleton(c, th.Space(8), th.Space(8)).Radius(t.Radius.Lg).Shrink(0).Label(name)
					case loading:
						kit.AvatarImage(c, name, r.shownAvatar(m))
					default:
						kit.AvatarImage(c, name, b.avatar)
					}
				})
				// The name's line tops the avatar, whose margin keeps it where its folded row had it.
				ui.Column(c).Shrink(1).Gap(th.Space(0.5)).AlignSelf(ui.Start).Margin(th.Space(1.5), 0, 0, 0).Children(func() {
					ui.Row(c).Wrap().Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
						// Its hover clears the text by its padding; the margin keeps the text where it was.
						who := kit.Pressable(c, kit.PressableOpts{}).Gap(th.Space(1)).AlignItems(ui.Center).Label(target).
							Padding(th.Space(0.5), th.Space(1.5)).Margin(-th.Space(0.5), -th.Space(1.5)).Radius(t.Radius.Md)
						senderActions(who)
						who.Children(func() {
							ui.Text(c, name).FontWeight(500).SingleLine().Transition(nameMotion(m.ID))
							if addr != "" && addr != name { // Name <address>, the address quieter
								kit.Text(c, "<"+addr+">", t.Text.Base).TextColor(t.Muted).SingleLine().Shrink(1)
							}
							ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconXs, theme.IconXs).TextColor(t.Muted)
						})
						if r.isBlocked(m.From) {
							badge := kit.Pressable(c, kit.PressableOpts{})
							senderActions(badge)
							badge.Children(func() {
								kit.Badge(c, i18n.T("reader.blocked_badge"))
							})
						}
						r.securityBadge(c, m.ID, sec)
					})
					ui.Row(c).Wrap().Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
						r.recipientsLine(c, m, b)
						r.trackersNote(c, b)
						r.imagesNote(c, m, b, addr)
					})
				})
				ui.Spacer(c)
				// on the name's line: its middle, a smaller text's and the chevron's
				kit.Text(c, senderDate(r.p.a.clock.Now(), m.Date), t.Text.Sm).TextColor(t.Muted).Shrink(0).AlignSelf(ui.Start).Margin(th.Space(2), 0, 0, 0)
				if len(r.msgs) > 1 { // only a thread of more than one message folds
					fold := kit.Pressable(c, kit.PressableOpts{OnClick: func() { r.toggle(m.ID) }}).
						Size(th.Space(7), th.Space(7)).Radius(t.Radius.Md).Center().Shrink(0).Label(i18n.T("reader.fold")).
						AlignSelf(ui.Start).Margin(th.Space(0.5), 0, 0, 0)
					fold.Children(func() {
						ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted).Rotate(180)
					})
					kit.TooltipLabelled(c, fold, i18n.T("reader.fold"))
				}
			})
			r.aiArea(c, m)
			r.cards(c, m)
			switch {
			case b != nil && b.err != nil && !b.read.loading:
				kit.Alert(c, kit.Danger, humanize(b.err), func() {
					kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
						OnClick(func() { r.loadBody(m.ID) })
				})
				return
			case b == nil || !b.ready:
				kit.Spinner(c)
				return
			}
			bodyReady = true
			bodyBlocked := b.msg.RemoteImagesBlocked > 0 && !b.remote && !b.trusted
			if b.allowErr != nil && (bodyBlocked || addr != "" && b.avatarBlocked && !b.avatarAllow) {
				kit.Alert(c, kit.Danger, humanize(b.allowErr), func() {
					kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
						OnClick(func() { r.allowImages(m.ID) })
				})
			}
			if b.imgFailed > 0 && !bodyBlocked {
				ui.Row(c).Wrap().Gap(th.Space(3)).AlignItems(ui.Center).Padding(th.Space(2), th.Space(3)).Radius(t.Radius.Lg).
					Background(t.WarningBg).Border(theme.Hairline, t.WarningBorder).Children(func() {
					kit.Text(c, i18n.Tf("reader.images_failed", b.imgFailed), t.Text.Base).LineHeight(theme.LineCJK).TextColor(t.WarningText)
					kit.TextLink(c, i18n.T("common.retry"), t.WarningText, t.Text.Base).OnClick(func() {
						b.imgFailed = 0
						r.p.a.plat.AllowImages(m.ID)
					})
				})
			}
			r.translationState(c, m)
		})
		if !bodyReady {
			return
		}
		if b.view != nil { // the body takes the pane's width; what frames it keeps to the column
			// a line until the page reports; rounded, so a mail filled edge to edge
			// with its own color sits as a card; Draw's box is where the web view goes this frame
			kit.WebView(c, b.view).Height(max(float32(b.height), th.Space(6))).Radius(t.Radius.Xl).Clip().
				Draw(func(_ *ui.Painter, box ui.Rect) { r.glass(b, box) })
		}
		r.column(c).Gap(th.Space(3.5)).Children(func() {
			if b.cut {
				ui.Row(c).Wrap().Gap(th.Space(3)).Children(func() {
					kit.Text(c, i18n.T("reader.truncated"), t.Text.Sm).TextColor(t.Muted)
					kit.Button(c, i18n.T("reader.open_in_browser"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm}).
						OnClick(func() { r.viewInBrowser(m.ID) })
				})
			}
			r.attachments(c, b)
		})
	})
}

// imagesNote follows the recipients while remote images wait (the body's,
// or the sender's avatar): how many, then 加载 and 信任发件人. A failed
// load is an alert under the header instead.
// trackersNote is, in the header's second line, the tracking pixels the
// body's page left out: always, trusted sender or not, so it offers nothing.
func (r *reader) trackersNote(c *ui.Context, b *msgBody) {
	if b == nil || !b.ready || b.msg.TrackersBlocked == 0 {
		return
	}
	t, th := theme.Of(c), c.Theme()
	kit.Text(c, "·", t.Text.Sm).TextColor(t.Muted)
	note := ui.Row(c).Gap(th.Space(1)).AlignItems(ui.Center).TextColor(t.Muted).Children(func() {
		ui.Icon(c, icons.Get("shield")).Size(theme.IconXs, theme.IconXs)
		kit.Text(c, i18n.Tf("reader.trackers_blocked", b.msg.TrackersBlocked), t.Text.Sm)
	})
	kit.Tooltip(c, note, i18n.T("reader.trackers_tip"))
}

func (r *reader) imagesNote(c *ui.Context, m core.MessageSummary, b *msgBody, addr string) {
	if b == nil || !b.ready || b.allowErr != nil {
		return
	}
	t := theme.Of(c)
	bodyBlocked := b.msg.RemoteImagesBlocked > 0 && !b.remote && !b.trusted
	avatarBlocked := addr != "" && b.avatarBlocked && !b.avatarAllow
	if !bodyBlocked && !avatarBlocked {
		return
	}
	n, tip := b.msg.RemoteImagesBlocked, i18n.Tf("reader.remote_blocked", b.msg.RemoteImagesBlocked)
	if !bodyBlocked { // only the avatar waits
		n, tip = 1, i18n.T("reader.avatar_blocked")
	}
	kit.Text(c, "·", t.Text.Sm).TextColor(t.Muted)
	kit.Tooltip(c, kit.Text(c, i18n.Tf("reader.images_not_loaded", n), t.Text.Sm).TextColor(t.Muted), tip)
	if bodyBlocked && b.allow {
		kit.Spinner(c)
		return
	}
	kit.TextLink(c, i18n.T("reader.load"), t.Foreground, t.Text.Sm).OnClick(func() { r.allowImages(m.ID) })
	if addr != "" && !b.read.loading && !b.msg.InJunk && r.p.a.settings.RemoteImages != "ask" { // core never trusts junk, and "ask" ignores trust
		kit.TextLink(c, i18n.T("reader.trust"), t.Foreground, t.Text.Sm).OnClick(func() { r.trustSender(b.msg.From, addr) })
	}
}

// column is the reading pane's centred column for all but a message's
// body: the subject, its tags and labels, the sender, the AI's parts and
// what follows the body, never wider than ReaderMaxWidth.
func (r *reader) column(c *ui.Context) ui.Element {
	return ui.Column(c).FillWidth().MaxWidth(r.p.a.readerMaxWidth()).AlignSelf(ui.Center)
}

// attachments lists the files attached: a click opens one, Save as copies it;
// when several files are attached, they can be selected and saved together.
func (r *reader) attachments(c *ui.Context, b *msgBody) {
	var files []core.AttachmentInfo
	for _, att := range b.msg.Attachments {
		if !att.Inline {
			files = append(files, att)
		}
	}
	if len(files) == 0 {
		return
	}
	t, th := theme.Of(c), c.Theme()
	multi := len(files) >= 2
	ui.Column(c).FillWidth().Gap(th.Space(2)).Children(func() {
		if multi {
			var selCount, selSize, totalSize int
			var selected []core.AttachmentInfo
			for _, att := range files {
				totalSize += att.Size
				if b.attSel[att.ID] {
					selCount++
					selSize += att.Size
					selected = append(selected, att)
				}
			}
			ui.Row(c).FillWidth().Gap(th.Space(3)).AlignItems(ui.Center).Children(func() {
				all := selCount == len(files)
				if kit.Checkbox(c.Key("select-all"), &all, i18n.T("reader.attachments.select_all")).Changed() {
					if all {
						if b.attSel == nil {
							b.attSel = map[int64]bool{}
						}
						for _, att := range files {
							b.attSel[att.ID] = true
						}
					} else {
						clear(b.attSel)
					}
				}
				summary := i18n.Plural("reader.attachments.count", len(files)) + " · " + byteSize(totalSize)
				if selCount > 0 {
					summary = i18n.Tf("reader.attachments.selected", selCount, len(files)) + " · " + byteSize(selSize)
				}
				kit.Text(c, summary, t.Text.Sm).TextColor(t.Muted)
				ui.Spacer(c)
				if selCount > 0 {
					kit.Button(c, i18n.T("reader.attachments.save_selected"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Icon: "download"}).
						OnClick(func() { r.saveAttachments(b.msg.ID, selected) })
				} else {
					kit.Button(c, i18n.T("reader.attachments.save_all"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Icon: "download"}).
						OnClick(func() { r.saveAttachments(b.msg.ID, files) })
				}
			})
		}
		ui.Row(c).FillWidth().Wrap().Gap(th.Space(2)).Children(func() {
			for _, att := range files {
				c := c.Key(att.ID)
				name := core.FileName(att.Filename, "attachment")
				checked := multi && b.attSel[att.ID]
				border, bg := t.Border, ui.Transparent
				if checked {
					border, bg = t.Primary, t.Hover.Mix(t.Background, 0.5)
				}
				ui.Row(c).Width(theme.AttachmentWidth).Gap(th.Space(1.5)).Padding(th.Space(2), th.Space(2.5)).
					Border(theme.Hairline, border).Background(bg).Radius(t.Radius.Lg).AlignItems(ui.Center).Children(func() {
					if multi {
						if kit.Checkbox(c.Key("sel"), &checked, "").Shrink(0).Label(i18n.Tf("reader.attachments.select_item", name)).Changed() {
							if checked {
								if b.attSel == nil {
									b.attSel = map[int64]bool{}
								}
								b.attSel[att.ID] = true
							} else {
								delete(b.attSel, att.ID)
							}
						}
					}
					kit.Pressable(c, kit.PressableOpts{OnClick: func() { r.p.a.openAttachment(att) }}).Grow(1).Shrink(1).Gap(th.Space(2.5)).Children(func() {
						if img := r.p.a.typeIcon(iconExt(att.Filename)); img != nil {
							ui.Image(c, img).Size(th.Space(8.5), th.Space(8.5)).Fit(ui.Contain).Shrink(0)
						} else {
							ui.Box(c).Size(th.Space(7), th.Space(8.5)).Radius(t.Radius.Sm).Background(t.Hover).Center().Shrink(0).Children(func() {
								kit.Text(c, fileKind(att), t.Text.Xs).TextColor(t.Muted)
							})
						}
						ui.Column(c).Shrink(1).Children(func() {
							ui.Text(c, name).SingleLine() // the name it opens and saves under
							kit.Text(c, byteSize(att.Size), t.Text.Sm).TextColor(t.Muted)
						})
					})
					kit.IconButton(c, "download", i18n.T("reader.save_as"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
						OnClick(func() { r.p.a.saveAttachment(att) })
				})
			}
		})
	})
}

// fileKind is a short type for an attachment's icon: its extension, else
// its media subtype.
func fileKind(att core.AttachmentInfo) string {
	if ext := strings.TrimPrefix(filepath.Ext(att.Filename), "."); ext != "" && len(ext) <= 4 {
		return strings.ToUpper(ext)
	}
	_, sub, _ := strings.Cut(att.ContentType, "/")
	return strings.ToUpper(cmp.Or(sub, "FILE"))
}

// typeIconPx is the size typeIcon asks for: sharp at up to 4x for the
// attachment tile, as ui does not say the display's scale.
const typeIconPx = 136

// typeIcon is the file manager's icon for files with extension ext, nil
// while it loads, for no ext, or when the platform has none.
func (a *App) typeIcon(ext string) ui.ImageSource {
	if ext == "" {
		return nil
	}
	if img, ok := a.typeIcons[ext]; ok {
		return img
	}
	if a.typeIcons == nil {
		a.typeIcons = map[string]ui.ImageSource{}
	}
	a.typeIcons[ext] = nil
	plat := a.plat
	var img ui.ImageSource
	a.do(func(context.Context) error {
		png, err := plat.TypeIcon(ext, typeIconPx)
		if err == nil {
			img = decodeIcon("image/png", png)
		}
		return nil // without one the tile keeps fileKind's label
	}, func(error) { a.typeIcons[ext] = img })
	return nil
}

// iconExt is name's extension for typeIcon, lower case without the dot:
// "" unless it is 1 to 10 ASCII letters and digits, as it names a file the
// platform writes.
func iconExt(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if len(ext) > 10 || strings.ContainsFunc(ext, func(r rune) bool { return (r < 'a' || r > 'z') && (r < '0' || r > '9') }) {
		return ""
	}
	return ext
}

// byteSize is a size in B, KB or MB.
func byteSize(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%d KB", (n+512)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
}

// allowImages lets message id's page load its remote images (加载图片),
// and every page made for it again while the thread shows; the notice
// waits for the page's body to have them.
func (r *reader) allowImages(id int64) {
	r.loadImages(id, true)
}

func (r *reader) loadImages(id int64, manual bool) {
	b := r.bodies[id]
	if b == nil {
		return
	}
	b.allow, b.allowErr = true, nil
	r.loadAvatar(id, manual)
	if b.view != nil {
		r.p.a.plat.AllowImages(id)
	}
}

// trustSender trusts addr's remote images from now on (始终信任此发件人)
// and loads them in its messages shown. Core gets the From as sent: a quoted
// local part parses there, its bare address does not.
func (r *reader) trustSender(from, addr string) {
	a, s := r.p.a, r.session
	a.do(func(ctx context.Context) error { return a.svc.TrustImageSender(ctx, from) }, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		if !r.has(s) {
			return
		}
		for id, b := range r.bodies {
			if _, from := sender(b.msg.From); strings.EqualFold(from, addr) && !b.read.loading && !b.msg.InJunk { // as core: never junk
				r.loadImages(id, false)
			}
		}
	})
}

// The calls of a body's page (view), on the UI thread. Each acts only
// while view is still a page the reader shows: one queued before its
// page was closed, or replaced, is dropped.

// readerOf is the reader whose bodies hold page v: the one shown, or the
// thread kept on screen while a switch's body builds (#667).
func (a *App) readerOf(v ui.NativeWebView) *reader {
	r := a.mail.reader
	if p := r.prev; p != nil {
		for _, b := range p.bodies {
			if b.view != nil && b.view == v {
				return r.on(p)
			}
		}
	}
	return r
}

// BodyHeight is told the height, in CSS pixels, of message's body as its
// page laid it out: its element takes it.
func (a *App) BodyHeight(view ui.NativeWebView, message int64, px int) {
	if b := a.readerOf(view).shown(view, message); b != nil {
		b.height = px
		b.glass = [2]int{-1, -1} // the page listens now: what it was told before it loaded was dropped
		logBodyTiming(&b.timing, message)
	}
}

// BodyLink opens a link of a body's page as one of the native reader's:
// http(s) after the confirmation, mailto in a compose window.
func (a *App) BodyLink(view ui.NativeWebView, link string) {
	if a.readerOf(view).current(view) {
		a.openLink(link)
	}
}

// BodyNavigate goes back or forward for a body's page (⌘[ and ⌘]).
func (a *App) BodyNavigate(view ui.NativeWebView, dir int) {
	if a.readerOf(view).current(view) {
		a.Navigate(dir)
	}
}

// BodySearch opens the search palette for a body's page (⌘F, ⌘K).
func (a *App) BodySearch(view ui.NativeWebView) {
	if a.readerOf(view).current(view) {
		a.openPalette()
	}
}

// BodySave saves the message of a body's page (⌘S).
func (a *App) BodySave(view ui.NativeWebView) {
	if r := a.readerOf(view); r.focusView(view) {
		r.saveMail()
	}
}

// BodyPrint prints the message of a body's page (⌘P).
func (a *App) BodyPrint(view ui.NativeWebView) {
	if r := a.readerOf(view); r.focusView(view) {
		r.printMail()
	}
}

// BodyBuilt is told message's body as its page got it: whether it is only
// the start (ReaderBody.Cut), for the truncated notice, and has the remote
// images (Remote), which hides their notice.
func (a *App) BodyBuilt(view ui.NativeWebView, message int64, cut, remote bool) {
	if b := a.readerOf(view).shown(view, message); b != nil {
		b.cut, b.remote = cut, remote
		b.timing.builtDone()
		if remote {
			b.allowErr = nil
		}
	}
}

// BodyImagesFailed is told n of message's images could not load:
// the notice offers a retry.
func (a *App) BodyImagesFailed(view ui.NativeWebView, message int64, n int) {
	if b := a.readerOf(view).shown(view, message); b != nil {
		b.imgFailed = n
	}
}

// BodyFailed is told reading message's body with its remote images
// failed: the notice offers a retry.
func (a *App) BodyFailed(view ui.NativeWebView, message int64, err error) {
	if b := a.readerOf(view).shown(view, message); b != nil && b.allow {
		b.allowErr = err
	}
}

// senderDate is a message's time in the sender line: its date as the list
// and its folded row give it (today, yesterday, the weekday this week, else
// the locale's numeric date), with the hour.
func senderDate(now, t time.Time) string {
	clock := t.In(now.Location()).Format("15:04")
	if day := listTime(now, t); day != clock {
		return day + " " + clock
	}
	return i18n.T("reader.today") + " " + clock
}

// unsubMessage is the message the thread's 退订 acts on (labelRow): the
// newest open one whose headers allow it, as its list's mail in a thread
// all would.
func (r *reader) unsubMessage() int64 {
	for i := len(r.msgs) - 1; i >= 0; i-- {
		if id := r.msgs[i].ID; r.open[id] {
			if b := r.bodies[id]; b != nil && b.unsub.Method != "" {
				return id
			}
		}
	}
	return 0
}

// unsubscribe is a message's 退订, by its headers: one-click asks first, a
// web page opens after the link confirmation, a mailto drafts.
func (r *reader) unsubscribe(id int64) {
	b := r.bodies[id]
	if b == nil || b.unsubbing {
		return
	}
	switch b.unsub.Method {
	case core.UnsubscribeOneClick:
		r.unsubAsk, r.unsubDlg = id, true
	case core.UnsubscribeWeb:
		r.p.a.openLink(b.unsub.URL)
	case core.UnsubscribeMail:
		r.sendUnsubscribe(id)
	}
}

// sendUnsubscribe has core unsubscribe from message id: a one-click result
// toasts (a failure offers the page in the browser), a draft opens in a
// compose window, never sent.
func (r *reader) sendUnsubscribe(id int64) {
	a, b := r.p.a, r.bodies[id]
	if b == nil || b.unsubbing {
		return
	}
	b.unsubbing = true
	u := b.unsub
	var res core.UnsubscribeResult
	a.do(func(ctx context.Context) (err error) {
		res, err = a.svc.Unsubscribe(ctx, id)
		return err
	}, func(err error) {
		b.unsubbing = false
		a.unsubscribed(u, res, err)
	})
}

// unsubscribed tells what core's Unsubscribe by u did: a one-click result
// toasts (a failure offers the page in the browser), a draft opens in a
// compose window, never sent.
func (a *App) unsubscribed(u core.Unsubscribe, res core.UnsubscribeResult, err error) {
	switch {
	case err != nil:
		a.toastErr(err)
	case res.DraftID != 0:
		a.plat.OpenCompose(res.DraftID)
	case res.Failure == "":
		a.toastAs(kit.ToastSuccess, i18n.Tf("reader.unsubscribe.sent", res.Host))
	default:
		msg := i18n.Tf("reader.unsubscribe.failed."+res.Failure, res.Host, strconv.Itoa(res.Status))
		a.toasts = append(a.toasts, ui.Toast{ID: msg, Title: msg, Type: kit.ToastError,
			Action: i18n.T("reader.unsubscribe.open"), OnAction: func() { a.openLink(u.URL) }})
	}
}

// unsubscribeDialog confirms a one-click unsubscribe: the host the request
// goes to in bold, and the list's name; 退订 sends it.
func (r *reader) unsubscribeDialog(c *ui.Context) {
	if !r.unsubDlg {
		return
	}
	id, b := r.unsubAsk, r.bodies[r.unsubAsk]
	if b == nil || b.unsub.Method != core.UnsubscribeOneClick {
		r.unsubDlg = false
		return
	}
	unsubscribeDialog(c, &r.unsubDlg, b.unsub, func() { r.sendUnsubscribe(id) })
}

// attachmentConflictDialog asks what to do when batch-saving attachments into
// a directory that already holds files of the same name.
func (r *reader) attachmentConflictDialog(c *ui.Context) {
	if !r.attConflictDlg {
		r.attConflict = attConflictAsk{}
		return
	}
	if len(r.attConflict.conflicts) == 0 {
		r.attConflict, r.attConflictDlg = attConflictAsk{}, false
		return
	}
	th := c.Theme()
	opts := kit.DialogOpts{
		Alert: true,
		Desc:  i18n.Plural("reader.attachments.conflict_desc", len(r.attConflict.conflicts)),
		Close: i18n.T("common.close"),
	}
	kit.Dialog(c, &r.attConflictDlg, i18n.T("reader.attachments.conflict_title"), opts, func() {
		ui.Column(c).Gap(th.Space(1)).Children(func() {
			for _, name := range r.attConflict.conflicts {
				ui.Text(c, name).FontWeight(500).SingleLine()
			}
		})
	}, func() {
		if kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Ghost}).Clicked() {
			r.attConflict, r.attConflictDlg = attConflictAsk{}, false
		}
		ui.Spacer(c)
		if kit.Button(c, i18n.T("reader.attachments.conflict_skip"), kit.ButtonOpts{Variant: kit.Outline}).Clicked() {
			r.resolveAttachmentConflict(attConflictSkip)
		}
		if kit.Button(c, i18n.T("reader.attachments.conflict_replace"), kit.ButtonOpts{Variant: kit.DestructiveOutline}).Clicked() {
			r.resolveAttachmentConflict(attConflictReplace)
		}
		if kit.Button(c, i18n.T("reader.attachments.conflict_keep_both"), kit.ButtonOpts{}).AutoFocus().Clicked() {
			r.resolveAttachmentConflict(attConflictKeepBoth)
		}
	})
}

// unsubscribeDialog confirms a one-click unsubscribe by u while open: the
// host the request goes to in bold, and the list's name; 退订 runs send.
func unsubscribeDialog(c *ui.Context, open *bool, u core.Unsubscribe, send func()) {
	t := theme.Of(c)
	opts := kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: i18n.T("reader.unsubscribe.desc"), Close: i18n.T("common.close")}
	kit.Dialog(c, open, i18n.T("reader.unsubscribe.title"), opts, func() {
		ui.Text(c, u.Host).FontWeight(600)
		if u.List != "" {
			kit.Text(c, i18n.Tf("reader.unsubscribe.list", u.List), t.Text.Sm).TextColor(t.Muted)
		}
	}, func() {
		if kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).Clicked() {
			*open = false
		}
		if kit.Button(c, i18n.T("reader.unsubscribe"), kit.ButtonOpts{}).AutoFocus().Clicked() {
			*open = false
			send()
		}
	})
}

// isBlocked is whether from (a From header or its address) is a blocked sender.
func (r *reader) isBlocked(from string) bool {
	return slices.Contains(r.p.a.blocked, gomail.AddrKey(from))
}

// setBlocked blocks or unblocks from's address, in core and then in the
// list shown, so the badge and lists change at once; done follows success.
func (a *App) setBlocked(from string, block bool, done func()) {
	a.do(func(ctx context.Context) error {
		if block {
			return a.svc.BlockSender(ctx, from)
		}
		return a.svc.UnblockSender(ctx, from)
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		key := gomail.AddrKey(from)
		l := slices.DeleteFunc(slices.Clone(a.blocked), func(s string) bool { return s == key })
		if block {
			l = append(l, key)
		}
		a.blocked = l
		if done != nil {
			done()
		}
	})
}

// senderMenu is the sender's actions: view them in contacts, copy the address,
// block or unblock the sender, edit labels, or adjust security classification.
func (r *reader) senderMenu(c *ui.Context, m *ui.Menu, addr, target string, msgID int64, sec gomail.SecurityReport) {
	if addr != "" && m.Item(i18n.T("reader.view_contact")).Icon(menuIcon("contacts")).Chosen() {
		r.p.a.openContact(addr)
	}
	if m.Item(i18n.T("reader.copy_address")).Icon(menuIcon("copy")).Chosen() {
		c.WriteClipboard(target)
		r.p.a.toast(i18n.T("reader.address_copied"))
	}
	if r.isBlocked(target) {
		if m.Item(i18n.T("reader.unblock_sender")).Icon(menuIcon("shield")).Chosen() {
			r.p.a.setBlocked(target, false, func() { r.p.a.toast(i18n.T("reader.sender_unblocked")) })
		}
	} else if m.Item(i18n.T("reader.block_sender")).Icon(menuIcon("spam")).Chosen() {
		r.p.a.setBlocked(target, true, func() { r.p.a.toast(i18n.T("reader.sender_blocked")) })
	}
	m.Separator()
	labelSubmenu(m, theme.Of(c), r.p.a.labels, r.labelState, r.setLabel)
	securitySubmenu(m, r.securityChecked(sec), func(reason string) { r.applySecurity(msgID, reason) })
}

func secWarningText(sec gomail.SecurityReport) string {
	switch sec.WarningKind {
	case gomail.WarnDisplaySpoof:
		return i18n.T("reader.security.spoof_brand")
	case gomail.WarnEmailInName:
		return i18n.T("reader.security.spoof_email")
	case gomail.WarnAuthFailed:
		return i18n.T("reader.security.auth_failed")
	case gomail.WarnAIFraud:
		return i18n.T("reader.security.ai_fraud")
	default:
		return i18n.T("reader.security.warning_generic")
	}
}

func (r *reader) secExplanation(sec gomail.SecurityReport) string {
	switch sec.Reason {
	case "manual_safe":
		return i18n.T("reader.security.manual_safe_desc")
	case "manual_suspicious":
		return i18n.T("reader.security.manual_suspicious_desc")
	case "manual_warning":
		return i18n.T("reader.security.manual_warning_desc")
	}
	switch sec.Status {
	case gomail.SecurityCertifiedBrand:
		return i18n.T("reader.security.certified_desc")
	case gomail.SecurityVerifiedDomain:
		return i18n.T("reader.security.verified_desc")
	case gomail.SecurityNeedsReview:
		if sec.ActualDomain != "" && (sec.DMARCPass || sec.DKIMPass) {
			return i18n.Tf("reader.security.review_authenticated_desc", sec.ActualDomain)
		}
		return i18n.T("reader.security.review_desc")
	case gomail.SecurityFraud:
		return secWarningText(sec)
	case gomail.SecurityNeutral:
		return i18n.T("reader.security.unverified_desc")
	default:
		return ""
	}
}

// applySecurity updates the security classification for a message.
func (r *reader) applySecurity(msgID int64, reason string) {
	a := r.p.a
	// Optimistically update the message's security in the reader for instant feedback
	status, _ := gomail.ManualSecurityStatus(reason)
	sec := gomail.DBToSecurityReport("", "", status, reason)
	for i := range r.msgs {
		if r.msgs[i].ID == msgID {
			r.msgs[i].Security = sec
		}
	}
	if b, ok := r.bodies[msgID]; ok && b != nil {
		b.msg.Security = sec
	}
	ids := []int64{msgID}
	a.writes.Chain("security", func(ctx context.Context) error {
		return a.svc.SetMessageSecurity(ctx, ids, reason)
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
		} else {
			if b, ok := r.bodies[msgID]; ok && b != nil {
				r.loadAvatar(msgID, b.avatarAllow)
			}
			a.toast(i18n.T("reader.security.updated"))
		}
	})
}

// securityItems are the classifications mail can be set to, in the reader's
// menus and a mail row's; checked says which one it has.
func securityItems(m *ui.Menu, checked func(reason string) bool, apply func(reason string)) {
	item := func(label, icon, reason string) {
		if m.Item(i18n.T(label)).Icon(menuIcon(icon)).Checked(checked(reason)).Chosen() {
			apply(reason)
		}
	}
	item("mail.security.mark_safe", "circle-check", "manual_safe")
	item("mail.security.mark_suspicious", "triangle-alert", "manual_suspicious")
	item("mail.security.mark_warning", "skull", "manual_warning")
	m.Separator()
	item("mail.security.reset", "refresh-cw", "")
}

// securitySubmenu is securityItems as a menu's submenu.
func securitySubmenu(m *ui.Menu, checked func(reason string) bool, apply func(reason string)) {
	m.Submenu(i18n.T("mail.menu.security"), func(sm *ui.Menu) { securityItems(sm, checked, apply) }).Icon(menuIcon("shield"))
}

func (r *reader) securityChecked(sec gomail.SecurityReport) func(string) bool {
	return func(reason string) bool { return sec.Reason == reason }
}

// securityBadge is the sender's verdict as an icon by the name; hovering it
// explains the verdict, and clicking it opens a menu to adjust the classification.
func (r *reader) securityBadge(c *ui.Context, msgID int64, sec gomail.SecurityReport) {
	isManual := gomail.IsManualSecurity(sec.Reason)
	if !isManual && !showSecurity(r.p.a.settings, sec.Status) {
		return
	}
	t, th := theme.Of(c), c.Theme()
	iconName, label, color := secVisual(t, sec)
	mark := kit.Pressable(c, kit.PressableOpts{}).Shrink(0).Label(label).Radius(t.Radius.Sm).Padding(th.Space(0.5), th.Space(1))
	mark.Menu(func(m *ui.Menu) {
		securityItems(m, r.securityChecked(sec), func(reason string) { r.applySecurity(msgID, reason) })
	})
	mark.Children(func() {
		ui.Icon(c, icons.Fill(iconName)).Size(theme.IconXs, theme.IconXs).TextColor(color)
	})
	kit.HoverCard(c, mark, theme.SecurityCardWidth, func() {
		ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Fill(iconName)).Size(theme.Icon, theme.Icon).TextColor(color)
			kit.Text(c, label, t.Text.Base).FontWeight(600).TextColor(color)
		})
		kit.Text(c, r.secExplanation(sec), t.Text.Sm).LineHeight(theme.LineCJK).TextColor(t.Foreground)
		kit.Separator(c, false)
		kit.Text(c, i18n.T("reader.security.click_to_edit"), t.Text.Xs).TextColor(t.Muted)
	})
}

// securityIcon is a mail list row's warning, after the sender's name. Verified
// senders show theirs in the reader only: most rows would carry one.
func securityIcon(c *ui.Context, st core.Settings, sec gomail.SecurityReport) {
	status := sec.Status
	isManual := sec.Reason == "manual_warning" || sec.Reason == "manual_suspicious"
	if !isManual && (status != gomail.SecurityFraud && status != gomail.SecurityNeedsReview || !showSecurity(st, status)) {
		return
	}
	iconName, label, color := secVisual(theme.Of(c), sec)
	ui.Icon(c, icons.Fill(iconName)).Size(theme.IconXs, theme.IconXs).Shrink(0).TextColor(color).Label(label)
}

// showSecurity reports whether the settings show a sender verdict: warnings
// and badges come from the native checks, so neither depends on SecurityAI.
func showSecurity(st core.Settings, status gomail.SecurityStatus) bool {
	switch status {
	case gomail.SecurityFraud, gomail.SecurityNeedsReview:
		return st.SecurityWarnings
	case gomail.SecurityCertifiedBrand, gomail.SecurityVerifiedDomain:
		return st.VerifiedBadge
	}
	return false // neutral is also the zero value of mail not yet checked: no badge
}

// secVisual returns the icon, translated label, and theme color for a security status or manual classification.
func secVisual(t theme.Tokens, sec gomail.SecurityReport) (iconName string, label string, color ui.Color) {
	switch sec.Reason {
	case "manual_safe":
		return "circle-check-fill", i18n.T("reader.security.manual_safe_badge"), t.Muted
	case "manual_suspicious":
		return "triangle-alert-fill", i18n.T("reader.security.manual_suspicious_badge"), t.WarningText
	case "manual_warning":
		return "skull-fill", i18n.T("reader.security.manual_warning_badge"), t.DangerText
	}
	switch sec.Status {
	case gomail.SecurityCertifiedBrand:
		return "vip-diamond-fill", i18n.T("reader.security.certified_badge"), t.Unread
	case gomail.SecurityVerifiedDomain:
		return "shield-check-fill", i18n.T("reader.security.verified_badge"), t.SuccessText
	case gomail.SecurityNeedsReview:
		return "triangle-alert-fill", i18n.T("reader.security.review_badge"), t.WarningText
	case gomail.SecurityFraud:
		return "skull-fill", i18n.T("reader.security.fraud_badge"), t.DangerText
	case gomail.SecurityNeutral:
		return "question-fill", i18n.T("reader.security.unverified_badge"), t.Muted
	default:
		return "question-fill", i18n.T("reader.security.unverified_badge"), t.Muted
	}
}

// rawMessage is the message's original, read from the file the backend writes
// it to (RawMessageFile) and removed: a large original does not cross the
// Local API whole.
func rawMessage(ctx context.Context, svc Service, id int64) ([]byte, error) {
	path, err := svc.RawMessageFile(ctx, id)
	if err != nil {
		return nil, err
	}
	defer os.Remove(path)
	return os.ReadFile(path)
}
