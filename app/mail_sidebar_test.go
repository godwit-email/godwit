package app

import (
	"slices"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
)

func TestSidebarFolders(t *testing.T) {
	fs := []core.Folder{
		{ID: 1, Name: "INBOX", Role: "inbox", Delim: ".", Unread: 3},
		{ID: 2, Name: "[Gmail]/All Mail", Role: "archive", Delim: "/"}, // ListFolders resolves All Mail as the archive
		{ID: 11, Name: "[Gmail]/Archive copy", Role: "all", Delim: "/"},
		{ID: 3, Name: "[Gmail]/Starred", Role: "flagged", Delim: "/"},
		{ID: 4, Name: "[Gmail]/Important", Role: "important", Delim: "/"},
		{ID: 5, Name: "[Gmail]/Spam", Role: "junk", Delim: "/"},
		{ID: 6, Name: "INBOX.Projects.Q4.Contracts", Delim: ".", Unread: 2},
		{ID: 7, Name: "INBOX.Projects.Expenses", Delim: ".", Unread: 1},
		{ID: 8, Name: "Projects Old", Delim: "."},
		{ID: 9, Name: "INBOX.Projects", Delim: "."},
		{ID: 10, Name: "flat", Delim: ""},
		{ID: 12, Name: "Projects", Delim: ".", Unread: 4}, // not INBOX.Projects, though it shows the same name
	}
	var got []string
	for _, f := range sidebarFolders(7, fs) {
		row := strings.Repeat("  ", f.depth-1) + f.name
		if f.ID == 0 && f.Role == "" {
			row += " (prefix)"
		}
		if f.kids {
			row += " +" + string(rune('0'+f.sum))
		}
		got = append(got, row)
	}
	want := []string{
		"收件箱", "草稿", "归档", "垃圾邮件", // drafts are local
		"Projects +3", "  Expenses", "  Q4 (prefix) +2", "    Contracts", "Projects", "Projects Old", "flat",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rows\n%q\nwant\n%q", got, want)
	}
}

// TestSidebarFolderTree: an account's own folders nest on the server's
// delimiter, each level 16 further in; a parent folds, then counts the unread
// under it.
func TestSidebarFolderTree(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	srv := h.env.IMAP(accs[0].ID).User
	for _, m := range []string{"Projects/Q4/Contracts", "Projects/Q4/Invoices", "Projects/Expenses", "Clients", "Clients/Acme"} {
		if err := srv.Create(m, nil); err != nil {
			t.Fatal(err)
		}
	}
	h.put(accs[0], "Projects/Q4/Contracts", coretest.Msg("bob@example.com", accs[0].Email, "contract", "x"))
	h.put(accs[0], "Projects/Expenses", coretest.Msg("bob@example.com", accs[0].Email, "receipt", "x"))
	h.syncNow(accs[0])
	h.waitMail("the folder tree", func() bool { return h.tt.HasText("Contracts") })
	x := func(name string) float32 {
		r, ok := h.tt.Find(name)
		if !ok {
			t.Fatalf("no %s: %q", name, h.tt.Texts())
		}
		return r.X
	}
	if d1, d2 := x("Q4")-x("Projects"), x("Contracts")-x("Q4"); d1 != 16 || d2 != 16 || x("Projects") != x("收件箱") {
		t.Fatalf("indents %v %v, Projects at %v and 收件箱 at %v", d1, d2, x("Projects"), x("收件箱"))
	}
	h.click("Projects") // only a prefix: the row folds it
	h.until("Projects folded", func() bool { return !h.tt.HasText("Contracts") && !h.tt.HasText("Expenses") })
	if ts := h.tt.Texts(); slices.Index(ts, "Projects") < 0 || ts[slices.Index(ts, "Projects")+1] != "2" {
		t.Fatalf("folded Projects does not count the unread under it: %q", ts)
	}
	h.click("Projects")
	h.until("Projects unfolded", func() bool { return h.tt.HasText("Contracts") })
	h.click("Contracts")
	if v := h.app.route.View; v.FolderID == 0 {
		t.Fatalf("view %+v", v)
	}
	h.click("收起Clients") // a folder itself: its chevron folds it, the row opens it
	h.until("Clients folded", func() bool { return !h.tt.HasText("Acme") })
	h.click("Clients")
	if v := h.app.route.View; v.FolderID != h.folderNamed(accs[0], "Clients").ID {
		t.Fatalf("view %+v", v)
	}
}

func (h *harness) folderNamed(acc core.Account, name string) core.Folder {
	h.t.Helper()
	for _, f := range h.app.mail.folders[acc.ID] {
		if f.Name == name {
			return f
		}
	}
	h.t.Fatalf("no folder %s", name)
	return core.Folder{}
}
