package app

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"io"
	"maps"
	"math"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

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
// newest open. All its state lives on the UI thread.
type reader struct {
	copiedCard string // the card button that just copied (message/plugin/button), shown as Copied for a moment
	p          *mailPage

	key     [2]int64 // the route's ThreadID and MessageID shown
	gen     uint64   // bumped per key; older results are dropped
	readGen uint64   // bumped per thread read; only the newest read applies
	loaded  bool
	subject string
	msgs    []core.MessageSummary // oldest first
	// threadIDs are every message of the thread, msgs or not (a route to one
	// message shows only it): what label changes apply to.
	threadIDs    []int64
	labels       []core.AppliedLabel // the thread's
	labelMenu    bool
	labelQuery   string
	open         map[int64]bool // messages shown in full
	focus        int64          // the message last expanded, or whose page took ⌘S/⌘P: what save and print act on while it is open
	historyShown bool           // the thread's history (its messages before the newest it keeps in sight) shown: a history card clicked
	historyOpen  map[int64]bool // folded messages shown though the history is collapsed: ones the reader folded
	revealID     int64          // a message to scroll to once it is laid out (a digest's source); 0 for none
	bodies       map[int64]*msgBody
	scroll       ui.ScrollState // the messages' scrolling; a body's page hands on the wheels it does not use

	ai          map[int64]*msgAI         // by message; kept across switches so a summary runs once
	aiCtx       context.Context          // the AI work of the key shown; nil until some starts
	aiStop      context.CancelFunc       // cancels aiCtx
	skills      fetch[[]core.SkillInfo]  // the skill menu: skills on messages but summarize and translate
	actions     fetch[[]core.ActionInfo] // and plugin actions on messages
	egress      egressAsk                // the action waiting for egress approval
	egressDlg   bool                     // its dialog shows
	unsubAsk    int64                    // the message whose one-click unsubscribe waits to be confirmed
	unsubDlg    bool                     // its dialog shows
	senderOpen  bool                     // the sender action popover shows
	senderMsgID int64                    // which message's sender popover is open
	detailsOpen bool                     // the recipients' details popover shows
	detailsID   int64                    // which message's details popover is open
	secOpen     bool                     // the security explanation popover shows
	secMsgID    int64                    // which message's security popover is open
	moreOpen    bool                     // the toolbar's more menu (save, print) shows
	barTop      float32                  // the floating toolbar's height as the last frame laid it out: the messages start under it
	barBottom   float32                  // the reply bar's, likewise: the messages end above it
	pane        ui.Rect                  // the messages' scroll box as the last frame laid it out, in the window: the bars float at its top and bottom
	replyAsk    string                   // the reply bar's question to Wit, until it is asked

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

// msgBody is a message read in full, for its header, notices and
// attachments; its body shows in a page of its own (Platform.NewBodyView)
// while the message is expanded.
type msgBody struct {
	msg     core.Message
	cut     bool // the page's body is only the start (ReaderBody.Cut, told by BodyBuilt)
	loading bool
	bodyGen uint64 // older reads cannot restore policy after a same-message folder move
	ready   bool
	err     error // the last read, or making the page, failed; a retry reads again

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
	// imgFailed is the remote images the proxy could not serve
	// (BodyImagesFailed); a retry reloads the body.
	imgFailed int

	unsub     core.Unsubscribe // how it unsubscribes, by its headers; none until asked
	unsubbing bool             // its unsubscribe is under way

	avatar                                 ui.ImageSource
	avatarStop                             context.CancelFunc
	avatarGen                              uint64
	avatarDone, avatarAllow, avatarBlocked bool
}

func newReader(p *mailPage) *reader {
	return &reader{p: p, open: map[int64]bool{}, bodies: map[int64]*msgBody{}, ai: map[int64]*msgAI{},
		getThread: p.a.svc.GetThread, getMessage: p.a.svc.GetMessage, getAvatar: p.a.svc.SenderAvatar, runSkill: p.a.svc.RunSkill,
		cardsFn: p.a.svc.PluginCards, tagsFn: p.a.svc.PluginTags, quickFn: p.a.svc.QuickReplies, gistFn: p.a.svc.MessageGist, skillsFn: p.a.svc.ListSkills, actionsFn: p.a.svc.ListActions}
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
	case k != r.key:
		r.reset(k)
	case !reread:
		return
	}
	if k[0] != 0 {
		r.load()
	}
}

// reset forgets what was shown and closes its pages; the next thread
// starts at its top.
func (r *reader) reset(k [2]int64) {
	for _, b := range r.bodies {
		b.stopAvatar()
		b.closeView()
	}
	r.stopAI()
	r.key, r.loaded, r.subject, r.msgs, r.focus = k, false, "", nil, 0
	r.threadIDs, r.labels, r.labelMenu, r.labelQuery = nil, nil, false, ""
	r.senderOpen, r.senderMsgID, r.moreOpen, r.replyAsk = false, 0, false, "" // a question is for the thread it was typed in
	r.detailsOpen, r.detailsID = false, 0
	r.open, r.historyOpen, r.historyShown, r.revealID, r.bodies = map[int64]bool{}, map[int64]bool{}, false, 0, map[int64]*msgBody{}
	r.scroll.X, r.scroll.Y = 0, 0
	r.gen++
}

func (r *reader) load() {
	r.readGen++
	a, gen, read, k, get := r.p.a, r.gen, r.readGen, r.key, r.getThread
	var th core.Thread
	var labels map[int64][]core.AppliedLabel
	a.do(func(ctx context.Context) (err error) {
		if th, err = get(ctx, k[0]); err != nil {
			return err
		}
		labels, err = a.svc.ThreadLabels(ctx, []int64{k[0]})
		return err
	}, func(err error) {
		if gen != r.gen || read != r.readGen {
			return
		}
		if err != nil {
			a.toastErr(err)
			return
		}
		r.threadIDs, r.labels = nil, labels[k[0]]
		for _, m := range th.Messages { // before the cut below, which reuses the array
			r.threadIDs = append(r.threadIDs, m.ID)
		}
		msgs := th.Messages
		if k[1] != 0 {
			msgs = slices.DeleteFunc(msgs, func(m core.MessageSummary) bool { return m.ID != k[1] })
		}
		known := map[int64]int64{} // messages shown before keep their state
		for _, m := range r.msgs {
			known[m.ID] = m.FolderID
		}
		r.loaded, r.subject, r.msgs = true, th.Subject, msgs
		for id, b := range r.bodies { // gone from the thread: its page goes too
			if !slices.ContainsFunc(msgs, func(m core.MessageSummary) bool { return m.ID == id }) {
				b.stopAvatar()
				b.closeView()
				delete(r.open, id)
				delete(r.bodies, id)
			}
		}
		var unread []core.MessageSummary
		for i, m := range msgs {
			if folder, ok := known[m.ID]; ok {
				if b := r.bodies[m.ID]; b != nil && folder != m.FolderID {
					b.msg.MessageSummary = m
					b.trusted = false
					if !b.avatarAllow {
						b.stopAvatar()
						b.avatar, b.avatarDone, b.avatarBlocked = nil, false, true
					}
					b.loading = false
					r.loadBody(m.ID)
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
		if !b.avatarDone && b.avatarStop == nil {
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
	a, gen, get := r.p.a, r.gen, r.getMessage
	b := r.bodies[id]
	if b == nil {
		b = &msgBody{}
		r.bodies[id] = b
	}
	if b.loading {
		return
	}
	b.loading, b.err = true, nil
	b.bodyGen++
	read := b.bodyGen
	var m core.Message
	var trusted bool
	a.do(func(ctx context.Context) (err error) {
		if m, err = get(ctx, id, false); err != nil {
			return err
		}
		trusted, err = a.svc.ImagesAllowed(ctx, id)
		return err
	}, func(err error) {
		if gen != r.gen || r.bodies[id] != b || read != b.bodyGen {
			return
		}
		b.loading = false
		if err != nil {
			b.err = err
			return
		}
		b.msg, b.ready, b.trusted = m, true, trusted
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
	a, gen, b := r.p.a, r.gen, r.bodies[id]
	if b == nil { // opening its page may already have switched the reader away
		return
	}
	var u core.Unsubscribe
	a.do(func(ctx context.Context) (err error) {
		u, err = a.svc.UnsubscribeMethod(ctx, id)
		return err
	}, func(err error) {
		if err == nil && gen == r.gen && r.bodies[id] == b {
			b.unsub = u
		}
	})
}

// loadAvatar does both network work and image conversion off the UI thread.
// Its result belongs to this body, message and generation, including retries.
func (r *reader) loadAvatar(id int64, manual bool) {
	b := r.bodies[id]
	if b == nil || !b.ready {
		return
	}
	b.stopAvatar()
	a, gen, seq, get := r.p.a, r.gen, b.avatarGen, r.getAvatar
	b.avatarAllow, b.avatarDone = manual, false
	if !manual && a.settingsVer != a.settingsSavedVer {
		b.avatar, b.avatarBlocked = nil, true
		return
	}
	ctx, stop := context.WithCancel(a.ctx)
	b.avatarStop = stop
	var image ui.ImageSource
	var trusted bool
	a.do(func(context.Context) (err error) {
		trusted, err = a.svc.ImagesAllowed(ctx, id)
		if err != nil || !manual && !trusted {
			return err
		}
		avatar, err := get(ctx, id, manual)
		if err != nil || len(avatar.Data) == 0 {
			return err
		}
		image, err = avatarImage(avatar)
		if err == nil && !manual {
			trusted, err = a.svc.ImagesAllowed(ctx, id)
			if !trusted {
				image = nil
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		return err
	}, func(err error) {
		defer stop()
		if gen != r.gen || r.bodies[id] != b || seq != b.avatarGen || ctx.Err() != nil {
			return
		}
		b.avatarStop, b.avatarDone, b.avatarBlocked = nil, true, !trusted && !manual
		if err == nil {
			b.avatar = image
		}
	})
}

func (b *msgBody) stopAvatar() {
	if b.avatarStop != nil {
		b.avatarStop()
	}
	b.avatarStop = nil
	b.avatarGen++
}

// invalidateAvatars clears displayed images and in-flight results, preserving
// manual grants unless the image policy changed or the reader is closing.
func (r *reader) invalidateAvatars(revokeManual bool) {
	for _, b := range r.bodies {
		if b.avatarAllow && !revokeManual {
			continue
		}
		b.stopAvatar()
		b.avatar, b.avatarDone, b.avatarAllow, b.avatarBlocked = nil, false, false, true
	}
}

func (r *reader) refreshAvatars() {
	for id, b := range r.bodies {
		if r.open[id] && b.ready && !b.avatarDone && b.avatarStop == nil {
			r.loadAvatar(id, b.avatarAllow)
		}
	}
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
	gen := r.gen
	v, closeView, err := r.p.a.plat.NewBodyView(id)
	if gen != r.gen || r.bodies[id] != b || !r.open[id] || b.view != nil || r.p.a.ctx.Err() != nil {
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
	if b.allow {
		r.p.a.plat.AllowImages(id)
	}
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
	r.invalidateAvatars(true)
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
func folderAccount(ctx context.Context, svc *core.Service, folderID int64) (int64, error) {
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

// last is the newest message shown: the one replies and the AI buttons act on.
func (r *reader) last() (core.MessageSummary, bool) {
	if len(r.msgs) == 0 {
		return core.MessageSummary{}, false
	}
	return r.msgs[len(r.msgs)-1], true
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

// draft starts a reply, reply-all or forward of the newest message and
// opens it in a compose window, even when the reader has moved on since:
// the window does not cover it.
func (r *reader) draft(mode string) {
	if m, ok := r.last(); ok {
		r.p.a.draft(m, r.account(), mode)
	}
}

// draft starts a reply, reply-all or forward of m from account acc (0 to
// look it up by m's folder) and opens it in a compose window.
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
// to a temporary file under a CSP and opens it in the browser.
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
		f, err := os.CreateTemp(plat.TempDir(), "godwit-*.html")
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

// fileName makes s a file name: separators and control characters become
// _, leading and trailing dots and spaces go, and a name Windows reserves
// (CON, COM1.txt…) gets a _ in front. Empty, it is fallback.
func fileName(s, fallback string) string {
	s = strings.Trim(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(`/\:*?"<>|`, r) { // Cf: U+202E turns "fdp.exe" into "exe.pdf"
			return '_'
		}
		return r
	}, s), ". ")
	stem, _, _ := strings.Cut(strings.ToUpper(s), ".")
	if reserved[stem] || len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
		s = "_" + s
	}
	return cmp.Or(s, fallback)
}

var reserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

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
		dst := filepath.Join(dir, fileName(att.Filename, "attachment"))
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
		dst, err := plat.SaveFile(fileName(att.Filename, "attachment"))
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

// saveMail saves the target message to an .eml file.
func (r *reader) saveMail() {
	m, ok := r.target()
	if !ok {
		return
	}
	a := r.p.a
	plat := a.plat
	a.do(func(ctx context.Context) error {
		stem := []rune(m.Subject)
		stem = stem[:min(len(stem), 100)] // file systems cap names near 255 bytes
		dst, err := plat.SaveFile(fileName(string(stem), "email") + ".eml")
		if err != nil || dst == "" {
			return err
		}
		raw, err := a.svc.GetRawMessage(ctx, m.ID)
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
		return os.WriteFile(dst, raw, 0o644)
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
		} else {
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

// view is the reading pane: the toolbar over the subject and the messages.
func (r *reader) view(c *ui.Context) {
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
	if c.Shortcut(ui.Cmd, ui.KeyS) {
		r.saveMail()
	}
	if c.Shortcut(ui.Cmd, ui.KeyP) {
		r.printMail()
	}
	// The toolbar and the reply bar float over the messages, which scroll
	// under them: one sheet, the bars frosted over it.
	col.Children(func() {
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
					if len(r.msgs) > 1 { // a thread's summary is the thread's: under its subject, not in a message
						if m, ok := r.last(); ok {
							r.column(c).Children(func() { r.summaryPanel(c, m) })
						}
					}
					r.renderMessages(c)
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
		file := []kit.GroupItem{{Icon: starIcon, Tip: starTip, OnClick: func() { r.act("toggle_star") }}}
		role := r.p.currentFolderRole()
		if role != "inbox" {
			file = append(file, kit.GroupItem{Icon: "inbox", Tip: i18n.T("mail.action.inbox"), OnClick: func() { r.act("inbox") }})
		}
		if role != "archive" {
			file = append(file, kit.GroupItem{Icon: "archive", Tip: i18n.T("mail.action.archive"), OnClick: func() { r.act("archive") }})
		}
		if role != "junk" {
			file = append(file, kit.GroupItem{Icon: "spam", Tip: i18n.T("mail.action.junk"), OnClick: func() { r.act("junk") }})
		}
		file = append(file, kit.GroupItem{Icon: "trash-2", Tip: i18n.T("mail.action.delete"), OnClick: func() { r.act("delete") }})
		kit.GroupDivider(c).Margin(0, th.Space(1.5))
		kit.ButtonGroup(c, file...)
		more := kit.IconButton(c, "ellipsis", i18n.T("reader.more"), kit.ButtonOpts{Variant: kit.Ghost}).TextColor(t.Foreground).
			OnClick(func() { r.moreOpen = !r.moreOpen })
		if r.moreOpen {
			// kit's menu, not a native one: its items carry icons.
			kit.MenuAt(c, more, &r.moreOpen, ui.AnchorBottomLeft, ui.AnchorTopLeft, theme.PopupOffset, i18n.T("reader.more"), func() {
				for i, it := range []struct {
					icon, label string
					run         func()
				}{{"save", i18n.T("reader.save"), r.saveMail}, {"printer", i18n.T("reader.print"), r.printMail}} {
					kit.MenuItem(c.Key(i), false).Label(it.label).OnClick(func() { r.moreOpen = false; it.run() }).Children(func() {
						ui.Icon(c, icons.Get(it.icon)).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
						ui.Text(c, it.label).SingleLine()
					})
				}
			})
		}
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
	// part renders one part of the thread under key, with motion; a hairline
	// keeps to the message after it (sep).
	part := func(sep, key any, motion ui.ElementTransition, f func()) ui.Element {
		if n > 0 {
			r.column(c).Key(sep).Transition(threadMotion).Children(func() { kit.Separator(c, false) })
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
		FillWidth().MaxWidth(theme.ReaderMaxWidth).AlignSelf(ui.Center).Gap(th.Space(2.5)).Padding(th.Space(1.5), 0).Justify(ui.Center)
	b.Children(func() {
		ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted).Rotate(rot)
		kit.Text(c, label, t.Text.Sm).FontWeight(500).TextColor(t.Foreground)
	})
}

// folded is a message as one line: avatar, sender, the start of its text,
// time, and the chevron that expands it.
func (r *reader) folded(c *ui.Context, m core.MessageSummary) {
	t, th := theme.Of(c), c.Theme()
	name, _ := sender(m.From)
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
			if icon, label, colr := secVisual(t, m.Security.Status); icon != "" {
				showBadge := false
				if r.p.a.settings.SecurityAI {
					if (m.Security.Status == gomail.SecurityFraud || m.Security.Status == gomail.SecurityNeedsReview) && r.p.a.settings.SecurityWarnings {
						showBadge = true
					} else if (m.Security.Status == gomail.SecurityCertifiedBrand || m.Security.Status == gomail.SecurityVerifiedDomain || m.Security.Status == gomail.SecurityNeutral) && r.p.a.settings.VerifiedBadge {
						showBadge = true
					}
				}
				if showBadge {
					secRow := ui.Row(c).Gap(th.Space(0.5)).AlignItems(ui.Center)
					secRow.Children(func() {
						ui.Icon(c, icons.Fill(icon)).Size(theme.IconXs, theme.IconXs).TextColor(colr)
						kit.Text(c, label, t.Text.Sm).TextColor(colr)
					})
				}
			}
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

// shownAvatar is an avatar the thread already shows for m's sender: m's,
// read when it was open, else one of the sender's other messages'. Only
// those: an avatar loads as its message allows (loadAvatar).
func (r *reader) shownAvatar(m core.MessageSummary) ui.ImageSource {
	if b := r.bodies[m.ID]; b != nil && b.avatar != nil {
		return b.avatar
	}
	_, addr := sender(m.From)
	if addr == "" {
		return nil
	}
	for _, b := range r.bodies {
		if _, from := sender(b.msg.From); b.avatar != nil && strings.EqualFold(from, addr) {
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

// recipients is the header's line of who a message went to: the user's
// address that got it ("me" with one account) and how many others, To and Cc
// together; when the user is only Cc'd, To's first and the user under Cc;
// when neither (Bcc, a list), To's first; with no To or Cc, the account.
func (r *reader) recipients(m core.MessageSummary) string {
	accounts := r.p.a.accounts
	mine := func(addr string) bool {
		return slices.ContainsFunc(accounts, func(a core.Account) bool { return strings.EqualFold(a.Email, addr) })
	}
	me := func(addr string) string {
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
			row := func(key int, label string, values []string) {
				if len(values) == 0 {
					return
				}
				ui.Row(c.Key(key)).Gap(th.Space(3)).AlignItems(ui.Start).Children(func() {
					kit.Text(c, label, t.Text.Sm).TextColor(t.Muted).Width(theme.DetailsLabel).Shrink(0).Unselectable()
					ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1)).Children(func() {
						for i, v := range values {
							r.detailsValue(c.Key(i), v, cut)
						}
					})
				})
			}
			row(0, i18n.T("reader.details.from"), []string{m.From})
			row(1, i18n.T("reader.details.to"), m.To)
			row(2, i18n.T("reader.details.cc"), m.Cc)
			row(3, i18n.T("reader.details.reply_to"), replyTo)
			row(4, i18n.T("reader.details.date"), []string{i18n.FullDate(date) + " " + i18n.T(fmt.Sprintf("mail.weekday.%d", date.Weekday())) + " " + date.Format("15:04")})
			if acc := r.accountOf(m); acc != "" && len(r.p.a.accounts) > 1 {
				row(5, i18n.T("reader.details.account"), []string{acc})
			}
		})
	})
}

// detailsValue is a value of details: an address as Name <address>, the
// address quieter, anything else (the date) as it is. One line, cut to fit
// (noted in cut), whole on hover.
func (r *reader) detailsValue(c *ui.Context, raw string, cut map[string]bool) {
	t, th := theme.Of(c), c.Theme()
	name, addr := sender(raw)
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
	ui.Column(c).FillWidth().Gap(th.Space(3.5)).Children(func() {
		name, addr := sender(m.From)
		head := r.column(c).Gap(th.Space(3.5))
		var bodyReady bool
		head.Children(func() {
			// The sender, its avatar or name, opens its actions (block, copy,
			// mail from them); it no longer folds.
			target := cmp.Or(addr, m.From)
			senderActions := func() {
				if r.senderOpen && r.senderMsgID == m.ID {
					r.senderOpen = false
				} else {
					r.senderOpen = true
					r.senderMsgID = m.ID
				}
			}
			ui.Row(c).Gap(th.Space(2.5)).AlignItems(ui.Center).Children(func() {
				// At its folded row's place (its padding), so it stays still as the message opens.
				kit.Pressable(c, kit.PressableOpts{OnClick: senderActions}).Shrink(0).Radius(t.Radius.Lg).Label(target).
					AlignSelf(ui.Start).Margin(th.Space(1.5), 0, 0, 0).Children(func() {
					// Until the avatar is fetched or refused, a pulse, or in a thread what
					// its folded row showed (no grey flash as it opens); then the image or
					// the initial.
					switch loading := b == nil || b.err == nil && !b.ready || b.avatarStop != nil; {
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
						who := kit.Pressable(c, kit.PressableOpts{OnClick: senderActions}).Gap(th.Space(1)).AlignItems(ui.Center).Label(target).
							Padding(th.Space(0.5), th.Space(1.5)).Margin(-th.Space(0.5), -th.Space(1.5)).Radius(t.Radius.Md)
						who.Children(func() {
							ui.Text(c, name).FontWeight(500).SingleLine().Transition(nameMotion(m.ID))
							if addr != "" && addr != name { // Name <address>, the address quieter
								kit.Text(c, "<"+addr+">", t.Text.Base).TextColor(t.Muted).SingleLine().Shrink(1)
							}
						})
						if r.senderOpen && r.senderMsgID == m.ID {
							r.senderMenu(c, who, addr, m.From)
						}
						if r.isBlocked(m.From) {
							kit.Badge(c, i18n.T("reader.blocked_badge"))
						}
					})
					ui.Row(c).Wrap().Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
						r.recipientsLine(c, m, b)
						r.trackersNote(c, b)
						r.imagesNote(c, m, b, addr)
						r.securityBadge(c, m.ID, sec)
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
			case b != nil && b.err != nil && !b.loading:
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
			r.attachments(c, b.msg.Attachments)
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
	if addr != "" && !b.loading && !b.msg.InJunk && r.p.a.settings.RemoteImages != "ask" { // core never trusts junk, and "ask" ignores trust
		kit.TextLink(c, i18n.T("reader.trust"), t.Foreground, t.Text.Sm).OnClick(func() { r.trustSender(b.msg.From, addr) })
	}
}

// column is the reading pane's centred column for all but a message's
// body: the subject, its tags and labels, the sender, the AI's parts and
// what follows the body, never wider than ReaderMaxWidth.
func (r *reader) column(c *ui.Context) ui.Element {
	return ui.Column(c).FillWidth().MaxWidth(theme.ReaderMaxWidth).AlignSelf(ui.Center)
}

// attachments lists the files attached: a click opens one, Save as copies it.
func (r *reader) attachments(c *ui.Context, atts []core.AttachmentInfo) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Wrap().Gap(th.Space(2)).Children(func() {
		for _, att := range atts {
			if att.Inline {
				continue
			}
			ui.Row(c).Width(theme.AttachmentWidth).Gap(th.Space(1)).Padding(th.Space(2), th.Space(2.5)).Border(theme.Hairline, t.Border).Radius(t.Radius.Lg).Children(func() {
				kit.Pressable(c, kit.PressableOpts{OnClick: func() { r.p.a.openAttachment(att) }}).Grow(1).Shrink(1).Gap(th.Space(2.5)).Children(func() {
					ui.Box(c).Size(th.Space(7), th.Space(8.5)).Radius(t.Radius.Sm).Background(t.Hover).Center().Shrink(0).Children(func() {
						kit.Text(c, fileKind(att), t.Text.Xs).TextColor(t.Muted)
					})
					ui.Column(c).Shrink(1).Children(func() {
						ui.Text(c, fileName(att.Filename, "attachment")).SingleLine() // the name it opens and saves under
						kit.Text(c, byteSize(att.Size), t.Text.Sm).TextColor(t.Muted)
					})
				})
				kit.IconButton(c, "download", i18n.T("reader.save_as"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
					OnClick(func() { r.p.a.saveAttachment(att) })
			})
		}
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
	a, gen := r.p.a, r.gen
	a.do(func(ctx context.Context) error { return a.svc.TrustImageSender(ctx, from) }, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		if gen != r.gen {
			return
		}
		for id, b := range r.bodies {
			if _, from := sender(b.msg.From); strings.EqualFold(from, addr) && !b.loading && !b.msg.InJunk { // as core: never junk
				r.loadImages(id, false)
			}
		}
	})
}

// The calls of a body's page (view), on the UI thread. Each acts only
// while view is still a page the reader shows: one queued before its
// page was closed, or replaced, is dropped.

// BodyHeight is told the height, in CSS pixels, of message's body as its
// page laid it out: its element takes it.
func (a *App) BodyHeight(view ui.NativeWebView, message int64, px int) {
	if b := a.mail.reader.shown(view, message); b != nil {
		b.height = px
		b.glass = [2]int{-1, -1} // the page listens now: what it was told before it loaded was dropped
	}
}

// BodyWheel is told a vertical wheel delta a body's page did not use: the
// reader scrolls by it, within its content.
func (a *App) BodyWheel(view ui.NativeWebView, dy float64) {
	if !a.mail.reader.current(view) {
		return
	}
	s := &a.mail.reader.scroll
	s.Y = min(max(s.Y+float32(dy), 0), s.MaxY)
}

// BodyLink opens a link of a body's page as one of the native reader's:
// http(s) after the confirmation, mailto in a compose window.
func (a *App) BodyLink(view ui.NativeWebView, link string) {
	if a.mail.reader.current(view) {
		a.openLink(link)
	}
}

// BodyNavigate goes back or forward for a body's page (⌘[ and ⌘]).
func (a *App) BodyNavigate(view ui.NativeWebView, dir int) {
	if a.mail.reader.current(view) {
		a.Navigate(dir)
	}
}

// BodySearch opens the search palette for a body's page (⌘F, ⌘K).
func (a *App) BodySearch(view ui.NativeWebView) {
	if a.mail.reader.current(view) {
		a.openPalette()
	}
}

// BodySave saves the message of a body's page (⌘S).
func (a *App) BodySave(view ui.NativeWebView) {
	if a.mail.reader.focusView(view) {
		a.mail.reader.saveMail()
	}
}

// BodyPrint prints the message of a body's page (⌘P).
func (a *App) BodyPrint(view ui.NativeWebView) {
	if a.mail.reader.focusView(view) {
		a.mail.reader.printMail()
	}
}

// BodyBuilt is told message's body as its page got it: whether it is only
// the start (ReaderBody.Cut), for the truncated notice, and has the remote
// images (Remote), which hides their notice.
func (a *App) BodyBuilt(view ui.NativeWebView, message int64, cut, remote bool) {
	if b := a.mail.reader.shown(view, message); b != nil {
		b.cut, b.remote = cut, remote
		if remote {
			b.allowErr = nil
		}
	}
}

// BodyImagesFailed is told n of message's remote images could not load:
// the notice offers a retry.
func (a *App) BodyImagesFailed(view ui.NativeWebView, message int64, n int) {
	if b := a.mail.reader.shown(view, message); b != nil {
		b.imgFailed = n
	}
}

// BodyFailed is told reading message's body with its remote images
// failed: the notice offers a retry.
func (a *App) BodyFailed(view ui.NativeWebView, message int64, err error) {
	if b := a.mail.reader.shown(view, message); b != nil && b.allow {
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
	})
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
	t := theme.Of(c)
	opts := kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: i18n.T("reader.unsubscribe.desc"), Close: i18n.T("common.close")}
	kit.Dialog(c, &r.unsubDlg, i18n.T("reader.unsubscribe.title"), opts, func() {
		ui.Text(c, b.unsub.Host).FontWeight(600)
		if b.unsub.List != "" {
			kit.Text(c, i18n.Tf("reader.unsubscribe.list", b.unsub.List), t.Text.Sm).TextColor(t.Muted)
		}
	}, func() {
		if kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).Clicked() {
			r.unsubDlg = false
		}
		if kit.Button(c, i18n.T("reader.unsubscribe"), kit.ButtonOpts{}).AutoFocus().Clicked() {
			r.unsubDlg = false
			r.sendUnsubscribe(id)
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

// senderMenu is the sender's actions under anchor, the reader's menu as its
// toolbar's more menu is: copy the address, block or unblock the sender.
func (r *reader) senderMenu(c *ui.Context, anchor ui.Element, addr, rawFrom string) {
	target := cmp.Or(addr, rawFrom)
	if target == "" {
		return
	}
	t := theme.Of(c)
	item := func(i int, label, icon string, danger bool, act func()) {
		color := t.Muted
		e := kit.MenuItem(c.Key(i), false).Label(label).OnClick(func() { r.senderOpen = false; act() })
		if danger {
			color = t.DangerText
			e.TextColor(color)
		}
		e.Children(func() {
			ui.Icon(c, icons.Get(icon)).Size(theme.IconSm, theme.IconSm).TextColor(color)
			ui.Text(c, label).SingleLine()
		})
	}
	kit.MenuAt(c, anchor, &r.senderOpen, ui.AnchorBottomLeft, ui.AnchorTopLeft, theme.PopupOffset, target, func() {
		item(0, i18n.T("reader.copy_address"), "copy", false, func() {
			c.WriteClipboard(target)
			r.p.a.toast(i18n.T("reader.address_copied"))
		})
		if r.isBlocked(target) {
			item(1, i18n.T("reader.unblock_sender"), "shield", false, func() {
				r.p.a.setBlocked(target, false, func() { r.p.a.toast(i18n.T("reader.sender_unblocked")) })
			})
		} else {
			item(1, i18n.T("reader.block_sender"), "spam", true, func() {
				r.p.a.setBlocked(target, true, func() { r.p.a.toast(i18n.T("reader.sender_blocked")) })
			})
		}
	})
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

func (r *reader) securityBadge(c *ui.Context, msgID int64, sec gomail.SecurityReport) {
	if !r.p.a.settings.SecurityAI {
		return
	}
	if (sec.Status == gomail.SecurityFraud || sec.Status == gomail.SecurityNeedsReview) && !r.p.a.settings.SecurityWarnings {
		return
	}
	if (sec.Status == gomail.SecurityCertifiedBrand || sec.Status == gomail.SecurityVerifiedDomain || sec.Status == gomail.SecurityNeutral) && !r.p.a.settings.VerifiedBadge {
		return
	}

	t, th := theme.Of(c), c.Theme()
	iconName, label, color := secVisual(t, sec.Status)
	if iconName == "" {
		return
	}

	badge := kit.Pressable(c, kit.PressableOpts{
		OnClick: func() {
			if r.secOpen && r.secMsgID == msgID {
				r.secOpen = false
			} else {
				r.secOpen = true
				r.secMsgID = msgID
			}
		},
	}).Label(label).
		Radius(t.Radius.Sm).Padding(th.Space(0.5), th.Space(1.5)).
		Margin(0, -th.Space(1.5), 0, 0).
		AlignItems(ui.Center).Gap(th.Space(1))
	badge.Children(func() {
		ui.Icon(c, icons.Fill(iconName)).Size(theme.IconXs, theme.IconXs).TextColor(color)
		kit.Text(c, label, t.Text.Sm).TextColor(color)
	})

	if r.secOpen && r.secMsgID == msgID {
		kit.Popover(c, badge, &r.secOpen, func() {
			r.securityPopover(c, sec)
		})
	}
}

func (r *reader) securityPopover(c *ui.Context, sec gomail.SecurityReport) {
	t, th := theme.Of(c), c.Theme()
	iconName, title, color := secVisual(t, sec.Status)
	if iconName == "" {
		return
	}

	ui.Column(c).Width(theme.SecurityPopoverWidth).Gap(th.Space(2)).Children(func() {
		ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Fill(iconName)).Size(theme.Icon, theme.Icon).TextColor(color)
			kit.Text(c, title, t.Text.Base).FontWeight(600).TextColor(color)
		})
		kit.Text(c, r.secExplanation(sec), t.Text.Sm).LineHeight(theme.LineCJK).TextColor(t.Foreground)
	})
}

// secVisual returns the icon, translated label, and theme color for a security status.
func secVisual(t theme.Tokens, status gomail.SecurityStatus) (iconName string, label string, color ui.Color) {
	switch status {
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
		return "", "", color
	}
}
