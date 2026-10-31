package app

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSidebarContextMenuEmptyTrash(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	base := h.clock.Now()
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "trash mail", "body", at(base, 1)))
	h.syncNow(accs[0])
	h.waitMail("inbox mail", func() bool { return len(h.items()) == 1 })

	// Move the message to Trash
	msgID := h.items()[0].ID
	require.NoError(t, h.app.svc.Delete(t.Context(), []int64{msgID}))
	h.post(func() { h.app.mail.reload() })
	h.waitMail("trash folder has mail", func() bool {
		return h.app.mail.roleFolder(accs[0].ID, "trash").Total == 1
	})

	trashName := i18n.T("mail.folder.trash")
	emptyTrashAct := i18n.T("mail.empty_trash")

	// Right click on Trash in the sidebar
	require.NoError(t, h.tt.RightClick(trashName))
	menu := h.tt.Menu()
	t.Logf("trashName: %q, menu items: %v", trashName, menu)
	require.True(t, slices.Contains(menu, emptyTrashAct), "context menu should have Empty Trash")

	// Choose Empty Trash
	require.NoError(t, h.tt.ChooseMenuItem(emptyTrashAct))

	// Dialog should appear
	h.until("dialog shows", func() bool { return h.app.mail.emptyFolderAsk })
	assert.True(t, h.app.mail.emptyFolderAsk)

	// Confirm dialog
	h.click(i18n.T("mail.empty_trash"))
	h.waitMail("dialog closes and trash empty", func() bool {
		h.syncNow(accs[0])
		return !h.app.mail.emptyFolderAsk && h.app.mail.roleFolder(accs[0].ID, "trash").Total == 0
	})
	assert.Equal(t, 0, h.app.mail.roleFolder(accs[0].ID, "trash").Total)
}

func TestSidebarContextMenuEmptySpam(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	if err := h.env.IMAP(accs[0].ID).User.Create("Junk", nil); err != nil {
		t.Fatal(err)
	}
	base := h.clock.Now()
	h.put(accs[0], "Junk", coretest.Msg("spammer@example.com", "alice@example.com", "spam mail", "body", at(base, 1)))
	h.syncNow(accs[0])
	h.post(func() { h.app.mail.reload() })
	h.waitMail("junk folder resolved", func() bool {
		return h.app.mail.roleFolder(accs[0].ID, "junk").ID != 0 && h.app.mail.roleFolder(accs[0].ID, "junk").Total == 1
	})

	junkName := i18n.T("mail.folder.junk")
	emptySpamAct := i18n.T("mail.empty_spam")

	// Right click on Junk in the sidebar
	require.NoError(t, h.tt.RightClick(junkName))
	menu := h.tt.Menu()
	require.True(t, slices.Contains(menu, emptySpamAct), "context menu should have Empty Spam")

	// Choose Empty Spam
	require.NoError(t, h.tt.ChooseMenuItem(emptySpamAct))

	// Dialog should appear
	h.until("dialog shows", func() bool { return h.app.mail.emptyFolderAsk })
	assert.True(t, h.app.mail.emptyFolderAsk)

	// Confirm dialog
	h.click(i18n.T("mail.empty_spam"))
	h.waitMail("dialog closes and junk empty", func() bool {
		h.syncNow(accs[0])
		return !h.app.mail.emptyFolderAsk && h.app.mail.roleFolder(accs[0].ID, "junk").Total == 0
	})
	assert.Equal(t, 0, h.app.mail.roleFolder(accs[0].ID, "junk").Total)
}

func TestSidebarContextMenuMarkAllRead(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	base := h.clock.Now()
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "mail 1", "body", at(base, 1)))
	h.put(accs[0], "INBOX", coretest.Msg("charlie@example.com", "alice@example.com", "mail 2", "body", at(base, 2)))
	h.syncNow(accs[0])
	h.waitMail("two unread mails", func() bool { return len(h.items()) == 2 && h.app.mail.inboxUnread() == 2 })

	// First navigate to Starred so that "全部收件箱" is not in the title bar
	starredName := i18n.T("mail.unified.starred")
	h.click(starredName)
	h.until("in starred view", func() bool { return h.app.route.View.Unified == "starred" })

	inboxName := i18n.T("mail.unified.inbox")
	markReadAct := i18n.T("mail.mark_all_read")

	// Right click on Unified Inbox in sidebar
	require.NoError(t, h.tt.RightClick(inboxName))
	menu := h.tt.Menu()
	require.True(t, slices.Contains(menu, markReadAct), "context menu should have Mark all as read")

	// Choose Mark all as read
	require.NoError(t, h.tt.ChooseMenuItem(markReadAct))

	// All messages should now be marked as read
	h.until("all marked as read", func() bool {
		return h.app.mail.inboxUnread() == 0
	})
	assert.Equal(t, 0, h.app.mail.inboxUnread())
}

func TestSidebarContextMenuAccountItems(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.waitMail("loaded", func() bool { return len(h.items()) == 0 && h.app.mail.inboxUnread() == 0 })
	name := accountName(accs[0])

	h.until("trash in the sidebar", func() bool { _, ok := h.tt.Find(i18n.T("mail.folder.trash")); return ok }) // the folder list loads after the inbox
	require.NoError(t, h.tt.RightClick(i18n.T("mail.folder.trash")))
	menu := h.tt.Menu()
	require.Contains(t, menu, i18n.Tf("mail.account_sync", name))
	require.Contains(t, menu, i18n.Tf("mail.account_edit", name))

	require.NoError(t, h.tt.ChooseMenuItem(i18n.Tf("mail.account_edit", name)))
	h.until("account settings open", func() bool {
		return h.app.route.Page == PageSettings && h.app.route.Settings == accountRoute(accs[0].ID)
	})
}

func TestSidebarContextMenuDisabledStates(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	h.waitMail("loaded", func() bool { return len(h.items()) == 0 && h.app.mail.inboxUnread() == 0 })

	// Right click on Unified Inbox in sidebar: Mark all as read should be disabled
	// First navigate to Starred so title bar doesn't shadow Unified Inbox
	h.click(i18n.T("mail.unified.starred"))
	h.until("in starred view", func() bool { return h.app.route.View.Unified == "starred" })

	inboxName := i18n.T("mail.unified.inbox")
	markReadAct := i18n.T("mail.mark_all_read")
	require.NoError(t, h.tt.RightClick(inboxName))
	// Attempting to choose disabled item returns an error
	err := h.tt.ChooseMenuItem(markReadAct)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")

	// Right click on Trash: Empty Trash should be disabled (Total == 0)
	trashName := i18n.T("mail.folder.trash")
	emptyTrashAct := i18n.T("mail.empty_trash")
	require.NoError(t, h.tt.RightClick(trashName))
	err = h.tt.ChooseMenuItem(emptyTrashAct)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")

	// Right click on Sent: only the account's own entries
	h.tt.CloseMenu()
	sentName := i18n.T("mail.folder.sent")
	require.NoError(t, h.tt.RightClick(sentName))
	name := accountName(accs[0])
	assert.Equal(t, []string{i18n.Tf("mail.account_sync", name), i18n.Tf("mail.account_edit", name)}, h.tt.Menu())
}

func TestPaletteCommandsFolderActions(t *testing.T) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	base := h.clock.Now()
	h.put(accs[0], "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "mail 1", "body", at(base, 1)))
	h.syncNow(accs[0])
	h.waitMail("inbox mail", func() bool { return len(h.items()) == 1 && h.app.mail.inboxUnread() == 1 })

	// Open palette
	h.tt.Key(ui.Cmd, ui.KeyK)
	h.until("the palette", func() bool { return h.app.palette.open })

	// Mark all as read command should be in actions
	acts := h.app.palette.actions()
	hasMarkRead := slices.ContainsFunc(acts, func(it palItem) bool {
		return it.title == i18n.T("palette.action.mark_all_read")
	})
	assert.True(t, hasMarkRead, "palette actions should contain Mark all as read")

	hasEmptyTrash := slices.ContainsFunc(acts, func(it palItem) bool {
		return it.title == i18n.T("palette.action.empty_trash")
	})
	assert.True(t, hasEmptyTrash, "palette actions should contain Empty Trash")

	hasEmptySpam := slices.ContainsFunc(acts, func(it palItem) bool {
		return it.title == i18n.T("palette.action.empty_spam")
	})
	assert.True(t, hasEmptySpam, "palette actions should contain Empty Spam")

	// Execute mark all read via palette item
	for _, it := range acts {
		if it.title == i18n.T("palette.action.mark_all_read") {
			it.open()
			break
		}
	}

	h.until("all marked read via palette", func() bool {
		return h.app.mail.inboxUnread() == 0
	})
	assert.Equal(t, 0, h.app.mail.inboxUnread())
}
