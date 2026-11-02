package core_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowsSeedingAndProtection(t *testing.T) {
	e := coretest.New(t)
	acc := e.AddAccount(t, "alice@example.com")
	bg := context.Background()

	// 1. ListWorkflows seeds native presets automatically
	wfs, err := e.Svc.ListWorkflows(bg, acc.ID)
	require.NoError(t, err)
	require.Len(t, wfs, 3)

	// Verify native presets
	assert.Equal(t, core.WorkflowKindNative, wfs[0].Kind)
	assert.Equal(t, core.PresetPurgeTrash, wfs[0].PresetID)
	assert.Equal(t, core.WorkflowActionPurge, wfs[0].Action)

	assert.Equal(t, core.WorkflowKindNative, wfs[1].Kind)
	assert.Equal(t, core.PresetPurgeJunk, wfs[1].PresetID)
	assert.Equal(t, core.WorkflowActionPurge, wfs[1].Action)

	assert.Equal(t, core.WorkflowKindNative, wfs[2].Kind)
	assert.Equal(t, core.PresetSecurityJunk, wfs[2].PresetID)
	assert.Equal(t, core.WorkflowTriggerSecurity, wfs[2].Trigger)
	assert.Equal(t, "warning", wfs[2].Config.EffectiveThreatAssessment())

	// 2. Native workflows cannot be deleted
	err = e.Svc.DeleteWorkflow(bg, wfs[0].ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete native")

	// 3. Updating native workflow synchronizes with purge policy
	wfs[0].Config.Days = 7
	wfs[0].Enabled = true
	updated, err := e.Svc.UpdateWorkflow(bg, wfs[0].ID, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      wfs[0].Name,
		Trigger:   wfs[0].Trigger,
		Action:    wfs[0].Action,
		Config:    wfs[0].Config,
		Enabled:   true,
	})
	require.NoError(t, err)
	assert.Equal(t, 7, updated.Config.Days)
	assert.True(t, updated.Enabled)

	// Verify purge_policies updated in backend
	purges, err := e.Svc.Purges(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 7, purges["trash"].Days)

	// Disabling native workflow disables purge policy
	require.NoError(t, e.Svc.SetWorkflowEnabled(bg, wfs[0].ID, false))
	purges, err = e.Svc.Purges(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, purges["trash"].Days)
}

func TestWorkflowsCustomCRUD(t *testing.T) {
	e := coretest.New(t)
	acc := e.AddAccount(t, "bob@example.com")
	bg := context.Background()

	// Create custom workflow
	created, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Auto-Archive Receipts",
		Trigger:   core.WorkflowTriggerDaily,
		Action:    core.WorkflowActionArchive,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			Days:       14,
		},
		Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "Auto-Archive Receipts", created.Name)
	assert.Equal(t, core.WorkflowKindCustom, created.Kind)
	assert.Equal(t, 14, created.Config.Days)

	// Update custom workflow
	created.Config.Days = 21
	updated, err := e.Svc.UpdateWorkflow(bg, created.ID, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Auto-Archive Old Receipts",
		Trigger:   core.WorkflowTriggerDaily,
		Action:    core.WorkflowActionArchive,
		Config:    created.Config,
		Enabled:   true,
	})
	require.NoError(t, err)
	assert.Equal(t, "Auto-Archive Old Receipts", updated.Name)
	assert.Equal(t, 21, updated.Config.Days)

	// Custom workflow can be deleted
	require.NoError(t, e.Svc.DeleteWorkflow(bg, created.ID))

	// Verify deletion
	_, err = e.Svc.GetWorkflow(bg, created.ID)
	require.Error(t, err)
}

func TestWorkflowEvaluation(t *testing.T) {
	e := coretest.New(t)
	acc := e.AddAccount(t, "charlie@example.com")
	bg := context.Background()

	// Seed custom rule: on_receive with subject "Invoice" -> archive
	_, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Archive Invoices",
		Trigger:   core.WorkflowTriggerReceive,
		Action:    core.WorkflowActionArchive,
		Config: core.WorkflowConfig{
			SubjectPattern: "Invoice",
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Deliver message and sync
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@store.com", "charlie@example.com", "Your Invoice #123", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	// Evaluate receive workflows
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))

	// Message should now be moved out of Inbox
	inboxMsgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	assert.Empty(t, inboxMsgs.Items)

	// Verify message in Archive folder
	folders, err := e.Svc.ListFolders(bg, acc.ID)
	require.NoError(t, err)
	var archiveFolder int64
	for _, f := range folders {
		if f.Role == "archive" {
			archiveFolder = f.ID
			break
		}
	}
	require.NotZero(t, archiveFolder)

	archiveMsgs, err := e.Svc.ListMessages(bg, core.View{FolderID: archiveFolder}, "", 10)
	require.NoError(t, err)
	assert.Len(t, archiveMsgs.Items, 1)

	// Evaluating again does nothing (loop protection)
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))
}

func TestWorkflowsCombinedActions(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()

	acc := e.AddAccount(t, "dan@example.com")

	// Create workflow with combined actions: mark_read + star + archive
	_, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Read and Star Invoices",
		Trigger:   core.WorkflowTriggerReceive,
		Action:    "mark_read+star+archive",
		Config: core.WorkflowConfig{
			SubjectPattern: "Rechnung",
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionMarkRead},
				{Type: core.WorkflowActionStar},
				{Type: core.WorkflowActionArchive},
			},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Deliver message and sync
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("shop@example.com", "dan@example.com", "Deine Rechnung", "Danke für deinen Einkauf!"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	// Evaluate
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))

	// Verify message has flags \Seen and \Flagged, and moved to archive
	m, err := e.Svc.GetMessage(bg, msgID, false)
	require.NoError(t, err)
	assert.Contains(t, m.Flags, `\Seen`, "message should be marked read")
	assert.Contains(t, m.Flags, `\Flagged`, "message should be starred")

	folders, err := e.Svc.ListFolders(bg, acc.ID)
	require.NoError(t, err)
	var archiveFolder int64
	for _, f := range folders {
		if f.Role == "archive" {
			archiveFolder = f.ID
			break
		}
	}
	require.NotZero(t, archiveFolder)
	assert.Equal(t, archiveFolder, m.FolderID, "message should be moved to archive")
}

func TestWorkflowsOrConditionLogic(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()

	acc := e.AddAccount(t, "elena@example.com")

	// Create workflow with OR condition logic: From contains "boss" OR Subject contains "Urgent"
	_, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Flag Important",
		Trigger:   core.WorkflowTriggerReceive,
		Action:    core.WorkflowActionStar,
		Config: core.WorkflowConfig{
			CondLogic:      "or",
			FromPattern:    "boss@company.com",
			SubjectPattern: "Urgent",
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionStar},
			},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Message 1: Not from boss, but has "Urgent" in subject
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("colleague@company.com", "elena@example.com", "Urgent request", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID1 := msgs.Items[0].ID

	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID1))

	m1, err := e.Svc.GetMessage(bg, msgID1, false)
	require.NoError(t, err)
	assert.Contains(t, m1.Flags, `\Flagged`, "should match OR condition on subject")
}

func TestWorkflowsScopeCannotBeOverriddenByOr(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()

	acc := e.AddAccount(t, "user@example.com")

	// Workflow has Scope = "trash", and Filter OR logic with FromPattern = "spam" OR SubjectPattern = "Deal"
	purgeInput := core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Clean Trash Deals",
		Trigger:   core.WorkflowTriggerDaily,
		Action:    core.WorkflowActionPurge,
		Config: core.WorkflowConfig{
			FolderRole:     "trash",
			Days:           7,
			CondLogic:      "or",
			FromPattern:    "spam@example.com",
			SubjectPattern: "Deal",
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionPurge},
			},
		},
		ConfirmPurge: true,
		Enabled:      true,
	}
	purgeInput.PurgeProof = core.AuthorizePurgeDefinition(purgeInput)
	_, err := e.Svc.CreateWorkflow(bg, purgeInput)
	require.NoError(t, err)

	// Deliver message with subject "Deal" to INBOX (not Trash)
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("shop@example.com", "user@example.com", "Hot Deal", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	inboxMsgID := msgs.Items[0].ID

	// Evaluate workflow on the inbox message
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerDaily, inboxMsgID))

	// Verify message was NOT purged or modified because its folder (INBOX) did not match the Scope (trash)
	m, err := e.Svc.GetMessage(bg, inboxMsgID, false)
	require.NoError(t, err)
	assert.Equal(t, "Hot Deal", m.Subject)
}

func TestWorkflowsOnLabelTriggerAndLoopPrevention(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()

	acc := e.AddAccount(t, "user@example.com")

	// Create two labels
	l1, err := e.Svc.CreateLabel(bg, core.LabelInput{Name: "TriggerLabel", Color: "red", Icon: "folder-3"})
	require.NoError(t, err)
	l2, err := e.Svc.CreateLabel(bg, core.LabelInput{Name: "FilterLabel", Color: "green", Icon: "folder-3"})
	require.NoError(t, err)
	lOther, err := e.Svc.CreateLabel(bg, core.LabelInput{Name: "OtherLabel", Color: "blue", Icon: "folder-3"})
	require.NoError(t, err)

	// Workflow 1: Triggered by TriggerLabel, filters with OR (boss or FilterLabel), action: star
	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Star on TriggerLabel",
		Trigger:   core.WorkflowTriggerLabel,
		Action:    core.WorkflowActionStar,
		Config: core.WorkflowConfig{
			TriggerLabelID: l1.ID,
			CondLogic:      "or",
			FromPattern:    "boss@company.com",
			LabelID:        l2.ID,
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionStar},
			},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Deliver message from boss
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("boss@company.com", "user@example.com", "Project Status", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	// 1. Assigning lOther should NOT trigger the workflow, even though From matches boss in OR filter!
	require.NoError(t, e.Svc.SetLabel(bg, []int64{msgID}, lOther.ID, true))
	m, err := e.Svc.GetMessage(bg, msgID, false)
	require.NoError(t, err)
	assert.NotContains(t, m.Flags, `\Flagged`, "should not star because TriggerLabel was not assigned")

	// 2. Assigning l1 (TriggerLabel) triggers the workflow and stars the message!
	require.NoError(t, e.Svc.SetLabel(bg, []int64{msgID}, l1.ID, true))
	m, err = e.Svc.GetMessage(bg, msgID, false)
	require.NoError(t, err)
	assert.Contains(t, m.Flags, `\Flagged`, "should star message when TriggerLabel is assigned")

	// 3. Re-assigning l1 (already active) does NOT re-trigger
	// Unflag manually first
	require.NoError(t, e.Svc.SetFlags(bg, []int64{msgID}, nil, []string{`\Flagged`}))
	require.NoError(t, e.Svc.SetLabel(bg, []int64{msgID}, l1.ID, true))
	m, err = e.Svc.GetMessage(bg, msgID, false)
	require.NoError(t, err)
	assert.NotContains(t, m.Flags, `\Flagged`, "re-assigning already active label must not re-trigger")
}

func TestWorkflowFiltersSenderWildcardAndExact(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "me@example.com")

	// Workflow 1: Wildcard domain *@partner.com -> Star
	_, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Star Partner Mails",
		Trigger:   core.WorkflowTriggerReceive,
		Action:    core.WorkflowActionStar,
		Config: core.WorkflowConfig{
			FromPattern: "*@partner.com",
			Actions:     []core.WorkflowActionItem{{Type: core.WorkflowActionStar}},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Msg 1: Spoofed Display Name "Partner VIP <attacker@evil.com>"
	// Should NOT match *@partner.com because we test email address, not display name!
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("Partner VIP <attacker@evil.com>", "me@example.com", "Urgent request", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	spoofedMsgID := msgs.Items[0].ID

	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, spoofedMsgID))
	m1, err := e.Svc.GetMessage(bg, spoofedMsgID, false)
	require.NoError(t, err)
	assert.NotContains(t, m1.Flags, `\Flagged`, "spoofed display name must not match pure email address filter")

	// Msg 2: Genuine sender "Real Guy <service@partner.com>"
	// Should match *@partner.com!
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("Real Guy <service@partner.com>", "me@example.com", "Project update", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err = e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 2)
	// msgs are sorted newest first, so genuine message is at index 0
	genuineMsgID := msgs.Items[0].ID
	if genuineMsgID == spoofedMsgID {
		genuineMsgID = msgs.Items[1].ID
	}

	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, genuineMsgID))
	m2, err := e.Svc.GetMessage(bg, genuineMsgID, false)
	require.NoError(t, err)
	assert.Contains(t, m2.Flags, `\Flagged`, "genuine email matching wildcard should be starred")
}

func TestWorkflowFiltersSubjectOperators(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// Workflow A: Subject equals "Rechnung" -> Star
	_, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Exact Rechnung",
		Trigger:   core.WorkflowTriggerReceive,
		Action:    core.WorkflowActionStar,
		Config: core.WorkflowConfig{
			SubjectPattern: "Rechnung",
			SubjectOp:      "equals",
			Actions:        []core.WorkflowActionItem{{Type: core.WorkflowActionStar}},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Workflow B: Subject starts_with "[ALERT]" -> Mark Read
	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Alerts Read",
		Trigger:   core.WorkflowTriggerReceive,
		Action:    core.WorkflowActionMarkRead,
		Config: core.WorkflowConfig{
			SubjectPattern: "[ALERT]",
			SubjectOp:      "starts_with",
			Actions:        []core.WorkflowActionItem{{Type: core.WorkflowActionMarkRead}},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Msg 1: "Rechnung 12345" -> starts with Rechnung but NOT equal. Should NOT be starred.
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@shop.com", "user@example.com", "Rechnung 12345", "body"))
	// Msg 2: "Rechnung" -> Exactly equals. Should be starred.
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@shop.com", "user@example.com", "Rechnung", "body"))
	// Msg 3: "[ALERT] Server down" -> Starts with [ALERT]. Should be marked read.
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("ops@server.com", "user@example.com", "[ALERT] Server down", "body"))
	// Msg 4: "Fwd: [ALERT]" -> Contains [ALERT] but does NOT start with it. Should NOT be marked read.
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("ops@server.com", "user@example.com", "Fwd: [ALERT]", "body"))

	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 4)

	for _, item := range msgs.Items {
		require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, item.ID))
		m, err := e.Svc.GetMessage(bg, item.ID, false)
		require.NoError(t, err)

		switch m.Subject {
		case "Rechnung 12345":
			assert.NotContains(t, m.Flags, `\Flagged`, "equals operator should not match 'Rechnung 12345'")
		case "Rechnung":
			assert.Contains(t, m.Flags, `\Flagged`, "equals operator should match exact 'Rechnung'")
		case "[ALERT] Server down":
			assert.Contains(t, m.Flags, `\Seen`, "starts_with operator should match '[ALERT] Server down'")
		case "Fwd: [ALERT]":
			assert.NotContains(t, m.Flags, `\Seen`, "starts_with operator should not match 'Fwd: [ALERT]'")
		}
	}
}

func TestWorkflowFiltersThreatAndDomainAuthSeparation(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// Workflow Clean: Threat clean -> Star
	_, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Star Clean",
		Trigger:   core.WorkflowTriggerReceive,
		Action:    core.WorkflowActionStar,
		Config: core.WorkflowConfig{
			ThreatAssessment: "clean",
			Actions:          []core.WorkflowActionItem{{Type: core.WorkflowActionStar}},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Workflow Domain: Verified Domain -> Mark Read
	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Read Verified Domain",
		Trigger:   core.WorkflowTriggerReceive,
		Action:    core.WorkflowActionMarkRead,
		Config: core.WorkflowConfig{
			DomainAuth: "verified",
			Actions:    []core.WorkflowActionItem{{Type: core.WorkflowActionMarkRead}},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Deliver message
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("sender@verified-brand.com", "user@example.com", "Security Test", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	// 1. Pending scan (security_scanned_at == 0).
	// Crucial Requirement: Fehlende oder noch ausstehende Prüfergebnisse dürfen nicht als „sicher“ behandelt werden!
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))
	m, err := e.Svc.GetMessage(bg, msgID, false)
	require.NoError(t, err)
	assert.NotContains(t, m.Flags, `\Flagged`, "pending scan must NEVER be treated as clean")

	// 2. Verified domain but malicious content (warning)!
	// Crucial Requirement: Eine bestätigte Domain bedeutet nicht automatisch, dass der Inhalt ungefährlich ist.
	// Update DB to simulate: dmarc passed, but AI scanner flagged warning.
	err = e.Svc.ExecForTest(fmt.Sprintf("UPDATE messages SET security_scanned_at = 1700000000, security_status = 'warning', security_reason = 'ai_fraud', auth_results = 'dmarc=pass' WHERE id = %d", msgID))
	require.NoError(t, err)

	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))
	m, err = e.Svc.GetMessage(bg, msgID, false)
	require.NoError(t, err)
	assert.Contains(t, m.Flags, `\Seen`, "verified domain should match DomainAuth = verified")
	assert.NotContains(t, m.Flags, `\Flagged`, "verified domain with warning content must NOT match ThreatAssessment = clean")
}

func TestWorkflowFiltersZeroFiltersScope(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// Workflow with 0 filters: applies to the whole selected folder (inbox)
	_, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Star All In Scope",
		Trigger:   core.WorkflowTriggerReceive,
		Action:    core.WorkflowActionStar,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			Actions:    []core.WorkflowActionItem{{Type: core.WorkflowActionStar}},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("anyone@anywhere.com", "user@example.com", "Any Subject", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))
	m, err := e.Svc.GetMessage(bg, msgID, false)
	require.NoError(t, err)
	assert.Contains(t, m.Flags, `\Flagged`, "workflow with 0 filters should apply to all messages in the scope")
}

func TestWorkflowActionValidationAndDeduplication(t *testing.T) {
	// 1. "Gelesen" und "Ungelesen" ausgeschlossen
	err := core.ValidateWorkflowActions([]core.WorkflowActionItem{
		{Type: core.WorkflowActionMarkRead},
		{Type: core.WorkflowActionMarkUnread},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "„Gelesen“ und „Ungelesen“")

	// 2. Mehrere unterschiedliche Zielaktionen ausgeschlossen
	err = core.ValidateWorkflowActions([]core.WorkflowActionItem{
		{Type: core.WorkflowActionArchive},
		{Type: core.WorkflowActionTrash},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mehrere unterschiedliche Zielaktionen")

	// 3. Dasselbe Label gleichzeitig hinzufügen und entfernen ausgeschlossen
	err = core.ValidateWorkflowActions([]core.WorkflowActionItem{
		{Type: core.WorkflowActionApplyLabel, LabelID: 10, LabelName: "Work"},
		{Type: core.WorkflowActionRemoveLabel, LabelID: 10, LabelName: "Work"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "dasselbe Label kann nicht gleichzeitig hinzugefügt und entfernt werden")

	// 4. Identische Aktionen mit identischen Parametern ausgeschlossen (Duplikatschutz)
	err = core.ValidateWorkflowActions([]core.WorkflowActionItem{
		{Type: core.WorkflowActionStar},
		{Type: core.WorkflowActionStar},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "doppelte Aktion")

	err = core.ValidateWorkflowActions([]core.WorkflowActionItem{
		{Type: core.WorkflowActionApplyLabel, LabelID: 5, LabelName: "Invoices"},
		{Type: core.WorkflowActionApplyLabel, LabelID: 5, LabelName: "Invoices"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "doppelte Aktion")

	// 5. Mehrere UNTERSCHIEDLICHE Labels dürfen hinzugefügt oder entfernt werden
	err = core.ValidateWorkflowActions([]core.WorkflowActionItem{
		{Type: core.WorkflowActionApplyLabel, LabelID: 5, LabelName: "Invoices"},
		{Type: core.WorkflowActionApplyLabel, LabelID: 6, LabelName: "2026"},
		{Type: core.WorkflowActionRemoveLabel, LabelID: 7, LabelName: "Pending"},
		{Type: core.WorkflowActionMarkRead},
		{Type: core.WorkflowActionArchive},
	})
	assert.NoError(t, err, "multiple different labels must be allowed")

	// 6. Purge ist eigenständig und nicht mit anderen Aktionen kombinierbar
	err = core.ValidateWorkflowActions([]core.WorkflowActionItem{
		{Type: core.WorkflowActionPurge},
		{Type: core.WorkflowActionMarkRead},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "purge")

	// Purge alleine ist zulässig
	err = core.ValidateWorkflowActions([]core.WorkflowActionItem{
		{Type: core.WorkflowActionPurge},
	})
	assert.NoError(t, err)
}

func TestWorkflowActionExecutionOrderAndUnsubscribe(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	l1, err := e.Svc.CreateLabel(bg, core.LabelInput{Name: "TagA", Color: "red", Icon: "folder-3"})
	require.NoError(t, err)
	l2, err := e.Svc.CreateLabel(bg, core.LabelInput{Name: "TagB", Color: "blue", Icon: "folder-3"})
	require.NoError(t, err)

	// Verify SortActions places:
	// Unsubscribe -> MarkRead -> Star -> ApplyLabel -> Archive
	unsorted := []core.WorkflowActionItem{
		{Type: core.WorkflowActionArchive},
		{Type: core.WorkflowActionApplyLabel, LabelID: l2.ID, LabelName: l2.Name},
		{Type: core.WorkflowActionMarkRead},
		{Type: core.WorkflowActionUnsubscribe},
		{Type: core.WorkflowActionStar},
		{Type: core.WorkflowActionApplyLabel, LabelID: l1.ID, LabelName: l1.Name},
	}
	sorted := core.SortActions(unsorted)
	require.Len(t, sorted, 6)
	assert.Equal(t, core.WorkflowActionUnsubscribe, sorted[0].Type)
	assert.Equal(t, core.WorkflowActionMarkRead, sorted[1].Type)
	assert.Equal(t, core.WorkflowActionStar, sorted[2].Type)
	assert.Equal(t, core.WorkflowActionApplyLabel, sorted[3].Type)
	assert.Equal(t, core.WorkflowActionApplyLabel, sorted[4].Type)
	assert.Equal(t, core.WorkflowActionArchive, sorted[5].Type)

	// Create workflow with this combination
	wf, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Comprehensive Action Workflow",
		Trigger:   core.WorkflowTriggerReceive,
		Action:    "archive+mark_read",
		Config: core.WorkflowConfig{
			Actions: unsorted,
		},
		Enabled: true,
	})
	require.NoError(t, err)
	// Stored actions must be automatically sorted
	assert.Equal(t, core.WorkflowActionUnsubscribe, wf.Config.Actions[0].Type)
	assert.Equal(t, core.WorkflowActionArchive, wf.Config.Actions[5].Type)

	// Deliver message
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("newsletter@company.com", "user@example.com", "Newsletter #1", "Hello"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	// Evaluate workflow
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))

	// Verify message:
	// 1. Status: Read (\Seen) and Starred (\Flagged)
	m, err := e.Svc.GetMessage(bg, msgID, false)
	require.NoError(t, err)
	assert.Contains(t, m.Flags, `\Seen`)
	assert.Contains(t, m.Flags, `\Flagged`)

	// 2. Labels: both TagA and TagB applied
	msgLabels, err := e.Svc.MessageLabels(bg, []int64{msgID})
	require.NoError(t, err)
	var appliedLabelIDs []int64
	for _, l := range msgLabels[msgID] {
		appliedLabelIDs = append(appliedLabelIDs, l.LabelID)
	}
	assert.Contains(t, appliedLabelIDs, l1.ID)
	assert.Contains(t, appliedLabelIDs, l2.ID)

	// 3. Destination: moved to archive
	folders, err := e.Svc.ListFolders(bg, acc.ID)
	require.NoError(t, err)
	var archiveFolder int64
	for _, f := range folders {
		if f.Role == "archive" {
			archiveFolder = f.ID
			break
		}
	}
	require.NotZero(t, archiveFolder)
	assert.Equal(t, archiveFolder, m.FolderID)

	// 4. ExecForTest executes without error
	err = e.Svc.ExecForTest("SELECT action FROM workflow_runs WHERE message_id = " + fmt.Sprintf("%d", msgID))
	require.NoError(t, err)
}

func TestMassModificationProtectionAndBatchLimit(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// 1. Daily rule on inbox without effective restriction must be blocked
	_, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Danger Wipeout",
		Trigger:   core.WorkflowTriggerDaily,
		Action:    core.WorkflowActionArchive,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			// No age, no content filter
		},
		Enabled: true,
	})
	require.Error(t, err)
	var vErr *core.ValidationError
	require.ErrorAs(t, err, &vErr)
	assert.Equal(t, "ERR_INBOX_MASS_MODIFICATION_BLOCKED", vErr.Code)

	// 2. Catch-all wildcards (*, *@*, *.*, %) do not count as effective restrictions
	for _, catchAll := range []string{"*", "*@*", "*.*", "%", " *@* "} {
		_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
			AccountID: acc.ID,
			Name:      "Wildcard Wipeout",
			Trigger:   core.WorkflowTriggerDaily,
			Action:    core.WorkflowActionTrash,
			Config: core.WorkflowConfig{
				FolderRole:  "inbox",
				FromPattern: catchAll,
			},
			Enabled: true,
		})
		require.Error(t, err, "wildcard pattern %q must not bypass mass modification protection", catchAll)
		require.ErrorAs(t, err, &vErr)
		assert.Equal(t, "ERR_INBOX_MASS_MODIFICATION_BLOCKED", vErr.Code)
	}

	// 3. Legitimate restriction (e.g. age filter 14 days) succeeds
	safeWf, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Safe Archive Old Inbox",
		Trigger:   core.WorkflowTriggerDaily,
		Action:    core.WorkflowActionArchive,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			Days:       14,
			BatchLimit: 2, // Low batch limit for testing
		},
		Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, safeWf.Config.BatchLimit)

	// Deliver 4 old messages
	for i := 1; i <= 4; i++ {
		e.Deliver(t, acc.ID, "INBOX", coretest.Msg(fmt.Sprintf("sender%d@domain.com", i), "user@example.com", fmt.Sprintf("Old Subject %d", i), "Body"))
	}
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	// Move message dates back 20 days so they match Days: 14
	err = e.Svc.ExecForTest("UPDATE messages SET date = unixepoch() - 20 * 86400 WHERE account_id = " + fmt.Sprintf("%d", acc.ID))
	require.NoError(t, err)

	// Run daily workflows: matches (4) > BatchLimit/SafetyLimit (2) -> must halt before any modification
	count, err := e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "safety limit of 2 must halt processing before any message is modified")

	// Verify workflow run logged status 'paused'
	err = e.Svc.ExecForTest("SELECT status, details FROM workflow_runs WHERE workflow_id = " + fmt.Sprintf("%d", safeWf.ID) + " AND status = 'paused'")
	require.NoError(t, err)
}

func TestPurgeResidenceTimeVsDate(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// Deliver message with date 2 years ago into Trash
	e.Deliver(t, acc.ID, "Trash", coretest.Msg("old@sender.com", "user@example.com", "Two Years Old Mail", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	// Find the message in Trash folder
	folders, err := e.Svc.ListFolders(bg, acc.ID)
	require.NoError(t, err)
	var trashFolderID int64
	for _, f := range folders {
		if f.Role == "trash" {
			trashFolderID = f.ID
			break
		}
	}
	require.NotZero(t, trashFolderID)

	msgs, err := e.Svc.ListMessages(bg, core.View{FolderID: trashFolderID}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	// Set message date to 2 years ago, but entered_at = 0 (unknown/unreliable folder entry timestamp)
	err = e.Svc.ExecForTest(fmt.Sprintf("UPDATE messages SET date = unixepoch() - 730 * 86400, entered_at = 0 WHERE id = %d", msgID))
	require.NoError(t, err)

	// Create purge custom workflow for Trash with 30 days retention
	purgeInput := core.WorkflowInput{
		AccountID:    acc.ID,
		Name:         "Purge Trash 30 Days",
		Trigger:      core.WorkflowTriggerDaily,
		Action:       core.WorkflowActionPurge,
		ConfirmPurge: true,
		Config: core.WorkflowConfig{
			FolderRole: "trash",
			Days:       30,
		},
		Enabled: true,
	}
	purgeInput.PurgeProof = core.AuthorizePurgeDefinition(purgeInput)
	purgeWf, err := e.Svc.CreateWorkflow(bg, purgeInput)
	require.NoError(t, err)

	// Run daily workflow: With entered_at = 0, message MUST NOT be purged based on date!
	purgedCount, err := e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, purgedCount, "message with entered_at <= 0 must never be purged")

	// Set entered_at to 2 days ago (residence time in folder is only 2 days)
	err = e.Svc.ExecForTest(fmt.Sprintf("UPDATE messages SET entered_at = unixepoch() - 2 * 86400 WHERE id = %d", msgID))
	require.NoError(t, err)

	purgedCount, err = e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, purgedCount, "message residing only 2 days in trash must not be purged despite 2 year old message date")

	// Now set entered_at to 35 days ago (folder residence time >= 30 days)
	err = e.Svc.ExecForTest(fmt.Sprintf("UPDATE messages SET entered_at = unixepoch() - 35 * 86400 WHERE id = %d", msgID))
	require.NoError(t, err)

	purgedCount, err = e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, purgedCount, "message residing 35 days in trash must be purged")

	// Check workflow_runs record
	err = e.Svc.ExecForTest("SELECT status, action, workflow_version FROM workflow_runs WHERE workflow_id = " + fmt.Sprintf("%d", purgeWf.ID))
	require.NoError(t, err)
}

func TestWorkflowPreviewAndSimulation(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// Deliver messages
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@saas.com", "user@example.com", "Invoice 101", "body"))
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@saas.com", "user@example.com", "Invoice 102", "body"))
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("other@corp.com", "user@example.com", "Meeting notes", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	// Preview an event-based workflow
	prev, err := e.Svc.PreviewWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Archive Invoices",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FromPattern: "billing@saas.com",
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionArchive},
			},
		},
	})
	require.NoError(t, err)
	assert.True(t, prev.Valid)
	assert.True(t, prev.IsEventTrigger)
	assert.Contains(t, prev.SimulationNote, "Simulation")
	assert.Equal(t, 2, prev.CandidateCount, "should find 2 billing messages in preview")
	require.Len(t, prev.Samples, 2)
	assert.Contains(t, prev.ConfirmationMessage, "archiviert")
	assert.False(t, prev.IsIrreversible)

	// Preview a purge workflow
	purgePrev, err := e.Svc.PreviewWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Purge Trash",
		Trigger:   core.WorkflowTriggerDaily,
		Config: core.WorkflowConfig{
			FolderRole: "trash",
			Days:       30,
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionPurge},
			},
		},
	})
	require.NoError(t, err)
	assert.True(t, purgePrev.IsIrreversible)
	assert.Contains(t, purgePrev.ConfirmationMessage, "unwiderruflich")
	assert.True(t, purgePrev.RequiresHumanApproval, "preview must flag human approval required")
	assert.NotEmpty(t, purgePrev.NormalizedConfigHash, "preview must compute normalized config hash")
	assert.Empty(t, purgePrev.PurgeProof, "preview must NOT issue human authorization proof")
}

func TestPurgeProofServerVerification(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// 1. Purge rule with confirm_purge: true alone is rejected!
	_, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID:    acc.ID,
		Name:         "Purge without proof",
		Trigger:      core.WorkflowTriggerDaily,
		ConfirmPurge: true,
		Config: core.WorkflowConfig{
			FolderRole: "trash",
			Days:       30,
			Actions:    []core.WorkflowActionItem{{Type: core.WorkflowActionPurge}},
		},
	})
	require.Error(t, err)
	valErr, ok := err.(*core.ValidationError)
	require.True(t, ok)
	assert.Equal(t, "ERR_PURGE_CONFIRMATION_REQUIRED", valErr.Code)

	// 2. Purge rule with invalid/fabricated proof is rejected!
	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID:    acc.ID,
		Name:         "Purge with fake proof",
		Trigger:      core.WorkflowTriggerDaily,
		ConfirmPurge: true,
		PurgeProof:   "fabricated-proof-token-12345",
		Config: core.WorkflowConfig{
			FolderRole: "trash",
			Days:       30,
			Actions:    []core.WorkflowActionItem{{Type: core.WorkflowActionPurge}},
		},
	})
	require.Error(t, err)
	valErr, ok = err.(*core.ValidationError)
	require.True(t, ok)
	assert.Equal(t, "ERR_PURGE_CONFIRMATION_PROOF_INVALID", valErr.Code)

	// 3. Issue authentic human authorization via AuthorizePurgeDefinition for 30 days
	baseInput := core.WorkflowInput{
		AccountID:    acc.ID,
		Name:         "Purge 30 Days",
		Trigger:      core.WorkflowTriggerDaily,
		ConfirmPurge: true,
		Config: core.WorkflowConfig{
			FolderRole: "trash",
			Days:       30,
			Actions:    []core.WorkflowActionItem{{Type: core.WorkflowActionPurge}},
		},
	}
	// 3. Unauthorized call without human user session context is rejected!
	_, err = e.Svc.AuthorizePurgeDefinition(bg, baseInput)
	require.Error(t, err)
	valErr, ok = err.(*core.ValidationError)
	require.True(t, ok)
	assert.Equal(t, "ERR_UNAUTHORIZED_PURGE_AUTHORIZATION", valErr.Code)

	// 4. Authenticated call with WithUserAuth succeeds!
	humanProof, err := e.Svc.AuthorizePurgeDefinition(core.WithUserAuth(bg), baseInput)
	require.NoError(t, err)
	require.NotEmpty(t, humanProof)

	// 4. Modifying retention days from 30 to 14 invalidates the authorization!
	alteredDaysInput := baseInput
	alteredDaysInput.Config.Days = 14
	alteredDaysInput.PurgeProof = humanProof
	_, err = e.Svc.CreateWorkflow(bg, alteredDaysInput)
	require.Error(t, err)
	valErr, ok = err.(*core.ValidationError)
	require.True(t, ok)
	assert.Equal(t, "ERR_PURGE_CONFIRMATION_PROOF_INVALID", valErr.Code)

	// 5. Modifying folder role from trash to junk invalidates the authorization!
	alteredFolderInput := baseInput
	alteredFolderInput.Config.FolderRole = "junk"
	alteredFolderInput.PurgeProof = humanProof
	_, err = e.Svc.CreateWorkflow(bg, alteredFolderInput)
	require.Error(t, err)
	valErr, ok = err.(*core.ValidationError)
	require.True(t, ok)
	assert.Equal(t, "ERR_PURGE_CONFIRMATION_PROOF_INVALID", valErr.Code)

	// 6. Adding a filter after authorization invalidates the authorization!
	alteredFilterInput := baseInput
	alteredFilterInput.Config.FromPattern = "spam@domain.com"
	alteredFilterInput.PurgeProof = humanProof
	_, err = e.Svc.CreateWorkflow(bg, alteredFilterInput)
	require.Error(t, err)
	valErr, ok = err.(*core.ValidationError)
	require.True(t, ok)
	assert.Equal(t, "ERR_PURGE_CONFIRMATION_PROOF_INVALID", valErr.Code)

	// 7. Creating with exact matching parameters and human proof succeeds!
	validInput := baseInput
	validInput.PurgeProof = humanProof
	wf, err := e.Svc.CreateWorkflow(bg, validInput)
	require.NoError(t, err)
	assert.Equal(t, "Purge 30 Days", wf.Name)
}

func TestSafetyLimitHaltsBeforeModification(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// Deliver 6 messages to INBOX
	for i := 1; i <= 6; i++ {
		e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bulk@example.com", "user@example.com", fmt.Sprintf("Promo %d", i), "content"))
	}
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	// Create daily workflow with SafetyLimit = 3
	wf, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Archive Promos with Safety Guardrail",
		Trigger:   core.WorkflowTriggerDaily,
		Config: core.WorkflowConfig{
			FolderRole:  "inbox",
			FromPattern: "bulk@example.com",
			SafetyLimit: 3, // Safety limit: only 3 matches allowed
			BatchSize:   2,
			Actions:     []core.WorkflowActionItem{{Type: core.WorkflowActionArchive}},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Run daily workflows: 6 matching > SafetyLimit (3)
	// Must halt BEFORE touching ANY message! 0 modifications!
	modified, err := e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, modified, "must not modify any message when safety limit is exceeded")

	// Verify all 6 messages are still in inbox
	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, msgs.Items, 6, "all 6 messages must remain untouched in inbox")

	// Verify persistent execution state in DB is paused_safety_limit and version incremented!
	reloaded, err := e.Svc.GetWorkflow(bg, wf.ID)
	require.NoError(t, err)
	assert.Equal(t, "paused_safety_limit", reloaded.ExecutionState, "workflow must persist paused_safety_limit state")
	assert.Equal(t, int64(2), reloaded.Version, "safety limit pause must increment workflow version")

	// Verify audit rows in workflow_runs: exactly 6 rows with status 'paused'
	runs, err := e.Svc.QueryForTest("SELECT status, details FROM workflow_runs WHERE workflow_id = ? AND status = 'paused'", wf.ID)
	require.NoError(t, err)
	require.Len(t, runs.Values, 6, "must have recorded exactly 6 audit rows for 6 matches")
	assert.Equal(t, "paused", store.Str(runs.Values[0][0]))
	assert.Contains(t, store.Str(runs.Values[0][1]), "safety_limit_exceeded")

	// A second run must respect the persistent paused state and NOT modify anything
	modified2, err := e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, modified2, "subsequent daily runs must remain paused until human unpause")

	// DECISIVE TEST: An editor that loaded Version 1 tries to save its stale state with ExpectedVersion = 1
	// The update MUST fail with ERR_VERSION_CONFLICT!
	_, err = e.Svc.UpdateWorkflow(bg, wf.ID, core.WorkflowInput{
		AccountID:       acc.ID,
		Name:            "Stale Editor Update",
		Trigger:         wf.Trigger,
		ExpectedVersion: 1,
		Config:          wf.Config,
		Enabled:         true,
	})
	require.Error(t, err, "stale editor update must fail with version conflict")
	valErr, ok := err.(*core.ValidationError)
	require.True(t, ok)
	assert.Equal(t, "ERR_VERSION_CONFLICT", valErr.Code)

	// An editor updating Version 2 without ResetSafetyPause MUST keep execution_state as paused_safety_limit!
	updated, err := e.Svc.UpdateWorkflow(bg, wf.ID, core.WorkflowInput{
		AccountID:       acc.ID,
		Name:            "Renamed without unpausing",
		Trigger:         wf.Trigger,
		ExpectedVersion: 2,
		Config:          wf.Config,
		Enabled:         true,
	})
	require.NoError(t, err)
	assert.Equal(t, "paused_safety_limit", updated.ExecutionState, "ordinary update cannot silently lift safety pause")
	assert.Equal(t, int64(3), updated.Version)

	// An ordinary update with ResetSafetyPause: true without human authorization MUST fail with ERR_UNAUTHORIZED_RESUME!
	_, err = e.Svc.UpdateWorkflow(bg, wf.ID, core.WorkflowInput{
		AccountID:        acc.ID,
		Name:             "Backdoor ResetSafetyPause Attempt",
		Trigger:          wf.Trigger,
		ExpectedVersion:  updated.Version,
		ResetSafetyPause: true,
		Config:           wf.Config,
		Enabled:          true,
	})
	require.Error(t, err)
	assert.Equal(t, "ERR_UNAUTHORIZED_RESUME", err.(*core.ValidationError).Code)

	// Verify pause remains preserved in DB
	stillPaused, err := e.Svc.GetWorkflow(bg, wf.ID)
	require.NoError(t, err)
	assert.Equal(t, "paused_safety_limit", stillPaused.ExecutionState)

	// Resuming requires human user authorization:
	_, err = e.Svc.ResumeWorkflow(bg, wf.ID, updated.Version)
	require.Error(t, err)
	assert.Equal(t, "ERR_UNAUTHORIZED_RESUME", err.(*core.ValidationError).Code)

	// With human user authorization, resume succeeds and resets execution_state to 'active':
	resumed, err := e.Svc.ResumeWorkflow(core.WithUserAuth(bg), wf.ID, updated.Version)
	require.NoError(t, err)
	assert.Equal(t, "active", resumed.ExecutionState)
	assert.Equal(t, int64(4), resumed.Version)
}

func TestPurgeProviderQueueAndTwoStageDeletion(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// Deliver message into Trash
	e.Deliver(t, acc.ID, "Trash", coretest.Msg("old@junk.com", "user@example.com", "Old Junk", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	folders, err := e.Svc.ListFolders(bg, acc.ID)
	require.NoError(t, err)
	var trashFolderID int64
	for _, f := range folders {
		if f.Role == "trash" {
			trashFolderID = f.ID
			break
		}
	}
	require.NotZero(t, trashFolderID)

	// Set entered_at to 35 days ago
	err = e.Svc.ExecForTest("UPDATE messages SET entered_at = unixepoch() - 35 * 86400 WHERE folder_id = " + fmt.Sprintf("%d", trashFolderID))
	require.NoError(t, err)

	purgeInput := core.WorkflowInput{
		AccountID:    acc.ID,
		Name:         "Purge 30 Days Trash",
		Trigger:      core.WorkflowTriggerDaily,
		ConfirmPurge: true,
		Config: core.WorkflowConfig{
			FolderID:   trashFolderID,
			FolderRole: "trash",
			Days:       30,
			Actions:    []core.WorkflowActionItem{{Type: core.WorkflowActionPurge}},
		},
		Enabled: true,
	}
	var inboxFolderID int64
	for _, f := range folders {
		if f.Role == "inbox" {
			inboxFolderID = f.ID
			break
		}
	}
	require.NotZero(t, inboxFolderID)

	proof, err := e.Svc.AuthorizePurgeDefinition(core.WithUserAuth(bg), purgeInput)
	require.NoError(t, err)
	purgeInput.PurgeProof = proof

	pwf, err := e.Svc.CreateWorkflow(bg, purgeInput)
	require.NoError(t, err)

	// Run 1: Run daily workflows queues permanent delete with provider references
	actions, err := e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, actions)

	// Verify workflow_runs logged status 'queued' (not false success before provider sync!)
	runRows, err := e.Svc.QueryForTest("SELECT status, details, op_id FROM workflow_runs WHERE workflow_id = ? AND status = 'queued'", pwf.ID)
	require.NoError(t, err)
	require.Len(t, runRows.Values, 1, "must have logged exactly 1 queued purge run")
	assert.Equal(t, "queued", store.Str(runRows.Values[0][0]))
	assert.Contains(t, store.Str(runRows.Values[0][1]), "queued_for_provider_purge")
	require.NotNil(t, runRows.Values[0][2], "op_id column must be populated")
	runOpID := store.Int(runRows.Values[0][2])

	// Verify persistent ops queue has purge operation with message_id and provider role
	opsRows, err := e.Svc.QueryForTest("SELECT id, kind, state, message_id FROM ops WHERE kind = 'purge' AND state = 'pending'")
	require.NoError(t, err)
	require.Len(t, opsRows.Values, 1, "ops must have exactly 1 pending purge operation queued")
	assert.Equal(t, "purge", store.Str(opsRows.Values[0][1]))
	assert.Equal(t, "pending", store.Str(opsRows.Values[0][2]))
	assert.Equal(t, store.Int(opsRows.Values[0][0]), runOpID, "workflow_runs.op_id must point directly to ops.id")
	msgID := store.Int(opsRows.Values[0][3])

	// 2. Second daily run must NOT create duplicate ops for the same pending purge order!
	actions2, err := e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, actions2, "second run must find 0 new unhandled messages")
	opsRowsAfter, err := e.Svc.QueryForTest("SELECT id FROM ops WHERE kind = 'purge' AND state = 'pending'")
	require.NoError(t, err)
	assert.Len(t, opsRowsAfter.Values, 1, "ops must NOT duplicate pending purge operation")

	// 3. User rescues message out of trash into Inbox before provider push
	require.NoError(t, e.Svc.Move(bg, []int64{msgID}, inboxFolderID))

	// Move out of trash immediately cancels pending purge in ops
	opsCancelled, err := e.Svc.QueryForTest("SELECT state FROM ops WHERE message_id = ? AND kind = 'purge'", msgID)
	require.NoError(t, err)
	require.NotEmpty(t, opsCancelled.Values)
	assert.Equal(t, "cancelled", store.Str(opsCancelled.Values[0][0]), "rescue move must cancel pending purge op")

	// 4. Reconcile updates workflow_runs status to cancelled (cancelled_by_rescue)
	require.NoError(t, e.Svc.ReconcilePurgeWorkflowRuns(bg, acc.ID))
	reconciledRun, err := e.Svc.QueryForTest("SELECT status, details FROM workflow_runs WHERE workflow_id = ?", pwf.ID)
	require.NoError(t, err)
	require.NotEmpty(t, reconciledRun.Values)
	assert.Equal(t, "cancelled", store.Str(reconciledRun.Values[0][0]))
	assert.Contains(t, store.Str(reconciledRun.Values[0][1]), "cancelled_by_rescue")
}

func TestMassModificationProtectionSemanticOr(t *testing.T) {
	// 1. In OR logic, if any branch is a catch-all / tautological pattern, the entire rule matches all!
	// It MUST be blocked.
	valErr := core.ValidateWorkflowInput(core.WorkflowInput{
		AccountID: 1,
		Name:      "Dangerous OR Inbox rule",
		Trigger:   core.WorkflowTriggerDaily,
		Config: core.WorkflowConfig{
			FolderRole:     "inbox",
			CondLogic:      "or",
			FromPattern:    "*",        // matches all!
			SubjectPattern: "Rechnung", // specific, but negated by From = * in OR
			Actions:        []core.WorkflowActionItem{{Type: core.WorkflowActionArchive}},
		},
	})
	require.NotNil(t, valErr)
	assert.Equal(t, "ERR_INBOX_MASS_MODIFICATION_BLOCKED", valErr.Code)

	// 2. In OR logic, when all active branches are restrictive, it is permitted!
	valErr2 := core.ValidateWorkflowInput(core.WorkflowInput{
		AccountID: 1,
		Name:      "Safe OR Inbox rule",
		Trigger:   core.WorkflowTriggerDaily,
		Config: core.WorkflowConfig{
			FolderRole:     "inbox",
			CondLogic:      "or",
			FromPattern:    "*@partner.de", // restrictive
			SubjectPattern: "Rechnung",     // restrictive
			Actions:        []core.WorkflowActionItem{{Type: core.WorkflowActionArchive}},
		},
	})
	assert.Nil(t, valErr2, "when all OR branches are restrictive, rule is safe")

	// 3. In AND logic, a restrictive branch restricts the catch-all
	valErr3 := core.ValidateWorkflowInput(core.WorkflowInput{
		AccountID: 1,
		Name:      "Safe AND Inbox rule",
		Trigger:   core.WorkflowTriggerDaily,
		Config: core.WorkflowConfig{
			FolderRole:     "inbox",
			CondLogic:      "and",
			FromPattern:    "*",
			SubjectPattern: "Rechnung", // restricts in AND
			Actions:        []core.WorkflowActionItem{{Type: core.WorkflowActionArchive}},
		},
	})
	assert.Nil(t, valErr3, "AND logic is effectively restricted by subject")
}

func TestStructuredThreatAndDomainAuthState(t *testing.T) {
	// 1. Threat warning indicators
	assert.Equal(t, core.ThreatWarning, core.ResolveThreatState(1000, "warning", ""))
	assert.Equal(t, core.ThreatWarning, core.ResolveThreatState(1000, "clean", "ai_fraud"))
	assert.Equal(t, core.ThreatWarning, core.ResolveThreatState(1000, "ok", "spoofing"))

	// 2. Suspicious indicators
	assert.Equal(t, core.ThreatSuspicious, core.ResolveThreatState(1000, "needs_review", ""))
	assert.Equal(t, core.ThreatSuspicious, core.ResolveThreatState(1000, "ok", "display_mismatch"))

	// 3. Clean ONLY when scanned and no warning/suspicious reason
	assert.Equal(t, core.ThreatClean, core.ResolveThreatState(1000, "clean", ""))
	assert.Equal(t, core.ThreatClean, core.ResolveThreatState(1000, "ok", ""))
	assert.NotEqual(t, core.ThreatClean, core.ResolveThreatState(0, "clean", ""), "unscanned must be pending")
	assert.Equal(t, core.ThreatPending, core.ResolveThreatState(0, "clean", ""))

	// 4. Domain authentication per RFC 7489 (DMARC evaluation)
	// DMARC pass -> Verified
	assert.Equal(t, core.DomainAuthVerified, core.ResolveDomainAuthState("user@corp.com", "dmarc=pass", "", "", 1000))

	// RFC 7489 §6.6.2: SPF fail with DKIM pass yields DMARC success -> Verified!
	assert.Equal(t, core.DomainAuthVerified, core.ResolveDomainAuthState("user@corp.com", "spf=fail; dkim=pass", "", "", 1000))

	// Explicit DMARC fail -> Failed
	assert.Equal(t, core.DomainAuthFailed, core.ResolveDomainAuthState("user@corp.com", "dmarc=fail", "", "", 1000))

	// Both SPF fail and DKIM fail -> Failed
	assert.Equal(t, core.DomainAuthFailed, core.ResolveDomainAuthState("user@corp.com", "spf=fail; dkim=fail", "", "", 1000))

	// Unscanned with empty auth -> Pending
	assert.Equal(t, core.DomainAuthPending, core.ResolveDomainAuthState("user@corp.com", "", "", "", 0))
}

func TestIntegerVersionConcurrentUpdate(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	wf, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Versioned Rule",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FromPattern: "boss@corp.com",
			Actions:     []core.WorkflowActionItem{{Type: core.WorkflowActionStar}},
		},
		Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), wf.Version, "newly created workflow starts at version 1")

	// Concurrency test: Two updates compete with initial version = 1
	// Update 1: expected version 1 -> succeeds, increments to version 2
	up1, err := e.Svc.UpdateWorkflow(bg, wf.ID, core.WorkflowInput{
		AccountID:       acc.ID,
		Name:            "Updated by Session A",
		Trigger:         core.WorkflowTriggerReceive,
		ExpectedVersion: 1,
		Config: core.WorkflowConfig{
			FromPattern: "boss@corp.com",
			Actions:     []core.WorkflowActionItem{{Type: core.WorkflowActionStar}},
		},
		Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), up1.Version, "successful update increments integer version")

	// Update 2: competing update also expecting version 1 -> MUST FAIL with ERR_VERSION_CONFLICT
	_, err = e.Svc.UpdateWorkflow(bg, wf.ID, core.WorkflowInput{
		AccountID:       acc.ID,
		Name:            "Updated by Session B",
		Trigger:         core.WorkflowTriggerReceive,
		ExpectedVersion: 1, // outdated version!
		Config: core.WorkflowConfig{
			FromPattern: "boss@corp.com",
			Actions:     []core.WorkflowActionItem{{Type: core.WorkflowActionStar}},
		},
		Enabled: true,
	})
	require.Error(t, err)
	valErr, ok := err.(*core.ValidationError)
	require.True(t, ok)
	assert.Equal(t, "ERR_VERSION_CONFLICT", valErr.Code)
}

func TestPreviewMatchesExecutionCandidateIDs(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// Deliver 4 matching messages and 2 non-matching messages
	for i := 1; i <= 4; i++ {
		e.Deliver(t, acc.ID, "INBOX", coretest.Msg("invoice@billing.com", "user@example.com", fmt.Sprintf("Rechnung #%d", i), "content"))
	}
	for i := 1; i <= 2; i++ {
		e.Deliver(t, acc.ID, "INBOX", coretest.Msg("newsletter@news.com", "user@example.com", fmt.Sprintf("News #%d", i), "content"))
	}
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	input := core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Archive Invoices",
		Trigger:   core.WorkflowTriggerDaily,
		Action:    core.WorkflowActionArchive,
		Config: core.WorkflowConfig{
			FolderRole:     "inbox",
			FromPattern:    "invoice@billing.com",
			SubjectPattern: "Rechnung",
			Actions:        []core.WorkflowActionItem{{Type: core.WorkflowActionArchive}},
		},
		Enabled: true,
	}

	// 1. Preview reports candidate count AND exact candidate message IDs
	preview, err := e.Svc.PreviewWorkflow(bg, input)
	require.NoError(t, err)
	assert.Equal(t, 4, preview.CandidateCount, "preview must accurately count matching candidates")
	require.Len(t, preview.CandidateIDs, 4, "preview must return the exact matching candidate IDs")

	// 2. Create and execute workflow
	wf, err := e.Svc.CreateWorkflow(bg, input)
	require.NoError(t, err)

	executedCount, err := e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 4, executedCount)

	// Query exact message IDs processed in workflow_runs
	runRows, err := e.Svc.QueryForTest("SELECT message_id FROM workflow_runs WHERE workflow_id = ? ORDER BY message_id ASC", wf.ID)
	require.NoError(t, err)
	require.Len(t, runRows.Values, 4)
	var executedIDs []int64
	for _, r := range runRows.Values {
		executedIDs = append(executedIDs, store.Int(r[0]))
	}

	// Preview CandidateIDs and executed message IDs MUST match identically!
	slices.Sort(preview.CandidateIDs)
	slices.Sort(executedIDs)
	assert.Equal(t, preview.CandidateIDs, executedIDs, "preview candidate IDs and executed message IDs must be 100% identical")
}

func TestConcurrentCreateIdempotencyKey(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	const concurrency = 5
	var wg sync.WaitGroup
	results := make([]core.Workflow, concurrency)
	errs := make([]error, concurrency)

	input := core.WorkflowInput{
		AccountID:      acc.ID,
		Name:           "Idempotent Rule",
		Trigger:        core.WorkflowTriggerReceive,
		IdempotencyKey: "key-concurrent-12345",
		Config: core.WorkflowConfig{
			FromPattern: "news@corp.com",
			Actions:     []core.WorkflowActionItem{{Type: core.WorkflowActionMarkRead}},
		},
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			wf, err := e.Svc.CreateWorkflow(bg, input)
			results[idx] = wf
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	// All 5 concurrent requests with identical parameters MUST succeed and return the SAME workflow ID!
	for i := 0; i < concurrency; i++ {
		require.NoError(t, errs[i], "concurrent request %d failed", i)
		assert.Equal(t, results[0].ID, results[i].ID, "concurrent request %d returned different workflow ID", i)
	}

	// Calling with same key but differing parameters causes an explicit conflict!
	alteredInput := input
	alteredInput.Name = "Altered Rule" // differing name!
	_, err := e.Svc.CreateWorkflow(bg, alteredInput)
	require.Error(t, err)
	valErr, ok := err.(*core.ValidationError)
	require.True(t, ok)
	assert.Equal(t, "ERR_IDEMPOTENCY_CONFLICT", valErr.Code)
}

func TestWorkflowLabelLoopDetection(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	l1, err := e.Svc.CreateLabel(bg, core.LabelInput{Name: "LabelA", Color: "red", Icon: "folder-3"})
	require.NoError(t, err)
	l2, err := e.Svc.CreateLabel(bg, core.LabelInput{Name: "LabelB", Color: "blue", Icon: "folder-3"})
	require.NoError(t, err)

	// 1. Direct loop protection at creation: cannot trigger on label X and apply label X
	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Self Loop Rule",
		Trigger:   core.WorkflowTriggerLabel,
		Config: core.WorkflowConfig{
			TriggerLabelID: l1.ID,
			Actions:        []core.WorkflowActionItem{{Type: core.WorkflowActionApplyLabel, LabelID: l1.ID}},
		},
	})
	require.Error(t, err)
	valErr, ok := err.(*core.ValidationError)
	require.True(t, ok)
	assert.Equal(t, "ERR_EVENT_LOOP_DETECTED", valErr.Code)

	// 2. Cross-rule cascade loop protection:
	// Rule 1: On LabelA -> Applies LabelB
	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Rule 1: A -> B",
		Trigger:   core.WorkflowTriggerLabel,
		Config: core.WorkflowConfig{
			TriggerLabelID: l1.ID,
			Actions:        []core.WorkflowActionItem{{Type: core.WorkflowActionApplyLabel, LabelID: l2.ID}},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Rule 2: On LabelB -> Applies LabelA (potential infinite ping-pong!)
	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Rule 2: B -> A",
		Trigger:   core.WorkflowTriggerLabel,
		Config: core.WorkflowConfig{
			TriggerLabelID: l2.ID,
			Actions:        []core.WorkflowActionItem{{Type: core.WorkflowActionApplyLabel, LabelID: l1.ID}},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Deliver message and assign LabelA
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("friend@example.com", "user@example.com", "Hello", "world"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))
	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	// Assign LabelA: triggers Rule 1 (applies LabelB).
	// Cascade detection tracks applied labels; when evaluating Rule 2 for LabelB,
	// cross-rule cycle detection halts cascade cleanly without entering an infinite loop!
	require.NoError(t, e.Svc.SetLabel(bg, []int64{msgID}, l1.ID, true))
}

func TestUntrustedAuthenticationResultsRejected(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// 1. Adversarial message 1: Receiver MTA reports 'none', but attacker injects forged
	// Authentication-Results header below the receiving hop with matching paypal.com claims!
	rawForged1 := []byte("Authentication-Results: mx.receiver.test; none\r\n" +
		"Received: from attacker.invalid (attacker.invalid [198.51.100.1]) by mx.receiver.test; Thu, 29 Oct 2026 00:00:00 +0000\r\n" +
		"Authentication-Results: mx.google.com; dkim=pass header.d=paypal.com; dmarc=pass header.from=paypal.com\r\n" +
		"From: PayPal <service@paypal.com>\r\n" +
		"To: user@example.com\r\n" +
		"Subject: Forged Lower Trace\r\n" +
		"Date: Thu, 29 Oct 2026 00:00:00 +0000\r\n" +
		"Message-ID: <forged1@attacker.invalid>\r\n\r\n" +
		"Attacker payload 1")

	// 2. Adversarial message 2: Attacker injects forged Authentication-Results header buried
	// below the original message headers (after 'From:').
	rawForged2 := []byte("Received: from attacker.invalid by mx.receiver.test; Thu, 29 Oct 2026 00:00:00 +0000\r\n" +
		"From: PayPal <service@paypal.com>\r\n" +
		"Authentication-Results: mx.receiver.test; dkim=pass header.d=paypal.com; dmarc=pass header.from=paypal.com\r\n" +
		"To: user@example.com\r\n" +
		"Subject: Forged Buried Header\r\n" +
		"Date: Thu, 29 Oct 2026 00:00:00 +0000\r\n" +
		"Message-ID: <forged2@attacker.invalid>\r\n\r\n" +
		"Attacker payload 2")

	// 3. Positive control: Authentic email where receiving MTA directly verified DKIM/DMARC for paypal.com
	rawLegit := []byte("Authentication-Results: mx.receiver.test; dkim=pass header.d=paypal.com; dmarc=pass header.from=paypal.com\r\n" +
		"Received: from mail.paypal.com (mail.paypal.com [198.51.100.2]) by mx.receiver.test; Thu, 29 Oct 2026 00:00:00 +0000\r\n" +
		"From: PayPal <service@paypal.com>\r\n" +
		"To: user@example.com\r\n" +
		"Subject: Legitimate Receipt\r\n" +
		"Date: Thu, 29 Oct 2026 00:00:00 +0000\r\n" +
		"Message-ID: <legit1@paypal.com>\r\n\r\n" +
		"Legitimate payment confirmation")

	// Deliver messages over wire without any manual DB manipulation of security status
	e.Deliver(t, acc.ID, "INBOX", rawForged1)
	e.Deliver(t, acc.ID, "INBOX", rawForged2)
	e.Deliver(t, acc.ID, "INBOX", rawLegit)

	// Verify database rows directly: the actual parser and security pipeline (without test pre-sets)
	// MUST independently classify the forged messages as neutral/unverified, and legitimate as verified!
	rows, err := e.Svc.QueryForTest("SELECT subject, security_status, security_reason, auth_results FROM messages WHERE account_id = ? ORDER BY id ASC", acc.ID)
	require.NoError(t, err)
	require.Len(t, rows.Values, 3)

	// Forged 1: shielded by top-level receiving hop 'none'
	assert.Equal(t, "Forged Lower Trace", store.Str(rows.Values[0][0]))
	assert.Equal(t, "neutral", store.Str(rows.Values[0][1]))
	assert.Equal(t, "unverified", store.Str(rows.Values[0][2]))
	assert.NotContains(t, store.Str(rows.Values[0][3]), "paypal.com", "forged lower hop claim must be discarded by ReceiverAuthenticationResults")

	// Forged 2: buried below 'From:' header
	assert.Equal(t, "Forged Buried Header", store.Str(rows.Values[1][0]))
	assert.Equal(t, "neutral", store.Str(rows.Values[1][1]))
	assert.Equal(t, "unverified", store.Str(rows.Values[1][2]))
	assert.Empty(t, store.Str(rows.Values[1][3]), "buried sender header must be rejected by authenticationTrace")

	// Legitimate: authentic receiving MTA evaluation
	assert.Equal(t, "Legitimate Receipt", store.Str(rows.Values[2][0]))
	assert.Equal(t, "verified_domain", store.Str(rows.Values[2][1]))
	assert.Contains(t, store.Str(rows.Values[2][3]), "dkim=pass")

	// Rule requiring verified domain authentication
	rule := core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Flag Verified PayPal",
		Trigger:   core.WorkflowTriggerDaily,
		Config: core.WorkflowConfig{
			FolderRole:  "inbox",
			FromPattern: "*@paypal.com",
			DomainAuth:  "verified",
			Actions:     []core.WorkflowActionItem{{Type: core.WorkflowActionStar}},
		},
		Enabled: true,
	}

	// 1. Preview must report exactly 1 candidate (the legitimate message, NOT the 2 forged ones)
	preview, err := e.Svc.PreviewWorkflow(bg, rule)
	require.NoError(t, err)
	assert.Equal(t, 1, preview.CandidateCount, "preview must exclude untrusted claims and match only genuine verified sender")

	// 2. Create and run daily workflow: only the legitimate message must be processed
	wf, err := e.Svc.CreateWorkflow(bg, rule)
	require.NoError(t, err)

	n, err := e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only legitimate verified message may be acted upon by workflow")

	// 3. Flags check: only the legitimate message is starred (\Flagged)
	flagRows, err := e.Svc.QueryForTest("SELECT subject, flags FROM messages WHERE account_id = ? ORDER BY id ASC", acc.ID)
	require.NoError(t, err)
	require.Len(t, flagRows.Values, 3)
	assert.NotContains(t, store.Str(flagRows.Values[0][1]), `\Flagged`, "forged message 1 must not be starred")
	assert.NotContains(t, store.Str(flagRows.Values[1][1]), `\Flagged`, "forged message 2 must not be starred")
	assert.Contains(t, store.Str(flagRows.Values[2][1]), `\Flagged`, "legitimate message must be starred")

	// 4. Direct evaluation of domain auth state confirms unverified for unverified security reason
	authState := core.ResolveDomainAuthState("PayPal <service@paypal.com>", "Authentication-Results: attacker.invalid; dmarc=pass header.from=paypal.com", "neutral", "unverified", 1700000000)
	assert.Equal(t, core.DomainAuthUnverified, authState, "unverified status must yield DomainAuthUnverified")

	_ = wf
}

func TestSafetyLimitVersionConflictSkipsPause(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// Deliver 6 promotional messages
	for i := 1; i <= 6; i++ {
		e.Deliver(t, acc.ID, "INBOX", coretest.Msg("promo@store.com", "user@example.com", fmt.Sprintf("Sale %d", i), "content"))
	}
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	// Create daily workflow with SafetyLimit = 3 (Version 1)
	wf, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Promotions Guardrail",
		Trigger:   core.WorkflowTriggerDaily,
		Config: core.WorkflowConfig{
			FolderRole:  "inbox",
			FromPattern: "promo@store.com",
			SafetyLimit: 3,
			Actions:     []core.WorkflowActionItem{{Type: core.WorkflowActionArchive}},
		},
		Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), wf.Version)

	// Simulate concurrent editor update: user tightens rule to Version 2 while a runner is evaluating
	updatedWf, err := e.Svc.UpdateWorkflow(bg, wf.ID, core.WorkflowInput{
		AccountID:       acc.ID,
		Name:            "Promotions Guardrail Tightened",
		Trigger:         wf.Trigger,
		ExpectedVersion: 1,
		Config: core.WorkflowConfig{
			FolderRole:  "inbox",
			FromPattern: "promo@store.com",
			SafetyLimit: 10, // Higher limit
			Actions:     []core.WorkflowActionItem{{Type: core.WorkflowActionArchive}},
		},
		Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), updatedWf.Version)
	assert.Equal(t, "active", updatedWf.ExecutionState)

	// Stale runner that evaluated Version 1 (with 6 matches > 3) now attempts to execute
	// its safety pause against the database:
	// UPDATE workflows SET execution_state = 'paused_safety_limit', version = version + 1
	// WHERE id = ? AND version = 1 AND execution_state != 'paused_safety_limit'
	// Because evaluated version was 1 but DB is at version 2, res.Changes == 0.
	// The runner must discard its obsolete pause decision, and NOT write audit rows claiming version 2 was paused!

	// Verify rule in DB remains active at Version 2:
	currentWf, err := e.Svc.GetWorkflow(bg, wf.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), currentWf.Version)
	assert.Equal(t, "active", currentWf.ExecutionState, "concurrent tightened version must NOT be paused by obsolete runner evaluation")

	// Verify 0 audit records were created with status 'paused' for Version 2:
	auditRuns, err := e.Svc.QueryForTest("SELECT id FROM workflow_runs WHERE workflow_id = ? AND status = 'paused'", wf.ID)
	require.NoError(t, err)
	assert.Empty(t, auditRuns.Values, "obsolete runner evaluation must not write false audit entries for incremented version")
}

func TestAtomicPurgeDeduplicationConcurrent(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	e.Deliver(t, acc.ID, "Trash", coretest.Msg("spam@domain.com", "user@example.com", "Spam Mail", "trash content"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	folders, err := e.Svc.ListFolders(bg, acc.ID)
	require.NoError(t, err)
	var trashFolderID int64
	for _, f := range folders {
		if f.Role == "trash" {
			trashFolderID = f.ID
			break
		}
	}
	require.NotZero(t, trashFolderID)

	msgs, err := e.Svc.ListMessages(bg, core.View{FolderID: trashFolderID}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	// Set entered_at to 40 days ago
	require.NoError(t, e.Svc.ExecForTest(fmt.Sprintf("UPDATE messages SET entered_at = unixepoch() - 40 * 86400 WHERE id = %d", msgID)))

	// Launch 5 concurrent goroutines queueing purge for the exact same message
	const concurrency = 5
	var wg sync.WaitGroup
	opIDs := make([]int64, concurrency)
	alreadyQueued := make([]bool, concurrency)
	errs := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			opID, q, err := e.Svc.QueueWorkflowPurgeForTest(bg, acc.ID, msgID)
			opIDs[idx] = opID
			alreadyQueued[idx] = q
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	// All concurrent callers must succeed without error
	for i := 0; i < concurrency; i++ {
		require.NoError(t, errs[i], "concurrent purge queueing caller %d failed", i)
		assert.Greater(t, opIDs[i], int64(0), "caller %d must get a valid op ID", i)
	}

	// All callers must reference the EXACT SAME op ID
	firstOpID := opIDs[0]
	for i := 1; i < concurrency; i++ {
		assert.Equal(t, firstOpID, opIDs[i], "all concurrent callers must reference the identical op_id")
	}

	// Exactly ONE active purge op must exist in ops table
	opsRows, err := e.Svc.QueryForTest("SELECT id, state FROM ops WHERE message_id = ? AND kind = 'purge'", msgID)
	require.NoError(t, err)
	require.Len(t, opsRows.Values, 1, "ops table must contain exactly ONE purge op across concurrent runners")
	assert.Equal(t, firstOpID, store.Int(opsRows.Values[0][0]))
	assert.Equal(t, "pending", store.Str(opsRows.Values[0][1]))

	// Direct database constraint verification:
	// Attempting to bypass application lock and insert a second active purge op directly
	// MUST fail with SQLite UNIQUE constraint violation due to idx_ops_unique_active_purge!
	err = e.Svc.ExecForTest(fmt.Sprintf(
		"INSERT INTO ops(account_id, message_id, kind, state, created_at) VALUES (%d, %d, 'purge', 'pending', unixepoch())",
		acc.ID, msgID,
	))
	require.Error(t, err, "database partial unique index idx_ops_unique_active_purge must reject duplicate active purge")
	assert.Contains(t, err.Error(), "UNIQUE constraint failed", "must trigger SQLite unique constraint violation")
}

func TestPurgeWorkflowFullLifecycle(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// Deliver 3 messages to Trash
	for i := 1; i <= 3; i++ {
		e.Deliver(t, acc.ID, "Trash", coretest.Msg("junk@example.com", "user@example.com", fmt.Sprintf("Trash %d", i), "content"))
	}
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	folders, err := e.Svc.ListFolders(bg, acc.ID)
	require.NoError(t, err)
	var trashFolderID, inboxFolderID int64
	for _, f := range folders {
		if f.Role == "trash" {
			trashFolderID = f.ID
		} else if f.Role == "inbox" {
			inboxFolderID = f.ID
		}
	}
	require.NotZero(t, trashFolderID)
	require.NotZero(t, inboxFolderID)

	trashMsgs, err := e.Svc.ListMessages(bg, core.View{FolderID: trashFolderID}, "", 10)
	require.NoError(t, err)
	require.Len(t, trashMsgs.Items, 3)

	msgSuccessID := trashMsgs.Items[0].ID
	msgFailedID := trashMsgs.Items[1].ID
	msgRescueID := trashMsgs.Items[2].ID

	// Set entered_at to 35 days ago for all 3 messages
	require.NoError(t, e.Svc.ExecForTest(fmt.Sprintf("UPDATE messages SET entered_at = unixepoch() - 35 * 86400 WHERE folder_id = %d", trashFolderID)))

	// Create daily purge workflow
	purgeInput := core.WorkflowInput{
		AccountID:    acc.ID,
		Name:         "Purge 30-Day Trash",
		Trigger:      core.WorkflowTriggerDaily,
		ConfirmPurge: true,
		Config: core.WorkflowConfig{
			FolderID:   trashFolderID,
			FolderRole: "trash",
			Days:       30,
			Actions:    []core.WorkflowActionItem{{Type: core.WorkflowActionPurge}},
		},
		Enabled: true,
	}
	proof, err := e.Svc.AuthorizePurgeDefinition(core.WithUserAuth(bg), purgeInput)
	require.NoError(t, err)
	purgeInput.PurgeProof = proof

	pwf, err := e.Svc.CreateWorkflow(bg, purgeInput)
	require.NoError(t, err)

	// 1. AUFTRAG EINREIHEN:
	// Execute daily run: all 3 messages are queued for purge with op_id recorded
	actions, err := e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, actions)

	// Verify all 3 workflow_runs rows have status 'queued' and non-nil op_id
	runs, err := e.Svc.QueryForTest("SELECT message_id, status, op_id FROM workflow_runs WHERE workflow_id = ? ORDER BY message_id ASC", pwf.ID)
	require.NoError(t, err)
	require.Len(t, runs.Values, 3)
	for _, r := range runs.Values {
		assert.Equal(t, "queued", store.Str(r[1]))
		assert.NotNil(t, r[2], "op_id must be populated")
	}

	// Wiederholter Lauf während Aufträge beim Provider ausstehen:
	// Die atomare Deduplikation erkennt aktive Aufträge in ops, und workflow_runs
	// erhält keine doppelten Audit-Einträge.
	actionsPendingRetry, err := e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, actionsPendingRetry, "pending purge ops must not be duplicated by subsequent runner evaluation")

	opCountBeforeConfirm, err := e.Svc.QueryForTest("SELECT COUNT(*) FROM ops WHERE message_id = ? AND kind = 'purge'", msgSuccessID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), store.Int(opCountBeforeConfirm.Values[0][0]), "ops queue must contain exactly 1 purge op per message")

	// 2. ENTSCHEIDENDER TEST: DER AUFTRAG IST NOCH NICHT BESTÄTIGT, ABER DER LOKALE NACHRICHTENEINTRAG FEHLT!
	// Message 1 entry is prematurely deleted locally, but its op in ops is STILL 'pending'.
	// Reconcile MUST NOT declare a false provider success from a missing message entry!
	// The run must remain 'queued'!
	op1Rows, err := e.Svc.QueryForTest("SELECT id, state FROM ops WHERE message_id = ?", msgSuccessID)
	require.NoError(t, err)
	require.NotEmpty(t, op1Rows.Values)
	op1ID := store.Int(op1Rows.Values[0][0])
	assert.Equal(t, "pending", store.Str(op1Rows.Values[0][1]))

	require.NoError(t, e.Svc.ExecForTest(fmt.Sprintf("DELETE FROM messages WHERE id = %d", msgSuccessID)))

	// Reconcile while op is still pending:
	require.NoError(t, e.Svc.ReconcilePurgeWorkflowRuns(bg, acc.ID))

	r1Premature, err := e.Svc.QueryForTest("SELECT status, details FROM workflow_runs WHERE workflow_id = ? AND message_id = ?", pwf.ID, msgSuccessID)
	require.NoError(t, err)
	require.Len(t, r1Premature.Values, 1)
	assert.Equal(t, "queued", store.Str(r1Premature.Values[0][0]), "missing local message alone must NOT cause status 'success' when op is unconfirmed!")
	assert.Contains(t, store.Str(r1Premature.Values[0][1]), "queued", "details must not claim provider_purge_completed without confirmation")

	// 3. NEUSTARTFESTIGKEIT (DIENST SCHLIESSEN -> DIESELBE DATENBANK ERNEUT ÖFFNEN)
	// Close the running service and re-open on the exact same DataDir without memory state.
	e.Restart(t)

	// Verify persistence: workflow, ops queue, and workflow_runs are fully intact after restart
	reloadedWf, err := e.Svc.GetWorkflow(bg, pwf.ID)
	require.NoError(t, err)
	assert.Equal(t, pwf.ID, reloadedWf.ID)

	reloadedRuns, err := e.Svc.QueryForTest("SELECT message_id, status, op_id FROM workflow_runs WHERE workflow_id = ? ORDER BY message_id ASC", pwf.ID)
	require.NoError(t, err)
	require.Len(t, reloadedRuns.Values, 3)
	for _, r := range reloadedRuns.Values {
		assert.Equal(t, "queued", store.Str(r[1]))
		assert.NotNil(t, r[2], "op_id linkage must be preserved across restart")
	}

	// 4. AUFTRAG WEITERVERARBEITEN:
	// Message 1: Provider now confirms purge completion -> ops.state = 'done'
	require.NoError(t, e.Svc.ExecForTest(fmt.Sprintf("UPDATE ops SET state = 'done' WHERE id = %d", op1ID)))

	// Message 2: Provider encounters error -> ops.state = 'failed'
	op2Rows, err := e.Svc.QueryForTest("SELECT id FROM ops WHERE message_id = ?", msgFailedID)
	require.NoError(t, err)
	require.NotEmpty(t, op2Rows.Values)
	op2ID := store.Int(op2Rows.Values[0][0])
	require.NoError(t, e.Svc.ExecForTest(fmt.Sprintf(
		"UPDATE ops SET state = 'failed', last_error = 'IMAP UID EXPUNGE failed: mailbox read-only' WHERE id = %d",
		op2ID,
	)))

	// Message 3: Rescued by moving to Inbox
	require.NoError(t, e.Svc.Move(bg, []int64{msgRescueID}, inboxFolderID))

	// 5. WORKFLOW-PROTOKOLL ABSCHLIESSEN:
	require.NoError(t, e.Svc.ReconcilePurgeWorkflowRuns(bg, acc.ID))

	// Case 1 (confirmed done) -> success with provider_purge_completed
	r1Final, err := e.Svc.QueryForTest("SELECT status, details FROM workflow_runs WHERE workflow_id = ? AND message_id = ?", pwf.ID, msgSuccessID)
	require.NoError(t, err)
	require.Len(t, r1Final.Values, 1)
	assert.Equal(t, "success", store.Str(r1Final.Values[0][0]))
	assert.Equal(t, "provider_purge_completed", store.Str(r1Final.Values[0][1]))

	// Case 2 (provider failed) -> failed with error message
	r2Final, err := e.Svc.QueryForTest("SELECT status, details FROM workflow_runs WHERE workflow_id = ? AND message_id = ?", pwf.ID, msgFailedID)
	require.NoError(t, err)
	require.Len(t, r2Final.Values, 1)
	assert.Equal(t, "failed", store.Str(r2Final.Values[0][0]))
	assert.Contains(t, store.Str(r2Final.Values[0][1]), "provider_purge_failed: IMAP UID EXPUNGE failed: mailbox read-only")

	// Case 3 (rescued) -> cancelled with cancelled_by_rescue
	r3Final, err := e.Svc.QueryForTest("SELECT status, details FROM workflow_runs WHERE workflow_id = ? AND message_id = ?", pwf.ID, msgRescueID)
	require.NoError(t, err)
	require.Len(t, r3Final.Values, 1)
	assert.Equal(t, "cancelled", store.Str(r3Final.Values[0][0]))
	assert.Equal(t, "cancelled_by_rescue", store.Str(r3Final.Values[0][1]))

	// 6. KLARE AUFRÄUMREIHENFOLGE (ON DELETE RESTRICT):
	// Der Fremdschlüssel op_id verhindert, dass der Provider-Auftrag vorzeitig gelöscht werden kann,
	// solange das Workflow-Auditprotokoll (workflow_runs) darauf verweist!
	delOpErr := e.Svc.ExecForTest(fmt.Sprintf("DELETE FROM ops WHERE id = %d", op2ID))
	require.Error(t, delOpErr, "ON DELETE RESTRICT must prevent deleting purge op while workflow_runs references it")
	assert.Contains(t, delOpErr.Error(), "FOREIGN KEY constraint failed")

	// Audit-Aufbewahrungsfrist abgelaufen: Nach Löschen des Protokolleintrags ist das Löschen des Auftrags erlaubt
	r2IDRows, err := e.Svc.QueryForTest("SELECT id FROM workflow_runs WHERE workflow_id = ? AND message_id = ?", pwf.ID, msgFailedID)
	require.NoError(t, err)
	r2ID := store.Int(r2IDRows.Values[0][0])
	require.NoError(t, e.Svc.ExecForTest(fmt.Sprintf("DELETE FROM workflow_runs WHERE id = %d", r2ID)))
	require.NoError(t, e.Svc.ExecForTest(fmt.Sprintf("DELETE FROM ops WHERE id = %d", op2ID)), "deleting op succeeds once referencing workflow_run audit entry is cleaned up")

	// 7. WIEDERHOLBARKEIT & IDEMPOTENZ DER ABGLEICHSFUNKTION (RECONCILE):
	// Ein erneuter Aufruf von ReconcilePurgeWorkflowRuns verändert abgeschlossene Läufe nicht
	// und wirft keinerlei Fehler
	require.NoError(t, e.Svc.ReconcilePurgeWorkflowRuns(bg, acc.ID))
	r1Recheck, err := e.Svc.QueryForTest("SELECT status, details FROM workflow_runs WHERE workflow_id = ? AND message_id = ?", pwf.ID, msgSuccessID)
	require.NoError(t, err)
	require.Len(t, r1Recheck.Values, 1)
	assert.Equal(t, "success", store.Str(r1Recheck.Values[0][0]))
	assert.Equal(t, "provider_purge_completed", store.Str(r1Recheck.Values[0][1]))
}

func TestReconcilePurgeWorkflowRunsErrorResilienceAndRetry(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// Message in Trash
	e.Deliver(t, acc.ID, "Trash", coretest.Msg("ad@spam.com", "user@example.com", "Spam 1", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	folders, err := e.Svc.ListFolders(bg, acc.ID)
	require.NoError(t, err)
	var trashFolderID int64
	for _, f := range folders {
		if f.Role == "trash" {
			trashFolderID = f.ID
			break
		}
	}
	msgs, err := e.Svc.ListMessages(bg, core.View{FolderID: trashFolderID}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	// Backdate entered_at to 40 days ago
	require.NoError(t, e.Svc.ExecForTest(fmt.Sprintf(
		"UPDATE messages SET entered_at = unixepoch() - 3456000 WHERE id = %d", msgID,
	)))

	// Purge workflow
	purgeInput := core.WorkflowInput{
		AccountID:    acc.ID,
		Name:         "Purge 30d",
		Trigger:      core.WorkflowTriggerDaily,
		Action:       core.WorkflowActionPurge,
		ConfirmPurge: true,
		Config: core.WorkflowConfig{
			FolderID:   trashFolderID,
			FolderRole: "trash",
			Days:       30,
			Actions:    []core.WorkflowActionItem{{Type: core.WorkflowActionPurge}},
		},
		Enabled: true,
	}
	proof, err := e.Svc.AuthorizePurgeDefinition(core.WithUserAuth(bg), purgeInput)
	require.NoError(t, err)
	purgeInput.PurgeProof = proof

	pwf, err := e.Svc.CreateWorkflow(bg, purgeInput)
	require.NoError(t, err)

	// Run daily workflows -> queues op in ops, writes run in workflow_runs (status: queued)
	actions, err := e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, actions)

	// Simulated provider completion: provider executed purge on server (ops.state = 'done')
	opRows, err := e.Svc.QueryForTest("SELECT id FROM ops WHERE message_id = ?", msgID)
	require.NoError(t, err)
	require.NotEmpty(t, opRows.Values)
	opID := store.Int(opRows.Values[0][0])
	require.NoError(t, e.Svc.ExecForTest(fmt.Sprintf("UPDATE ops SET state = 'done' WHERE id = %d", opID)))

	// 1. Error during reconciliation: canceled context simulates transient DB / execution failure
	canceledCtx, cancel := context.WithCancel(bg)
	cancel()
	err = e.Svc.ReconcilePurgeWorkflowRuns(canceledCtx, acc.ID)
	require.Error(t, err, "reconciliation must propagate errors and not ignore them")

	// Verify that run status remains 'queued' when reconciliation fails
	runRows, err := e.Svc.QueryForTest("SELECT status, details FROM workflow_runs WHERE workflow_id = ? AND message_id = ?", pwf.ID, msgID)
	require.NoError(t, err)
	require.Len(t, runRows.Values, 1)
	assert.Equal(t, "queued", store.Str(runRows.Values[0][0]), "run must remain queued after failed reconcile pass")

	// 2. Retry safety: subsequent workflow execution while status is queued does NOT re-trigger provider purge
	actions2, err := e.Svc.RunDailyWorkflows(bg, acc.ID)
	require.NoError(t, err)
	// Because ReconcilePurgeWorkflowRuns runs at start of RunDailyWorkflows with valid context,
	// it reconciles the run to success and executes 0 new purges!
	assert.Equal(t, 0, actions2, "no new purge may be queued during retry")

	// Verify final state is success with provider_purge_completed
	finalRows, err := e.Svc.QueryForTest("SELECT status, details FROM workflow_runs WHERE workflow_id = ? AND message_id = ?", pwf.ID, msgID)
	require.NoError(t, err)
	require.Len(t, finalRows.Values, 1)
	assert.Equal(t, "success", store.Str(finalRows.Values[0][0]))
	assert.Equal(t, "provider_purge_completed", store.Str(finalRows.Values[0][1]))

	// Exactly 1 op was ever created
	opTotal, err := e.Svc.QueryForTest("SELECT COUNT(*) FROM ops WHERE message_id = ? AND kind = 'purge'", msgID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), store.Int(opTotal.Values[0][0]))
}

func TestDomainAuthRejectsMTAImpersonationAndForgedHeaders(t *testing.T) {
	e := coretest.New(t)
	bg := context.Background()
	acc := e.AddAccount(t, "user@example.com")

	// 1. Attacker header with completely mimicked, internally matching findings:
	// "Authentication-Results: mx.google.com; dkim=pass header.d=paypal.com; dmarc=pass header.from=paypal.com"
	// Senders claim From: PayPal <service@paypal.com>.
	// Here server name (mx.google.com) and domain claims (paypal.com) all match!
	// But because the receiving path did not authenticate it (security_status = 'neutral', security_reason = 'unverified'),
	// it MUST NOT be trusted as verified!
	authSpoofedAllMatching := core.ResolveDomainAuthState(
		"PayPal <service@paypal.com>",
		"Authentication-Results: mx.google.com; dkim=pass header.d=paypal.com; dmarc=pass header.from=paypal.com",
		"neutral", "unverified", 1700000000,
	)
	assert.Equal(t, core.DomainAuthUnverified, authSpoofedAllMatching, "completely mimicked header without trusted reception proof must yield DomainAuthUnverified")

	// 2. Attacker header impersonating Google MTA with unaligned DKIM:
	// "Authentication-Results: mx.google.com; dkim=pass header.i=@attacker.com; dmarc=none"
	auth1 := core.ResolveDomainAuthState(
		"Billing <billing@paypal.com>",
		"Authentication-Results: mx.google.com; dkim=pass header.i=@attacker.com; dmarc=none",
		"neutral", "unverified", 1700000000,
	)
	assert.Equal(t, core.DomainAuthUnverified, auth1, "unaligned DKIM must yield DomainAuthUnverified")

	// 3. Attacker header impersonating company relay with neutral results:
	// "Authentication-Results: relay.corp.internal; dkim=none; spf=neutral"
	auth2 := core.ResolveDomainAuthState(
		"Support <support@corp.com>",
		"Authentication-Results: relay.corp.internal; dkim=none; spf=neutral",
		"neutral", "", 1700000000,
	)
	assert.Equal(t, core.DomainAuthUnverified, auth2, "neutral internal relay header must yield DomainAuthUnverified")

	// 4. Real message receipt boundary via EvaluateWorkflows with completely mimicked header:
	// Deliver an incoming message where an attacker injected the fully matching header
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("PayPal <service@paypal.com>", "user@example.com", "Urgent Invoice", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	forgedMsgID := msgs.Items[0].ID

	// Set completely forged matching header on message, but reception evaluation marked it unverified
	require.NoError(t, e.Svc.ExecForTest(fmt.Sprintf(
		"UPDATE messages SET auth_results = 'Authentication-Results: mx.google.com; dkim=pass header.d=paypal.com; dmarc=pass header.from=paypal.com', security_status = 'neutral', security_reason = 'unverified', security_scanned_at = 1700000000 WHERE id = %d",
		forgedMsgID,
	)))

	// Create an on_receive rule that marks messages as read ONLY IF domain_auth is 'verified'
	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Mark Verified Mail Read",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			DomainAuth: "verified",
			Actions:    []core.WorkflowActionItem{{Type: core.WorkflowActionMarkRead}},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Trigger receive workflow evaluation for forged message
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, forgedMsgID))

	// The forged message MUST NOT be marked as read! It must remain unread (\Seen flag NOT set)!
	msgAfter, err := e.Svc.GetMessage(bg, forgedMsgID, false)
	require.NoError(t, err)
	assert.False(t, slices.Contains(msgAfter.Flags, `\Seen`), "message with forged auth header must NOT match verified domain_auth rule; must remain unread")

	// In contrast, when the trusted receiving path genuinely verified the domain:
	require.NoError(t, e.Svc.ExecForTest(fmt.Sprintf(
		"UPDATE messages SET security_status = 'verified_domain', security_reason = 'dmarc_pass' WHERE id = %d",
		forgedMsgID,
	)))
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, forgedMsgID))

	msgGenuine, err := e.Svc.GetMessage(bg, forgedMsgID, false)
	require.NoError(t, err)
	assert.True(t, slices.Contains(msgGenuine.Flags, `\Seen`), "message authenticated by trusted receiving path must match and be marked read")
}

func TestWorkflowActions_Validation_NewActions(t *testing.T) {
	bg := context.Background()
	e := coretest.New(t)
	acc := e.AddAccount(t, "test@example.com")

	// 1. Conflicting star and unstar
	valErr := e.Svc.ValidateWorkflowInput(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Conflicting Star",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionStar},
				{Type: core.WorkflowActionUnstar},
			},
		},
		Enabled: true,
	})
	require.NotNil(t, valErr)
	assert.Equal(t, "ERR_ACTIONS_INVALID", valErr.Code)

	// 2. Forward without address
	valErr = e.Svc.ValidateWorkflowInput(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Forward Missing Addr",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionForward},
			},
		},
		Enabled: true,
	})
	require.NotNil(t, valErr)
	assert.Equal(t, "ERR_ACTIONS_INVALID", valErr.Code)

	// 3. Export without target directory
	valErr = e.Svc.ValidateWorkflowInput(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Export Missing Dir",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionExportEML},
			},
		},
		Enabled: true,
	})
	require.NotNil(t, valErr)
	assert.Equal(t, "ERR_TARGET_DIRECTORY_REQUIRED", valErr.Code)

	// 4. Wit Draft without instructions
	valErr = e.Svc.ValidateWorkflowInput(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Wit Draft Missing Inst",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionWitDraft},
			},
		},
		Enabled: true,
	})
	require.NotNil(t, valErr)
	assert.Equal(t, "ERR_WIT_INSTRUCTIONS_REQUIRED", valErr.Code)
}

func TestWorkflowActions_Execution_Unstar(t *testing.T) {
	bg := context.Background()
	e := coretest.New(t)
	acc := e.AddAccount(t, "test@example.com")

	// Deliver message and sync
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("sender@example.com", "test@example.com", "Starred Message", "body"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	// Star the message initially
	require.NoError(t, e.Svc.SetFlags(bg, []int64{msgID}, []string{`\Flagged`}, nil))

	msgBefore, err := e.Svc.GetMessage(bg, msgID, false)
	require.NoError(t, err)
	assert.True(t, slices.Contains(msgBefore.Flags, `\Flagged`))

	// Create unstar workflow
	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Unstar Mail",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			Actions:    []core.WorkflowActionItem{{Type: core.WorkflowActionUnstar}},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Evaluate workflow
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))

	// Flagged should be removed
	msgAfter, err := e.Svc.GetMessage(bg, msgID, false)
	require.NoError(t, err)
	assert.False(t, slices.Contains(msgAfter.Flags, `\Flagged`), "message should no longer have Flagged flag")
}

func TestWorkflowActions_Execution_Forward_LoopProtection(t *testing.T) {
	bg := context.Background()
	e := coretest.New(t)
	acc := e.AddAccount(t, "me@example.com")

	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("billing@vendor.com", "me@example.com", "Important Invoice", "Hello"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	// Create direct forward workflow
	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Auto Forward to Accountant",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionForward, ForwardTo: "accountant@taxoffice.com"},
			},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Evaluate workflow
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))

	// Check outbox has queued the forwarded email with Loop Prevention Header
	outboxItems, err := e.Svc.ListOutbox(bg, acc.ID)
	require.NoError(t, err)
	require.Len(t, outboxItems, 1)
	assert.Contains(t, outboxItems[0].Subject, "Fwd: Important Invoice")
	assert.Equal(t, "queued", outboxItems[0].State)

	// Now verify loop protection:
	// A message with loop markers in its raw content must NOT be forwarded
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("someone@example.com", "me@example.com", "Auto Forward Check", "Test"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))
	msgs2, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs2.Items, 2)
	var loopMsgID int64
	for _, m := range msgs2.Items {
		if m.ID != msgID {
			loopMsgID = m.ID
		}
	}

	require.NoError(t, e.Svc.ExecForTest(fmt.Sprintf(
		"UPDATE messages SET auth_results = 'X-Godwit-Automated-Forward: 1\r\nAuto-Submitted: auto-generated' WHERE id = %d",
		loopMsgID,
	)))

	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, loopMsgID))

	// Outbox count should still be 1 (no new forwarded email created for loop message)
	outboxAfter, err := e.Svc.ListOutbox(bg, acc.ID)
	require.NoError(t, err)
	assert.Len(t, outboxAfter, 1)
}

func TestWorkflowActions_Execution_CaseFolder_Exports(t *testing.T) {
	bg := context.Background()
	e := coretest.New(t)
	acc := e.AddAccount(t, "test@example.com")

	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("client@partner.com", "test@example.com", "Projektanfrage Webdesign", "Sehr geehrte Damen und Herren,\nbitte um ein Angebot für Webdesign.\nMit freundlichen Grüßen"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	tmpDir := t.TempDir()
	bundleTrue := true
	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Case Folder Bundle",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole:      "inbox",
			TargetDirectory: tmpDir,
			BundleSubfolder: &bundleTrue,
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionExportEML},
				{Type: core.WorkflowActionExportPDF},
				{Type: core.WorkflowActionWitExtract, ExtractTemplate: "Projektanfrage"},
			},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))

	// Find the created case folder inside tmpDir
	entries, err := os.ReadDir(tmpDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "should have created exactly 1 case folder")
	assert.True(t, entries[0].IsDir())
	caseFolderName := entries[0].Name()
	assert.Contains(t, caseFolderName, "client@partner.com")
	assert.Contains(t, caseFolderName, "Projektanfrage Webdesign")

	// Check files inside the case folder
	caseDirPath := filepath.Join(tmpDir, caseFolderName)
	files, err := os.ReadDir(caseDirPath)
	require.NoError(t, err)
	require.Len(t, files, 3, "should contain .eml, .pdf, and Zusammenfassung.md")

	fileNames := make([]string, len(files))
	for i, f := range files {
		fileNames[i] = f.Name()
	}
	assert.Contains(t, fileNames, "Projektanfrage Webdesign.eml")
	assert.Contains(t, fileNames, "Projektanfrage Webdesign.pdf")
	assert.Contains(t, fileNames, "Zusammenfassung.md")

	// Verify PDF is valid PDF format
	pdfBytes, err := os.ReadFile(filepath.Join(caseDirPath, "Projektanfrage Webdesign.pdf"))
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(pdfBytes, []byte("%PDF-1.4")))

	// Verify Markdown content
	mdBytes, err := os.ReadFile(filepath.Join(caseDirPath, "Zusammenfassung.md"))
	require.NoError(t, err)
	assert.Contains(t, string(mdBytes), "# Vorgangs-Zusammenfassung (Wit Extraktion)")
	assert.Contains(t, string(mdBytes), "client@partner.com")
}

func TestWorkflowActions_Execution_WitDraft(t *testing.T) {
	bg := context.Background()
	e := coretest.New(t)
	acc := e.AddAccount(t, "test@example.com")

	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("chef@kunde.de", "test@example.com", "Anfrage Terminvereinbarung", "Können wir uns nächste Woche treffen?"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Wit Reply Drafter",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionWitDraft, Instructions: "Höflich zusagen für Dienstag 14 Uhr"},
			},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))

	// Verify draft was created in drafts table
	drafts, err := e.Svc.ListDrafts(bg, acc.ID)
	require.NoError(t, err)
	require.NotEmpty(t, drafts)
	lastDraft := drafts[0]
	assert.Contains(t, lastDraft.Subject, "Re: Anfrage Terminvereinbarung")
	assert.Contains(t, lastDraft.HTML, "Höflich zusagen für Dienstag 14 Uhr")
}

func TestWorkflowActions_Execution_WitExtract_FullBodyAndStructure(t *testing.T) {
	bg := context.Background()
	e := coretest.New(t)
	acc := e.AddAccount(t, "test@example.com")
	tmpDir := t.TempDir()

	longBody := "Sehr geehrte Damen und Herren,\n\n" +
		"Hierzu schlage ich Ihnen folgenden Termin vor: Dienstag, 10. November um 14:00 Uhr.\n" +
		"Bitte geben Sie mir kurz Bescheid, ob Ihnen der Termin passt.\n\n" +
		"Mit freundlichen Grüßen,\nMax Mustermann"

	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("partner@firma.de", "test@example.com", "Terminvorschlag für Abstimmung", longBody))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Wit Extract Full Test",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole:      "inbox",
			TargetDirectory: tmpDir,
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionWitExtract, ExtractTemplate: "Alle wichtigen Informationen extrahieren und in Bullet Points zusammenfassen."},
			},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))

	// Find the created markdown file
	var mdPath string
	err = filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".md") {
			mdPath = path
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, mdPath, "Zusammenfassung.md should have been created")

	mdContent, err := os.ReadFile(mdPath)
	require.NoError(t, err)
	mdStr := string(mdContent)

	// Check headers and structure
	assert.Contains(t, mdStr, "# Vorgangs-Zusammenfassung (Wit Extraktion)")
	assert.Contains(t, mdStr, "partner@firma.de")
	assert.Contains(t, mdStr, "Terminvorschlag für Abstimmung")
	assert.Contains(t, mdStr, "Alle wichtigen Informationen extrahieren und in Bullet Points zusammenfassen.")
	assert.Contains(t, mdStr, "## Extrahierte Kerndaten")
	assert.Contains(t, mdStr, "## Zusammenfassung & Kernpunkte")
	assert.Contains(t, mdStr, "## Vollständiger E-Mail-Inhalt")
	// Verify that the full body is present and NOT cut off mid-sentence
	assert.Contains(t, mdStr, "Hierzu schlage ich Ihnen folgenden Termin vor: Dienstag, 10. November um 14:00 Uhr.")
	assert.Contains(t, mdStr, "Mit freundlichen Grüßen")
}

func TestWorkflowActions_Execution_WitExtract_WithActiveModel(t *testing.T) {
	bg := context.Background()
	e := coretest.New(t)
	acc := e.AddAccount(t, "test@example.com")
	tmpDir := t.TempDir()

	llm := e.LLM(t)
	expectedBullets := "- Vereinbarter Termin: Dienstag, 10. November um 14:00 Uhr\n- Ort: Google Meet\n- Thema: Projekt-Kickoff und Roadmap"
	llm.Script(coretest.Turn{Text: []string{expectedBullets}})

	emailBody := "Hallo Team,\n\nHierzu schlage ich Ihnen folgenden Termin vor: Dienstag, 10. November um 14:00 Uhr.\n" +
		"Wir treffen uns auf Google Meet für den Projekt-Kickoff und die Roadmap.\n\nViele Grüße,\nAlex"

	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("alex@partner.com", "test@example.com", "Kickoff Abstimmung", emailBody))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Wit AI Extract Test",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole:      "inbox",
			TargetDirectory: tmpDir,
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionWitExtract, ExtractTemplate: "Alle wichtigen Informationen extrahieren und in Bullet Points zusammenfassen."},
			},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))

	var mdPath string
	err = filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".md") {
			mdPath = path
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, mdPath)

	mdContent, err := os.ReadFile(mdPath)
	require.NoError(t, err)
	mdStr := string(mdContent)

	// Verify AI-generated bullet points were inserted into the summary
	assert.Contains(t, mdStr, "## Zusammenfassung & Kernpunkte")
	assert.Contains(t, mdStr, "- Vereinbarter Termin: Dienstag, 10. November um 14:00 Uhr")
	assert.Contains(t, mdStr, "- Ort: Google Meet")
	assert.Contains(t, mdStr, "- Thema: Projekt-Kickoff und Roadmap")
	// Verify full body is in the preview section
	assert.Contains(t, mdStr, "## Vollständiger E-Mail-Inhalt")
	assert.Contains(t, mdStr, "Wir treffen uns auf Google Meet für den Projekt-Kickoff")
}

func TestWorkflowActions_Execution_WitDraft_WithActiveModel(t *testing.T) {
	bg := context.Background()
	e := coretest.New(t)
	acc := e.AddAccount(t, "test@example.com")

	llm := e.LLM(t)
	replyText := "Sehr geehrte Damen und Herren,\n\nvielen Dank für Ihre Anfrage. Gerne bestätige ich Ihnen den Termin für Dienstag um 14:00 Uhr.\n\nMit freundlichen Grüßen,\nIhr Team"
	llm.Script(coretest.Turn{Text: []string{replyText}})

	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("kunde@firma.de", "test@example.com", "Terminbestätigung erbeten", "Können wir Dienstag 14 Uhr festhalten?"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Wit Reply Drafter AI",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole: "inbox",
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionWitDraft, Instructions: "Höflich zusagen für Dienstag 14 Uhr"},
			},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))

	drafts, err := e.Svc.ListDrafts(bg, acc.ID)
	require.NoError(t, err)
	require.NotEmpty(t, drafts)
	lastDraft := drafts[0]
	assert.Contains(t, lastDraft.HTML, "vielen Dank für Ihre Anfrage. Gerne bestätige ich Ihnen den Termin für Dienstag um 14:00 Uhr.")
	assert.Contains(t, lastDraft.HTML, "Sehr geehrte Damen und Herren")
}

func TestWorkflowRuns_ListAndParse(t *testing.T) {
	bg := context.Background()
	e := coretest.New(t)
	acc := e.AddAccount(t, "user@test.com")
	tmpDir := t.TempDir()

	// 1. Deliver message from user@test.com (same as forward_to) to trigger loop prevention
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("user@test.com", "user@test.com", "Self Test Message", "Test message body content"))
	require.NoError(t, e.Svc.SyncNow(bg, acc.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	_, err = e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc.ID,
		Name:      "Self Forward & Draft & Export",
		Trigger:   core.WorkflowTriggerReceive,
		Config: core.WorkflowConfig{
			FolderRole:      "inbox",
			TargetDirectory: tmpDir,
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionForward, ForwardTo: "user@test.com"},
				{Type: core.WorkflowActionWitDraft, Instructions: "Test response"},
				{Type: core.WorkflowActionExportPDF},
			},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc.ID, core.WorkflowTriggerReceive, msgID))

	// 2. Query ListWorkflowRuns
	runs, err := e.Svc.ListWorkflowRuns(bg, acc.ID, 10)
	require.NoError(t, err)
	require.NotEmpty(t, runs)

	run := runs[0]
	assert.Equal(t, "Self Forward & Draft & Export", run.WorkflowName)
	assert.Equal(t, "Self Test Message", run.Subject)
	assert.Equal(t, "success", run.Status)
	assert.Contains(t, run.SkipReason, "loop_prevention:user@test.com")
	assert.Greater(t, run.DraftID, int64(0))
	assert.NotEmpty(t, run.FilePath)
	assert.NotEmpty(t, run.DirPath)

	// 3. Test ClearWorkflowRuns
	require.NoError(t, e.Svc.ClearWorkflowRuns(bg, acc.ID))
	clearedRuns, err := e.Svc.ListWorkflowRuns(bg, acc.ID, 10)
	require.NoError(t, err)
	assert.Empty(t, clearedRuns)
}

func TestWorkflowSecurityTriggerAndMultiAccountRuns(t *testing.T) {
	e := coretest.New(t)
	acc1 := e.AddAccount(t, "user1@example.com")
	acc2 := e.AddAccount(t, "user2@example.com")
	bg := context.Background()

	// Create a security-triggered workflow on acc1
	_, err := e.Svc.CreateWorkflow(bg, core.WorkflowInput{
		AccountID: acc1.ID,
		Name:      "Security Alert Workflow",
		Trigger:   core.WorkflowTriggerSecurity,
		Config: core.WorkflowConfig{
			ThreatAssessment: "warning",
			Actions: []core.WorkflowActionItem{
				{Type: core.WorkflowActionArchive},
			},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	// Deliver message
	e.Deliver(t, acc1.ID, "INBOX", coretest.Msg("hacker@malicious.com", "user1@example.com", "Phishing Alert", "Click here now"))
	require.NoError(t, e.Svc.SyncNow(bg, acc1.ID))

	msgs, err := e.Svc.ListMessages(bg, core.View{AccountID: acc1.ID, Unified: "inbox"}, "", 10)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID

	// If message has no security scan assessment (or clean), EvaluateWorkflows should NOT run
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc1.ID, core.WorkflowTriggerSecurity, msgID))
	runs1, err := e.Svc.ListWorkflowRuns(bg, acc1.ID, 10)
	require.NoError(t, err)
	assert.Empty(t, runs1)

	// Update security assessment on message to "warning"
	_, err = e.Svc.DBForTest().Execute(bg, "UPDATE messages SET security_scanned_at = ?, security_status = 'warning', security_reason = 'phishing' WHERE id = ?", time.Now().Unix(), msgID)
	require.NoError(t, err)

	// Evaluate again -> should match and run
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc1.ID, core.WorkflowTriggerSecurity, msgID))
	runs2, err := e.Svc.ListWorkflowRuns(bg, acc1.ID, 10)
	require.NoError(t, err)
	require.Len(t, runs2, 1)
	assert.Equal(t, "Security Alert Workflow", runs2[0].WorkflowName)
	assert.Equal(t, "user1@example.com", runs2[0].AccountEmail)

	// Query across all accounts (accountID <= 0)
	allRuns, err := e.Svc.ListWorkflowRuns(bg, 0, 10)
	require.NoError(t, err)
	require.Len(t, allRuns, 1)
	assert.Equal(t, "user1@example.com", allRuns[0].AccountEmail)

	// Verify historical email protection: old emails (dated before workflow creation) are skipped
	oldDate := time.Now().Unix() - 86400*30
	_, err = e.Svc.DBForTest().Execute(bg, "UPDATE messages SET date = ?, entered_at = ? WHERE id = ?", oldDate, oldDate, msgID)
	require.NoError(t, err)
	require.NoError(t, e.Svc.ClearWorkflowRuns(bg, acc1.ID))

	// Re-evaluating historical email must skip execution
	require.NoError(t, e.Svc.EvaluateWorkflows(bg, acc1.ID, core.WorkflowTriggerSecurity, msgID))
	runsHistorical, err := e.Svc.ListWorkflowRuns(bg, acc1.ID, 10)
	require.NoError(t, err)
	assert.Empty(t, runsHistorical, "historical message must not trigger custom security workflow")

	// Query for acc2 -> should be empty
	runsAcc2, err := e.Svc.ListWorkflowRuns(bg, acc2.ID, 10)
	require.NoError(t, err)
	assert.Empty(t, runsAcc2)
}
