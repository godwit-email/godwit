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
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/richtext"
	"github.com/godwit-email/godwit/internal/theme"
)

// reader shows the thread the route names, or only its message when the
// route names one: older read messages fold to a line, unread ones and the
// newest open. All its state lives on the UI thread.
type reader struct {
	p *mailPage

	key     [2]int64 // the route's ThreadID and MessageID shown
	gen     uint64   // bumped per key; older results are dropped
	loaded  bool
	subject string
	msgs    []core.MessageSummary // oldest first
	open    map[int64]bool        // messages shown in full
	bodies  map[int64]*msgBody

	allowLoopback bool // tests: images from httptest servers
}

// msgBody is a message read in full and its body converted.
type msgBody struct {
	msg     core.Message
	doc     richtext.Doc
	cut     bool // the body was cut: by the Service or the conversion's budget
	remote  bool // remote images were asked for
	loading bool
	ready   bool
	images  *imageLoader
}

func newReader(p *mailPage) *reader {
	return &reader{p: p, open: map[int64]bool{}, bodies: map[int64]*msgBody{}}
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

// reset forgets what was shown and cancels its image loads.
func (r *reader) reset(k [2]int64) {
	for _, b := range r.bodies {
		if b.images != nil {
			b.images.cancel()
		}
	}
	r.key, r.loaded, r.subject, r.msgs = k, false, "", nil
	r.open, r.bodies = map[int64]bool{}, map[int64]*msgBody{}
	r.gen++
}

func (r *reader) load() {
	a, gen, k := r.p.a, r.gen, r.key
	var th core.Thread
	a.do(func(ctx context.Context) (err error) {
		th, err = a.svc.GetThread(ctx, k[0])
		return err
	}, func(err error) {
		if gen != r.gen {
			return
		}
		if err != nil {
			a.toastErr(err)
			return
		}
		msgs := th.Messages
		if k[1] != 0 {
			msgs = slices.DeleteFunc(msgs, func(m core.MessageSummary) bool { return m.ID != k[1] })
		}
		first := !r.loaded
		r.loaded, r.subject, r.msgs = true, th.Subject, msgs
		if !first {
			return
		}
		var unread []core.MessageSummary
		for i, m := range msgs {
			if !isSeen(m) {
				unread = append(unread, m)
			}
			if !isSeen(m) || i == len(msgs)-1 {
				r.expand(m.ID)
			}
		}
		r.markRead(unread)
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
		if !isSeen(m) {
			r.msgs[i].Flags = append(slices.Clone(m.Flags), `\Seen`)
		}
	}
}

func (r *reader) expand(id int64) {
	r.open[id] = true
	if r.bodies[id] == nil {
		r.loadBody(id, false)
	}
}

func (r *reader) toggle(id int64) {
	if r.open[id] {
		delete(r.open, id)
		return
	}
	r.expand(id)
}

// loadBody reads the message and converts its body off the UI thread; with
// remote, remote images are kept for its image loader.
func (r *reader) loadBody(id int64, remote bool) {
	a, gen := r.p.a, r.gen
	b := r.bodies[id]
	if b == nil {
		b = &msgBody{}
		r.bodies[id] = b
	}
	if b.loading {
		return
	}
	b.loading = true
	var m core.Message
	var doc richtext.Doc
	var cut bool
	a.do(func(ctx context.Context) (err error) {
		if m, err = a.svc.GetMessage(ctx, id, remote); err != nil {
			return err
		}
		doc, cut = messageDoc(m)
		return nil
	}, func(err error) {
		if gen != r.gen {
			return
		}
		b.loading = false
		if err != nil {
			a.toastErr(err)
			return
		}
		if b.images != nil {
			b.images.cancel()
		}
		b.msg, b.doc, b.cut, b.remote, b.ready = m, doc, cut || m.Truncated, remote, true
		b.images = newImageLoader(a.ctx, a.onUI, r.allowLoopback)
	})
}

var blankLine = regexp.MustCompile(`\n[ \t]*\n`)

// messageDoc is the message's body as a document: its HTML converted within
// the budget, else its text in paragraphs. cut reports the budget cut it.
func messageDoc(m core.Message) (richtext.Doc, bool) {
	if m.HTML != "" {
		return richtext.FromMailHTML(m.HTML, richtext.DefaultLimits)
	}
	var d richtext.Doc
	for _, p := range blankLine.Split(strings.ReplaceAll(m.Text, "\r\n", "\n"), -1) {
		if p = strings.Trim(p, "\n"); strings.TrimSpace(p) == "" {
			continue
		}
		if len(d.Blocks) == richtext.DefaultLimits.MaxBlocks {
			return d, true
		}
		d.Blocks = append(d.Blocks, richtext.Block{Kind: richtext.Paragraph, Inlines: []richtext.Inline{{Text: p}}})
	}
	return d, false
}

// accountOf is the account of a folder; 0 when the folder is unknown.
func (p *mailPage) accountOf(folderID int64) int64 {
	f, _ := p.folder(folderID)
	return f.AccountID
}

// last is the newest message shown: the one replies and the More menu act on.
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

// act applies a list action to what is shown; archiving and deleting close it.
func (r *reader) act(op string) {
	r.p.batch(op, r.items())
	if op == "archive" || op == "delete" {
		rt := &r.p.a.route
		rt.ThreadID, rt.MessageID = 0, 0
		r.sync(false)
	}
}

// draft starts a reply, reply-all or forward of the newest message and
// opens it in the composer.
func (r *reader) draft(mode string) {
	m, ok := r.last()
	if !ok {
		return
	}
	a, gen, acc := r.p.a, r.gen, r.account()
	var id int64
	a.do(func(ctx context.Context) error {
		d, err := a.svc.NewDraft(ctx, acc, mode, m.ID)
		id = d.ID
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		if gen == r.gen {
			a.navigate(Route{Page: PageCompose, DraftID: id})
		}
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

// exportEML saves the message's raw source where the user chooses.
func (r *reader) exportEML(m core.MessageSummary) {
	a := r.p.a
	plat := a.plat
	a.do(func(ctx context.Context) error {
		path, err := plat.SaveFile(fileName(m.Subject, "message") + ".eml")
		if err != nil || path == "" {
			return err
		}
		raw, err := a.svc.GetRawMessage(ctx, m.ID)
		if err != nil {
			return err
		}
		return os.WriteFile(path, raw, 0o600)
	}, a.toastErr)
}

// fileName makes s a file name: separators and control characters become _.
func fileName(s, fallback string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
	return cmp.Or(s, fallback)
}

// openAttachment fetches the attachment and opens it with its application.
func (r *reader) openAttachment(id int64) {
	a := r.p.a
	plat := a.plat
	a.do(func(ctx context.Context) error {
		path, _, err := a.svc.GetAttachment(ctx, id)
		if err != nil {
			return err
		}
		return plat.OpenPath(path)
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
	if r.key[0] == 0 || !r.loaded || len(r.msgs) == 0 {
		return
	}
	col.Children(func() {
		r.toolbar(c)
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).MaxWidth(theme.ReaderMaxWidth).Padding(th.Space(5), th.Space(7)).Gap(th.Space(3.5)).Children(func() {
				subject := cmp.Or(r.subject, i18n.T("mail.no_subject"))
				ui.Text(c, subject).FontSize(t.Text.X2l).FontWeight(600)
				for _, m := range r.msgs {
					if r.open[m.ID] {
						r.message(c, m)
					} else {
						r.folded(c, m)
					}
				}
			})
		})
	})
}

// toolbar: reply, reply all, forward, archive, delete; star, mark unread
// and More at the end.
func (r *reader) toolbar(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	out := kit.ButtonOpts{Variant: kit.Outline, Small: true}
	ghost := kit.ButtonOpts{Variant: kit.Ghost, Small: true}
	ui.Row(c).Wrap().Gap(th.Space(1)).Padding(th.Space(2), th.Space(4)).BorderWidth(0, 0, theme.Hairline, 0).BorderColor(t.Border).Children(func() {
		kit.Button(c, i18n.T("reader.reply"), out).OnClick(func() { r.draft("reply") })
		kit.IconButton(c, "reply-all", i18n.T("reader.reply_all"), ghost).OnClick(func() { r.draft("reply_all") })
		kit.Button(c, i18n.T("reader.forward"), out).OnClick(func() { r.draft("forward") })
		kit.Button(c, i18n.T("mail.action.archive"), out).OnClick(func() { r.act("archive") })
		kit.Button(c, i18n.T("mail.action.delete"), out).OnClick(func() { r.act("delete") })
		ui.Spacer(c)
		starred := !slices.ContainsFunc(r.msgs, func(m core.MessageSummary) bool { return !slices.Contains(m.Flags, `\Flagged`) })
		if starred {
			kit.IconButton(c, "star", i18n.T("reader.unstar"), ghost).OnClick(func() { r.act("unstar") })
		} else {
			kit.IconButton(c, "star", i18n.T("mail.action.star"), ghost).OnClick(func() { r.act("star") })
		}
		kit.IconButton(c, "mail", i18n.T("mail.action.unread"), ghost).OnClick(func() { r.act("unread") })
		m, _ := r.last()
		kit.IconButton(c, "ellipsis", i18n.T("reader.more"), ghost).Menu(func(menu *ui.Menu) {
			if menu.Item(i18n.T("reader.view_in_browser")).Chosen() {
				r.viewInBrowser(m.ID)
			}
			if menu.Item(i18n.T("reader.export_eml")).Chosen() {
				r.exportEML(m)
			}
		})
	})
}

// folded is a message as one line: sender, the start of its text, time.
func (r *reader) folded(c *ui.Context, m core.MessageSummary) {
	t, th := theme.Of(c), c.Theme()
	name, _ := sender(m.From)
	ui.ButtonBase(c).Gap(th.Space(3)).Padding(th.Space(2), th.Space(3)).Radius(t.Radius.Md).Border(theme.Hairline, t.Border).Children(func() {
		ui.Text(c, name).FontWeight(500).SingleLine().Shrink(0)
		ui.Text(c, m.Snippet).TextColor(t.Muted).SingleLine().Grow(1).Shrink(1)
		ui.Text(c, listTime(r.p.a.clock.Now(), m.Date)).FontSize(t.Text.Sm).TextColor(t.Muted).Shrink(0)
	}).OnClick(func() { r.toggle(m.ID) })
}

// sender splits a From into a name to show and the address.
func sender(from string) (name, addr string) {
	if a, err := mail.ParseAddress(from); err == nil {
		return cmp.Or(a.Name, a.Address), a.Address
	}
	return from, ""
}

// initial is the first letter of a name, upper case.
func initial(name string) string {
	r, _ := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError {
		return ""
	}
	return string(unicode.ToUpper(r))
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
// notice, the body and the attachments.
func (r *reader) message(c *ui.Context, m core.MessageSummary) {
	t, th := theme.Of(c), c.Theme()
	b := r.bodies[m.ID]
	ui.Column(c).Gap(th.Space(3.5)).Children(func() {
		name, addr := sender(m.From)
		head := ui.ButtonBase(c).Gap(th.Space(2.5)).Justify(ui.Start).Children(func() {
			ui.Box(c).Size(th.Space(8), th.Space(8)).Radius(t.Radius.Full).Background(t.Hover).Center().Shrink(0).Children(func() {
				ui.Text(c, initial(name)).FontWeight(600)
			})
			ui.Column(c).Shrink(1).Children(func() {
				ui.Row(c).Gap(th.Space(1)).Children(func() {
					ui.Text(c, name).FontWeight(500).SingleLine()
					if addr != "" && addr != name {
						ui.Text(c, "<"+addr+">").TextColor(t.Muted).SingleLine()
					}
				})
				when := m.Date.In(r.p.a.clock.Now().Location()).Format("2006-01-02 15:04")
				ui.Text(c, i18n.Tf("reader.to", r.recipients(m), when)).FontSize(t.Text.Sm).TextColor(t.Muted)
			})
		})
		if len(r.msgs) > 1 {
			head.OnClick(func() { r.toggle(m.ID) })
		}
		r.aiArea(c, m)
		r.cards(c, m)
		if b == nil || !b.ready {
			kit.Spinner(c)
			return
		}
		if b.msg.RemoteImagesBlocked > 0 && !b.remote {
			kit.Alert(c, kit.Warning, i18n.Tf("reader.remote_blocked", b.msg.RemoteImagesBlocked), func() {
				kit.Button(c, i18n.T("reader.load_images"), kit.ButtonOpts{Variant: kit.Ghost, Small: true}).
					OnClick(func() { r.loadBody(m.ID, true) })
			})
		}
		richtext.Render(c, b.doc, richtext.RenderOptions{Images: b.images.Load, OnLink: r.p.a.openLink})
		if b.cut {
			ui.Row(c).Wrap().Gap(th.Space(3)).Children(func() {
				ui.Text(c, i18n.T("reader.truncated")).FontSize(t.Text.Sm).TextColor(t.Muted)
				kit.Button(c, i18n.T("reader.open_in_browser"), kit.ButtonOpts{Variant: kit.Outline, Small: true}).
					OnClick(func() { r.viewInBrowser(m.ID) })
			})
		}
		r.attachments(c, b.msg.Attachments)
	})
}

// aiArea is where the summary and translation show (Task 12).
func (r *reader) aiArea(*ui.Context, core.MessageSummary) {}

// cards is where plugin cards show (Task 12).
func (r *reader) cards(*ui.Context, core.MessageSummary) {}

// attachments lists the files attached: a click opens one, Save as copies it.
func (r *reader) attachments(c *ui.Context, atts []core.AttachmentInfo) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Wrap().Gap(th.Space(2)).Children(func() {
		for _, att := range atts {
			if att.Inline {
				continue
			}
			ui.Row(c).Width(theme.AttachmentWidth).Gap(th.Space(1)).Padding(th.Space(2), th.Space(2.5)).Border(theme.Hairline, t.Border).Radius(t.Radius.Md).Children(func() {
				ui.ButtonBase(c).Grow(1).Shrink(1).Gap(th.Space(2.5)).Justify(ui.Start).Children(func() {
					ui.Box(c).Size(th.Space(7), th.Space(8.5)).Radius(t.Radius.Xs).Background(t.Hover).Center().Shrink(0).Children(func() {
						ui.Text(c, fileKind(att)).FontSize(t.Text.Xs).TextColor(t.Muted)
					})
					ui.Column(c).Shrink(1).Children(func() {
						ui.Text(c, att.Filename).SingleLine()
						ui.Text(c, byteSize(att.Size)).FontSize(t.Text.Sm).TextColor(t.Muted)
					})
				}).OnClick(func() { r.openAttachment(att.ID) })
				kit.IconButton(c, "download", i18n.T("reader.save_as"), kit.ButtonOpts{Variant: kit.Ghost, Small: true}).
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
