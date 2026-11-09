package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/stretchr/testify/require"
)

// newArchive makes a local archive named name from the sidebar.
func (h *harness) newArchive(name string) core.Account {
	h.t.Helper()
	h.click(i18n.T("mail.archive.new"))
	h.typeText(name)
	h.click(i18n.T("mail.archive.create"))
	h.see(name)
	i := slices.IndexFunc(h.app.accounts, func(a core.Account) bool { return a.Kind == core.KindLocalArchive })
	require.GreaterOrEqual(h.t, i, 0)
	return h.app.accounts[i]
}

// emls writes a .eml per subject into a new folder and returns their paths.
func emls(t *testing.T, subjects ...string) []string {
	dir := t.TempDir()
	var paths []string
	for _, s := range subjects {
		p := filepath.Join(dir, s+".eml")
		require.NoError(t, os.WriteFile(p, coretest.Msg("bob@example.com", "alice@example.com", s, "body"), 0o600))
		paths = append(paths, p)
	}
	return paths
}

// A new local archive imports the .eml files the picker returns, says how
// many it imported, and lists them in its inbox.
func TestLocalArchiveImport(t *testing.T) {
	h, _ := mailHarness(t, nil, "alice@example.com")
	acc := h.newArchive("Old mail")
	h.plat.files = emls(t, "Old invoice", "Trip photos")
	require.NoError(t, h.tt.RightClick("Old mail"))
	require.NoError(t, h.tt.ChooseMenuItem(i18n.T("mail.archive.import_files")))
	h.see(i18n.Tf("mail.archive.import.done", 2))
	inbox := h.folderOf(acc, "inbox")
	h.post(func() { h.app.navigate(Route{Page: PageMail, View: core.View{FolderID: inbox.ID}}) })
	h.waitMail("the imported mail", func() bool { return h.tt.HasText("Old invoice") && h.tt.HasText("Trip photos") })
}

// Replying to archive mail drafts from an account that can send.
func TestLocalArchiveReplyFromMailAccount(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	acc := h.newArchive("Old mail")
	if _, err := h.env.Svc.ImportEML(t.Context(), acc.ID, h.folderOf(acc, "inbox").ID, emls(t, "Old invoice")); err != nil {
		t.Fatal(err)
	}
	page, err := h.env.Svc.ListMessages(t.Context(), core.View{FolderID: h.folderOf(acc, "inbox").ID}, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	h.post(func() { h.app.draft(page.Items[0], acc.ID, "reply") })
	id := h.composeOpened(0)
	d := h.draft(id)
	require.Equal(t, accs[0].ID, d.AccountID)
	require.Equal(t, []string{"bob@example.com"}, d.To)
}
