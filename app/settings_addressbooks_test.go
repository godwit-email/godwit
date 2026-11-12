package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
)

func (h *harness) addressBooks() []core.AddressBook {
	h.t.Helper()
	books, err := h.env.Svc.ListAddressBooks(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return books
}

// addICloud adds an iCloud mail account on fake servers, signing in as
// alice with password secret.
func addICloud(t *testing.T, e *coretest.Env) core.Account {
	t.Helper()
	ih, ip, sh, sp, user, pass := e.StartServers(t)
	acc, err := e.Svc.AddAccount(context.Background(), core.AccountInput{Email: "alice@icloud.com", Username: user, Password: pass, Provider: "icloud", IsDefault: true,
		Receiving: core.Server{Host: ih, Port: ip, Security: core.SecurityNone}, Sending: core.Server{Host: sh, Port: sp, Security: core.SecurityNone}})
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

const bobCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n"

// The iCloud account's page turns its address book on with the account's
// password: one collection, so no list, its count on the switch row.
func TestSettingsAccountLinksAddressBook(t *testing.T) {
	var acc core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) { acc = addICloud(t, e) })
	h.until("initial load", func() bool { return h.app.loaded })
	srv := h.env.StartCardDAV(t)
	srv.PutCard("contacts", "bob", bobCard)
	core.SetAddressBookPresetURLForTest(t, core.PresetICloud, srv.URL())

	h.openSettings(accountRoute(acc.ID))
	h.see("用这个账户的登录读取，只读，不写回 iCloud")
	if h.tt.HasText("通讯录同步") {
		t.Fatal("sync row shown before the address book is on")
	}
	h.click("同步 iCloud 通讯录")
	h.until("linked with the account's password", func() bool {
		books := h.addressBooks()
		return len(books) == 1 && books[0].Account == acc.ID && books[0].Status == "ok"
	})
	h.see("1 张名片 · 用这个账户的登录读取，只读，不写回 iCloud")
	h.see("通讯录同步")
	h.see("邮件 · 通讯录") // the sidebar row
	if _, ok := h.tt.Find("启用子地址簿「contacts」"); ok {
		t.Fatal("a one-collection address book lists its collection")
	}
	h.click("同步 iCloud 通讯录")
	h.until("turned off", func() bool { return len(h.addressBooks()) == 0 && !h.tt.HasText("通讯录同步") })
}

// A reused password the CardDAV server refuses leaves the address book
// waiting for its own; with two collections it lists them.
func TestSettingsLinkedAddressBookRefusedPassword(t *testing.T) {
	var acc core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) { acc = addICloud(t, e) })
	h.until("initial load", func() bool { return h.app.loaded })
	srv := h.env.StartCardDAV(t)
	srv.SetBasicAuth("alice", "contacts-only")
	srv.PutCollection("work", "Work")
	srv.PutCollection("family", "Family")
	srv.PutCard("work", "bob", bobCard)
	core.SetAddressBookPresetURLForTest(t, core.PresetICloud, srv.URL())

	h.openSettings(accountRoute(acc.ID))
	h.click("同步 iCloud 通讯录")
	h.see("需要更新凭据")
	h.see("服务器拒绝了用户名或密码，请更新凭据。")
	h.see("需要处理") // the account's sidebar badge
	h.click("更新「iCloud」的登录凭据")
	h.fill("应用专用密码", "contacts-only")
	h.click("保存")
	h.until("its own password works", func() bool {
		books := h.addressBooks()
		return len(books) == 1 && books[0].Status == "ok" && len(books[0].Collections) == 2 && !h.tt.HasText("需要更新凭据")
	})
	h.see("Work")
	h.see("Family")
	h.click("启用子地址簿「Family」")
	h.until("Family off", func() bool {
		books := h.addressBooks()
		return len(books) == 1 && !books[0].Collections[1].Enabled
	})
}

// 添加账户's 只有通讯录 adds a standalone address book, with its own page.
func TestSettingsStandaloneAddressBook(t *testing.T) {
	h, _ := withAccount(t)
	srv := h.env.StartCardDAV(t)
	srv.PutCollection("work", "Work")
	srv.PutCollection("family", "Family")
	srv.PutCard("work", "bob", bobCard)

	h.openSettings("general")
	h.app.openAddAccount()
	h.click("只有通讯录（CardDAV）")
	h.see("添加通讯录")
	h.fill("CardDAV 服务器地址", srv.URL())
	h.fill("用户名", "alice")
	h.fill("密码", "secret")
	h.fill("显示名称（可选）", "公司通讯录")
	h.click("连接")
	h.until("its page", func() bool {
		books := h.addressBooks()
		return len(books) == 1 && h.app.route.Settings == addressBookRoute(books[0].ID)
	})
	id := h.addressBooks()[0].ID
	h.see("侧栏显示名称")
	h.see("通讯录 · 127.0.0.1")
	h.see("Work")
	h.see("1 张名片")
	h.see("服务器上的每个地址簿各有一个开关；关掉的那个，名片不会出现在联系人里。")

	h.fillRow("侧栏显示名称", "同事")
	h.until("renamed", func() bool { return h.addressBooks()[0].Name == "同事" })

	srv.SetBasicAuth("alice", "rotated")
	h.click("立即同步「同事」")
	h.until("the sidebar's action badge", func() bool { _, ok := h.tt.Find("需要处理"); return ok })

	h.click("删除通讯录「同事」")
	h.see("删除通讯录「同事」？")
	h.click("删除")
	h.until("removed, back to an account", func() bool {
		return len(h.addressBooks()) == 0 && h.app.route.Settings != addressBookRoute(id)
	})
}

// Linking a Gmail account's contacts signs in with that account's own OAuth
// client (core resolves it from Account), not straight away the public one.
func TestLinkGmailAddressBookUsesAccountClient(t *testing.T) {
	h, _ := withAccount(t)
	fo := &fakeOAuth{flows: map[string]chan flowResult{}}
	h.app.oauth = fo
	done := make(chan error, 1)
	go func() { done <- h.app.linkAddressBook(context.Background(), 7, core.PresetGoogle, "me@gmail.com") }()
	h.until("the sign-in began", func() bool { return len(fo.began()) == 1 })
	if c := fo.began()[0]; c.Account != 7 || c.ID != "" {
		t.Fatalf("began with %+v, want the account's client (Account 7)", c)
	}
	fo.fail("f1", context.Canceled)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

// A sole collection left turned off (the server dropped the other) keeps its
// switch: it is the only way back on.
func TestSettingsSoleDisabledCollectionKeepsSwitch(t *testing.T) {
	h, _ := withAccount(t)
	srv := h.env.StartCardDAV(t)
	srv.PutCollection("work", "Work")
	srv.PutCollection("family", "Family")
	ab, err := h.env.Svc.AddAddressBook(context.Background(), core.AddressBookInput{Preset: core.PresetCustom, URL: srv.URL(), Username: "alice", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range ab.Collections {
		if col.Name == "Family" {
			if err := h.env.Svc.SetAddressBookCollectionEnabled(context.Background(), col.ID, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	srv.RemoveCollection("work")
	if err := h.env.Svc.SyncAddressBook(context.Background(), ab.ID); err != nil {
		t.Fatal(err)
	}
	h.post(func() { h.app.load() })
	h.openSettings(addressBookRoute(ab.ID))
	h.until("the one collection left, still switchable", func() bool {
		books := h.addressBooks()
		if len(books) != 1 || len(books[0].Collections) != 1 {
			return false
		}
		_, ok := h.tt.Find(i18n.Tf("settings.addressbooks.collection.toggle", "Family"))
		return ok
	})
}

// The accounts list is one order for mail accounts and standalone address
// books, and that order is the address books' priority.
func TestSettingsAccountsListOrdersAddressBooks(t *testing.T) {
	var acc core.Account
	h := newHarnessWith(t, []coretest.Option{coretest.WithoutAutoSync()}, func(e *coretest.Env) { acc = addICloud(t, e) })
	h.until("initial load", func() bool { return h.app.loaded })
	linked := h.env.StartCardDAV(t)
	linked.PutCard("contacts", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob iCloud\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	core.SetAddressBookPresetURLForTest(t, core.PresetICloud, linked.URL())
	la, err := h.env.Svc.AddAddressBook(context.Background(), core.AddressBookInput{Account: acc.ID, Preset: core.PresetICloud})
	if err != nil {
		t.Fatal(err)
	}
	other := h.env.StartCardDAV(t)
	other.PutCard("contacts", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob Corp\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	sa, err := h.env.Svc.AddAddressBook(context.Background(), core.AddressBookInput{Preset: core.PresetCustom, Name: "公司通讯录", URL: other.URL(), Username: "alice", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	h.app.load()
	h.openSettings("general")
	h.see("公司通讯录")

	first, _ := h.tt.Find("alice@icloud.com")
	second, _ := h.tt.Find("公司通讯录")
	x, y := second.X, second.Y+second.H/2
	h.tt.Move(x, y)
	h.tt.Press(x, y)
	h.tt.Move(x, y-(second.Y-first.Y)/2)
	h.tt.Move(x, first.Y-2) // above the account
	h.tt.Release(x, first.Y-2)
	h.drain()
	h.until("the standalone address book first", func() bool {
		books := h.addressBooks()
		return len(books) == 2 && books[0].ID == sa.ID && books[1].ID == la.ID
	})
	m, err := h.env.Svc.SearchContacts(context.Background(), "bob@example.com", 5)
	if err != nil || len(m) != 1 || m[0].Name != "Bob Corp" {
		t.Fatalf("contact %+v, %v", m, err)
	}
	h.click("公司通讯录")
	h.until("a row still opens its page", func() bool { return h.app.route.Settings == addressBookRoute(sa.ID) })
}

// Adding an iCloud account in the sheet adds its address book with the same
// sign-in; unticking 邮件 goes to the address book alone.
func TestAddAccountSheetMailAndContacts(t *testing.T) {
	plaintext(t)
	h, _ := withAccount(t)
	srv := h.env.StartCardDAV(t)
	srv.PutCard("contacts", "bob", bobCard)
	core.SetAddressBookPresetURLForTest(t, core.PresetICloud, srv.URL())

	h.app.openAddAccount()
	h.click("iCloud")
	h.see("要同步的内容")
	h.see("只读同步 iCloud 通讯录里的姓名、公司职位、邮箱和头像")
	h.fill("邮箱地址", "alice@icloud.com")
	h.manualServers()
	h.fill("应用专用密码", "secret")
	h.click("连接")
	h.see("连接成功")
	h.click("完成")
	h.until("the account and its address book", func() bool {
		books := h.addressBooks()
		return h.app.addAccount == nil && len(books) == 1 && books[0].Account != 0 && books[0].Status == "ok"
	})

	h.app.openAddAccount()
	h.click("iCloud")
	h.post(func() { h.app.addAccount.noMail = true }) // the sheet's 邮件 box; the nav has a 邮件 too
	h.until("the mail sign-in gone with 邮件 unticked", func() bool { _, ok := h.tt.Find("手动设置服务器"); return !ok })
	h.click("继续")
	h.see("添加通讯录")
	if v := h.app.pages[PageSettings].(*settingsPage).addressBooks; v.preset != core.PresetICloud || h.app.addAccount != nil {
		t.Fatalf("dialog on %q, sheet %v", v.preset, h.app.addAccount)
	}
}

func TestSettingsAddressBooksGoogleOAuthAndSearch(t *testing.T) {
	h, _ := withAccount(t)
	fo := &fakeOAuth{flows: map[string]chan flowResult{}}
	h.app.oauth = fo

	p := h.openSettings("general")
	for _, q := range []string{"通讯录", "侧栏显示名称"} {
		if m := p.matching(q); !m.accounts {
			t.Fatalf("query %q did not match the accounts: %+v", q, m)
		}
	}

	v := p.addressBooks
	var added core.AddressBookInput
	v.add = func(_ context.Context, in core.AddressBookInput) (core.AddressBook, error) {
		added = in
		return core.AddressBook{ID: 42, Preset: core.PresetGoogle, Name: "Google", AuthType: "oauth", Username: in.Username, Status: "ok"}, nil
	}

	// Closing the Add dialog mid-OAuth cancels the active OAuth flow.
	h.app.openAddressBook(core.PresetGoogle, "", "")
	h.see("使用前请知悉")
	h.click("在浏览器中登录")
	h.until("browser opened for f1", func() bool {
		return slices.Contains(h.plat.Calls(), "OpenURL https://login.example/authorize?n=1")
	})
	h.click("取消")
	h.until("f1 canceled", func() bool {
		return slices.Equal(fo.Canceled(), []string{"f1"})
	})

	// Sign in with user's own OAuth client and complete Google CardDAV setup.
	h.app.openAddressBook(core.PresetGoogle, "", "")
	h.click("自己创建的")
	h.fill("客户端 ID", "777-carddav.apps.googleusercontent.com")
	v.client.secret = "GOCSPX-carddav"
	h.click("在浏览器中登录")
	h.until("browser opened for f2", func() bool {
		return slices.Contains(h.plat.Calls(), "OpenURL https://login.example/authorize?n=2")
	})
	fo.finish("f2", "alice@gmail.com")
	h.until("Google CardDAV input submitted", func() bool {
		return added.OAuthFlowID == "f2" && v.dialog == ""
	})
	if added.Preset != core.PresetGoogle || added.Username != "alice@gmail.com" || added.Client.ID != "777-carddav.apps.googleusercontent.com" {
		t.Fatalf("unexpected Google CardDAV input: %+v", added)
	}

	// Re-login on an OAuth address book in reauth_required calls UpdateAddressBookAuth.
	var reauthedFlow string
	v.updateAuth = func(_ context.Context, id int64, pw, flow string, _ core.OAuthClient) (core.AddressBook, error) {
		if id == 42 && pw == "" {
			reauthedFlow = flow
		}
		return core.AddressBook{ID: 42, Preset: core.PresetGoogle, Name: "Google", AuthType: "oauth", Username: "alice@gmail.com", Status: "ok"}, nil
	}
	h.post(func() {
		h.app.loadGen++
		h.app.addressBooks = []core.AddressBook{{
			ID: 42, Preset: core.PresetGoogle, Name: "Google", AuthType: "oauth",
			Username: "alice@gmail.com", Status: "reauth_required", StatusDetail: "token_revoked",
			Collections: []core.AddressBookCollection{{ID: 1, AddressBookID: 42, Name: "Contacts", Enabled: true}},
		}}
		h.app.navigate(Route{Page: PageSettings, Settings: addressBookRoute(42)})
	})
	h.see("登录授权已过期或被撤销，请重新登录。")
	h.click("更新「Google」的登录凭据")
	h.until("browser opened for f3", func() bool {
		return slices.Contains(h.plat.Calls(), "OpenURL https://login.example/authorize?n=3")
	})
	fo.finish("f3", "alice@gmail.com")
	h.until("OAuth reauth completed", func() bool {
		return reauthedFlow == "f3"
	})
}

// The 只有通讯录 dialog asks for a CardDAV server only (iCloud, Fastmail and
// Gmail are chosen in the add-account sheet), and closes once the address
// book is saved, while its first sync is still going.
func TestAddAddressBookDialogClosesBeforeFirstSync(t *testing.T) {
	h, _ := withAccount(t)
	srv := h.env.StartCardDAV(t)
	srv.PutCard("contacts", "bob", bobCard)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	srv.SetMultigetHook(func(string) int { <-release; return 200 })

	h.openSettings("general")
	h.app.openAddressBook(core.PresetCustom, "", "")
	h.see("CardDAV 服务器地址")
	for _, tile := range []string{"iCloud", "Fastmail", "Google CardDAV"} {
		if _, ok := h.tt.Find(tile); ok {
			t.Fatalf("the dialog offers %s", tile)
		}
	}
	h.fill("CardDAV 服务器地址", srv.URL())
	h.fill("用户名", "alice")
	h.fill("密码", "secret")
	h.click("连接")
	v := h.app.pages[PageSettings].(*settingsPage).addressBooks
	h.until("closed onto the saved address book, its cards still on the way", func() bool {
		books := h.addressBooks()
		return v.dialog == "" && len(books) == 1 && h.app.route.Settings == addressBookRoute(books[0].ID)
	})
	once.Do(func() { close(release) })
	h.until("the first sync done", func() bool {
		cs, err := h.env.Svc.Contacts(context.Background())
		return err == nil && len(cs) > 0
	})
}
