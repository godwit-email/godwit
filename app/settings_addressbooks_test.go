package app

import (
	"context"
	"slices"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

func (h *harness) addressBooks() []core.AddressBook {
	h.t.Helper()
	books, err := h.env.Svc.ListAddressBooks(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return books
}

func TestSettingsAddressBooksAddAndRemove(t *testing.T) {
	h, _ := withAccount(t)

	srvCustom := h.env.StartCardDAV(t)
	srvCustom.PutCollection("work", "Work")
	srvCustom.PutCollection("family", "Family")
	srvCustom.PutCard("work", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob Chen\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	srvCustom.PutCard("work", "carol", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Carol Wu\r\nEMAIL:carol@example.com\r\nEND:VCARD\r\n")
	srvCustom.PutCard("family", "mom", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Mom\r\nEMAIL:mom@example.com\r\nEND:VCARD\r\n")

	srvICloud := h.env.StartCardDAV(t)
	srvICloud.SetBasicAuth("alice@icloud.com", "app-secret-1234")
	srvICloud.PutCard("contacts", "dan", "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Dan Li\r\nEMAIL:dan@example.com\r\nEND:VCARD\r\n")
	core.SetAddressBookPresetURLForTest(t, core.PresetICloud, srvICloud.URL())

	h.click("设置")
	h.click("通讯录")
	h.see("还没有通讯录")

	// Add a custom CardDAV server discovered via /.well-known/carddav.
	h.click("添加通讯录")
	h.click("其他 CardDAV 服务器")
	h.fill("CardDAV 服务器地址", srvCustom.URL())
	h.fill("用户名", "alice")
	h.fill("密码", "secret")
	h.fill("显示名称（可选）", "公司通讯录")
	h.click("连接")

	h.see("公司通讯录")
	h.see("Work")
	h.see("2 张名片")
	h.see("Family")
	h.see("1 张名片")
	h.until("custom address book saved", func() bool {
		books := h.addressBooks()
		return len(books) == 1 && books[0].Name == "公司通讯录" && len(books[0].Collections) == 2
	})

	// Add an iCloud preset address book without entering a server URL.
	h.click("添加通讯录")
	h.click("如何获取应用专用密码 ↗")
	h.until("help link opened", func() bool {
		return slices.Contains(h.plat.Calls(), "OpenURL https://support.apple.com/102654")
	})
	h.fill("用户名", "alice@icloud.com")
	h.fill("应用专用密码", "app-secret-1234")
	h.click("连接")

	h.until("two address books saved", func() bool {
		return len(h.addressBooks()) == 2
	})
	h.see("iCloud")

	// Remove the iCloud address book.
	h.click("删除通讯录「iCloud」")
	h.see("删除通讯录「iCloud」？")
	h.click("删除")
	h.until("iCloud removed", func() bool {
		books := h.addressBooks()
		return len(books) == 1 && books[0].Name == "公司通讯录" && !h.tt.HasText("alice@icloud.com")
	})
}

func TestSettingsAddressBooksCollectionToggleAndSyncNow(t *testing.T) {
	h, _ := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")

	srv := h.env.StartCardDAV(t)
	srv.PutCollection("work", "Work")
	srv.PutCollection("archive", "Archive")
	srv.PutCard("work", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	srv.PutCard("work", "carol", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Carol\r\nEMAIL:carol@example.com\r\nEND:VCARD\r\n")
	srv.PutCard("archive", "old", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Old Contact\r\nEMAIL:old@example.com\r\nEND:VCARD\r\n")

	if _, err := h.env.Svc.AddAddressBook(context.Background(), core.AddressBookInput{
		Preset: core.PresetFastmail, Name: "Fastmail 通讯录", URL: srv.HomeSetURL(), Username: "alice", Password: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	h.app.load()

	h.openSettings("addressbooks")
	h.see("Fastmail 通讯录")
	h.see("Work")
	h.see("2 张名片")
	h.see("Archive")
	h.see("1 张名片")

	// Disable the Archive sub-address-book -> immediately clears cached cards to 0.
	h.click("启用子地址簿「Archive」")
	h.until("Archive disabled and cleared", func() bool {
		books := h.addressBooks()
		return len(books) == 1 &&
			!books[0].Collections[1].Enabled &&
			books[0].Collections[1].CardCount == 0 &&
			h.tt.HasText("0 张名片")
	})

	// Add a new remote card to Work and click 立即同步 -> pulls new card into Work while Archive stays off.
	srv.PutCard("work", "dan", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Dan\r\nEMAIL:dan@example.com\r\nEND:VCARD\r\n")
	h.click("立即同步")
	h.until("Work synced to 3 cards", func() bool {
		books := h.addressBooks()
		return len(books) == 1 &&
			books[0].Collections[0].CardCount == 3 &&
			books[0].Collections[1].CardCount == 0 &&
			h.tt.HasText("3 张名片")
	})

	// Re-enable Archive and sync now -> pulls Archive's card back.
	h.click("启用子地址簿「Archive」")
	h.until("Archive re-enabled", func() bool {
		books := h.addressBooks()
		return len(books) == 1 && books[0].Collections[1].Enabled
	})
	h.click("立即同步")
	h.until("Archive synced back to 1 card", func() bool {
		books := h.addressBooks()
		return len(books) == 1 &&
			books[0].Collections[1].CardCount == 1 &&
			h.tt.HasText("1 张名片")
	})
}

func TestSettingsAddressBooksReorder(t *testing.T) {
	h, _ := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")

	srv1 := h.env.StartCardDAV(t)
	srv1.PutCard("contacts", "a", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Alice Personal\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	ab1, err := h.env.Svc.AddAddressBook(context.Background(), core.AddressBookInput{
		Preset: core.PresetCustom, Name: "个人通讯录", URL: srv1.URL(), Username: "alice", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	srv2 := h.env.StartCardDAV(t)
	srv2.PutCard("contacts", "b", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Alice Corp\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	ab2, err := h.env.Svc.AddAddressBook(context.Background(), core.AddressBookInput{
		Preset: core.PresetCustom, Name: "团队通讯录", URL: srv2.URL(), Username: "alice", Password: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.app.load()

	h.openSettings("addressbooks")
	h.see("个人通讯录")
	h.see("团队通讯录")
	h.see("从上到下按优先级排列；多张名片字段冲突时优先采用排在前面的通讯录，拖动可调整顺序。")

	// Wait for measured row heights to settle before dragging.
	h.tt.Frame()
	h.tt.Frame()

	first, ok1 := h.tt.Find("个人通讯录")
	second, ok2 := h.tt.Find("团队通讯录")
	if !ok1 || !ok2 {
		t.Fatalf("missing rows: %v %v", ok1, ok2)
	}
	x, y := first.X+20, first.Y+24
	endY := second.Y + second.H*0.75
	h.tt.Move(x, y)
	h.tt.Press(x, y)
	h.tt.Move(x, second.Y)
	h.tt.Move(x, endY)
	h.tt.Release(x, endY)
	h.drain()

	h.until("priority order swapped", func() bool {
		books := h.addressBooks()
		return len(books) == 2 &&
			books[0].ID == ab2.ID && books[0].SortOrder == 0 &&
			books[1].ID == ab1.ID && books[1].SortOrder == 1
	})
}

func TestSettingsAddressBooksStatusDotAndAuthRecovery(t *testing.T) {
	h, _ := mailHarness(t, []coretest.Option{coretest.WithoutAutoSync()}, "alice@example.com")

	srv := h.env.StartCardDAV(t)
	srv.PutCard("contacts", "bob", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Bob\r\nEMAIL:bob@example.com\r\nEND:VCARD\r\n")
	if _, err := h.env.Svc.AddAddressBook(context.Background(), core.AddressBookInput{
		Preset: core.PresetICloud, Name: "iCloud", URL: srv.HomeSetURL(), Username: "alice", Password: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	h.app.load()

	h.openSettings("addressbooks")
	h.see("iCloud")
	if _, hasAction := h.tt.Find("需要处理"); hasAction {
		t.Fatal("unexpected action dot while healthy")
	}
	if _, hasRetry := h.tt.Find("正在重试"); hasRetry {
		t.Fatal("unexpected retrying dot while healthy")
	}

	// Transient 503 failure lights up the amber status dot on 通讯录 in the sidebar.
	srv.FailRequests(503)
	h.click("立即同步")
	h.until("amber status dot on sidebar", func() bool {
		_, ok := h.tt.Find("正在重试")
		return ok && h.tt.HasText("通讯录同步失败")
	})

	// Credential rotation causes 401 -> lights up the red action dot and shows reason.
	srv.FailRequests(0)
	srv.SetBasicAuth("alice", "rotated-app-9988")
	h.click("立即同步")
	h.until("red action dot on sidebar", func() bool {
		_, ok := h.tt.Find("需要处理")
		return ok && h.tt.HasText("需要更新凭据") && h.tt.HasText("服务器拒绝了用户名或密码，请更新凭据。")
	})

	// Updating with a wrong credential keeps the dialog open with an error alert;
	// updating with the new credential recovers status to ok and clears the sidebar dot.
	h.click("更新应用专用密码")
	h.fill("应用专用密码", "wrong-secret")
	h.click("保存")
	h.see("服务器拒绝了登录")

	h.fill("应用专用密码", "rotated-app-9988")
	h.click("保存")
	h.until("recovered and sidebar dot cleared", func() bool {
		_, hasAction := h.tt.Find("需要处理")
		_, hasRetry := h.tt.Find("正在重试")
		books := h.addressBooks()
		return !hasAction && !hasRetry && len(books) == 1 && books[0].Status == "ok"
	})
}

func TestSettingsAddressBooksGoogleOAuthAndSearch(t *testing.T) {
	h, _ := withAccount(t)
	fo := &fakeOAuth{flows: map[string]chan flowResult{}}
	h.app.oauth = fo

	p := h.openSettings("general")
	for _, q := range []string{"CardDAV", "子地址簿", "通讯录"} {
		if m := p.matching(q); !slices.Contains(m.apps, "addressbooks") {
			t.Fatalf("query %q did not match addressbooks: %+v", q, m)
		}
	}

	h.openSettings("addressbooks")
	v := p.addressBooks
	var added core.AddressBookInput
	v.add = func(_ context.Context, in core.AddressBookInput) (core.AddressBook, error) {
		added = in
		return core.AddressBook{ID: 42, Preset: core.PresetGoogle, Name: "Google", AuthType: "oauth", Username: in.Username, Status: "ok"}, nil
	}

	// Closing the Add dialog mid-OAuth cancels the active OAuth flow.
	h.click("添加通讯录")
	h.click("Google CardDAV")
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
	h.click("添加通讯录")
	h.click("Google CardDAV")
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
		}}
	})
	h.see("登录授权已过期或被撤销，请重新登录。")
	h.click("重新登录")
	h.until("browser opened for f3", func() bool {
		return slices.Contains(h.plat.Calls(), "OpenURL https://login.example/authorize?n=3")
	})
	fo.finish("f3", "alice@gmail.com")
	h.until("OAuth reauth completed", func() bool {
		return reauthedFlow == "f3"
	})
}

func TestSettingsAddressBooksPresetSwitchClearsCustomURL(t *testing.T) {
	h, _ := withAccount(t)
	srvCustom := h.env.StartCardDAV(t)
	srvCustom.SetBasicAuth("other", "otherpass")
	srvICloud := h.env.StartCardDAV(t)
	srvICloud.SetBasicAuth("alice@icloud.com", "icloud-app-pass")
	srvICloud.PutCard("contacts", "c1", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Ivy\r\nEMAIL:ivy@example.com\r\nEND:VCARD\r\n")
	core.SetAddressBookPresetURLForTest(t, core.PresetICloud, srvICloud.URL())

	h.click("设置")
	h.click("通讯录")
	h.click("添加通讯录")
	h.click("其他 CardDAV 服务器")
	h.fill("CardDAV 服务器地址", srvCustom.URL())

	// Switch back to iCloud without closing the dialog: custom URL must not be sent.
	h.click("iCloud")
	h.fill("用户名", "alice@icloud.com")
	h.fill("应用专用密码", "icloud-app-pass")
	h.click("连接")
	h.until("iCloud address book saved", func() bool {
		books := h.addressBooks()
		return len(books) == 1 && books[0].Preset == core.PresetICloud
	})
	if len(srvCustom.Requests()) != 0 {
		t.Fatalf("expected zero requests to stale custom URL, got %+v", srvCustom.Requests())
	}
}
