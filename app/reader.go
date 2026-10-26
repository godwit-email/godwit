package app

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"io"
	"maps"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
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
	threadIDs   []int64
	labels      []core.AppliedLabel // the thread's
	labelMenu   bool
	labelQuery  string
	open        map[int64]bool // messages shown in full
	historyOpen map[int64]bool // collapsed runs of folded messages shown anyway, by the run's first message
	bodies      map[int64]*msgBody
	scroll      ui.ScrollState // the messages' scrolling; a body's page hands on the wheels it does not use

	ai        map[int64]*msgAI         // by message; kept across switches so a summary runs once
	aiCtx     context.Context          // the AI work of the key shown; nil until some starts
	aiStop    context.CancelFunc       // cancels aiCtx
	skills    fetch[[]core.SkillInfo]  // the skill menu: skills on messages but summarize and translate
	actions   fetch[[]core.ActionInfo] // and plugin actions on messages
	egress    egressAsk                // the action waiting for egress approval
	egressDlg bool                     // its dialog shows
	unsubAsk  int64                    // the message whose one-click unsubscribe waits to be confirmed
	unsubDlg  bool                     // its dialog shows

	getThread  func(ctx context.Context, id int64) (core.Thread, error)                       // tests wrap it
	getMessage func(ctx context.Context, id int64, remote bool) (core.Message, error)         // tests wrap it
	getAvatar  func(context.Context, int64, bool) (core.Avatar, error)                        // tests wrap it
	runSkill   func(ctx context.Context, name string, t core.Target) (<-chan ai.Event, error) // tests wrap it
	cardsFn    func(ctx context.Context, messageID int64) ([]core.Card, error)                // tests wrap it
	tagsFn     func(ctx context.Context, messageID int64) ([]core.Tag, error)
	quickFn    func(ctx context.Context, messageID int64) ([]core.QuickReply, error) // tests wrap it
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
		cardsFn: p.a.svc.PluginCards, tagsFn: p.a.svc.PluginTags, quickFn: p.a.svc.QuickReplies, skillsFn: p.a.svc.ListSkills, actionsFn: p.a.svc.ListActions}
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
	r.key, r.loaded, r.subject, r.msgs = k, false, "", nil
	r.threadIDs, r.labels, r.labelMenu, r.labelQuery = nil, nil, false, ""
	r.open, r.historyOpen, r.bodies = map[int64]bool{}, map[int64]bool{}, map[int64]*msgBody{}
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
			if !isSeen(m) || i == len(msgs)-1 {
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
		if b := r.bodies[id]; b != nil {
			b.closeView()
		}
		return
	}
	r.expand(id)
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

// act applies a list action to what is shown; archiving, deleting and
// moving to junk close it.
func (r *reader) act(op string) {
	r.p.batch(op, r.items())
	if op == "archive" || op == "delete" || op == "junk" {
		rt := &r.p.a.route
		rt.ThreadID, rt.MessageID = 0, 0
		r.sync(false)
	}
}

// draft starts a reply, reply-all or forward of the newest message and
// opens it in a compose window, even when the reader has moved on since:
// the window does not cover it.
func (r *reader) draft(mode string) {
	m, ok := r.last()
	if !ok {
		return
	}
	a, acc := r.p.a, r.account()
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
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
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
func (r *reader) openAttachment(att core.AttachmentInfo) {
	a := r.p.a
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
func (r *reader) saveAttachment(att core.AttachmentInfo) {
	a := r.p.a
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
	return out.Close()
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
	col.Children(func() {
		r.toolbar(c)
		ui.Scroll(c).Grow(1).TrackScroll(&r.scroll).Children(func() {
			ui.Column(c).FillWidth().Padding(th.Space(5), th.Space(7)).Children(func() {
				ui.Column(c).FillWidth().Gap(th.Space(3.5)).Children(func() {
					subject := cmp.Or(r.subject, i18n.T("mail.no_subject"))
					r.column(c).Gap(th.Space(2)).Children(func() {
						// Plugins' tags follow the subject, so a glance at it is enough.
						ui.Row(c).FillWidth().Wrap().AlignItems(ui.Center).Gap(th.Space(1.5)).Children(func() {
							tx := kit.Text(c, subject, t.Text.X2l).FontWeight(600).LineHeight(theme.LineSubject).Shrink(1).Selectable() // copied as often as read
							if !r.hasTags() {
								// shortcut: MyGo measures large semibold text too narrow in a row, so it wraps early; with no tag to follow it, take the row. Drop once MyGo measures it right.
								tx.Grow(1)
							}
							r.tagChips(c)
						})
						r.labelRow(c)
					})
					r.renderMessages(c)
				})
			})
		})
	})
}

// toolbar: the reply group and the archive group as icon buttons, then
// the AI buttons.
func (r *reader) toolbar(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Wrap().Gap(th.Space(2)).AlignItems(ui.Center).Padding(th.Space(2), th.Space(4)).BorderWidth(0, 0, theme.Hairline, 0).BorderColor(t.Border).Children(func() {
		kit.ButtonGroup(c,
			kit.GroupItem{Icon: "reply", Tip: i18n.T("reader.reply"), OnClick: func() { r.draft("reply") }},
			kit.GroupItem{Icon: "reply-all", Tip: i18n.T("reader.reply_all"), OnClick: func() { r.draft("reply_all") }},
			kit.GroupItem{Icon: "forward", Tip: i18n.T("reader.forward"), OnClick: func() { r.draft("forward") }},
		)
		starIcon, starTip := "star", i18n.T("mail.action.star")
		if r.starred() {
			starIcon, starTip = "star-fill", i18n.T("mail.action.unstar")
		}
		file := []kit.GroupItem{
			{Icon: starIcon, Tip: starTip, OnClick: func() { r.act("toggle_star") }},
			{Icon: "archive", Tip: i18n.T("mail.action.archive"), OnClick: func() { r.act("archive") }},
			{Icon: "trash-2", Tip: i18n.T("mail.action.delete"), OnClick: func() { r.act("delete") }},
		}
		if !r.p.inJunk() {
			file = append(file, kit.GroupItem{Icon: "spam", Tip: i18n.T("mail.action.junk"), OnClick: func() { r.act("junk") }})
		}
		kit.ButtonGroup(c, file...)
		ui.Spacer(c)
		ui.Row(c).Wrap().Gap(th.Space(1)).AlignItems(ui.Center).Children(func() { r.aiTools(c) })
	})
}

func (r *reader) starred() bool {
	return slices.ContainsFunc(r.msgs, func(m core.MessageSummary) bool {
		return slices.Contains(m.Flags, `\Flagged`)
	})
}

// renderMessages renders a thread's messages. In a thread of at least
// Settings.ThreadCollapseThreshold messages, each run of two or more folded
// messages collapses into one history card, which expands that run alone.
func (r *reader) renderMessages(c *ui.Context) {
	st := r.p.a.settings
	collapse := st.ThreadCollapse && len(r.msgs) >= st.ThreadCollapseThreshold
	for i := 0; i < len(r.msgs); {
		m := r.msgs[i]
		if r.open[m.ID] {
			r.message(c, m)
			i++
			continue
		}
		j := i + 1
		for j < len(r.msgs) && !r.open[r.msgs[j].ID] {
			j++
		}
		run := r.msgs[i:j]
		if collapse && len(run) >= 2 {
			r.historyToggle(c, m.ID, len(run))
		}
		if !collapse || len(run) < 2 || r.historyOpen[m.ID] {
			for _, f := range run {
				r.folded(c, f)
			}
		}
		i = j
	}
}

// historyToggle is the card that expands or collapses the run of n folded
// messages starting at message first.
func (r *reader) historyToggle(c *ui.Context, first int64, n int) {
	t, th := theme.Of(c), c.Theme()
	label, rot := i18n.Tf("reader.thread.expand", n), float32(0)
	if r.historyOpen[first] {
		label, rot = i18n.T("reader.thread.collapse"), 180
	}
	b := kit.Pressable(c, kit.PressableOpts{Bordered: true, OnClick: func() { r.historyOpen[first] = !r.historyOpen[first] }}).
		FillWidth().MaxWidth(theme.ReaderMaxWidth).AlignSelf(ui.Center).Gap(th.Space(2.5)).Padding(th.Space(2), th.Space(3)).Justify(ui.Center)
	b.Children(func() {
		ui.Icon(c, icons.Get("chevron-down")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted).Rotate(rot)
		kit.Text(c, label, t.Text.Sm).FontWeight(500).TextColor(t.Foreground)
	})
}

// folded is a message as one line: sender, the start of its text, time.
func (r *reader) folded(c *ui.Context, m core.MessageSummary) {
	t, th := theme.Of(c), c.Theme()
	name, _ := sender(m.From)
	kit.Pressable(c, kit.PressableOpts{Bordered: true, OnClick: func() { r.toggle(m.ID) }}).FillWidth().MaxWidth(theme.ReaderMaxWidth).AlignSelf(ui.Center).Gap(th.Space(3)).Padding(th.Space(2), th.Space(3)).Children(func() {
		ui.Text(c, name).FontWeight(500).SingleLine().Shrink(0)
		ui.Text(c, m.Snippet).TextColor(t.Muted).SingleLine().Grow(1).Shrink(1)
		kit.Text(c, listTime(r.p.a.clock.Now(), m.Date), t.Text.Sm).TextColor(t.Muted).Shrink(0)
	})
}

// sender splits a From into a name to show and the address.
func sender(from string) (name, addr string) {
	if a, err := mail.ParseAddress(from); err == nil {
		return cmp.Or(a.Name, a.Address), a.Address
	}
	return from, ""
}

// recipients names who a message went to: "me" for the user's accounts.
func (r *reader) recipients(m core.MessageSummary) string {
	mine := map[string]bool{}
	for _, acc := range r.p.a.accounts {
		mine[strings.ToLower(acc.Email)] = true
	}
	var out []string
	me := false
	for _, to := range m.To {
		name, addr := sender(to)
		if mine[strings.ToLower(addr)] {
			me = true
			continue
		}
		out = append(out, name)
	}
	if me {
		out = append([]string{i18n.T("reader.me")}, out...)
	}
	return strings.Join(out, i18n.T("mail.list_sep"))
}

// message is a message in full: who and when (a click folds it when the
// thread has others), the AI area and plugin cards, the remote image
// notice, the body (or its translation), the attachments and quick replies.
func (r *reader) message(c *ui.Context, m core.MessageSummary) {
	t, th := theme.Of(c), c.Theme()
	b := r.bodies[m.ID]
	ui.Column(c).FillWidth().Gap(th.Space(3.5)).Children(func() {
		name, addr := sender(m.From)
		head := r.column(c).Gap(th.Space(3.5))
		var bodyReady bool
		head.Children(func() {
			var fold func() // only a thread of more than one message folds
			if len(r.msgs) > 1 {
				fold = func() { r.toggle(m.ID) }
			}
			ui.Row(c).Gap(th.Space(2)).AlignItems(ui.Center).Children(func() {
				kit.Pressable(c, kit.PressableOpts{OnClick: fold}).Grow(1).Shrink(1).Gap(th.Space(2.5)).Children(func() {
					// Until the avatar is fetched or refused, a pulse; then the image or the initial.
					if b == nil || b.err == nil && !b.ready || b.avatarStop != nil {
						kit.Skeleton(c, th.Space(8), th.Space(8)).Radius(t.Radius.Lg).Shrink(0).Label(name)
					} else {
						kit.AvatarImage(c, name, b.avatar)
					}
					ui.Column(c).Shrink(1).Children(func() {
						ui.Row(c).Gap(th.Space(1)).Children(func() {
							ui.Text(c, name).FontWeight(500).SingleLine()
							if addr != "" && addr != name {
								ui.Text(c, "<"+addr+">").TextColor(t.Muted).SingleLine()
							}
						})
						when := senderDate(r.p.a.clock.Now(), m.Date)
						kit.Text(c, i18n.Tf("reader.to", r.recipients(m), when), t.Text.Sm).TextColor(t.Muted)
					})
				})
				if b != nil && b.unsub.Method != "" { // each message by its own headers
					if b.unsubbing {
						kit.Spinner(c)
					} else {
						kit.Button(c, i18n.T("reader.unsubscribe"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
							OnClick(func() { r.unsubscribe(m.ID) })
					}
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
			avatarBlocked := addr != "" && b.avatarBlocked && !b.avatarAllow
			switch {
			case !bodyBlocked && !avatarBlocked:
			case b.allowErr != nil:
				kit.Alert(c, kit.Danger, humanize(b.allowErr), func() {
					kit.Button(c, i18n.T("common.retry"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
						OnClick(func() { r.allowImages(m.ID) })
				})
			default: // until the page has them; a slimmer notice than an Alert, at Base
				ui.Row(c).Wrap().Gap(th.Space(3)).AlignItems(ui.Center).Padding(th.Space(2), th.Space(3)).Radius(t.Radius.Lg).
					Background(t.WarningBg).Border(theme.Hairline, t.WarningBorder).Children(func() {
					text := i18n.T("reader.avatar_blocked")
					if bodyBlocked {
						text = i18n.Tf("reader.remote_blocked", b.msg.RemoteImagesBlocked)
					}
					kit.Text(c, text, t.Text.Base).LineHeight(theme.LineCJK).TextColor(t.WarningText)
					if bodyBlocked && b.allow {
						kit.Spinner(c)
						return
					}
					kit.TextLink(c, i18n.T("reader.load_images"), t.WarningText, t.Text.Base).OnClick(func() { r.allowImages(m.ID) })
					if addr != "" && !b.loading && !b.msg.InJunk && r.p.a.settings.RemoteImages != "ask" { // core never trusts junk, and "ask" ignores trust
						kit.TextLink(c, i18n.T("reader.trust_sender"), t.WarningText, t.Text.Base).OnClick(func() { r.trustSender(b.msg.From, addr) })
					}
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
			ui.WebView(c, b.view).Height(max(float32(b.height), th.Space(6))) // a line until the page reports
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
			r.quickReplies(c, m)
		})
	})
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
				kit.Pressable(c, kit.PressableOpts{OnClick: func() { r.openAttachment(att) }}).Grow(1).Shrink(1).Gap(th.Space(2.5)).Children(func() {
					ui.Box(c).Size(th.Space(7), th.Space(8.5)).Radius(t.Radius.Sm).Background(t.Hover).Center().Shrink(0).Children(func() {
						kit.Text(c, fileKind(att), t.Text.Xs).TextColor(t.Muted)
					})
					ui.Column(c).Shrink(1).Children(func() {
						ui.Text(c, att.Filename).SingleLine()
						kit.Text(c, byteSize(att.Size), t.Text.Sm).TextColor(t.Muted)
					})
				})
				kit.IconButton(c, "download", i18n.T("reader.save_as"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
					OnClick(func() { r.saveAttachment(att) })
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

// BodySearch opens the mail list's search for a body's page (⌘F).
func (a *App) BodySearch(view ui.NativeWebView) {
	if a.mail.reader.current(view) {
		a.mail.searchOpen = true
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

// senderDate is a message's time in the sender line: today or yesterday,
// the weekday this week, with the hour; else month and day.
func senderDate(now, t time.Time) string {
	t = t.In(now.Location())
	clock := t.Format("15:04")
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	weekStart := today.AddDate(0, 0, -(int(now.Weekday())+6)%7)
	switch {
	case !t.Before(today):
		return i18n.T("reader.today") + " " + clock
	case t.Before(weekStart) && t.Before(today.AddDate(0, 0, -1)): // as listTime: older than yesterday and this week
		return i18n.Tf("reader.date", i18n.T(fmt.Sprintf("reader.month.%d", t.Month())), t.Day())
	}
	return listTime(now, t) + " " + clock
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
	opts := kit.DialogOpts{Size: kit.DialogSm, Desc: i18n.T("reader.unsubscribe.desc"), Close: i18n.T("common.close")}
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
