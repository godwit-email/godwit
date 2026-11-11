package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// On a server without UIDPLUS, Empty Trash is greyed out and says why.
func TestEmptyTrashUnsupported(t *testing.T) {
	h, accs := mailHarness(t, []coretest.Option{coretest.WithoutUIDPlus()}, "alice@example.com")
	h.put(accs[0], "Trash", coretest.Msg("bob@example.com", "alice@example.com", "old", "body", at(h.clock.Now(), 1)))
	h.syncNow(accs[0])
	h.waitMail("trash has mail and the account its features", func() bool {
		i := slices.IndexFunc(h.app.accounts, func(a core.Account) bool { return a.ID == accs[0].ID })
		return h.app.mail.roleFolder(accs[0].ID, "trash").Total == 1 && i >= 0 && h.app.accounts[i].Features != nil
	})
	require.NoError(t, h.tt.RightClick(i18n.T("mail.folder.trash")))
	err := h.tt.ChooseMenuItem(i18n.T("mail.empty_trash"))
	require.ErrorContains(t, err, "disabled")
	assert.Equal(t, i18n.T("feature.reason.imap.no_uidplus"), h.app.unsupported(accs[0].ID, backend.FeaturePurge))
}

// Every reason key a backend declares (backend.Support.Reason, "<protocol>.<why>")
// has text in the catalog, which the UI and Wit show for it.
func TestFeatureReasonsHaveText(t *testing.T) {
	reason := regexp.MustCompile(`"((?:imap|jmap|ews|graph)\.[a-z_]+)"`)
	var found []string
	err := filepath.WalkDir("../backend", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		src, err := os.ReadFile(p)
		for _, m := range reason.FindAllStringSubmatch(string(src), -1) {
			found = append(found, m[1])
		}
		return err
	})
	require.NoError(t, err)
	assert.Subset(t, found, []string{"imap.no_uidplus", "imap.no_move", "jmap.no_copy", "jmap.folders_pending", "jmap.no_submission"})
	keys := i18n.Keys()
	for _, r := range found {
		assert.True(t, slices.Contains(keys, "feature_reason_"+strings.ReplaceAll(r, ".", "_")), "no text for reason %s", r)
	}
}
