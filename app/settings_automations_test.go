package app

import (
	"context"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettingsAutomationsPage(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings("automations")
	require.NotNil(t, p)

	// Wait until automations are loaded
	h.until("automations page loaded", func() bool {
		h.tt.Frame()
		return p.automations.loaded && len(p.automations.workflows) >= 3
	})

	// Verify all 3 native presets exist (newsletter_unsub was removed)
	wfs, err := h.env.Svc.ListWorkflows(context.Background(), acc.ID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(wfs), 3)

	var foundTrash, foundJunk, foundSec bool
	for _, w := range wfs {
		if w.Kind == core.WorkflowKindNative {
			switch w.PresetID {
			case core.PresetPurgeTrash:
				foundTrash = true
			case core.PresetPurgeJunk:
				foundJunk = true
			case core.PresetSecurityJunk:
				foundSec = true
			}
		}
	}
	assert.True(t, foundTrash, "expected purge_trash preset")
	assert.True(t, foundJunk, "expected purge_junk preset")
	assert.True(t, foundSec, "expected security_junk preset")

	// Verify native workflow cannot be deleted
	trashWF := wfs[0]
	err = h.env.Svc.DeleteWorkflow(context.Background(), trashWF.ID)
	assert.Error(t, err, "native workflow should not be deletable")

	// Verify updating purge days via view method
	p.automations.updatePurgeDays(trashWF, 7)
	h.settle()

	wfsAfter, err := h.env.Svc.ListWorkflows(context.Background(), acc.ID)
	require.NoError(t, err)
	for _, w := range wfsAfter {
		if w.PresetID == core.PresetPurgeTrash {
			assert.Equal(t, 7, w.Config.Days)
			assert.True(t, w.Enabled)
		}
	}

	// Verify purges synced
	purges, err := h.env.Svc.Purges(context.Background(), acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 7, purges["trash"].Days)

	// Create a custom workflow via tree builder save
	p.automations.openBuilder(nil)
	assert.True(t, p.automations.builderOpen)
	assert.Equal(t, int64(0), p.automations.editID)

	p.automations.name = "Werbung archivieren"
	p.automations.trigger = core.WorkflowTriggerDaily
	p.automations.activeActions = []core.WorkflowActionItem{{Type: core.WorkflowActionArchive}}
	p.automations.activeConds = []string{CondFolder, CondDays, CondLabel}
	p.automations.folderRole = "inbox"
	p.automations.olderThanDays = 14
	p.automations.labelName = "Werbung"
	p.automations.save()
	assert.False(t, p.automations.builderOpen)

	h.settle()

	// Verify custom workflow was created in database
	wfsCustom, err := h.env.Svc.ListWorkflows(context.Background(), acc.ID)
	require.NoError(t, err)
	var customWF *core.Workflow
	for i := range wfsCustom {
		if wfsCustom[i].Kind == core.WorkflowKindCustom && wfsCustom[i].Name == "Werbung archivieren" {
			customWF = &wfsCustom[i]
			break
		}
	}
	require.NotNil(t, customWF, "custom workflow should be created")
	assert.Equal(t, core.WorkflowActionArchive, customWF.Action)
	assert.Equal(t, "Werbung", customWF.Config.LabelName)
	assert.Equal(t, 14, customWF.Config.Days)

	// Edit custom workflow in builder
	p.automations.openBuilder(customWF)
	assert.Equal(t, customWF.ID, p.automations.editID)
	assert.Equal(t, customWF.Config.FolderRole, p.automations.folderRole)
	assert.Contains(t, p.automations.activeConds, CondDays)
	assert.Contains(t, p.automations.activeConds, CondLabel)

	p.automations.name = "Werbung sofort löschen"
	p.automations.activeActions = []core.WorkflowActionItem{{Type: core.WorkflowActionDelete}}
	p.automations.save()

	h.settle()

	updated, err := h.env.Svc.GetWorkflow(context.Background(), customWF.ID)
	require.NoError(t, err)
	assert.Equal(t, "Werbung sofort löschen", updated.Name)
	assert.Equal(t, core.WorkflowActionDelete, updated.Action)

	// Delete custom workflow
	p.automations.deleteWorkflow(customWF.ID)
	h.settle()

	_, err = h.env.Svc.GetWorkflow(context.Background(), customWF.ID)
	assert.Error(t, err, "custom workflow should be deleted")
}

func TestSettingsAutomationsValidation(t *testing.T) {
	h, _ := withAccount(t)
	p := h.openSettings("automations")
	require.NotNil(t, p)

	v := p.automations

	// 1. Initial state must be completely clean (Anhang 2)
	v.openBuilder(nil)
	assert.True(t, v.builderOpen)
	assert.Empty(t, v.trigger, "trigger must be empty on fresh builder")
	assert.Empty(t, v.activeConds, "conditions must be empty on fresh builder")
	assert.Empty(t, v.activeActions, "actions must be empty on fresh builder")

	valid, msg := v.validate()
	assert.False(t, valid)
	assert.NotEmpty(t, msg)

	// 2. Dangerous Anhang 4 scenario: Daily + Inbox + Purge MUST be rejected!
	v.name = "Gefährlicher Workflow"
	v.trigger = core.WorkflowTriggerDaily
	v.activeConds = []string{CondFolder}
	v.folderRole = "inbox"
	v.activeActions = []core.WorkflowActionItem{{Type: core.WorkflowActionPurge}}
	valid, msg = v.validate()
	assert.False(t, valid, "Daily + Inbox + Purge must be rejected")
	assert.Equal(t, i18n.T("settings.automations.err_purge_folder"), msg)

	// 3. Purge in trash without age condition must also be rejected
	v.folderRole = "trash"
	v.olderThanDays = 0
	valid, msg = v.validate()
	assert.False(t, valid, "Purge without age condition must be rejected")

	// 4. Purge with valid trash + olderThanDays is allowed
	v.activeConds = []string{CondFolder, CondDays}
	v.olderThanDays = 30
	valid, msg = v.validate()
	assert.True(t, valid, "Purge in trash older than 30 days must be valid")

	// 5. Trigger receive without any conditions is valid (rule applies to entire scope)
	v.trigger = core.WorkflowTriggerReceive
	v.activeConds = nil
	v.activeActions = []core.WorkflowActionItem{{Type: core.WorkflowActionArchive}}
	valid, msg = v.validate()
	assert.True(t, valid, "Receive trigger with no filter conditions is valid (applies to entire scope)")
	assert.Empty(t, msg)

	// 6. Label trigger without selected label must be rejected
	v.trigger = core.WorkflowTriggerLabel
	v.triggerLabelID = 0
	v.triggerLabelName = ""
	valid, msg = v.validate()
	assert.False(t, valid, "Label trigger without selected label must be rejected")
	v.triggerLabelID = 5
	valid, msg = v.validate()
	assert.True(t, valid, "Label trigger with selected trigger label must be valid")

	// 7. Verify singular "Älter als 1 Tag" option exists (Anhang 3)
	dayOpts := olderThanOptions()
	require.NotEmpty(t, dayOpts)
	assert.Equal(t, i18n.T("settings.automations.older_than_one"), dayOpts[0].Label)

	// 8. Filters available across all 3 triggers
	for _, trig := range []string{core.WorkflowTriggerDaily, core.WorkflowTriggerReceive, core.WorkflowTriggerLabel} {
		allowed := v.allowedConditionsForTrigger(trig)
		assert.Contains(t, allowed, CondFrom, "From should be allowed for trigger %s", trig)
		assert.Contains(t, allowed, CondSubject, "Subject should be allowed for trigger %s", trig)
		assert.Contains(t, allowed, CondLabel, "Label should be allowed for trigger %s", trig)
		assert.Contains(t, allowed, CondHasAtt, "Attachments should be allowed for trigger %s", trig)
		assert.Contains(t, allowed, CondThreat, "Threat should be allowed for trigger %s", trig)
		assert.Contains(t, allowed, CondDomainAuth, "DomainAuth should be allowed for trigger %s", trig)
	}

	// 9. Subject options contains contains, equals, starts_with
	sOpts := subjectOpOptions()
	require.Len(t, sOpts, 3)
	assert.Equal(t, "contains", sOpts[0].Value)
	assert.Equal(t, "equals", sOpts[1].Value)
	assert.Equal(t, "starts_with", sOpts[2].Value)

	// 10. Threat and Domain Auth options are separate
	thOpts := threatOptions()
	require.Len(t, thOpts, 3)
	assert.Equal(t, "warning", thOpts[0].Value)
	assert.Equal(t, "suspicious", thOpts[1].Value)
	assert.Equal(t, "clean", thOpts[2].Value)

	daOpts := domainAuthOptions()
	require.Len(t, daOpts, 3)
	assert.Equal(t, "verified", daOpts[0].Value)
	assert.Equal(t, "failed", daOpts[1].Value)
	assert.Equal(t, "unverified", daOpts[2].Value)
}

func TestSettingsAutomationsNewFiltersSave(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings("automations")
	require.NotNil(t, p)

	v := p.automations
	v.openBuilder(nil)
	v.name = "Advanced Security Rule"
	v.trigger = core.WorkflowTriggerReceive
	v.condLogic = "or"
	v.activeConds = []string{CondThreat, CondDomainAuth, CondFrom, CondSubject, CondHasAtt}
	v.threatAssessment = "clean"
	v.domainAuth = "verified"
	v.fromPattern = "*@trusted.org"
	v.subjectPattern = "Urgent Report"
	v.subjectOp = "starts_with"
	v.hasAttachments = 1
	v.activeActions = []core.WorkflowActionItem{{Type: core.WorkflowActionStar}}

	valid, msg := v.validate()
	require.True(t, valid, "should be valid: %s", msg)

	// Save the rule
	v.save()
	h.settle()

	wfs, err := h.env.Svc.ListWorkflows(context.Background(), acc.ID)
	require.NoError(t, err)

	var saved *core.Workflow
	for i := range wfs {
		if wfs[i].Name == "Advanced Security Rule" {
			saved = &wfs[i]
			break
		}
	}
	require.NotNil(t, saved)
	assert.Equal(t, "clean", saved.Config.EffectiveThreatAssessment())
	assert.Equal(t, "verified", saved.Config.DomainAuth)
	assert.Equal(t, "*@trusted.org", saved.Config.FromPattern)
	assert.Equal(t, "Urgent Report", saved.Config.SubjectPattern)
	assert.Equal(t, "starts_with", saved.Config.SubjectOp)
	require.NotNil(t, saved.Config.HasAttachments)
	assert.True(t, *saved.Config.HasAttachments)
	assert.Equal(t, "or", saved.Config.CondLogic)

	// Test filterSummary formatting
	summary := v.filterSummary(*saved)
	assert.Contains(t, summary, "Bedrohung:")
	assert.Contains(t, summary, "Domain:")
	assert.Contains(t, summary, "Absender: *@trusted.org")
	assert.Contains(t, summary, "Betreff ^=")

	// Test reopening in builder
	v.openBuilder(saved)
	assert.Equal(t, saved.ID, v.editID)
	assert.Equal(t, "clean", v.threatAssessment)
	assert.Equal(t, "verified", v.domainAuth)
	assert.Equal(t, "*@trusted.org", v.fromPattern)
	assert.Equal(t, "Urgent Report", v.subjectPattern)
	assert.Equal(t, "starts_with", v.subjectOp)
	assert.Equal(t, 1, v.hasAttachments)
	assert.Contains(t, v.activeConds, CondThreat)
	assert.Contains(t, v.activeConds, CondDomainAuth)
	assert.Contains(t, v.activeConds, CondFrom)
	assert.Contains(t, v.activeConds, CondSubject)
	assert.Contains(t, v.activeConds, CondHasAtt)
}

func TestSettingsAutomationsActionExclusionsAndOrder(t *testing.T) {
	h, _ := withAccount(t)
	p := h.openSettings("automations")
	require.NotNil(t, p)

	v := p.automations
	v.openBuilder(nil)
	v.name = "Action Rules Test"
	v.trigger = core.WorkflowTriggerReceive
	v.labels = []core.Label{
		{ID: 101, Name: "Tag1"},
		{ID: 102, Name: "Tag2"},
	}

	// 1. "Gelesen" und "Ungelesen" ausgeschlossen
	v.activeActions = []core.WorkflowActionItem{
		{Type: core.WorkflowActionMarkRead},
		{Type: core.WorkflowActionMarkUnread},
	}
	valid, msg := v.validate()
	assert.False(t, valid)
	assert.Contains(t, msg, "„Gelesen“ und „Ungelesen“")

	// 2. Mehrere unterschiedliche Zielaktionen ausgeschlossen
	v.activeActions = []core.WorkflowActionItem{
		{Type: core.WorkflowActionArchive},
		{Type: core.WorkflowActionTrash},
	}
	valid, msg = v.validate()
	assert.False(t, valid)
	assert.Contains(t, msg, "Zielaktion")

	// 3. Dasselbe Label gleichzeitig hinzufügen und entfernen ausgeschlossen
	v.activeActions = []core.WorkflowActionItem{
		{Type: core.WorkflowActionApplyLabel, LabelID: 101, LabelName: "Tag1"},
		{Type: core.WorkflowActionRemoveLabel, LabelID: 101, LabelName: "Tag1"},
	}
	valid, msg = v.validate()
	assert.False(t, valid)
	assert.Contains(t, msg, "dasselbe Label")

	// 4. Identische Aktionen mit identischen Parametern ausgeschlossen (Duplikatschutz)
	v.activeActions = []core.WorkflowActionItem{
		{Type: core.WorkflowActionStar},
		{Type: core.WorkflowActionStar},
	}
	valid, msg = v.validate()
	assert.False(t, valid)
	assert.Contains(t, msg, "doppelte Aktion")

	// 5. Mehrere unterschiedliche Labels erlaubt
	v.activeActions = []core.WorkflowActionItem{
		{Type: core.WorkflowActionApplyLabel, LabelID: 101, LabelName: "Tag1"},
		{Type: core.WorkflowActionApplyLabel, LabelID: 102, LabelName: "Tag2"},
		{Type: core.WorkflowActionMarkRead},
		{Type: core.WorkflowActionArchive},
	}
	valid, msg = v.validate()
	assert.True(t, valid, "multiple different labels must be allowed: %s", msg)

	// 6. Purge ist eigenständig und nicht mit anderen Aktionen kombinierbar
	v.activeActions = []core.WorkflowActionItem{
		{Type: core.WorkflowActionPurge},
		{Type: core.WorkflowActionMarkRead},
	}
	valid, msg = v.validate()
	assert.False(t, valid)
	assert.Contains(t, msg, "purge")

	// 7. availableActionOptions Dynamik:
	// Wenn MarkRead gewählt ist: weder MarkRead noch MarkUnread dürfen angeboten werden
	v.activeActions = []core.WorkflowActionItem{{Type: core.WorkflowActionMarkRead}}
	opts := v.availableActionOptions()
	for _, o := range opts {
		assert.NotEqual(t, core.WorkflowActionMarkRead, o.Value)
		assert.NotEqual(t, core.WorkflowActionMarkUnread, o.Value)
	}

	// Wenn Archive gewählt ist: keine weitere Zielaktion darf angeboten werden
	v.activeActions = []core.WorkflowActionItem{{Type: core.WorkflowActionArchive}}
	opts = v.availableActionOptions()
	for _, o := range opts {
		assert.NotEqual(t, core.WorkflowActionArchive, o.Value)
		assert.NotEqual(t, core.WorkflowActionTrash, o.Value)
		assert.NotEqual(t, core.WorkflowActionJunk, o.Value)
		assert.NotEqual(t, core.WorkflowActionPurge, o.Value)
	}

	// Wenn Purge gewählt ist: keine weiteren Aktionen dürfen angeboten werden
	v.activeActions = []core.WorkflowActionItem{{Type: core.WorkflowActionPurge}}
	opts = v.availableActionOptions()
	// Nur Platzhalter (Leere Option)
	assert.Len(t, opts, 1)

	// 8. addAction sortiert sofort in kanonische Reihenfolge:
	// Externe Aktion -> Status -> Labels -> Zielaktion
	v.activeActions = nil
	v.addAction(core.WorkflowActionArchive)
	v.addAction(core.WorkflowActionApplyLabel)
	v.addAction(core.WorkflowActionMarkRead)
	v.addAction(core.WorkflowActionUnsubscribe)

	require.Len(t, v.activeActions, 4)
	assert.Equal(t, core.WorkflowActionUnsubscribe, v.activeActions[0].Type)
	assert.Equal(t, core.WorkflowActionMarkRead, v.activeActions[1].Type)
	assert.Equal(t, core.WorkflowActionApplyLabel, v.activeActions[2].Type)
	assert.Equal(t, core.WorkflowActionArchive, v.activeActions[3].Type)

	// 9. actionsSummary rendert mit " → "
	summary := v.actionsSummary(core.Workflow{Config: core.WorkflowConfig{Actions: v.activeActions}})
	assert.Contains(t, summary, " → ")

	// 10. Übersetzungstest: unsubscribe und neue Aktionen müssen lokalisiert sein (nicht raw ID)
	assert.Equal(t, i18n.T("settings.automations.action.unsubscribe"), actionLabel(core.WorkflowActionUnsubscribe))
	assert.NotEqual(t, "unsubscribe", actionLabel(core.WorkflowActionUnsubscribe))
	assert.Equal(t, i18n.T("settings.automations.action.unstar"), actionLabel(core.WorkflowActionUnstar))
	assert.Equal(t, i18n.T("settings.automations.action.forward"), actionLabel(core.WorkflowActionForward))
	assert.Equal(t, i18n.T("settings.automations.action.export_eml"), actionLabel(core.WorkflowActionExportEML))
	assert.Equal(t, i18n.T("settings.automations.action.export_pdf"), actionLabel(core.WorkflowActionExportPDF))
	assert.Equal(t, i18n.T("settings.automations.action.export_attachments"), actionLabel(core.WorkflowActionExportAttachments))
	assert.Equal(t, i18n.T("settings.automations.action.wit_draft"), actionLabel(core.WorkflowActionWitDraft))
	assert.Equal(t, i18n.T("settings.automations.action.wit_extract"), actionLabel(core.WorkflowActionWitExtract))

	// 11. Ausschluss von Star und Unstar
	v.activeActions = []core.WorkflowActionItem{
		{Type: core.WorkflowActionStar},
		{Type: core.WorkflowActionUnstar},
	}
	valid, msg = v.validate()
	assert.False(t, valid)
	assert.NotEmpty(t, msg)

	// 12. Alle Aktions- und Condition-Icons müssen in internal/icons existieren (kein Panic!)
	allActionTypes := []string{
		core.WorkflowActionArchive,
		core.WorkflowActionJunk,
		core.WorkflowActionTrash,
		core.WorkflowActionDelete,
		core.WorkflowActionPurge,
		core.WorkflowActionMarkRead,
		core.WorkflowActionMarkUnread,
		core.WorkflowActionStar,
		core.WorkflowActionUnstar,
		core.WorkflowActionApplyLabel,
		core.WorkflowActionRemoveLabel,
		core.WorkflowActionUnsubscribe,
		core.WorkflowActionForward,
		core.WorkflowActionExportEML,
		core.WorkflowActionExportPDF,
		core.WorkflowActionExportAttachments,
		core.WorkflowActionWitDraft,
		core.WorkflowActionWitExtract,
	}
	for _, actType := range allActionTypes {
		iconName := actionIconName(actType)
		assert.NotEmpty(t, iconName)
		assert.NotPanics(t, func() {
			_ = icons.Get(iconName)
		}, "icon %q for action %q must exist", iconName, actType)
	}
}

func TestSettingsAutomationsSearchIndex(t *testing.T) {
	h, _ := withAccount(t)
	p := h.openSettings("automations")
	require.NotNil(t, p)

	m := p.matching(i18n.T("settings.automations"))
	assert.Contains(t, m.apps, "automations")
}

func TestSettingsAutomationsPickDirectory(t *testing.T) {
	h, _ := withAccount(t)
	p := h.openSettings("automations")
	require.NotNil(t, p)

	v := p.automations
	v.openBuilder(nil)
	assert.False(t, v.hasExportAction(), "expected false with no actions")

	v.activeActions = []core.WorkflowActionItem{{Type: core.WorkflowActionStar}}
	assert.False(t, v.hasExportAction(), "expected false for non-export actions")

	v.activeActions = []core.WorkflowActionItem{
		{Type: core.WorkflowActionStar},
		{Type: core.WorkflowActionExportPDF},
		{Type: core.WorkflowActionWitExtract},
	}
	assert.True(t, v.hasExportAction(), "expected true when export actions exist")

	h.plat.dir = "/tmp/my-godwit-export"
	v.pickDirectory(nil)

	h.until("directory chosen", func() bool {
		h.tt.Frame()
		return v.targetDirectory == "/tmp/my-godwit-export" && !v.pickingDir
	})
	assert.Equal(t, "/tmp/my-godwit-export", v.targetDirectory)
	assert.Equal(t, "/tmp/my-godwit-export", v.activeActions[1].TargetDirectory)
	assert.Equal(t, "/tmp/my-godwit-export", v.activeActions[2].TargetDirectory)
}

func TestShortenPath(t *testing.T) {
	assert.Equal(t, "", shortenPath("", 28))
	assert.Equal(t, "~/Downloads", shortenPath("~/Downloads", 28))
	assert.Equal(t, "~/.../packages/common", shortenPath("~/Desktop/AFFiNE-Gemeinsamer-PR-2026-10-19/Dateien/packages/common", 28))
	assert.Equal(t, "~/.../Invoices", shortenPath("~/Documents/VeryLongSubfolderName1234567890/Invoices", 28))
}

func TestSettingsAutomationsHistoryTab(t *testing.T) {
	h, acc := withAccount(t)
	p := h.openSettings("automations")
	require.NotNil(t, p)

	v := p.automations
	h.until("automations page loaded", func() bool {
		h.tt.Frame()
		return v.loaded
	})

	// Switch to History tab
	v.tabIndex = 1
	v.loadRuns()

	h.until("history loaded", func() bool {
		h.tt.Frame()
		return v.runsLoaded
	})

	// Empty runs initially
	assert.True(t, v.runsLoaded)
	assert.Equal(t, acc.ID, v.runsAccountID)

	// Action chip labels
	assert.Equal(t, "Weiterleitung (übersprungen)", actionChipLabel("forward_skipped:loop_prevention"))
	assert.Equal(t, "Weitergeleitet", actionChipLabel("forward:user@example.com"))
	assert.Equal(t, "Wit-Entwurf", actionChipLabel("wit_draft:123"))
	assert.Equal(t, "Wit-Extraktion", actionChipLabel("wit_extract:/path/summary.md"))
	assert.Equal(t, "PDF exportiert", actionChipLabel("export_pdf:/path/doc.pdf"))
	assert.Equal(t, "EML exportiert", actionChipLabel("export_eml:/path/doc.eml"))
}
