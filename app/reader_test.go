package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
)

var readerAvatarSVG = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"><rect width="64" height="64" fill="#ff0000"/></svg>`)

// avatarHarness maps image requests to a local TLS fixture; the real core,
// image cache, image validation and reader conversion remain in the path.
func avatarHarness(t *testing.T, mode string, handler http.HandlerFunc) (*harness, core.Account) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig.ServerName = u.Hostname()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, u.Host)
	}
	t.Cleanup(tr.CloseIdleConnections)
	var acc core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) {
		if err := e.Svc.Close(); err != nil {
			t.Fatal(err)
		}
		e.Svc, err = core.New(t.Context(), core.Options{DataDir: e.DataDir, ImageClient: &http.Client{Transport: tr}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.Svc.Close() })
		st, err := e.Svc.GetSettings(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		st.RemoteImages = mode
		if _, err := e.Svc.SetSettings(t.Context(), st); err != nil {
			t.Fatal(err)
		}
		acc = e.AddAccount(t, "alice@example.com")
	})
	h.plat.svc = h.env.Svc
	h.until("mail page", func() bool { return h.app.loaded && h.app.route.Page == PageMail })
	return h, acc
}

func TestReaderAvatarLoadsWithoutLayoutJump(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	h, acc := avatarHarness(t, "ask", func(w http.ResponseWriter, req *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.Write(readerAvatarSVG)
	})
	t.Cleanup(func() { close(release) })
	m := h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Avatar", "plain"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.see("B")
	before, ok := h.tt.Find("Bob")
	if !ok || before.W != 32 || before.H != 32 {
		t.Fatalf("initial avatar %+v", before)
	}
	h.see("已阻止发件人头像")
	h.click("加载图片")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("avatar request did not start")
	}
	if !h.app.mail.reader.bodies[m.ID].ready {
		t.Fatal("avatar blocked body loading")
	}
	release <- struct{}{}
	h.until("avatar", func() bool { return h.app.mail.reader.bodies[m.ID].avatar != nil })
	after, _ := h.tt.Find("Bob")
	if before != after || slices.Contains(h.tt.Texts(), "B") {
		t.Fatalf("layout %v -> %v, texts %q", before, after, h.tt.Texts())
	}
	if px := h.tt.Image().RGBAAt(int(after.X+16), int(after.Y+16)); px.R < 250 || px.G > 5 || px.B > 5 {
		t.Fatalf("avatar color %v", px)
	}
}

func TestReaderAvatarPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, mode, folder string
		blocked            bool
	}{
		{"disabled", "ask", "INBOX", true}, {"untrusted", "trusted", "INBOX", true}, {"junk", "auto", "Junk", true}, {"automatic", "auto", "INBOX", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			h, acc := avatarHarness(t, tc.mode, func(w http.ResponseWriter, req *http.Request) { hits.Add(1); w.Write(readerAvatarSVG) })
			if tc.folder == "Junk" {
				if err := h.env.IMAP(acc.ID).User.Create("Junk", nil); err != nil {
					t.Fatal(err)
				}
			}
			h.env.Deliver(t, acc.ID, tc.folder, coretest.Msg("Bob <bob@example.com>", acc.Email, "Avatar", "plain"))
			folders, err := h.env.Svc.ListFolders(t.Context(), acc.ID)
			if err != nil {
				t.Fatal(err)
			}
			var folder int64
			for _, f := range folders {
				if f.Name == tc.folder {
					folder = f.ID
				}
			}
			page, err := h.env.Svc.ListMessages(t.Context(), core.View{FolderID: folder}, "", 20)
			if err != nil || len(page.Items) == 0 {
				t.Fatalf("messages %v %v", page, err)
			}
			m := page.Items[0]
			h.openThread(m.ThreadID)
			h.seeBody(m.ID)
			b := h.app.mail.reader.bodies[m.ID]
			if tc.blocked {
				h.see("已阻止发件人头像")
				if hits.Load() != 0 || b.msg.RemoteImagesBlocked != 0 {
					t.Fatalf("blocked: hits %d, body count %d", hits.Load(), b.msg.RemoteImagesBlocked)
				}
				h.click("加载图片")
			}
			h.until("avatar ready", func() bool { return b.avatar != nil })
			if hits.Load() != 1 {
				t.Fatalf("requests %d", hits.Load())
			}
			if tc.blocked && !b.allow {
				t.Fatal("manual permission not kept for current message")
			}
		})
	}
}

func TestReaderAvatarDropsStaleResults(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("Bob <bob@example.com>", accs[0].Email, "Old", "old"))
	n := h.deliverOne(accs[0], coretest.Msg("Carol <carol@example.com>", accs[0].Email, "New", "new"))
	r := h.app.mail.reader
	started, release, finished := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
	t.Cleanup(func() { close(release) })
	r.getAvatar = func(ctx context.Context, id int64, manual bool) (core.Avatar, error) {
		if id == m.ID {
			close(started)
			<-release
			close(finished)
			return core.Avatar{Data: readerAvatarSVG, Format: "svg"}, nil
		}
		return core.Avatar{}, nil
	}
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("avatar did not start")
	}
	old := r.bodies[m.ID]
	h.openThread(n.ThreadID)
	h.seeBody(n.ID)
	release <- struct{}{}
	<-finished
	h.queued("old avatar callback", 1)
	h.drain()
	if old.avatar != nil || r.bodies[n.ID].avatar != nil {
		t.Fatal("stale avatar applied")
	}
}

func TestReaderAvatarPulsesUntilDone(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	r := h.app.mail.reader
	release := make(chan struct{})
	r.getAvatar = func(context.Context, int64, bool) (core.Avatar, error) {
		<-release
		return core.Avatar{}, nil
	}
	m := h.deliverOne(accs[0], coretest.Msg("Bob <bob@example.com>", accs[0].Email, "Hi", "body"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.until("avatar started", func() bool { return r.bodies[m.ID].avatarStop != nil })
	initial := func() bool { h.tt.Frame(); return slices.Contains(h.tt.Texts(), "B") }
	if initial() {
		t.Fatal("the initial shows while the avatar loads")
	}
	close(release)
	h.until("the initial after", initial)
}

func TestReaderAvatarParseFailure(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	r := h.app.mail.reader
	r.getAvatar = func(context.Context, int64, bool) (core.Avatar, error) {
		return core.Avatar{Data: []byte("invalid SVG"), Format: "svg"}, nil
	}
	m := h.deliverOne(accs[0], coretest.Msg("Bob <bob@example.com>", accs[0].Email, "Bad", "body"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.until("avatar completed", func() bool { b := r.bodies[m.ID]; return b.avatarStop == nil })
	h.see("B")
	if r.bodies[m.ID].avatar != nil {
		t.Fatal("invalid SVG displayed")
	}
}

func TestReaderAvatarSettingsInvalidation(t *testing.T) {
	h, acc := avatarHarness(t, "auto", func(w http.ResponseWriter, req *http.Request) { w.Write(readerAvatarSVG) })
	m := h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Avatar", "plain"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	b := h.app.mail.reader.bodies[m.ID]
	h.until("initial avatar", func() bool { return b.avatar != nil })
	st := h.app.settings
	st.RemoteImages = "ask"
	h.app.changeSettings(st)
	if b.avatar != nil {
		t.Fatal("optimistic setting left avatar displayed")
	}
	h.until("setting saved", func() bool { st, _ := h.env.Svc.GetSettings(t.Context()); return st.RemoteImages == "ask" })
	h.advance(refreshEvery)
	h.see("已阻止发件人头像")
	if b.avatar != nil || b.allow {
		t.Fatal("automatic trust became manual permission")
	}
	h.click("加载图片")
	h.until("explicitly allowed cached avatar", func() bool { return b.avatar != nil })
	st.RemoteImages = "trusted"
	h.app.changeSettings(st)
	if b.avatar != nil {
		t.Fatal("manual avatar survived changed policy")
	}
	h.until("trusted setting saved", func() bool { st, _ := h.env.Svc.GetSettings(t.Context()); return st.RemoteImages == "trusted" })
	// A failed write rolls back to the actual trusted policy, still blocking.
	h.app.saveSettings = func(context.Context, core.Settings) (core.Settings, error) {
		return core.Settings{}, errors.New("disk full")
	}
	st.RemoteImages = "auto"
	h.app.changeSettings(st)
	h.see("disk full")
	if b.avatar != nil {
		t.Fatal("failed settings save exposed avatar")
	}
}

func TestReaderAvatarWaitsForSettingsSave(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fail, manual bool
	}{{"saved ask", false, false}, {"rollback auto", true, false}, {"explicit permission while saving", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			h, acc := avatarHarness(t, "ask", func(w http.ResponseWriter, req *http.Request) {
				hits.Add(1)
				w.Write(readerAvatarSVG)
			})
			// First-sync imports do not race arrival prefetch against the policy changes below.
			h.put(acc, "INBOX", coretest.Msg("Bob <bob@example.com>", acc.Email, "First", "plain"))
			h.put(acc, "INBOX", coretest.Msg("Carol <carol@example.net>", acc.Email, "Second", "plain"))
			h.syncNow(acc)
			page, err := h.env.Svc.ListMessages(t.Context(), core.View{Unified: "inbox"}, "", 200)
			if err != nil || len(page.Items) != 2 {
				t.Fatalf("initial messages: %d, %v", len(page.Items), err)
			}
			m := page.Items[slices.IndexFunc(page.Items, func(m core.MessageSummary) bool { return m.Subject == "First" })]
			n := page.Items[slices.IndexFunc(page.Items, func(m core.MessageSummary) bool { return m.Subject == "Second" })]
			st := h.app.settings
			st.RemoteImages = "auto"
			h.app.changeSettings(st)
			h.until("auto saved", func() bool { return h.app.settingsVer == h.app.settingsSavedVer })
			h.openThread(m.ThreadID)
			h.seeBody(m.ID)
			r := h.app.mail.reader
			h.until("initial avatar", func() bool { return r.bodies[m.ID].avatar != nil })
			if hits.Load() != 1 {
				t.Fatalf("initial requests %d", hits.Load())
			}
			started, release := make(chan struct{}), make(chan struct{}, 1)
			t.Cleanup(func() { close(release) })
			save := h.app.saveSettings
			h.app.saveSettings = func(ctx context.Context, st core.Settings) (core.Settings, error) {
				close(started)
				select {
				case <-ctx.Done():
					return core.Settings{}, ctx.Err()
				case <-release:
				}
				if tc.fail {
					return core.Settings{}, errors.New("blocked save failed")
				}
				return save(ctx, st)
			}
			st.RemoteImages = "ask"
			h.app.changeSettings(st)
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("save did not start")
			}
			persisted, err := h.env.Svc.GetSettings(t.Context())
			if err != nil || persisted.RemoteImages != "auto" {
				t.Fatalf("persisted policy %q, %v", persisted.RemoteImages, err)
			}
			r.toggle(m.ID)
			r.toggle(m.ID)
			h.seeBody(m.ID)
			b := r.bodies[m.ID]
			h.until("reopened avatar attempt", func() bool { return b.avatarDone || b.avatarStop == nil })
			if b.avatar != nil {
				t.Error("cached avatar displayed while restrictive setting is unsaved")
			}
			h.openThread(n.ThreadID)
			h.seeBody(n.ID)
			b = r.bodies[n.ID]
			h.until("new message avatar attempt", func() bool { return b.avatarDone || b.avatarStop == nil })
			if b.avatar != nil || hits.Load() != 1 {
				t.Errorf("unsaved ask allowed new avatar: image %v, requests %d", b.avatar != nil, hits.Load())
			}
			if tc.manual {
				h.click("加载图片")
				h.until("explicit avatar while saving", func() bool { return b.avatar != nil })
				if hits.Load() != 2 {
					t.Errorf("explicit requests %d", hits.Load())
				}
			}
			release <- struct{}{}
			h.until("settings settled", func() bool { return h.app.settingsVer == h.app.settingsSavedVer })
			h.advance(refreshEvery)
			if tc.fail {
				h.until("rollback restores automatic avatar", func() bool { return b.avatar != nil })
				if h.app.settings.RemoteImages != "auto" || hits.Load() != 2 {
					t.Fatalf("rollback policy %q, requests %d", h.app.settings.RemoteImages, hits.Load())
				}
			} else if !tc.manual {
				h.until("saved ask policy applied", func() bool { return b.avatarDone && b.avatarBlocked })
				if b.avatar != nil || hits.Load() != 1 {
					t.Fatal("saved ask automatically loaded avatar")
				}
				h.click("加载图片")
				h.until("manual avatar after save", func() bool { return b.avatar != nil })
				if hits.Load() != 2 {
					t.Errorf("manual requests %d", hits.Load())
				}
			}
		})
	}
}

func TestReaderAvatarManualPermissionSurvivesUnrelatedSettings(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(strconv.FormatBool(external), func(t *testing.T) {
			var hits atomic.Int64
			h, acc := avatarHarness(t, "ask", func(w http.ResponseWriter, req *http.Request) {
				hits.Add(1)
				w.Write(readerAvatarSVG)
			})
			m := h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Avatar", "plain"))
			h.openThread(m.ThreadID)
			h.seeBody(m.ID)
			h.click("加载图片")
			b := h.app.mail.reader.bodies[m.ID]
			h.until("manual avatar", func() bool { return b.avatar != nil })
			before := h.app.events
			st := h.app.settings
			st.Theme = "dark"
			if external {
				if _, err := h.env.Svc.SetSettings(t.Context(), st); err != nil {
					t.Fatal(err)
				}
			} else {
				h.app.changeSettings(st)
			}
			h.until("settings event", func() bool {
				return h.app.events > before && h.app.settingsVer == h.app.settingsSavedVer
			})
			h.app.load()
			h.until("new settings loaded", func() bool { return h.app.settings.Theme == "dark" && h.app.accountsGen == h.app.loadGen })
			if b.avatar == nil || !b.avatarAllow || !b.allow || hits.Load() != 1 {
				t.Fatalf("unrelated setting revoked permission: avatar=%v manual=%v body=%v requests=%d", b.avatar != nil, b.avatarAllow, b.allow, hits.Load())
			}
			st.RemoteImages = "trusted"
			if _, err := h.env.Svc.SetSettings(t.Context(), st); err != nil {
				t.Fatal(err)
			}
			h.app.load()
			h.until("changed policy loaded", func() bool { return h.app.settings.RemoteImages == "trusted" })
			if b.avatar != nil || b.avatarAllow {
				t.Fatal("relevant policy change retained manual permission")
			}
		})
	}
}

func TestReaderAvatarLifecycleCancellation(t *testing.T) {
	for _, action := range []string{"settings", "account", "close", "body replacement"} {
		t.Run(action, func(t *testing.T) {
			h, accs := mailHarness(t, nil, "alice@example.com")
			m := h.deliverOne(accs[0], coretest.Msg("Bob <bob@example.com>", accs[0].Email, "Avatar", "plain"))
			r := h.app.mail.reader
			started, release, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{})
			t.Cleanup(func() { close(release) })
			r.getAvatar = func(ctx context.Context, id int64, manual bool) (core.Avatar, error) {
				close(started)
				<-ctx.Done()
				close(cancelled)
				<-release
				return core.Avatar{Data: readerAvatarSVG, Format: "svg"}, nil
			}
			h.openThread(m.ThreadID)
			h.seeBody(m.ID)
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("avatar did not start")
			}
			old := r.bodies[m.ID]
			switch action {
			case "settings":
				st := h.app.settings
				st.RemoteImages = "ask"
				h.app.changeSettings(st)
			case "account":
				if err := h.env.Svc.RemoveAccount(t.Context(), accs[0].ID); err != nil {
					t.Fatal(err)
				}
				h.app.load()
				h.until("removed account", func() bool { return len(h.app.accounts) == 0 })
			case "close":
				h.app.Close()
			case "body replacement":
				r.reset(r.key)
			}
			select {
			case <-cancelled:
			case <-time.After(5 * time.Second):
				t.Fatal("avatar context not cancelled")
			}
			if old.avatar != nil {
				t.Fatal("stale avatar displayed")
			}
		})
	}
}

// deliverOne delivers one message to the account's INBOX and returns its summary.
func (h *harness) deliverOne(acc core.Account, raw []byte) core.MessageSummary {
	h.t.Helper()
	h.env.Deliver(h.t, acc.ID, "INBOX", raw)
	page, err := h.env.Svc.ListMessages(context.Background(), core.View{Unified: "inbox"}, "", 200)
	if err != nil || len(page.Items) == 0 {
		h.t.Fatalf("no message: %v", err)
	}
	return page.Items[0] // newest first
}

// openThread opens a thread in the reader as a click on its row does.
func (h *harness) openThread(id int64) {
	h.app.mail.open([]mailItem{{ID: id, Thread: true}}, 0)
	h.tt.Frame()
}

func (h *harness) messages(threadID int64) []core.MessageSummary {
	h.t.Helper()
	th, err := h.env.Svc.GetThread(context.Background(), threadID)
	if err != nil {
		h.t.Fatal(err)
	}
	return th.Messages
}

func (h *harness) countTexts(sub string) int {
	n := 0
	for _, s := range h.tt.Texts() {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// called waits for a platform call starting with prefix and returns its argument.
func (h *harness) callArg(prefix string) string {
	h.t.Helper()
	var arg string
	h.until(prefix, func() bool {
		for _, c := range h.plat.Calls() {
			if a, ok := strings.CutPrefix(c, prefix+" "); ok {
				arg = a
				return true
			}
		}
		return false
	})
	return arg
}

func (h *harness) hasCall(prefix string) bool {
	return slices.ContainsFunc(h.plat.Calls(), func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func seen(m core.MessageSummary) bool { return slices.Contains(m.Flags, `\Seen`) }

func TestReaderThreadCollapse(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc, base := accs[0], h.clock.Now()
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Plan", "first words", coretest.MessageID("a@x"), at(base, 3)))
	h.put(acc, "INBOX", coretest.Msg("carol@example.com", acc.Email, "Re: Plan", "second words", coretest.MessageID("b@x"), coretest.InReplyTo("a@x"), at(base, 2)))
	h.put(acc, "INBOX", coretest.Msg("dave@example.com", acc.Email, "Re: Plan", "third words", coretest.MessageID("c@x"), coretest.InReplyTo("b@x"), at(base, 1)))
	h.syncNow(acc)
	page, _ := h.env.Svc.ListThreads(context.Background(), core.View{Unified: "inbox"}, "", 10)
	thread := page.Items[0].ID
	msgs := h.messages(thread)
	if len(msgs) != 3 {
		t.Fatalf("%d messages in the thread", len(msgs))
	}
	if err := h.env.Svc.SetFlags(context.Background(), []int64{msgs[0].ID, msgs[1].ID}, []string{`\Seen`}, nil); err != nil {
		t.Fatal(err)
	}
	h.openThread(thread)
	h.seeBody(msgs[2].ID)
	r := h.app.mail.reader
	if !r.open[msgs[2].ID] || r.open[msgs[0].ID] || r.open[msgs[1].ID] {
		t.Fatalf("open %v; want only the newest", r.open)
	}
	if n := h.plat.openViews(); n != 1 {
		t.Fatalf("%d body pages, want the newest's", n)
	}
	h.see("bob@example.com") // the collapsed rows: sender, snippet
	h.see("second words")
	if n := h.countTexts("发给"); n != 1 {
		t.Fatalf("%d expanded headers, want 1: %q", n, h.tt.Texts())
	}
	h.click("bob@example.com")
	h.until("the oldest expanded", func() bool { return r.open[msgs[0].ID] && h.countTexts("发给") == 2 })
	h.seeBody(msgs[0].ID)
	h.click("bob@example.com")
	h.until("the oldest collapsed", func() bool {
		return !r.open[msgs[0].ID] && h.countTexts("发给") == 1 && h.plat.view(msgs[0].ID) == nil
	})
}

func TestReaderThreadHistoryCollapse(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc, base := accs[0], h.clock.Now()
	words := []string{"one", "two", "three", "four", "five", "six", "seven"}
	for i, w := range words {
		opts := []coretest.MsgOption{coretest.MessageID(w + "@x"), at(base, len(words)-i)}
		if i > 0 {
			opts = append(opts, coretest.InReplyTo(words[i-1]+"@x"))
		}
		h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Plan", "msg "+w, opts...))
	}
	h.syncNow(acc)
	page, _ := h.env.Svc.ListThreads(context.Background(), core.View{Unified: "inbox"}, "", 10)
	thread := page.Items[0].ID
	msgs := h.messages(thread)
	if len(msgs) != 7 {
		t.Fatalf("%d messages in the thread, want 7", len(msgs))
	}
	// all read but the fourth: it and the newest open, splitting the folded ones into runs of 3 and 2
	var seen []int64
	for i, m := range msgs {
		if i != 3 {
			seen = append(seen, m.ID)
		}
	}
	if err := h.env.Svc.SetFlags(context.Background(), seen, []string{`\Seen`}, nil); err != nil {
		t.Fatal(err)
	}
	h.openThread(thread)
	h.seeBody(msgs[6].ID)
	r := h.app.mail.reader
	if !r.open[msgs[3].ID] || !r.open[msgs[6].ID] || r.open[msgs[0].ID] {
		t.Fatalf("open %v; want the fourth and the newest", r.open)
	}
	h.until("each run is a card", func() bool {
		return h.tt.HasText("展开历史邮件（3 封）") && h.tt.HasText("展开历史邮件（2 封）") && !h.tt.HasText("msg one") && !h.tt.HasText("msg five")
	})

	h.click("展开历史邮件（3 封）")
	h.until("only the first run expanded", func() bool {
		return h.tt.HasText("msg one") && h.tt.HasText("msg three") && h.countTexts("折叠历史邮件") == 1 &&
			h.tt.HasText("展开历史邮件（2 封）") && !h.tt.HasText("msg five")
	})
	h.click("折叠历史邮件")
	h.until("collapsed again", func() bool {
		return h.tt.HasText("展开历史邮件（3 封）") && !h.tt.HasText("msg one")
	})

	// the threshold counts the thread's messages: 7 collapses at 7, not at 8
	st := h.app.settings
	st.ThreadCollapseThreshold = 8
	h.app.changeSettings(st)
	h.until("below the threshold", func() bool {
		return !h.tt.HasText("展开历史邮件（3 封）") && h.tt.HasText("msg one") && h.tt.HasText("msg five")
	})
	st.ThreadCollapseThreshold = 7
	h.app.changeSettings(st)
	h.until("at the threshold", func() bool {
		return h.tt.HasText("展开历史邮件（3 封）") && !h.tt.HasText("msg one")
	})

	st.ThreadCollapse = false
	h.app.changeSettings(st)
	h.until("collapsing off", func() bool {
		return !h.tt.HasText("展开历史邮件（3 封）") && h.tt.HasText("msg one") && h.tt.HasText("msg five")
	})
}

func TestReaderMarksRead(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc, base := accs[0], h.clock.Now()
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Plan", "one", coretest.MessageID("a@x"), at(base, 2)))
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Re: Plan", "two", coretest.MessageID("b@x"), coretest.InReplyTo("a@x"), at(base, 1)))
	h.syncNow(acc)
	thread := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Re: Plan", "three", coretest.MessageID("c@x"), coretest.InReplyTo("b@x"))).ThreadID
	h.openThread(thread)
	r := h.app.mail.reader
	h.until("all three expanded", func() bool { return len(r.open) == 3 })
	h.until("all read", func() bool {
		return !slices.ContainsFunc(h.messages(thread), func(m core.MessageSummary) bool { return !seen(m) })
	})
}

func TestReaderAttachments(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	data := []byte("%PDF-1.4 report body")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Report", "see attached",
		coretest.Attachment("report.pdf", "application/pdf", data)))
	h.openThread(m.ThreadID)
	h.see("report.pdf")
	full, err := h.env.Svc.GetMessage(context.Background(), m.ID, false)
	if err != nil || len(full.Attachments) != 1 {
		t.Fatalf("attachments %+v, %v", full.Attachments, err)
	}
	h.see(byteSize(full.Attachments[0].Size))
	h.see("PDF")
	tmp := t.TempDir()
	h.plat.mu.Lock()
	h.plat.tempDir = tmp
	h.plat.mu.Unlock()
	h.click("report.pdf")
	path := h.callArg("OpenPath")
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("opened %q: %q, %v", path, got, err)
	}
	if filepath.Base(path) != "report.pdf" || !strings.HasPrefix(path, tmp+string(filepath.Separator)) {
		t.Fatalf("opened %q, not a report.pdf under %q", path, tmp)
	}
	defer func() {
		h.app.Close()
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Errorf("the opened copy's directory outlived Close: %v", err)
		}
	}()
	h.plat.mu.Lock()
	h.plat.savePath = filepath.Join(t.TempDir(), "saved.pdf")
	h.plat.mu.Unlock()
	h.click("另存为…")
	h.callArg("SaveFile")
	h.until("the copy written", func() bool {
		got, err := os.ReadFile(h.plat.savePath)
		return err == nil && bytes.Equal(got, data)
	})
}

// scrollTo scrolls the reader until label is in the window.
func (h *harness) scrollTo(label string) {
	h.t.Helper()
	for range 500 {
		if r, ok := h.tt.Find(label); ok && r.Y > 0 && r.Y+r.H < 820 {
			return
		}
		h.tt.Scroll(900, 400, 0, 100000)
		h.tt.Frame()
	}
	h.t.Fatalf("%q never came into view", label)
}

// View in browser is offered only for a body cut short: the notice's button.
func TestViewInBrowserHasCSP(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.plat.tempDir = t.TempDir()
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Page", "text",
		coretest.HTML(`<p>Shown in a browser</p><script>alert(1)</script><img src="https://tracker.example/p.gif">`)))
	h.openThread(m.ThreadID)
	v := h.seeBody(m.ID)
	h.app.BodyBuilt(v, m.ID, true, false)
	h.tt.Frame()
	h.click("在浏览器中查看")
	u, err := url.Parse(h.callArg("OpenURL"))
	if err != nil || u.Scheme != "file" {
		t.Fatalf("opened %v (%v)", u, err)
	}
	path := filepath.FromSlash(strings.TrimPrefix(u.Path, "/"))
	if !filepath.IsAbs(path) {
		path = filepath.FromSlash(u.Path) // not Windows: the path starts with /
	}
	if !strings.HasPrefix(path, h.plat.tempDir) {
		t.Fatalf("file %q not in the temp dir %q", path, h.plat.tempDir)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	csp := `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'">`
	if !strings.Contains(s, csp) || strings.Contains(strings.ToLower(s), "<script") || !strings.Contains(s, "Shown in a browser") {
		t.Fatalf("file:\n%s", s)
	}
}

func TestReaderReplyCreatesDraft(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Question", "can you?"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.click("回复")
	d, err := h.env.Svc.GetDraft(context.Background(), h.composeOpened(0))
	if err != nil {
		t.Fatal(err)
	}
	if d.Mode != "reply" || d.SourceMessageID != m.ID || !slices.ContainsFunc(d.To, func(s string) bool { return strings.Contains(s, "bob@example.com") }) {
		t.Fatalf("draft %+v", d)
	}
}

func TestReaderArchive(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	h.app.sidebarHidden = true // its Archive folder is named as the button is
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Done", "file it"))
	h.waitMail("the folders", func() bool { return len(h.app.mail.folders[accs[0].ID]) > 0 }) // batch finds archives by them
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.click("归档")
	h.until("archived", func() bool { return h.countIn(h.folderOf(accs[0], "archive")) == 1 })
	if r := h.app.route; r.ThreadID != 0 || r.MessageID != 0 {
		t.Fatalf("the reader still shows %+v", r)
	}
}

// A body's link (ReaderPage.OpenLink → BodyLink) asks first, showing the
// whole address; 打开 opens it, 复制链接 copies it.
func TestLinkConfirm(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	link := "https://shop.example.com/deal?id=7"
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Deal", "text",
		coretest.HTML(`<p>Go to <a href="`+link+`">the deal</a> now</p>`)))
	h.openThread(m.ThreadID)
	v := h.seeBody(m.ID)
	h.app.BodyLink(v, link)
	h.tt.Frame()
	h.see("shop.example.com")
	h.see("/deal?id=7")
	if h.hasCall("OpenURL") {
		t.Fatal("opened before confirming")
	}
	h.click("打开")
	if got := h.callArg("OpenURL"); got != link {
		t.Fatalf("opened %q", got)
	}
	h.until("the dialog closed", func() bool { return !h.tt.HasText("复制链接") })

	h.app.BodyLink(v, link)
	h.tt.Frame()
	h.see("复制链接")
	h.click("复制链接")
	if got := h.tt.Clipboard(); got != link {
		t.Fatalf("clipboard %q", got)
	}
}

func TestReaderRefreshKeepsNewest(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Star me", "body"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	r := h.app.mail.reader
	fetched, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	real := r.getThread
	r.getThread = func(ctx context.Context, id int64) (core.Thread, error) {
		th, err := real(ctx, id)
		if calls.Add(1) == 1 {
			close(fetched)
			<-release
			defer close(returned)
		}
		return th, err
	}
	r.sync(true) // the older refresh: reads the thread unstarred, answers last
	<-fetched
	if err := h.env.Svc.SetFlags(context.Background(), []int64{m.ID}, []string{`\Flagged`}, nil); err != nil {
		t.Fatal(err)
	}
	r.sync(true) // the newer refresh: reads it starred
	flagged := func() bool { return len(r.msgs) == 1 && slices.Contains(r.msgs[0].Flags, `\Flagged`) }
	h.until("the newer refresh applied", flagged)
	close(release)
	<-returned
	frames := 0
	h.until("the older answer posted and run", func() bool { frames++; return frames > 50 })
	if !flagged() {
		t.Fatalf("an older refresh overwrote the newer: %+v", r.msgs)
	}
}

func TestReaderNewReplyExpandsAndReads(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Plan", "first", coretest.MessageID("p1@x"), at(h.clock.Now(), 2)))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	r := h.app.mail.reader
	h.env.Deliver(t, acc.ID, "INBOX", coretest.Msg("carol@example.com", acc.Email, "Re: Plan", "reply words",
		coretest.MessageID("p2@x"), coretest.InReplyTo("p1@x"), at(h.clock.Now(), 1)))
	msgs := h.messages(m.ThreadID)
	if len(msgs) != 2 {
		t.Fatalf("%d messages", len(msgs))
	}
	reply := msgs[1].ID
	h.waitMail("the reply expanded", func() bool { return r.open[reply] && h.plat.view(reply) != nil })
	h.until("the reply read", func() bool { return seen(h.messages(m.ThreadID)[1]) })
}

func TestReaderSenderPopover(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	m := h.deliverOne(acc, coretest.Msg("Spammer <spammer@example.com>", acc.Email, "Spam", "buy now"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	r := h.app.mail.reader
	if r.isBlocked("spammer@example.com") {
		t.Fatal("unexpectedly blocked")
	}

	// Verify opening Godwit-native popover via the chevron button
	h.click("spammer@example.com")
	h.until("popover open", func() bool { return r.senderOpen && r.senderMsgID == m.ID })
	h.see(i18n.T("reader.copy_address"))
	h.see(i18n.T("reader.block_sender"))

	// Blocking shows the badge at once and leaves the conversation where it is.
	h.click(i18n.T("reader.block_sender"))
	h.until("blocked", func() bool { return r.isBlocked("Spammer <spammer@example.com>") })
	h.see(i18n.T("reader.blocked_badge"))
	h.see(i18n.T("reader.sender_blocked"))
	if h.app.route.ThreadID != m.ThreadID {
		t.Fatalf("route %+v: blocking must not move the open conversation", h.app.route)
	}

	h.click("spammer@example.com")
	h.click(i18n.T("reader.unblock_sender"))
	h.until("unblocked", func() bool { return !r.isBlocked("spammer@example.com") })
	h.see(i18n.T("reader.sender_unblocked"))
}

func TestReaderBodyRetry(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Flaky", "eventually shown"))
	r := h.app.mail.reader
	var failed atomic.Bool
	real := r.getMessage
	r.getMessage = func(ctx context.Context, id int64, remote bool) (core.Message, error) {
		if failed.CompareAndSwap(false, true) {
			return core.Message{}, errors.New("server went away")
		}
		return real(ctx, id, remote)
	}
	h.openThread(m.ThreadID)
	h.see("server went away")
	h.click("重试")
	h.seeBody(m.ID)
}

func TestFileName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":   "report.pdf",
		"../../x":      "_.._x",
		"..":           "attachment",
		"":             "attachment",
		"  ":           "attachment",
		"a/b\\c.txt":   "a_b_c.txt",
		"tab\there":    "tab_here",
		"CON":          "_CON",
		"con.txt":      "_con.txt",
		"COM1.log":     "_COM1.log",
		"console.txt":  "console.txt",
		"trailing. . ": "trailing",
	} {
		if got := fileName(in, "attachment"); got != want {
			t.Errorf("fileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSingleMessageHeadDoesNotCollapse(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Plan", "only words"))
	h.openThread(m.ThreadID)
	v := h.seeBody(m.ID)
	var head string // the sender head's "发给 …" line, which only the reader shows
	for _, s := range h.tt.Texts() {
		if strings.HasPrefix(s, "发给") {
			head = s
		}
	}
	if head == "" {
		t.Fatalf("no sender head: %q", h.tt.Texts())
	}
	h.click(head)
	h.tt.Frame()
	if _, shown := h.viewBox(v); !h.app.mail.reader.open[m.ID] || !shown || v.closed {
		t.Fatal("a click on the only message's head collapsed it")
	}
}

// A draft created after the user switched mail still opens: its window
// does not cover the reader.
func TestReplyAfterSwitchingMailOpensWindow(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Question", "can you?"))
	other := h.deliverOne(accs[0], coretest.Msg("carol@example.com", accs[0].Email, "Other", "other body"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	if err := h.tt.Click("回复"); err != nil { // no drain: the draft's callback waits
		t.Fatal(err)
	}
	h.openThread(other.ThreadID)
	if d := h.draft(h.composeOpened(0)); d.Mode != "reply" || d.SourceMessageID != m.ID {
		t.Fatalf("draft %+v", d)
	}
}

func TestRelativeSenderDate(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) // a Wednesday
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 10, 14, 9, 41, 0, 0, time.UTC), "今天 09:41"},
		{time.Date(2026, 10, 13, 18, 5, 0, 0, time.UTC), "昨天 18:05"},
		{time.Date(2026, 10, 12, 7, 30, 0, 0, time.UTC), "周一 07:30"},
		{time.Date(2026, 10, 2, 7, 30, 0, 0, time.UTC), "10 月 2 日"},
	} {
		if got := senderDate(now, c.t); got != c.want {
			t.Errorf("senderDate(%v) = %q, want %q", c.t, got, c.want)
		}
	}

	diffYear := time.Date(2025, 10, 2, 7, 30, 0, 0, time.UTC)
	if got := senderDate(now, diffYear); got != "2025 年 10 月 2 日" {
		t.Errorf("senderDate diffYear = %q, want %q", got, "2025 年 10 月 2 日")
	}

	t.Cleanup(func() { i18n.SetLanguage("zh-CN") })
	i18n.SetLanguage("de")
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 10, 14, 9, 41, 0, 0, time.UTC), "Heute 09:41"},
		{time.Date(2026, 10, 13, 18, 5, 0, 0, time.UTC), "Gestern 18:05"},
		{time.Date(2026, 10, 12, 7, 30, 0, 0, time.UTC), "Mo. 07:30"},
		{time.Date(2026, 10, 2, 7, 30, 0, 0, time.UTC), "2. Okt."},
		{diffYear, "2. Okt. 2025"},
	} {
		if got := senderDate(now, c.t); got != c.want {
			t.Errorf("de senderDate(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

// TestReaderToolbarAt16: the toolbar's buttons fit the window, unclipped and
// apart, at font 13 and 16.
func TestReaderToolbarAt16(t *testing.T) {
	for _, font := range []int{13, 16} {
		t.Run(strconv.Itoa(font), func(t *testing.T) {
			h, acc, _ := aiHarness(t, true, noQuick)
			h.setSettings(func(s *core.Settings) { s.FontSize = font })
			m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Plan", "body"))
			h.openThread(m.ThreadID)
			h.seeBody(m.ID)
			var prev ui.Rect
			for i, label := range []string{"回复", "全部回复", "转发", "归档", "删除", "总结", "翻译"} {
				r, ok := h.tt.Find(label)
				if !ok {
					t.Fatalf("no %q", label)
				}
				if r.X < 0 || r.X+r.W > 1280 || r.Y < 0 {
					t.Errorf("font %d: %q clipped: %+v", font, label, r)
				}
				if i > 0 && r.Y == prev.Y && r.X < prev.X+prev.W {
					t.Errorf("font %d: %q overlaps the one before: %+v over %+v", font, label, r, prev)
				}
				prev = r
			}
		})
	}
}

func TestReaderJunk(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	if err := h.env.IMAP(accs[0].ID).User.Create("Junk", nil); err != nil {
		t.Fatal(err)
	}
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Win", "a prize"))
	h.waitMail("the junk folder", func() bool {
		return slices.ContainsFunc(h.app.mail.folders[accs[0].ID], func(f core.Folder) bool { return f.Role == "junk" })
	})
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.click("移到垃圾邮件")
	h.until("in junk", func() bool { return h.countIn(h.folderOf(accs[0], "junk")) == 1 })
	if r := h.app.route; r.ThreadID != 0 || r.MessageID != 0 {
		t.Fatalf("the reader still shows %+v", r)
	}
}

// An account without a junk folder is skipped and named; nothing moves.
func TestReaderJunkWithoutFolder(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Win", "a prize"))
	h.waitMail("the folders", func() bool { return len(h.app.mail.folders[accs[0].ID]) > 0 })
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.click("移到垃圾邮件")
	h.see("alice@example.com 没有垃圾邮件文件夹，已跳过")
	if h.countIn(h.folderOf(accs[0], "inbox")) != 1 {
		t.Fatal("the message moved")
	}
}

// Inside a junk folder 移到垃圾邮件 is not offered; elsewhere it is.
func TestJunkActionNotInJunk(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	if err := h.env.IMAP(acc.ID).User.Create("Junk", nil); err != nil {
		t.Fatal(err)
	}
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Hi", "x"))
	h.openThread(m.ThreadID)
	h.see("移到垃圾邮件")
	f := h.folderOf(acc, "junk")
	h.post(func() { h.app.navigate(Route{Page: PageMail, View: core.View{FolderID: f.ID}}) })
	h.until("the junk folder", func() bool { return h.app.mail.view.FolderID == f.ID && !h.app.mail.loading })
	h.openThread(m.ThreadID)
	h.see("回复")
	if h.tt.HasText("移到垃圾邮件") {
		t.Fatal("移到垃圾邮件 inside Junk")
	}
}

func TestReaderMoveToInbox(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "In Archive", "content"))
	f := h.folderOf(acc, "archive")
	if err := h.env.Svc.MoveToRole(context.Background(), []int64{m.ID}, "archive"); err != nil {
		t.Fatal(err)
	}
	h.post(func() { h.app.navigate(Route{Page: PageMail, View: core.View{FolderID: f.ID}}) })
	h.until("the archive folder", func() bool { return h.app.mail.view.FolderID == f.ID && !h.app.mail.loading })
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	h.click(i18n.T("mail.action.inbox"))
	h.until("in inbox", func() bool { return h.countIn(h.folderOf(acc, "inbox")) == 1 })
	if r := h.app.route; r.ThreadID != 0 || r.MessageID != 0 {
		t.Fatalf("the reader still shows %+v", r)
	}
}

func TestReaderAvatarMoveToJunk(t *testing.T) {
	h, acc := avatarHarness(t, "auto", func(w http.ResponseWriter, r *http.Request) { w.Write(readerAvatarSVG) })
	if err := h.env.IMAP(acc.ID).User.Create("Junk", nil); err != nil {
		t.Fatal(err)
	}
	h.syncNow(acc)
	m := h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Avatar", "plain"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	r := h.app.mail.reader
	h.until("initial avatar", func() bool { return r.bodies[m.ID].avatar != nil })
	h.advance(refreshEvery)
	h.until("settled avatar", func() bool { return r.bodies[m.ID].avatar != nil })
	if err := h.env.Svc.MoveToRole(t.Context(), []int64{m.ID}, "junk"); err != nil {
		t.Fatal(err)
	}
	h.app.refresh(nil)
	h.until("moved message summary", func() bool { return len(r.msgs) == 1 && r.msgs[0].FolderID != m.FolderID })
	allowed, err := h.env.Svc.ImagesAllowed(t.Context(), m.ID)
	if err != nil || allowed {
		t.Fatalf("junk policy allowed=%v err=%v", allowed, err)
	}
	if r.bodies[m.ID].avatar != nil {
		t.Fatal("automatic avatar remains displayed after same message moves to Junk")
	}
	b := r.bodies[m.ID]
	h.see("已阻止发件人头像")
	h.until("junk body metadata", func() bool { return b.msg.InJunk && !b.trusted && !b.loading })
	if b.msg.RemoteImagesBlocked != 0 || h.tt.HasText("始终信任此发件人") {
		t.Fatal("junk notice has stale body policy")
	}
	h.click("加载图片")
	h.until("manual junk avatar", func() bool { return b.avatar != nil && b.avatarAllow && b.allow })
	h.app.refresh(nil)
	h.until("same-message refresh", func() bool { return !h.app.mail.loading })
	if !b.avatarAllow || b.avatar == nil {
		t.Fatal("refresh revoked current-message manual permission")
	}
	if err := h.env.Svc.Move(t.Context(), []int64{m.ID}, m.FolderID); err != nil {
		t.Fatal(err)
	}
	h.app.refresh(nil)
	h.until("back in inbox", func() bool { return b.msg.FolderID == m.FolderID && !b.msg.InJunk && b.trusted && b.avatar != nil })
	if !b.avatarAllow || !b.allow {
		t.Fatal("folder move lost current-message permission")
	}
	// A fresh reader generation drops the manual grant; moving back then loads automatically.
	h.post(func() { r.reset(r.key); r.load() })
	h.until("fresh automatic inbox avatar", func() bool { b = r.bodies[m.ID]; return b != nil && b.avatar != nil && !b.avatarAllow })
	for _, role := range []string{"junk", "inbox"} {
		folder := h.folderOf(acc, role)
		if err := h.env.Svc.Move(t.Context(), []int64{m.ID}, folder.ID); err != nil {
			t.Fatal(err)
		}
		h.app.refresh(nil)
		if role == "junk" {
			h.until("automatic avatar revoked again", func() bool { return b.msg.InJunk && b.avatar == nil && b.avatarBlocked })
		} else {
			h.until("automatic avatar restored", func() bool { return !b.msg.InJunk && b.avatar != nil && !b.avatarAllow })
		}
	}
}

func TestReaderAvatarMoveDropsOldBodyRead(t *testing.T) {
	h, acc := avatarHarness(t, "auto", func(w http.ResponseWriter, r *http.Request) { w.Write(readerAvatarSVG) })
	if err := h.env.IMAP(acc.ID).User.Create("Junk", nil); err != nil {
		t.Fatal(err)
	}
	h.syncNow(acc)
	m := h.deliverOne(acc, coretest.Msg("Bob <bob@example.com>", acc.Email, "Avatar", "plain"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)
	r := h.app.mail.reader
	b := r.bodies[m.ID]
	h.until("automatic avatar", func() bool { return b.avatar != nil })
	started, release, finished := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
	t.Cleanup(func() { close(release) })
	get := r.getMessage
	var calls atomic.Int64
	r.getMessage = func(ctx context.Context, id int64, remote bool) (core.Message, error) {
		msg, err := get(ctx, id, remote)
		if calls.Add(1) == 1 {
			close(started)
			<-release
			close(finished)
		}
		return msg, err
	}
	h.post(func() { r.loadBody(m.ID) })
	h.until("old body read pending", func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	})
	if err := h.env.Svc.MoveToRole(t.Context(), []int64{m.ID}, "junk"); err != nil {
		t.Fatal(err)
	}
	h.app.refresh(nil)
	h.until("current junk policy", func() bool { return b.msg.InJunk && !b.loading && b.avatar == nil && b.avatarBlocked })
	h.see("已阻止发件人头像")
	release <- struct{}{}
	<-finished
	h.queued("old body callback", 1)
	h.drain()
	if !b.msg.InJunk || b.msg.FolderID == m.FolderID || b.trusted || b.avatar != nil {
		t.Fatal("old body read restored pre-move metadata or avatar")
	}
}

func TestReaderSaveAndPrint(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := accs[0]
	m := h.deliverOne(acc, coretest.Msg("bob@example.com", acc.Email, "Save and Print Test", "Body contents here"))
	h.openThread(m.ThreadID)
	h.seeBody(m.ID)

	tmp := t.TempDir()
	savePath := filepath.Join(tmp, "test-saved.eml")

	// 1. Save button in toolbar
	h.plat.mu.Lock()
	h.plat.savePath = savePath
	h.plat.mu.Unlock()

	h.click(i18n.T("reader.save"))
	arg := h.callArg("SaveFile")
	if !strings.HasSuffix(arg, ".eml") {
		t.Fatalf("expected .eml default name, got %q", arg)
	}
	h.until("mail saved to disk", func() bool {
		data, err := os.ReadFile(savePath)
		return err == nil && len(data) > 0
	})

	// 2. Print button in toolbar
	h.click(i18n.T("reader.print"))
	printArg := h.callArg("Print")
	if printArg != fmt.Sprint(m.ID) {
		t.Fatalf("expected Print call for message %d, got %q", m.ID, printArg)
	}

	// 3. Cmd+S shortcut
	savePath2 := filepath.Join(tmp, "shortcut-saved.eml")
	h.plat.mu.Lock()
	h.plat.savePath = savePath2
	h.plat.calls = nil
	h.plat.mu.Unlock()

	h.tt.Key(ui.Cmd, ui.KeyS)
	h.drain()
	h.until("shortcut mail saved", func() bool {
		data, err := os.ReadFile(savePath2)
		return err == nil && len(data) > 0
	})

	// 4. Cmd+P shortcut
	h.plat.mu.Lock()
	h.plat.calls = nil
	h.plat.mu.Unlock()

	h.tt.Key(ui.Cmd, ui.KeyP)
	h.drain()
	printArg2 := h.callArg("Print")
	if printArg2 != fmt.Sprint(m.ID) {
		t.Fatalf("expected Print call via shortcut for %d, got %q", m.ID, printArg2)
	}

	// 5. BodySave and BodyPrint via native webview callbacks
	view := h.plat.view(m.ID)
	if view == nil {
		t.Fatal("expected non-nil body view")
	}

	savePath3 := filepath.Join(tmp, "body-saved.eml")
	h.plat.mu.Lock()
	h.plat.savePath = savePath3
	h.plat.calls = nil
	h.plat.mu.Unlock()

	h.app.BodySave(view)
	h.until("body save completed", func() bool {
		data, err := os.ReadFile(savePath3)
		return err == nil && len(data) > 0
	})

	h.app.BodyPrint(view)
	printArg3 := h.callArg("Print")
	if printArg3 != fmt.Sprint(m.ID) {
		t.Fatalf("expected BodyPrint call for %d, got %q", m.ID, printArg3)
	}
}

func TestReaderPrintFocusedMessage(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc, base := accs[0], h.clock.Now()
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "Plan", "first words", coretest.MessageID("a@x"), at(base, 2)))
	h.put(acc, "INBOX", coretest.Msg("carol@example.com", acc.Email, "Re: Plan", "second words", coretest.MessageID("b@x"), coretest.InReplyTo("a@x"), at(base, 1)))
	h.syncNow(acc)
	page, _ := h.env.Svc.ListThreads(context.Background(), core.View{Unified: "inbox"}, "", 10)
	msgs := h.messages(page.Items[0].ID)
	if err := h.env.Svc.SetFlags(context.Background(), []int64{msgs[0].ID}, []string{`\Seen`}, nil); err != nil {
		t.Fatal(err)
	}
	h.openThread(page.Items[0].ID)
	h.seeBody(msgs[1].ID)
	r := h.app.mail.reader
	print := func(want int64, do func()) {
		t.Helper()
		h.plat.mu.Lock()
		h.plat.calls = nil
		h.plat.mu.Unlock()
		do()
		h.drain()
		if got := h.callArg("Print"); got != fmt.Sprint(want) {
			t.Fatalf("printed %s, want %d", got, want)
		}
	}
	key := func() { h.tt.Key(ui.Cmd, ui.KeyP) }

	print(msgs[1].ID, key) // nothing focused: the newest
	h.click("bob@example.com")
	h.seeBody(msgs[0].ID)
	print(msgs[0].ID, key) // the one just expanded
	print(msgs[1].ID, func() { h.app.BodyPrint(h.plat.view(msgs[1].ID)) })
	print(msgs[1].ID, key) // the page that took ⌘P keeps the focus
	r.toggle(msgs[1].ID)   // the focused one folded: the newest expanded
	print(msgs[0].ID, key)
}
