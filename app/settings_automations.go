package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/theme"
)

const (
	CondFolder     = "folder"
	CondDays       = "days"
	CondLabel      = "label"
	CondThreat     = "threat"
	CondDomainAuth = "domain_auth"
	CondSecurity   = "security"
	CondHasAtt     = "attachments"
	CondFrom       = "from"
	CondSubject    = "subject"
)

type automationsView struct {
	a *App

	accountID int64
	workflows []core.Workflow
	labels    []core.Label
	loaded    bool

	// Work-Tree Builder state
	builderOpen      bool
	editID           int64 // 0 for new, >0 for editing
	name             string
	trigger          string
	triggerLabelID   int64
	triggerLabelName string
	condLogic        string // "and" or "or"
	activeConds      []string
	newCondChoice    string
	activeActions    []core.WorkflowActionItem
	newActionChoice  string
	folderRole       string
	olderThanDays    int
	labelID          int64
	labelName        string
	threatAssessment string // "warning", "suspicious", "clean"
	domainAuth       string // "verified", "failed", "unverified"
	securityStatus   string
	hasAttachments   int // 1: yes, 2: no
	fromPattern      string
	subjectPattern   string
	subjectOp        string // "contains", "equals", "starts_with"
	targetDirectory  string
	bundleSubfolder  bool
	pickingDir       bool
	forwardTo        string
	instructions     string
	extractTemplate  string
	enabled          bool

	// Save confirmation dialog
	confirmOpen bool
	preview     core.WorkflowPreview

	// Delete confirmation
	deleteOpen   bool
	deleteTarget *core.Workflow

	// Navigation tab
	tabIndex int // 0: Rules, 1: History / Logs

	// Execution history
	runs          []core.WorkflowRun
	runsLoaded    bool
	runsAccountID int64
	runsLoading   bool

	// Multi-account builder state
	builderAllAccounts bool
	builderAccountIDs  []int64
}

func newAutomationsView(a *App) *automationsView {
	return &automationsView{a: a}
}

func (v *automationsView) loadRuns() {
	if v.accountID == 0 && len(v.a.accounts) == 1 {
		v.accountID = v.a.accounts[0].ID
	}
	accID := v.accountID
	v.runsLoading = true
	var runs []core.WorkflowRun
	v.a.query(func(ctx context.Context) (err error) {
		runs, err = v.a.svc.ListWorkflowRuns(ctx, accID, 50)
		return err
	}, func(err error) {
		v.runsLoading = false
		if err == nil {
			v.runs = runs
			v.runsLoaded = true
			v.runsAccountID = accID
		}
	})
}

func (v *automationsView) clearRuns() {
	if v.accountID == 0 && len(v.a.accounts) == 1 {
		v.accountID = v.a.accounts[0].ID
	}
	accID := v.accountID
	v.a.do(func(ctx context.Context) error {
		return v.a.svc.ClearWorkflowRuns(ctx, accID)
	}, func(err error) {
		v.loadRuns()
	})
}

func (v *automationsView) load() {
	a := v.a
	if v.accountID == 0 && len(a.accounts) == 1 {
		v.accountID = a.accounts[0].ID
	}
	if v.accountID == 0 {
		var allList []core.Workflow
		var labelList []core.Label
		a.query(func(ctx context.Context) error {
			for _, acc := range a.accounts {
				list, err := a.svc.ListWorkflows(ctx, acc.ID)
				if err == nil {
					allList = append(allList, list...)
				}
			}
			labelList, _ = a.svc.ListLabels(ctx)
			return nil
		}, func(err error) {
			if err == nil {
				v.workflows = allList
				v.labels = labelList
				v.loaded = true
			}
		})
		return
	}
	accID := v.accountID
	var list []core.Workflow
	var labelList []core.Label
	a.query(func(ctx context.Context) (err error) {
		list, err = a.svc.ListWorkflows(ctx, accID)
		if err != nil {
			return err
		}
		labelList, _ = a.svc.ListLabels(ctx)
		return nil
	}, func(err error) {
		if err == nil {
			v.workflows = list
			v.labels = labelList
			v.loaded = true
		}
	})
}

func (v *automationsView) view(c *ui.Context, p *settingsPage) {
	if !v.loaded {
		v.load()
	}

	t, th := theme.Of(c), c.Theme()
	a := v.a

	// Heading with "Mit Wit erstellen" and "Neuer Workflow" buttons
	pageHeading(c, i18n.T("settings.automations"), i18n.T("settings.automations.sub"), func() {
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			settingButton(c, i18n.T("settings.automations.wit_create"), kit.Outline, false).OnClick(func() {
				v.a.askWit(i18n.T("settings.automations.wit_prompt"))
			})
			if !v.builderOpen {
				settingButton(c, i18n.T("settings.automations.new"), kit.Outline, false).OnClick(func() {
					v.openBuilder(nil)
				})
			}
		})
	})

	// Account Switcher if multiple accounts
	if len(a.accounts) > 1 {
		var opts []kit.Option[int64]
		opts = append(opts, kit.Option[int64]{Value: 0, Label: i18n.T("settings.automations.all_accounts")})
		for _, acc := range a.accounts {
			opts = append(opts, kit.Option[int64]{Value: acc.ID, Label: accountName(acc)})
		}
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			kit.Text(c, i18n.T("settings.accounts")+":", t.Text.Body).FontWeight(500)
			if settingSelect(c, i18n.T("settings.accounts"), &v.accountID, opts).Changed() {
				v.load()
				if v.tabIndex == 1 {
					v.loadRuns()
				}
			}
		})
	}

	// Tab bar: Übersicht & Regeln / Verlauf & Protokoll
	ui.Row(c).Margin(th.Space(2), 0, th.Space(3), 0).Children(func() {
		if kit.Tabs(c, &v.tabIndex, i18n.T("settings.automations.tabs.rules"), i18n.T("settings.automations.tabs.history")).Changed() {
			if v.tabIndex == 1 && (!v.runsLoaded || v.runsAccountID != v.accountID) {
				v.loadRuns()
			}
		}
	})

	if v.tabIndex == 1 {
		if !v.runsLoaded || v.runsAccountID != v.accountID {
			v.loadRuns()
		}
		v.renderHistory(c, p)
		return
	}

	// Separate native presets and custom workflows
	var nativeWfs []core.Workflow
	var customWfs []core.Workflow
	for _, wf := range v.workflows {
		if wf.Kind == core.WorkflowKindNative {
			nativeWfs = append(nativeWfs, wf)
		} else {
			customWfs = append(customWfs, wf)
		}
	}

	// Native automations section
	var nativeRows []func()
	for _, wf := range nativeWfs {
		w := wf
		nativeRows = append(nativeRows, func() { v.workflowRow(c, w) })
	}
	settingsSection(c, i18n.T("settings.automations.native_badge"), "", nativeRows...)

	// Custom automations section with Inline Work-Tree Builder
	if v.builderOpen {
		settingsSection(c, i18n.T("settings.automations.builder_title"), "", func() {
			v.builderTreeCard(c)
		})
	}

	if len(customWfs) == 0 && !v.builderOpen {
		settingsSection(c, i18n.T("settings.automations.custom_badge"), "", func() {
			ui.Row(c).FillWidth().Padding(th.Space(6), th.Space(4)).Justify(ui.Center).Children(func() {
				ui.Column(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
					kit.Text(c, i18n.T("settings.automations.empty"), t.Text.Body).TextColor(t.Muted)
					settingButton(c, i18n.T("settings.automations.new"), kit.Outline, false).OnClick(func() {
						v.openBuilder(nil)
					})
				})
			})
		})
	} else if len(customWfs) > 0 {
		var customRows []func()
		for _, wf := range customWfs {
			w := wf
			customRows = append(customRows, func() { v.workflowRow(c, w) })
		}
		settingsSection(c, i18n.T("settings.automations.custom_badge"), "", customRows...)
	}

	// Save explanation & confirmation dialog
	v.confirmSaveDialog(c)

	// Delete confirmation dialog
	v.deleteDialog(c)
}

func (v *automationsView) workflowRow(c *ui.Context, wf core.Workflow) {
	t, th := theme.Of(c), c.Theme()
	isNative := wf.Kind == core.WorkflowKindNative

	badgeText := i18n.T("settings.automations.custom_badge")
	if isNative {
		badgeText = i18n.T("settings.automations.native_badge")
	}

	displayName := wf.Name
	if isNative {
		switch wf.PresetID {
		case core.PresetPurgeTrash:
			displayName = i18n.T("settings.automations.native_trash_title")
		case core.PresetPurgeJunk:
			displayName = i18n.T("settings.automations.native_junk_title")
		case core.PresetSecurityJunk:
			displayName = i18n.T("settings.automations.native_security_title")
		}
	}

	ui.Row(c).FillWidth().Gap(th.Space(4)).Padding(th.Space(3), th.Space(4)).MinHeight(th.Space(14)).AlignItems(ui.Center).Children(func() {
		// Left column: Badge + Name, and Condition chips + Action + Trigger
		ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(1.5)).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
				// Render pill matching Extensions / Plugins settings style
				statusPill(c, badgeText, false)
				kit.Text(c, displayName, t.Text.Body).FontWeight(600)
				if (v.accountID == 0 || len(v.a.accounts) > 1) && wf.AccountID > 0 {
					accEmail := v.accountEmail(wf.AccountID)
					if accEmail != "" {
						ui.Row(c).AlignItems(ui.Center).Padding(th.Space(0.25), th.Space(1.5)).
							Radius(t.Radius.Sm).Background(t.Hover).Children(func() {
							kit.Text(c, accEmail, t.Text.Xs).TextColor(t.Muted).FontWeight(500)
						})
					}
				}
			})
			// Chips row: Trigger | Scope | Filters | Action
			ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Wrap().Children(func() {
				triggerStr := triggerLabel(wf.Trigger)
				if wf.Trigger == core.WorkflowTriggerLabel {
					lbl := wf.Config.TriggerLabelName
					if lbl == "" && wf.Config.EffectiveTriggerLabelID() != 0 {
						for _, l := range v.labels {
							if l.ID == wf.Config.EffectiveTriggerLabelID() {
								lbl = l.Name
								break
							}
						}
					}
					if lbl != "" {
						triggerStr = fmt.Sprintf("%s (%s)", triggerLabel(wf.Trigger), lbl)
					}
				} else if wf.Trigger == core.WorkflowTriggerSecurity {
					threat := wf.Config.EffectiveThreatAssessment()
					if threat != "" {
						triggerStr = fmt.Sprintf("%s (%s)", triggerLabel(wf.Trigger), threatChipLabel(threat))
					}
				}
				workflowChip(c, i18n.T("settings.automations.trigger"), triggerStr)

				scopeStr := v.scopeSummary(wf)
				workflowChip(c, i18n.T("settings.automations.scope"), scopeStr)

				filterStr := v.filterSummary(wf)
				workflowChip(c, i18n.T("settings.automations.filters"), filterStr)

				actionStr := v.actionsSummary(wf)
				workflowChip(c, i18n.T("settings.automations.action"), actionStr)
			})
		})

		// Right column: Selects, Switch, Action Buttons
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2.5)).Children(func() {
			if isNative && (wf.PresetID == core.PresetPurgeTrash || wf.PresetID == core.PresetPurgeJunk) {
				currentDays := wf.Config.Days
				if settingSelect(c, i18n.T("settings.automations.field.days_select"), &currentDays, purgeDayOptions()).Changed() {
					v.updatePurgeDays(wf, currentDays)
				}
			}

			// Active Switch
			en := wf.Enabled
			if kit.Switch(c, &en).Label(displayName).Changed() {
				v.toggleEnabled(wf, en)
			}

			// Custom workflows have Edit and Delete buttons
			if !isNative {
				kit.IconButton(c, "square-pen", i18n.T("settings.automations.edit"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
					OnClick(func() { v.openBuilder(&wf) })
				kit.IconButton(c, "trash-2", i18n.T("settings.automations.delete"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
					OnClick(func() {
						v.deleteTarget = &wf
						v.deleteOpen = true
					})
			}
		})
	})
}

func workflowChip(c *ui.Context, label, val string) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1)).Padding(th.Space(0.5), th.Space(2)).
		Radius(t.Radius.Sm).Background(t.Hover).Children(func() {
		kit.Text(c, label+":", t.Text.Sm).TextColor(t.Muted)
		kit.Text(c, val, t.Text.Sm).FontWeight(500)
	})
}

func (v *automationsView) scopeSummary(wf core.Workflow) string {
	if wf.Config.FolderRole != "" {
		return folderLabel(wf.Config.FolderRole)
	}
	return i18n.T("settings.automations.scope_all_folders")
}

func (v *automationsView) filterSummary(wf core.Workflow) string {
	if wf.Kind == core.WorkflowKindNative {
		switch wf.PresetID {
		case core.PresetPurgeTrash, core.PresetPurgeJunk:
			d := wf.Config.Days
			if d <= 0 {
				return i18n.T("settings.account.mailbox.none")
			}
			return fmt.Sprintf("> %d Tage", d)
		case core.PresetSecurityJunk:
			return i18n.T("settings.account.mailbox.none")
		}
	}

	var parts []string
	if wf.Config.Days > 0 {
		if wf.Config.Days == 1 {
			parts = append(parts, i18n.T("settings.automations.older_than_one"))
		} else {
			parts = append(parts, fmt.Sprintf("> %d Tage", wf.Config.Days))
		}
	}
	if wf.Config.LabelName != "" || wf.Config.LabelID != 0 {
		if wf.Trigger != core.WorkflowTriggerLabel || (wf.Config.LabelID != 0 && wf.Config.LabelID != wf.Config.EffectiveTriggerLabelID()) {
			if wf.Config.LabelName != "" {
				parts = append(parts, "Label: "+wf.Config.LabelName)
			} else {
				parts = append(parts, "Label ID: "+strconv.FormatInt(wf.Config.LabelID, 10))
			}
		}
	}
	threat := wf.Config.EffectiveThreatAssessment()
	if threat != "" {
		secLabel := threat
		switch threat {
		case "warning":
			secLabel = i18n.T("settings.automations.field.threat_warning")
		case "suspicious":
			secLabel = i18n.T("settings.automations.field.threat_suspicious")
		case "clean":
			secLabel = i18n.T("settings.automations.field.threat_clean")
		}
		parts = append(parts, "Bedrohung: "+secLabel)
	}
	if wf.Config.DomainAuth != "" {
		authLabel := wf.Config.DomainAuth
		switch wf.Config.DomainAuth {
		case "verified":
			authLabel = i18n.T("settings.automations.field.domain_auth_verified")
		case "failed":
			authLabel = i18n.T("settings.automations.field.domain_auth_failed")
		case "unverified":
			authLabel = i18n.T("settings.automations.field.domain_auth_unverified")
		}
		parts = append(parts, "Domain: "+authLabel)
	}
	if wf.Config.HasAttachments != nil {
		if *wf.Config.HasAttachments {
			parts = append(parts, i18n.T("settings.automations.field.has_att"))
		} else {
			parts = append(parts, i18n.T("settings.automations.field.no_att"))
		}
	}
	if wf.Config.FromPattern != "" {
		parts = append(parts, "Absender: "+wf.Config.FromPattern)
	}
	if wf.Config.SubjectPattern != "" {
		opLabel := wf.Config.SubjectOp
		switch opLabel {
		case "equals":
			opLabel = "="
		case "starts_with":
			opLabel = "^="
		default:
			opLabel = "~"
		}
		parts = append(parts, fmt.Sprintf("Betreff %s %q", opLabel, wf.Config.SubjectPattern))
	}

	if len(parts) == 0 {
		return i18n.T("settings.account.mailbox.none")
	}
	sep := " · "
	if wf.Config.CondLogic == "or" {
		sep = " ODER "
	}
	return strings.Join(parts, sep)
}

func (v *automationsView) conditionSummary(wf core.Workflow) string {
	s := v.scopeSummary(wf)
	f := v.filterSummary(wf)
	if f == i18n.T("settings.account.mailbox.none") {
		return s
	}
	return s + " · " + f
}

func (v *automationsView) actionsSummary(wf core.Workflow) string {
	actions := wf.Config.Actions
	if len(actions) == 0 && wf.Action != "" {
		actions = []core.WorkflowActionItem{{Type: wf.Action, LabelID: wf.Config.LabelID, LabelName: wf.Config.LabelName}}
	}
	actions = core.SortActions(actions)
	if len(actions) > 0 {
		var names []string
		for _, a := range actions {
			lbl := actionLabel(a.Type)
			if (a.Type == core.WorkflowActionApplyLabel || a.Type == core.WorkflowActionRemoveLabel) && a.LabelName != "" {
				lbl += fmt.Sprintf(" (%s)", a.LabelName)
			}
			if a.Type == core.WorkflowActionForward && a.ForwardTo != "" {
				lbl += fmt.Sprintf(" (%s)", a.ForwardTo)
			}
			if a.Type == core.WorkflowActionExportEML || a.Type == core.WorkflowActionExportPDF || a.Type == core.WorkflowActionExportAttachments || a.Type == core.WorkflowActionWitExtract {
				dir := a.TargetDirectory
				if dir == "" {
					dir = wf.Config.TargetDirectory
				}
				if dir != "" {
					lbl += fmt.Sprintf(" [%s]", filepath.Base(dir))
				}
			}
			names = append(names, lbl)
		}
		return strings.Join(names, " → ")
	}
	return actionLabel(wf.Action)
}

// openBuilder initializes the work-tree builder. For a new workflow, all values start completely clean.
func (v *automationsView) openBuilder(wf *core.Workflow) {
	v.builderOpen = true
	v.confirmOpen = false
	v.newCondChoice = ""
	v.newActionChoice = ""

	if wf == nil {
		v.editID = 0
		v.name = ""
		v.trigger = "" // Clean start: No trigger selected yet
		v.triggerLabelID = 0
		v.triggerLabelName = ""
		v.condLogic = "and"
		v.folderRole = "inbox" // Default scope is inbox
		v.olderThanDays = 0
		v.labelID = 0
		v.labelName = ""
		v.threatAssessment = ""
		v.domainAuth = ""
		v.securityStatus = ""
		v.hasAttachments = 0
		v.fromPattern = ""
		v.subjectPattern = ""
		v.subjectOp = "contains"
		v.targetDirectory = ""
		v.bundleSubfolder = true
		v.pickingDir = false
		v.forwardTo = ""
		v.instructions = ""
		v.extractTemplate = ""
		v.enabled = true
		v.activeConds = nil   // Clean start: 0 filters
		v.activeActions = nil // Clean start: 0 actions
		if v.accountID == 0 {
			v.builderAllAccounts = true
			v.builderAccountIDs = nil
			for _, acc := range v.a.accounts {
				v.builderAccountIDs = append(v.builderAccountIDs, acc.ID)
			}
		} else {
			v.builderAllAccounts = false
			v.builderAccountIDs = []int64{v.accountID}
		}
	} else {
		v.editID = wf.ID
		v.builderAllAccounts = false
		v.builderAccountIDs = []int64{wf.AccountID}
		v.name = wf.Name
		v.trigger = wf.Trigger
		v.triggerLabelID = wf.Config.EffectiveTriggerLabelID()
		v.triggerLabelName = wf.Config.TriggerLabelName
		if v.triggerLabelID != 0 && v.triggerLabelName == "" {
			for _, l := range v.labels {
				if l.ID == v.triggerLabelID {
					v.triggerLabelName = l.Name
					break
				}
			}
		}
		v.condLogic = wf.Config.CondLogic
		if v.condLogic == "" {
			v.condLogic = "and"
		}
		v.folderRole = wf.Config.FolderRole
		if v.folderRole == "" && wf.Trigger == core.WorkflowTriggerDaily {
			v.folderRole = "inbox"
		}
		v.olderThanDays = wf.Config.Days
		v.labelID = wf.Config.LabelID
		v.labelName = wf.Config.LabelName
		v.threatAssessment = wf.Config.EffectiveThreatAssessment()
		v.domainAuth = wf.Config.DomainAuth
		v.securityStatus = v.threatAssessment
		v.hasAttachments = 0
		if wf.Config.HasAttachments != nil {
			if *wf.Config.HasAttachments {
				v.hasAttachments = 1
			} else {
				v.hasAttachments = 2
			}
		}
		v.fromPattern = wf.Config.FromPattern
		v.subjectPattern = wf.Config.SubjectPattern
		v.subjectOp = wf.Config.SubjectOp
		if v.subjectOp == "" {
			v.subjectOp = "contains"
		}
		v.targetDirectory = wf.Config.TargetDirectory
		if wf.Config.BundleSubfolder != nil {
			v.bundleSubfolder = *wf.Config.BundleSubfolder
		} else {
			foundBundle := false
			for _, a := range wf.Config.Actions {
				if a.BundleSubfolder != nil {
					v.bundleSubfolder = *a.BundleSubfolder
					foundBundle = true
					break
				}
			}
			if !foundBundle {
				v.bundleSubfolder = true
			}
		}
		if v.targetDirectory == "" {
			for _, a := range wf.Config.Actions {
				if a.TargetDirectory != "" {
					v.targetDirectory = a.TargetDirectory
					break
				}
			}
		}
		v.pickingDir = false
		v.forwardTo = wf.Config.ForwardTo
		v.instructions = wf.Config.Instructions
		v.extractTemplate = wf.Config.ExtractTemplate
		v.enabled = wf.Enabled

		// Populate active filters only for fields actually configured (folder is managed as Scope)
		var conds []string
		if v.threatAssessment != "" {
			conds = append(conds, CondThreat)
		}
		if v.domainAuth != "" {
			conds = append(conds, CondDomainAuth)
		}
		if wf.Config.Days > 0 {
			conds = append(conds, CondDays)
		}
		if wf.Trigger == core.WorkflowTriggerLabel {
			if wf.Config.LabelID != 0 && wf.Config.LabelID != v.triggerLabelID {
				conds = append(conds, CondLabel)
			}
		} else if wf.Config.LabelID != 0 || wf.Config.LabelName != "" {
			conds = append(conds, CondLabel)
		}
		if wf.Config.HasAttachments != nil {
			conds = append(conds, CondHasAtt)
		}
		if wf.Config.FromPattern != "" {
			conds = append(conds, CondFrom)
		}
		if wf.Config.SubjectPattern != "" {
			conds = append(conds, CondSubject)
		}
		v.activeConds = conds

		// Populate actions in deterministic canonical order
		if len(wf.Config.Actions) > 0 {
			v.activeActions = core.SortActions(wf.Config.Actions)
		} else if wf.Action != "" {
			v.activeActions = core.SortActions([]core.WorkflowActionItem{{Type: wf.Action, LabelID: wf.Config.LabelID, LabelName: wf.Config.LabelName}})
		} else {
			v.activeActions = nil
		}
	}
}

func (v *automationsView) onTriggerChanged(newTrigger string) {
	v.trigger = newTrigger
	if newTrigger == core.WorkflowTriggerDaily && v.folderRole == "" {
		v.folderRole = "inbox"
	}
	if newTrigger == core.WorkflowTriggerLabel && v.triggerLabelID == 0 && len(v.labels) > 0 {
		v.triggerLabelID = v.labels[0].ID
		v.triggerLabelName = v.labels[0].Name
	}
	if newTrigger == core.WorkflowTriggerSecurity && v.threatAssessment == "" {
		v.threatAssessment = "warning"
	}

	// Filter out conditions not valid for new trigger
	allowedConds := v.allowedConditionsForTrigger(newTrigger)
	var validConds []string
	for _, c := range v.activeConds {
		if slices.Contains(allowedConds, c) {
			validConds = append(validConds, c)
		}
	}
	v.activeConds = validConds

	// Filter out actions not valid for new trigger
	allowedActs := v.allowedActionsForTrigger(newTrigger)
	var validActs []core.WorkflowActionItem
	for _, a := range v.activeActions {
		if slices.Contains(allowedActs, a.Type) {
			validActs = append(validActs, a)
		}
	}
	v.activeActions = validActs
}

func (v *automationsView) allowedConditionsForTrigger(trigger string) []string {
	switch trigger {
	case core.WorkflowTriggerDaily:
		return []string{CondDays, CondFrom, CondSubject, CondLabel, CondHasAtt, CondThreat, CondDomainAuth}
	case core.WorkflowTriggerReceive:
		return []string{CondFrom, CondSubject, CondLabel, CondHasAtt, CondThreat, CondDomainAuth}
	case core.WorkflowTriggerLabel:
		return []string{CondFrom, CondSubject, CondLabel, CondHasAtt, CondThreat, CondDomainAuth}
	case core.WorkflowTriggerSecurity:
		return []string{CondFrom, CondSubject, CondLabel, CondHasAtt, CondDomainAuth}
	default:
		return nil
	}
}

func (v *automationsView) allowedActionsForTrigger(trigger string) []string {
	baseActs := []string{
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
		core.WorkflowActionArchive,
		core.WorkflowActionJunk,
		core.WorkflowActionTrash,
	}
	switch trigger {
	case core.WorkflowTriggerDaily:
		acts := append([]string{}, baseActs...)
		if v.folderRole == "trash" || v.folderRole == "junk" {
			acts = append([]string{core.WorkflowActionPurge}, acts...)
		}
		return acts
	case core.WorkflowTriggerLabel, core.WorkflowTriggerReceive, core.WorkflowTriggerSecurity:
		return baseActs
	default:
		return nil
	}
}

func (v *automationsView) buildWorkflowInput() core.WorkflowInput {
	var att *bool
	if v.hasAttachments == 1 {
		b := true
		att = &b
	} else if v.hasAttachments == 2 {
		b := false
		att = &b
	}
	days := 0
	if slices.Contains(v.activeConds, CondDays) {
		days = v.olderThanDays
	}
	fromPat := ""
	if slices.Contains(v.activeConds, CondFrom) {
		fromPat = v.fromPattern
	}
	subjPat := ""
	subjOp := ""
	if slices.Contains(v.activeConds, CondSubject) {
		subjPat = v.subjectPattern
		subjOp = v.subjectOp
	}
	lblID := int64(0)
	lblName := ""
	if slices.Contains(v.activeConds, CondLabel) {
		lblID = v.labelID
		lblName = v.labelName
	}
	threat := ""
	if v.trigger == core.WorkflowTriggerSecurity || slices.Contains(v.activeConds, CondThreat) {
		threat = v.threatAssessment
	}
	auth := ""
	if slices.Contains(v.activeConds, CondDomainAuth) {
		auth = v.domainAuth
	}

	accID := v.accountID
	if accID == 0 {
		accID = 1 // default for validation when in UI builder
	}

	acts := append([]core.WorkflowActionItem{}, v.activeActions...)
	for i := range acts {
		if acts[i].Type == core.WorkflowActionExportEML || acts[i].Type == core.WorkflowActionExportPDF ||
			acts[i].Type == core.WorkflowActionExportAttachments || acts[i].Type == core.WorkflowActionWitExtract {
			acts[i].TargetDirectory = v.targetDirectory
			bundleVal := v.bundleSubfolder
			acts[i].BundleSubfolder = &bundleVal
		}
	}

	return core.WorkflowInput{
		AccountID: accID,
		Name:      v.name,
		Trigger:   v.trigger,
		Config: core.WorkflowConfig{
			Days:             days,
			FolderRole:       v.folderRole,
			TriggerLabelID:   v.triggerLabelID,
			TriggerLabelName: v.triggerLabelName,
			LabelID:          lblID,
			LabelName:        lblName,
			ThreatAssessment: threat,
			DomainAuth:       auth,
			SecurityStatus:   threat,
			HasAttachments:   att,
			FromPattern:      fromPat,
			SubjectPattern:   subjPat,
			SubjectOp:        subjOp,
			CondLogic:        v.condLogic,
			TargetDirectory:  v.targetDirectory,
			BundleSubfolder:  &v.bundleSubfolder,
			ForwardTo:        v.forwardTo,
			Instructions:     v.instructions,
			ExtractTemplate:  v.extractTemplate,
			Actions:          acts,
		},
		ConfirmPurge: true, // confirmed in dialog
		Enabled:      v.enabled,
	}
}

// validate ensures the workflow is logically coherent and prevents dangerous mistakes using the central validation logic
func (v *automationsView) validate() (valid bool, warnReason string) {
	if strings.TrimSpace(v.name) == "" {
		return false, "Bitte gib einen Namen für den Workflow ein."
	}
	if v.trigger == "" {
		return false, i18n.T("settings.automations.no_trigger_selected")
	}
	if v.trigger == core.WorkflowTriggerSecurity && v.threatAssessment == "" {
		return false, "Bitte wähle eine Bedrohungsstufe aus (z. B. Bösartig, Phishing oder Verdächtig)."
	}
	if len(v.activeActions) == 0 {
		return false, i18n.T("settings.automations.no_actions_yet")
	}

	in := v.buildWorkflowInput()
	if valErr := core.ValidateWorkflowInput(in); valErr != nil {
		if valErr.Code == "ERR_PURGE_FOLDER_INVALID" {
			return false, i18n.T("settings.automations.err_purge_folder")
		}
		return false, valErr.Message
	}

	return true, ""
}

// builderTreeCard renders the progressive Work-Tree editor inline
func (v *automationsView) builderTreeCard(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()

	title := i18n.T("settings.automations.builder_title")
	if v.editID != 0 {
		title = i18n.T("settings.automations.builder_edit")
	}

	isValid, warnMsg := v.validate()

	ui.Column(c).FillWidth().Padding(th.Space(5), th.Space(5)).Gap(th.Space(4)).Children(func() {
		// Header row
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			statusPill(c, "Work-Tree", false)
			kit.Text(c, title, t.Text.Body).FontWeight(600)
			ui.Spacer(c)
			kit.IconButton(c, "x", i18n.T("common.close"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
				OnClick(func() { v.builderOpen = false })
		})

		// Workflow Name
		kit.Field(c, i18n.T("settings.automations.field.name"), "", "", func() {
			kit.Input(c, &v.name, kit.InputOpts{Placeholder: "z.B. Werbemails nach 30 Tagen archivieren"}).FillWidth()
		})

		// Geltungsbereich (Konten) bei mehreren Konten
		if len(v.a.accounts) > 1 {
			kit.Field(c, i18n.T("settings.automations.field.accounts"), "", "", func() {
				ui.Column(c).Gap(th.Space(1.5)).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
						if kit.Checkbox(c, &v.builderAllAccounts, i18n.T("settings.automations.all_accounts")).Changed() {
							if v.builderAllAccounts {
								v.builderAccountIDs = nil
								for _, acc := range v.a.accounts {
									v.builderAccountIDs = append(v.builderAccountIDs, acc.ID)
								}
							}
						}
					})
					if !v.builderAllAccounts {
						ui.Row(c).AlignItems(ui.Center).Gap(th.Space(3)).Wrap().Children(func() {
							for _, acc := range v.a.accounts {
								accID := acc.ID
								isSel := slices.Contains(v.builderAccountIDs, accID)
								if kit.Checkbox(c.Key(accID), &isSel, accountName(acc)).Changed() {
									if isSel {
										if !slices.Contains(v.builderAccountIDs, accID) {
											v.builderAccountIDs = append(v.builderAccountIDs, accID)
										}
									} else {
										v.builderAccountIDs = slices.DeleteFunc(v.builderAccountIDs, func(id int64) bool { return id == accID })
									}
								}
							}
						})
					}
				})
			})
		}

		// ── STEP 1: AUSLÖSER (TRIGGER) ──
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			stepBadge(c, "1")
			kit.Text(c, i18n.T("settings.automations.step_trigger"), t.Text.Body).FontWeight(600)
		})
		ui.Column(c).Padding(0, 0, 0, th.Space(8)).Gap(th.Space(1.5)).Children(func() {
			prevTrigger := v.trigger
			sel := settingSelect(c, i18n.T("settings.automations.trigger"), &v.trigger, triggerOptions())
			if sel.Changed() && v.trigger != prevTrigger {
				v.onTriggerChanged(v.trigger)
			}
			// Trigger explanation subtext
			kit.Text(c, v.triggerDescription(v.trigger), t.Text.Sm).TextColor(t.Muted)

			// Bei on_security ist die Bedrohungsstufe ein Pflichtparameter des Triggers
			if v.trigger == core.WorkflowTriggerSecurity {
				ui.Row(c).FillWidth().Margin(th.Space(2), 0, 0, 0).Padding(th.Space(2), th.Space(3)).Radius(t.Radius.Md).
					Background(t.Card).Border(theme.Hairline, t.Border).AlignItems(ui.Center).Gap(th.Space(3)).Children(func() {
					renderBranchHeader(c, "shield", i18n.T("settings.automations.field.threat"))
					settingSelect(c, i18n.T("settings.automations.field.threat"), &v.threatAssessment, threatOptions()).Grow(1)
				})
			}

			// Bei on_label ist das Auslöser-Label ein Pflichtparameter des Triggers
			if v.trigger == core.WorkflowTriggerLabel {
				ui.Row(c).FillWidth().Margin(th.Space(2), 0, 0, 0).Padding(th.Space(2), th.Space(3)).Radius(t.Radius.Md).
					Background(t.Card).Border(theme.Hairline, t.Border).AlignItems(ui.Center).Gap(th.Space(3)).Children(func() {
					renderBranchHeader(c, "tag", i18n.T("settings.automations.trigger_label_required"))
					if len(v.labels) > 0 {
						var lOpts []kit.Option[int64]
						for _, l := range v.labels {
							lOpts = append(lOpts, kit.Option[int64]{Value: l.ID, Label: l.Name})
						}
						sel := settingSelect(c, i18n.T("settings.automations.trigger_label_required"), &v.triggerLabelID, lOpts).Grow(1)
						if sel.Changed() {
							for _, l := range v.labels {
								if l.ID == v.triggerLabelID {
									v.triggerLabelName = l.Name
									break
								}
							}
						}
					} else {
						kit.Text(c, "("+i18n.T("settings.automations.no_labels")+")", t.Text.Sm).TextColor(t.Muted).Grow(1)
					}
				})
				kit.Text(c, i18n.T("settings.automations.trigger_label_help"), t.Text.Sm).TextColor(t.Muted)
			}
		})

		// Tree Connector: 1 -> 2
		treeBranchConnector(c)

		// ── STEP 2: GELTUNGSBEREICH (SCOPE) ──
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			stepBadge(c, "2")
			kit.Text(c, i18n.T("settings.automations.step_scope"), t.Text.Body).FontWeight(600)
		})
		ui.Column(c).Padding(0, 0, 0, th.Space(8)).Gap(th.Space(1.5)).Children(func() {
			if v.trigger == "" {
				ui.Row(c).FillWidth().Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Md).
					Background(t.Hover).AlignItems(ui.Center).Children(func() {
					kit.Text(c, i18n.T("settings.automations.select_trigger_first"), t.Text.Sm).TextColor(t.Muted)
				})
			} else {
				ui.Row(c).FillWidth().Padding(th.Space(2), th.Space(3)).Radius(t.Radius.Md).
					Background(t.Card).Border(theme.Hairline, t.Border).AlignItems(ui.Center).Gap(th.Space(3)).Children(func() {
					renderBranchHeader(c, "folder", i18n.T("settings.automations.field.folder"))
					prevRole := v.folderRole
					opts := scopeOptionsForTrigger(v.trigger)
					sel := settingSelect(c, i18n.T("settings.automations.field.folder"), &v.folderRole, opts).Grow(1)
					if sel.Changed() && v.folderRole != prevRole {
						if v.folderRole != "trash" && v.folderRole != "junk" {
							var filteredActs []core.WorkflowActionItem
							for _, a := range v.activeActions {
								if a.Type != core.WorkflowActionPurge {
									filteredActs = append(filteredActs, a)
								}
							}
							v.activeActions = filteredActs
						}
					}
				})
				switch v.trigger {
				case core.WorkflowTriggerDaily:
					kit.Text(c, i18n.T("settings.automations.scope_daily_help"), t.Text.Sm).TextColor(t.Muted)
				case core.WorkflowTriggerReceive:
					kit.Text(c, i18n.T("settings.automations.scope_receive_help"), t.Text.Sm).TextColor(t.Muted)
				case core.WorkflowTriggerLabel:
					kit.Text(c, i18n.T("settings.automations.scope_label_help"), t.Text.Sm).TextColor(t.Muted)
				default:
					kit.Text(c, i18n.T("settings.automations.scope_sub"), t.Text.Sm).TextColor(t.Muted)
				}
			}
		})

		// Tree Connector: 2 -> 3
		treeBranchConnector(c)

		// ── STEP 3: FILTER (FILTERS - MODULAR BRANCHES) ──
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			stepBadge(c, "3")
			kit.Text(c, i18n.T("settings.automations.step_filters"), t.Text.Body).FontWeight(600)
			ui.Spacer(c)
			if len(v.activeConds) > 1 {
				// AND / OR matching selector
				condLogicOpts := []kit.Option[string]{
					{Value: "and", Label: i18n.T("settings.automations.logic.and")},
					{Value: "or", Label: i18n.T("settings.automations.logic.or")},
				}
				settingSelect(c, i18n.T("settings.automations.logic_label"), &v.condLogic, condLogicOpts)
			}
		})

		// Active filter branches box
		ui.Column(c).Padding(0, 0, 0, th.Space(8)).Gap(th.Space(2)).Children(func() {
			if len(v.activeConds) > 1 {
				kit.Text(c, i18n.T("settings.automations.logic_help"), t.Text.Sm).TextColor(t.Muted)
			}
			if v.trigger == "" {
				ui.Row(c).FillWidth().Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Md).
					Background(t.Hover).AlignItems(ui.Center).Children(func() {
					kit.Text(c, i18n.T("settings.automations.select_trigger_first"), t.Text.Sm).TextColor(t.Muted)
				})
			} else if len(v.activeConds) == 0 {
				ui.Row(c).FillWidth().Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Md).
					Background(t.Hover).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
					ui.Icon(c, icons.Get("info")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
					kit.Text(c, i18n.T("settings.automations.no_filters_notice"), t.Text.Sm).TextColor(t.Muted)
				})
			} else {
				for _, condKey := range v.activeConds {
					ck := condKey
					v.renderConditionBranch(c, ck)
				}
			}

			// Add Filter Branch Selector (Only allowed and not yet added)
			if v.trigger != "" {
				allowed := v.allowedConditionsForTrigger(v.trigger)
				var availableConds []kit.Option[string]
				availableConds = append(availableConds, kit.Option[string]{Value: "", Label: i18n.T("settings.automations.add_filter")})

				if slices.Contains(allowed, CondFrom) && !slices.Contains(v.activeConds, CondFrom) {
					availableConds = append(availableConds, kit.Option[string]{Value: CondFrom, Label: i18n.T("settings.automations.field.from")})
				}
				if slices.Contains(allowed, CondSubject) && !slices.Contains(v.activeConds, CondSubject) {
					availableConds = append(availableConds, kit.Option[string]{Value: CondSubject, Label: i18n.T("settings.automations.field.subject")})
				}
				if slices.Contains(allowed, CondLabel) && !slices.Contains(v.activeConds, CondLabel) {
					availableConds = append(availableConds, kit.Option[string]{Value: CondLabel, Label: i18n.T("settings.automations.field.label_additional")})
				}
				if slices.Contains(allowed, CondHasAtt) && !slices.Contains(v.activeConds, CondHasAtt) {
					availableConds = append(availableConds, kit.Option[string]{Value: CondHasAtt, Label: i18n.T("settings.automations.field.has_att")})
				}
				if slices.Contains(allowed, CondThreat) && !slices.Contains(v.activeConds, CondThreat) {
					availableConds = append(availableConds, kit.Option[string]{Value: CondThreat, Label: i18n.T("settings.automations.field.threat")})
				}
				if slices.Contains(allowed, CondDomainAuth) && !slices.Contains(v.activeConds, CondDomainAuth) {
					availableConds = append(availableConds, kit.Option[string]{Value: CondDomainAuth, Label: i18n.T("settings.automations.field.domain_auth")})
				}
				if slices.Contains(allowed, CondDays) && !slices.Contains(v.activeConds, CondDays) {
					availableConds = append(availableConds, kit.Option[string]{Value: CondDays, Label: i18n.T("settings.automations.field.days_select")})
				}

				if len(availableConds) > 1 {
					ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Margin(th.Space(1), 0, 0, 0).Children(func() {
						sel := settingSelect(c, i18n.T("settings.automations.add_filter"), &v.newCondChoice, availableConds)
						if sel.Changed() && v.newCondChoice != "" {
							v.activeConds = append(v.activeConds, v.newCondChoice)
							// Set sensible defaults for newly added filter
							if v.newCondChoice == CondDays && v.olderThanDays == 0 {
								v.olderThanDays = 30
							}
							if v.newCondChoice == CondThreat && v.threatAssessment == "" {
								v.threatAssessment = "warning"
							}
							if v.newCondChoice == CondDomainAuth && v.domainAuth == "" {
								v.domainAuth = "verified"
							}
							if v.newCondChoice == CondHasAtt && v.hasAttachments == 0 {
								v.hasAttachments = 1
							}
							if v.newCondChoice == CondSubject && v.subjectOp == "" {
								v.subjectOp = "contains"
							}
							if v.newCondChoice == CondLabel && v.labelID == 0 && len(v.labels) > 0 {
								v.labelID = v.labels[0].ID
								v.labelName = v.labels[0].Name
							}
							v.newCondChoice = ""
						}
					})
				}
			}
		})

		// Tree Connector: 3 -> 4
		treeBranchConnector(c)

		// ── STEP 4: AKTIONEN (ACTIONS - MODULAR COMBINED BRANCHES) ──
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			stepBadge(c, "4")
			kit.Text(c, i18n.T("settings.automations.step_actions"), t.Text.Body).FontWeight(600)
		})

		// Active actions branches box
		ui.Column(c).Padding(0, 0, 0, th.Space(8)).Gap(th.Space(2)).Children(func() {
			if len(v.activeActions) > 1 {
				kit.Text(c, i18n.T("settings.automations.actions_order_hint"), t.Text.Sm).TextColor(t.Muted)
			}
			if v.trigger == "" {
				ui.Row(c).FillWidth().Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Md).
					Background(t.Hover).AlignItems(ui.Center).Children(func() {
					kit.Text(c, i18n.T("settings.automations.select_trigger_first"), t.Text.Sm).TextColor(t.Muted)
				})
			} else if len(v.activeActions) == 0 {
				ui.Row(c).FillWidth().Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Md).
					Background(t.Hover).AlignItems(ui.Center).Children(func() {
					kit.Text(c, i18n.T("settings.automations.no_actions_yet"), t.Text.Sm).TextColor(t.Muted)
				})
			} else {
				for i := range v.activeActions {
					idx := i
					v.renderActionBranch(c, idx)
				}
				if v.hasExportAction() {
					v.renderExportDestinationCard(c)
				}
			}

			// Add Action Branch Selector (Filters conflicting actions)
			if v.trigger != "" {
				availableActs := v.availableActionOptions()
				if len(availableActs) > 1 {
					ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Margin(th.Space(1), 0, 0, 0).Children(func() {
						sel := settingSelect(c, i18n.T("settings.automations.add_action"), &v.newActionChoice, availableActs)
						if sel.Changed() && v.newActionChoice != "" {
							v.addAction(v.newActionChoice)
							v.newActionChoice = ""
						}
					})
				}
			}
		})

		// Validation warning callout if invalid
		if !isValid && warnMsg != "" {
			ui.Row(c).FillWidth().Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Md).
				Background(t.WarningBg).Border(theme.Hairline, t.WarningBorder).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
				ui.Icon(c, icons.Get("triangle-alert")).Size(theme.IconSm, theme.IconSm).TextColor(t.WarningText)
				kit.Text(c, warnMsg, t.Text.Sm).TextColor(t.WarningText).FontWeight(500)
			})
		}

		// ── STEP 5: VORSCHAU & AKTIVIERUNG (FOOTER BAR) ──
		ui.Row(c).FillWidth().AlignItems(ui.Center).Margin(th.Space(3), 0, 0, 0).Children(func() {
			kit.Switch(c, &v.enabled).Label(i18n.T("settings.automations.active"))
			kit.Text(c, i18n.T("settings.automations.active"), t.Text.Sm).FontWeight(500).Margin(0, 0, 0, th.Space(2))
			ui.Spacer(c)
			kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).
				OnClick(func() { v.builderOpen = false })
			ui.Box(c).Width(th.Space(2))
			kit.Button(c, i18n.T("common.save"), kit.ButtonOpts{Disabled: !isValid}).
				OnClick(func() {
					if isValid {
						if v.a != nil && v.a.svc != nil {
							in := v.buildWorkflowInput()
							p, _ := v.a.svc.PreviewWorkflow(context.Background(), in)
							v.preview = p
						}
						v.confirmOpen = true // Open explanation & confirmation modal
					}
				})
		})
	})
}

// renderConditionBranch renders a single modular branch in the work tree
func (v *automationsView) renderConditionBranch(c *ui.Context, condKey string) {
	t, th := theme.Of(c), c.Theme()

	ui.Row(c).FillWidth().Padding(th.Space(2), th.Space(3)).Radius(t.Radius.Md).
		Background(t.Card).Border(theme.Hairline, t.Border).AlignItems(ui.Center).Gap(th.Space(3)).Children(func() {
		// Category icon and label using native Godwit Remix SVG icons
		switch condKey {
		case CondThreat, CondSecurity:
			renderBranchHeader(c, "triangle-alert", i18n.T("settings.automations.field.threat"))
			settingSelect(c, i18n.T("settings.automations.field.threat"), &v.threatAssessment, threatOptions()).Grow(1)
		case CondDomainAuth:
			renderBranchHeader(c, "shield-check", i18n.T("settings.automations.field.domain_auth"))
			settingSelect(c, i18n.T("settings.automations.field.domain_auth"), &v.domainAuth, domainAuthOptions()).Grow(1)
		case CondFolder:
			renderBranchHeader(c, "folder", i18n.T("settings.automations.field.folder"))
			prevRole := v.folderRole
			sel := settingSelect(c, i18n.T("settings.automations.field.folder"), &v.folderRole, folderOptions()).Grow(1)
			if sel.Changed() && v.folderRole != prevRole {
				// If folder changed and purge was active but folder is no longer trash/junk, remove purge
				if v.folderRole != "trash" && v.folderRole != "junk" {
					var filteredActs []core.WorkflowActionItem
					for _, a := range v.activeActions {
						if a.Type != core.WorkflowActionPurge {
							filteredActs = append(filteredActs, a)
						}
					}
					v.activeActions = filteredActs
				}
			}
		case CondDays:
			renderBranchHeader(c, "sliders-horizontal", i18n.T("settings.automations.field.days_select"))
			settingSelect(c, i18n.T("settings.automations.field.days_select"), &v.olderThanDays, olderThanOptions()).Grow(1)
		case CondLabel:
			renderBranchHeader(c, "tag", i18n.T("settings.automations.field.label_additional"))
			if len(v.labels) > 0 {
				var lOpts []kit.Option[int64]
				for _, l := range v.labels {
					lOpts = append(lOpts, kit.Option[int64]{Value: l.ID, Label: l.Name})
				}
				settingSelect(c, i18n.T("settings.automations.field.label_additional"), &v.labelID, lOpts).Grow(1)
			} else {
				kit.Text(c, "("+i18n.T("settings.automations.no_labels")+")", t.Text.Sm).TextColor(t.Muted).Grow(1)
			}
		case CondHasAtt:
			renderBranchHeader(c, "paperclip", i18n.T("settings.automations.field.has_att"))
			settingSelect(c, i18n.T("settings.automations.field.has_att"), &v.hasAttachments, attachmentOptions()).Grow(1)
		case CondFrom:
			renderBranchHeader(c, "user", i18n.T("settings.automations.field.from"))
			kit.Input(c, &v.fromPattern, kit.InputOpts{Placeholder: "z.B. user@firma.de oder *@firma.de"}).Grow(1)
		case CondSubject:
			renderBranchHeader(c, "mail", i18n.T("settings.automations.field.subject"))
			settingSelect(c, i18n.T("settings.automations.field.subject_op"), &v.subjectOp, subjectOpOptions())
			kit.Input(c, &v.subjectPattern, kit.InputOpts{Placeholder: "z.B. Rechnung, Bestätigung, Angebot ..."}).Grow(1)
		}

		// Remove branch button [ ✕ ]
		kit.IconButton(c, "x", i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
			OnClick(func() {
				v.removeConditionBranch(condKey)
			})
	})
}

// renderActionBranch renders a modular action branch in Step 4
func (v *automationsView) renderActionBranch(c *ui.Context, idx int) {
	if idx >= len(v.activeActions) {
		return
	}
	t, th := theme.Of(c), c.Theme()
	act := &v.activeActions[idx]

	ui.Column(c).FillWidth().Padding(th.Space(2.5), th.Space(3)).Radius(t.Radius.Md).
		Background(t.Card).Border(theme.Hairline, t.Border).Gap(th.Space(2.5)).Children(func() {
		// Top row: Sequence number, Category pill, Icon + Action title, optional inline controls, and Delete button
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			// Sequence number circle
			ui.Box(c).Width(th.Space(5.5)).Height(th.Space(5.5)).Radius(t.Radius.Full).Background(t.Hover).
				AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
				kit.Text(c, fmt.Sprintf("%d", idx+1), t.Text.Xs).FontWeight(700).TextColor(t.Muted)
			})

			// Category pill
			ui.Box(c).Padding(th.Space(0.5), th.Space(1.5)).Radius(t.Radius.Sm).Background(t.Hover).Shrink(0).Children(func() {
				kit.Text(c, actionCategoryLabel(act.Type), t.Text.Xs).FontWeight(600).TextColor(t.Muted).SingleLine()
			})

			// Icon & Title
			ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1.5)).Shrink(0).Children(func() {
				ui.Icon(c, icons.Get(actionIconName(act.Type))).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
				kit.Text(c, actionLabel(act.Type), t.Text.Sm).FontWeight(600).SingleLine()
			})

			ui.Spacer(c)

			// Inline label selector if apply_label or remove_label
			if act.Type == core.WorkflowActionApplyLabel || act.Type == core.WorkflowActionRemoveLabel {
				if len(v.labels) > 0 {
					var lOpts []kit.Option[int64]
					for _, l := range v.labels {
						lOpts = append(lOpts, kit.Option[int64]{Value: l.ID, Label: l.Name})
					}
					if act.LabelID == 0 && len(v.labels) > 0 {
						act.LabelID = v.labels[0].ID
						act.LabelName = v.labels[0].Name
					}
					sel := settingSelect(c, i18n.T("settings.automations.select_label"), &act.LabelID, lOpts)
					if sel.Changed() {
						for _, l := range v.labels {
							if l.ID == act.LabelID {
								act.LabelName = l.Name
								break
							}
						}
						v.activeActions = core.SortActions(v.activeActions)
					}
				} else {
					kit.Text(c, "("+i18n.T("settings.automations.no_labels")+")", t.Text.Sm).TextColor(t.Muted)
				}
			}

			// Remove action button [ ✕ ]
			kit.IconButton(c, "x", i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Ghost, Size: kit.SizeSm}).
				OnClick(func() {
					v.removeAction(idx)
				})
		})

		// Parameter row(s) for actions that require input configuration
		switch act.Type {
		case core.WorkflowActionForward:
			ui.Row(c).FillWidth().PaddingStart(th.Space(7)).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
				kit.Text(c, i18n.T("settings.automations.forward_to_label"), t.Text.Sm).TextColor(t.Muted).SingleLine().Shrink(0)
				kit.Input(c, &act.ForwardTo, kit.InputOpts{
					Placeholder: "z.B. partner@firma.de oder steuer@kanzlei.de",
				}).Grow(1)
			})

		case core.WorkflowActionWitDraft:
			ui.Row(c).FillWidth().PaddingStart(th.Space(7)).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
				kit.Text(c, i18n.T("settings.automations.wit_instructions_label"), t.Text.Sm).TextColor(t.Muted).SingleLine().Shrink(0)
				kit.Input(c, &act.Instructions, kit.InputOpts{
					Placeholder: "z.B. Höflich bedanken und Bearbeitung innerhalb von 24h ankündigen...",
				}).Grow(1)
			})

		case core.WorkflowActionWitExtract:
			ui.Row(c).FillWidth().PaddingStart(th.Space(7)).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
				kit.Text(c, i18n.T("settings.automations.wit_template_label"), t.Text.Sm).TextColor(t.Muted).SingleLine().Shrink(0)
				kit.Input(c, &act.ExtractTemplate, kit.InputOpts{
					Placeholder: "z.B. Rechnungsnummer, Betrag, Währung, Zahlungsfrist",
				}).Grow(1)
			})
		}
	})
}

func (v *automationsView) hasExportAction() bool {
	for _, a := range v.activeActions {
		switch a.Type {
		case core.WorkflowActionExportEML, core.WorkflowActionExportPDF,
			core.WorkflowActionExportAttachments, core.WorkflowActionWitExtract:
			return true
		}
	}
	return false
}

func shortenPath(path string, maxLen int) string {
	if path == "" || len(path) <= maxLen {
		return path
	}
	normalized := strings.ReplaceAll(path, "\\", "/")
	parts := strings.Split(normalized, "/")
	if len(parts) <= 2 {
		if len(path) > maxLen {
			return path[:maxLen-3] + "..."
		}
		return path
	}
	first := parts[0]
	if first == "" && len(parts) > 1 {
		first = "/" + parts[1]
	}
	last := parts[len(parts)-1]
	if len(parts) >= 3 && parts[len(parts)-2] != "" {
		secondLast := parts[len(parts)-2]
		candidate := first + "/.../" + secondLast + "/" + last
		if len(candidate) <= maxLen {
			return candidate
		}
	}
	candidate := first + "/.../" + last
	if len(candidate) <= maxLen {
		return candidate
	}
	if len(last) > maxLen-7 {
		return first + "/.../" + last[:maxLen-10] + "..."
	}
	return candidate
}

func (v *automationsView) renderExportDestinationCard(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	fullDisplay := ""
	shortDisplay := i18n.T("settings.automations.choose_dir")

	if v.targetDirectory != "" {
		fullDisplay = v.targetDirectory
		if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(fullDisplay, home) {
			fullDisplay = "~" + strings.TrimPrefix(fullDisplay, home)
		}
		shortDisplay = shortenPath(fullDisplay, 28)
	}

	ui.Column(c).FillWidth().Padding(th.Space(3), th.Space(3.5)).Radius(t.Radius.Md).
		Background(t.Card).Border(theme.Hairline, t.Border).Gap(th.Space(2.5)).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			ui.Icon(c, icons.Get("folder")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted).Shrink(0)
			kit.Text(c, i18n.T("settings.automations.export_destination_title"), t.Text.Sm).FontWeight(600).SingleLine().Shrink(0)
			kit.Text(c, "•", t.Text.Xs).TextColor(t.Muted).SingleLine().Shrink(0)
			kit.Text(c, i18n.T("settings.automations.export_destination_hint"), t.Text.Xs).TextColor(t.Muted).SingleLine().Shrink(1)
		})
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(th.Space(3)).Children(func() {
			kit.Text(c, i18n.T("settings.automations.target_dir_label"), t.Text.Sm).TextColor(t.Muted).SingleLine().Shrink(0)
			btn := kit.Button(c, shortDisplay, kit.ButtonOpts{
				Variant:  kit.Outline,
				Size:     kit.SizeSm,
				Icon:     "folder",
				Disabled: v.pickingDir,
			}).OnClick(func() {
				v.pickDirectory(nil)
			})
			if fullDisplay != "" {
				kit.Tooltip(c, btn, fullDisplay)
			}
			kit.Checkbox(c, &v.bundleSubfolder, i18n.T("settings.automations.bundle_case_folder")).Shrink(0)
		})
	})
}

func (v *automationsView) pickDirectory(act *core.WorkflowActionItem) {
	if v.pickingDir {
		return
	}
	v.pickingDir = true
	var dir string
	v.a.do(func(context.Context) (err error) {
		dir, err = v.a.plat.OpenDirectory()
		return err
	}, func(err error) {
		v.pickingDir = false
		if err == nil && dir != "" {
			if act != nil {
				act.TargetDirectory = dir
			}
			v.targetDirectory = dir
			for i := range v.activeActions {
				if v.activeActions[i].Type == core.WorkflowActionExportEML ||
					v.activeActions[i].Type == core.WorkflowActionExportPDF ||
					v.activeActions[i].Type == core.WorkflowActionExportAttachments ||
					v.activeActions[i].Type == core.WorkflowActionWitExtract {
					v.activeActions[i].TargetDirectory = dir
				}
			}
		}
	})
}

func renderBranchHeader(c *ui.Context, iconName, label string) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1.5)).Width(th.Space(52)).Children(func() {
		ui.Icon(c, icons.Get(iconName)).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
		kit.Text(c, label+":", t.Text.Sm).FontWeight(600)
	})
}

func (v *automationsView) availableActionOptions() []kit.Option[string] {
	allowed := v.allowedActionsForTrigger(v.trigger)
	var opts []kit.Option[string]
	opts = append(opts, kit.Option[string]{Value: "", Label: i18n.T("settings.automations.add_action")})

	hasPurge := false
	hasDestination := false
	hasMarkRead := false
	hasMarkUnread := false
	hasStar := false
	hasUnstar := false
	hasUnsub := false
	hasForward := false
	hasExportEML := false
	hasExportPDF := false
	hasExportAtt := false
	hasWitDraft := false
	hasWitExtract := false

	appliedLabels := make(map[int64]bool)
	removedLabels := make(map[int64]bool)

	for _, a := range v.activeActions {
		if a.Type == core.WorkflowActionPurge {
			hasPurge = true
		}
		if isDestinationAction(a.Type) {
			hasDestination = true
		}
		if a.Type == core.WorkflowActionMarkRead {
			hasMarkRead = true
		}
		if a.Type == core.WorkflowActionMarkUnread {
			hasMarkUnread = true
		}
		if a.Type == core.WorkflowActionStar {
			hasStar = true
		}
		if a.Type == core.WorkflowActionUnstar {
			hasUnstar = true
		}
		if a.Type == core.WorkflowActionUnsubscribe {
			hasUnsub = true
		}
		if a.Type == core.WorkflowActionForward {
			hasForward = true
		}
		if a.Type == core.WorkflowActionExportEML {
			hasExportEML = true
		}
		if a.Type == core.WorkflowActionExportPDF {
			hasExportPDF = true
		}
		if a.Type == core.WorkflowActionExportAttachments {
			hasExportAtt = true
		}
		if a.Type == core.WorkflowActionWitDraft {
			hasWitDraft = true
		}
		if a.Type == core.WorkflowActionWitExtract {
			hasWitExtract = true
		}
		if a.Type == core.WorkflowActionApplyLabel && a.LabelID != 0 {
			appliedLabels[a.LabelID] = true
		}
		if a.Type == core.WorkflowActionRemoveLabel && a.LabelID != 0 {
			removedLabels[a.LabelID] = true
		}
	}

	// Rule: purge is standalone, cannot be combined with any other action!
	if hasPurge {
		return opts
	}

	for _, actType := range allowed {
		if actType == core.WorkflowActionPurge && len(v.activeActions) > 0 {
			continue // Cannot combine purge with other actions
		}
		if isDestinationAction(actType) && hasDestination {
			continue // Only 1 destination action allowed
		}
		if actType == core.WorkflowActionMarkRead && (hasMarkRead || hasMarkUnread) {
			continue // Read and unread cannot be combined; no duplicate read
		}
		if actType == core.WorkflowActionMarkUnread && (hasMarkRead || hasMarkUnread) {
			continue // Read and unread cannot be combined; no duplicate unread
		}
		if actType == core.WorkflowActionStar && (hasStar || hasUnstar) {
			continue // Mutual exclusion & duplicate protection
		}
		if actType == core.WorkflowActionUnstar && (hasStar || hasUnstar) {
			continue // Mutual exclusion & duplicate protection
		}
		if actType == core.WorkflowActionForward && hasForward {
			continue // Duplicate protection
		}
		if actType == core.WorkflowActionExportEML && hasExportEML {
			continue // Duplicate protection
		}
		if actType == core.WorkflowActionExportPDF && hasExportPDF {
			continue // Duplicate protection
		}
		if actType == core.WorkflowActionExportAttachments && hasExportAtt {
			continue // Duplicate protection
		}
		if actType == core.WorkflowActionWitDraft && hasWitDraft {
			continue // Duplicate protection
		}
		if actType == core.WorkflowActionWitExtract && hasWitExtract {
			continue // Duplicate protection
		}
		if actType == core.WorkflowActionUnsubscribe && hasUnsub {
			continue // Duplicate protection
		}
		if actType == core.WorkflowActionApplyLabel {
			allApplied := true
			for _, l := range v.labels {
				if !appliedLabels[l.ID] {
					allApplied = false
					break
				}
			}
			if len(v.labels) == 0 || allApplied {
				continue
			}
		}
		if actType == core.WorkflowActionRemoveLabel {
			allRemoved := true
			for _, l := range v.labels {
				if !removedLabels[l.ID] {
					allRemoved = false
					break
				}
			}
			if len(v.labels) == 0 || allRemoved {
				continue
			}
		}
		opts = append(opts, kit.Option[string]{Value: actType, Label: actionLabel(actType)})
	}
	return opts
}

func isDestinationAction(act string) bool {
	return act == core.WorkflowActionArchive ||
		act == core.WorkflowActionJunk ||
		act == core.WorkflowActionTrash ||
		act == core.WorkflowActionDelete
}

func actionIconName(act string) string {
	switch act {
	case core.WorkflowActionArchive:
		return "archive"
	case core.WorkflowActionJunk:
		return "spam"
	case core.WorkflowActionTrash, core.WorkflowActionDelete, core.WorkflowActionPurge:
		return "trash-2"
	case core.WorkflowActionMarkRead:
		return "mail-open"
	case core.WorkflowActionMarkUnread:
		return "mail"
	case core.WorkflowActionStar, core.WorkflowActionUnstar:
		return "star"
	case core.WorkflowActionApplyLabel, core.WorkflowActionRemoveLabel:
		return "tag"
	case core.WorkflowActionUnsubscribe:
		return "mail-forbid"
	case core.WorkflowActionForward:
		return "forward"
	case core.WorkflowActionExportEML, core.WorkflowActionExportPDF:
		return "file-text"
	case core.WorkflowActionExportAttachments:
		return "paperclip"
	case core.WorkflowActionWitDraft:
		return "file-pen"
	case core.WorkflowActionWitExtract:
		return "sparkles"
	default:
		return "sparkles"
	}
}

func actionCategoryLabel(actionType string) string {
	switch actionType {
	case core.WorkflowActionUnsubscribe, core.WorkflowActionForward:
		return i18n.T("settings.automations.cat_external")
	case core.WorkflowActionMarkRead, core.WorkflowActionMarkUnread, core.WorkflowActionStar, core.WorkflowActionUnstar:
		return i18n.T("settings.automations.cat_status")
	case core.WorkflowActionExportEML, core.WorkflowActionExportPDF, core.WorkflowActionExportAttachments:
		return i18n.T("settings.automations.cat_export")
	case core.WorkflowActionWitDraft, core.WorkflowActionWitExtract:
		return i18n.T("settings.automations.cat_wit")
	case core.WorkflowActionApplyLabel, core.WorkflowActionRemoveLabel:
		return i18n.T("settings.automations.cat_label")
	case core.WorkflowActionArchive, core.WorkflowActionJunk, core.WorkflowActionTrash, core.WorkflowActionDelete:
		return i18n.T("settings.automations.cat_destination")
	case core.WorkflowActionPurge:
		return i18n.T("settings.automations.cat_purge")
	default:
		return i18n.T("settings.automations.action")
	}
}

func (v *automationsView) addAction(actType string) {
	item := core.WorkflowActionItem{Type: actType}
	if actType == core.WorkflowActionExportEML || actType == core.WorkflowActionExportPDF ||
		actType == core.WorkflowActionExportAttachments || actType == core.WorkflowActionWitExtract {
		item.TargetDirectory = v.targetDirectory
		bundleTrue := v.bundleSubfolder
		item.BundleSubfolder = &bundleTrue
	}
	if actType == core.WorkflowActionForward {
		item.ForwardTo = v.forwardTo
	}
	if actType == core.WorkflowActionWitDraft {
		item.Instructions = v.instructions
	}
	if actType == core.WorkflowActionWitExtract {
		item.ExtractTemplate = v.extractTemplate
	}
	if actType == core.WorkflowActionApplyLabel {
		applied := make(map[int64]bool)
		for _, a := range v.activeActions {
			if a.Type == core.WorkflowActionApplyLabel && a.LabelID != 0 {
				applied[a.LabelID] = true
			}
		}
		for _, l := range v.labels {
			if !applied[l.ID] {
				item.LabelID = l.ID
				item.LabelName = l.Name
				break
			}
		}
		if item.LabelID == 0 && len(v.labels) > 0 {
			item.LabelID = v.labels[0].ID
			item.LabelName = v.labels[0].Name
		}
	} else if actType == core.WorkflowActionRemoveLabel {
		removed := make(map[int64]bool)
		for _, a := range v.activeActions {
			if a.Type == core.WorkflowActionRemoveLabel && a.LabelID != 0 {
				removed[a.LabelID] = true
			}
		}
		for _, l := range v.labels {
			if !removed[l.ID] {
				item.LabelID = l.ID
				item.LabelName = l.Name
				break
			}
		}
		if item.LabelID == 0 && len(v.labels) > 0 {
			item.LabelID = v.labels[0].ID
			item.LabelName = v.labels[0].Name
		}
	}
	v.activeActions = core.SortActions(append(v.activeActions, item))
}

func (v *automationsView) removeAction(idx int) {
	if idx < 0 || idx >= len(v.activeActions) {
		return
	}
	v.activeActions = append(v.activeActions[:idx], v.activeActions[idx+1:]...)
}

func (v *automationsView) removeConditionBranch(condKey string) {
	var next []string
	for _, k := range v.activeConds {
		if k != condKey {
			next = append(next, k)
		}
	}
	v.activeConds = next

	// Reset state for removed branch
	switch condKey {
	case CondSecurity:
		v.securityStatus = ""
		v.threatAssessment = ""
	case CondThreat:
		v.threatAssessment = ""
	case CondDomainAuth:
		v.domainAuth = ""
	case CondFolder:
		v.folderRole = ""
	case CondDays:
		v.olderThanDays = 0
	case CondLabel:
		v.labelID = 0
		v.labelName = ""
	case CondHasAtt:
		v.hasAttachments = 0
	case CondFrom:
		v.fromPattern = ""
	case CondSubject:
		v.subjectPattern = ""
		v.subjectOp = "contains"
	}
}

func (v *automationsView) triggerDescription(trigger string) string {
	switch trigger {
	case core.WorkflowTriggerDaily:
		return i18n.T("settings.automations.trigger_daily_sub")
	case core.WorkflowTriggerLabel:
		return i18n.T("settings.automations.trigger_label_sub")
	case core.WorkflowTriggerReceive:
		return i18n.T("settings.automations.trigger_receive_sub")
	case core.WorkflowTriggerSecurity:
		return i18n.T("settings.automations.trigger_security_sub")
	default:
		return i18n.T("settings.automations.no_trigger_selected")
	}
}

func stepBadge(c *ui.Context, num string) {
	t, th := theme.Of(c), c.Theme()
	ui.Box(c).Width(th.Space(6)).Height(th.Space(6)).Radius(t.Radius.Full).Background(t.Primary).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		kit.Text(c, num, t.Text.Sm).FontWeight(700).TextColor(t.PrimaryText)
	})
}

func treeBranchConnector(c *ui.Context) {
	t, th := theme.Of(c), c.Theme()
	ui.Row(c).Padding(0, 0, 0, th.Space(2.5)).Children(func() {
		ui.Box(c).Width(th.Space(0.5)).Height(th.Space(3.5)).Background(t.Border)
	})
}

// confirmSaveDialog shows a clear explanation of what the workflow will do before persisting
func (v *automationsView) confirmSaveDialog(c *ui.Context) {
	if !v.confirmOpen {
		return
	}
	t, th := theme.Of(c), c.Theme()

	hasDestructive := false
	for _, a := range v.activeActions {
		if a.Type == core.WorkflowActionPurge || a.Type == core.WorkflowActionTrash || a.Type == core.WorkflowActionDelete {
			hasDestructive = true
			break
		}
	}

	dialogOpts := kit.DialogOpts{
		Size:   kit.DialogMd,
		Alert:  hasDestructive,
		Danger: hasDestructive,
		Close:  i18n.T("common.close"),
	}

	kit.Dialog(c, &v.confirmOpen, i18n.T("settings.automations.confirm_title"), dialogOpts, nil, func() {
		ui.Column(c).Gap(th.Space(3)).Padding(th.Space(2), 0).Children(func() {
			kit.Text(c, i18n.T("settings.automations.confirm_body"), t.Text.Sm).TextColor(t.Muted)

			// Detailed explanation card
			ui.Column(c).FillWidth().Padding(th.Space(3), th.Space(3)).Radius(t.Radius.Md).
				Background(t.Card).Border(theme.Hairline, t.Border).Gap(th.Space(2)).Children(func() {
				// Name
				ui.Row(c).Gap(th.Space(1.5)).Children(func() {
					kit.Text(c, "Name:", t.Text.Sm).FontWeight(600).Width(th.Space(30))
					kit.Text(c, v.name, t.Text.Sm).FontWeight(600)
				})
				// Auslöser
				ui.Row(c).Gap(th.Space(1.5)).Children(func() {
					kit.Text(c, "Auslöser:", t.Text.Sm).FontWeight(600).Width(th.Space(30))
					trigText := triggerLabel(v.trigger)
					if v.trigger == core.WorkflowTriggerLabel && (v.triggerLabelName != "" || v.triggerLabelID != 0) {
						lbl := v.triggerLabelName
						if lbl == "" {
							lbl = fmt.Sprintf("ID %d", v.triggerLabelID)
						}
						trigText = fmt.Sprintf("%s (%s)", trigText, lbl)
					}
					kit.Text(c, trigText, t.Text.Sm)
				})
				// Geltungsbereich
				ui.Row(c).Gap(th.Space(1.5)).Children(func() {
					kit.Text(c, i18n.T("settings.automations.confirm_scope"), t.Text.Sm).FontWeight(600).Width(th.Space(30))
					tempWF := core.Workflow{Trigger: v.trigger, Config: core.WorkflowConfig{FolderRole: v.folderRole}}
					kit.Text(c, v.scopeSummary(tempWF), t.Text.Sm)
				})
				// Filter
				ui.Row(c).Gap(th.Space(1.5)).Children(func() {
					kit.Text(c, i18n.T("settings.automations.confirm_filters"), t.Text.Sm).FontWeight(600).Width(th.Space(30))
					tempWF := core.Workflow{
						Trigger: v.trigger,
						Config: core.WorkflowConfig{
							Days:             v.olderThanDays,
							LabelID:          v.labelID,
							LabelName:        v.labelName,
							ThreatAssessment: v.threatAssessment,
							DomainAuth:       v.domainAuth,
							SecurityStatus:   v.threatAssessment,
							FromPattern:      v.fromPattern,
							SubjectPattern:   v.subjectPattern,
							SubjectOp:        v.subjectOp,
							CondLogic:        v.condLogic,
						},
					}
					if v.hasAttachments == 1 {
						b := true
						tempWF.Config.HasAttachments = &b
					} else if v.hasAttachments == 2 {
						b := false
						tempWF.Config.HasAttachments = &b
					}
					kit.Text(c, v.filterSummary(tempWF), t.Text.Sm)
				})
				// Aktionen
				ui.Row(c).Gap(th.Space(1.5)).Children(func() {
					kit.Text(c, "Aktionen:", t.Text.Sm).FontWeight(600).Width(th.Space(30))
					tempWF := core.Workflow{Config: core.WorkflowConfig{Actions: v.activeActions}}
					kit.Text(c, v.actionsSummary(tempWF), t.Text.Sm).FontWeight(500)
				})
				// Treffer-Vorschau
				if v.trigger == core.WorkflowTriggerDaily {
					ui.Row(c).Gap(th.Space(1.5)).Children(func() {
						kit.Text(c, i18n.T("settings.automations.confirm_hits"), t.Text.Sm).FontWeight(600).Width(th.Space(30))
						countText := fmt.Sprintf("%d E-Mails", v.preview.CandidateCount)
						kit.Text(c, countText, t.Text.Sm).FontWeight(600)
					})
					if len(v.preview.Samples) > 0 {
						ui.Column(c).Gap(th.Space(1)).Margin(th.Space(1), 0, 0, 0).Children(func() {
							kit.Text(c, i18n.T("settings.automations.confirm_samples_daily"), t.Text.Xs).FontWeight(600).TextColor(t.Muted)
							for _, sm := range v.preview.Samples {
								subj := sm.Subject
								if subj == "" {
									subj = "(Kein Betreff)"
								}
								kit.Text(c, fmt.Sprintf("• %s — %s", subj, sm.From), t.Text.Xs).TextColor(t.Muted)
							}
						})
					}
				} else {
					// Event-based triggers (Receive, Label)
					ui.Row(c).Gap(th.Space(1.5)).Children(func() {
						kit.Text(c, i18n.T("settings.automations.confirm_effect_title"), t.Text.Sm).FontWeight(600).Width(th.Space(30))
						kit.Text(c, i18n.T("settings.automations.confirm_effect_value"), t.Text.Sm).FontWeight(600)
					})
					ui.Row(c).Gap(th.Space(1.5)).Children(func() {
						kit.Text(c, i18n.T("settings.automations.confirm_existing_title"), t.Text.Sm).FontWeight(600).Width(th.Space(30))
						kit.Text(c, i18n.T("settings.automations.confirm_existing_value"), t.Text.Sm).TextColor(t.Muted)
					})
					if len(v.activeConds) > 0 && len(v.preview.Samples) > 0 {
						ui.Column(c).Gap(th.Space(1)).Margin(th.Space(1), 0, 0, 0).Children(func() {
							filterTestHeader := fmt.Sprintf(i18n.T("settings.automations.confirm_filter_test"), v.preview.CandidateCount)
							kit.Text(c, filterTestHeader, t.Text.Xs).FontWeight(600).TextColor(t.Muted)
							for _, sm := range v.preview.Samples {
								subj := sm.Subject
								if subj == "" {
									subj = "(Kein Betreff)"
								}
								kit.Text(c, fmt.Sprintf("• %s — %s", subj, sm.From), t.Text.Xs).TextColor(t.Muted)
							}
						})
					}
				}
			})

			hasPurge := false
			hasTrash := false
			for _, a := range v.activeActions {
				if a.Type == core.WorkflowActionPurge {
					hasPurge = true
				} else if a.Type == core.WorkflowActionTrash || a.Type == core.WorkflowActionDelete {
					hasTrash = true
				}
			}

			// Distinct confirmation banners: permanent deletion vs recoverable trash
			if hasPurge {
				ui.Row(c).FillWidth().Padding(th.Space(2), th.Space(3)).Radius(t.Radius.Md).
					Background(t.DangerBg).Border(theme.Hairline, t.DangerBorder).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
					ui.Icon(c, icons.Get("trash-2")).Size(theme.IconSm, theme.IconSm).TextColor(t.DangerText)
					kit.Text(c, "Endgültiges Löschen: Betroffene Nachrichten werden unwiderruflich lokal und auf dem Server des verbundenen Mailanbieters (IMAP) entfernt. Eine Wiederherstellung ist nicht möglich.", t.Text.Sm).TextColor(t.DangerText).FontWeight(500)
				})
			} else if hasTrash {
				ui.Row(c).FillWidth().Padding(th.Space(2), th.Space(3)).Radius(t.Radius.Md).
					Background(t.WarningBg).Border(theme.Hairline, t.WarningBorder).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
					ui.Icon(c, icons.Get("archive")).Size(theme.IconSm, theme.IconSm).TextColor(t.WarningText)
					kit.Text(c, "Verschieben in den Papierkorb: Betroffene Nachrichten werden in den Papierkorb verschoben und können bei Bedarf wiederhergestellt werden.", t.Text.Sm).TextColor(t.WarningText).FontWeight(500)
				})
			}

			// Buttons
			ui.Row(c).Justify(ui.End).Gap(th.Space(2)).Margin(th.Space(2), 0, 0, 0).Children(func() {
				kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() {
					v.confirmOpen = false
				})
				saveBtnOpts := kit.ButtonOpts{Variant: kit.Default}
				if hasPurge {
					saveBtnOpts.Variant = kit.Destructive
				}
				kit.Button(c, i18n.T("settings.automations.confirm_btn"), saveBtnOpts).OnClick(func() {
					v.confirmOpen = false
					v.commitSave()
				})
			})
		})
	})
}

func (v *automationsView) save() {
	v.commitSave()
}

func (v *automationsView) commitSave() {
	a := v.a

	// Build WorkflowConfig strictly from active conditions and actions
	v.activeActions = core.SortActions(v.activeActions)
	var cfg core.WorkflowConfig
	cfg.CondLogic = v.condLogic
	cfg.Actions = v.activeActions
	cfg.FolderRole = v.folderRole

	if v.trigger == core.WorkflowTriggerLabel {
		cfg.TriggerLabelID = v.triggerLabelID
		cfg.TriggerLabelName = v.triggerLabelName
		if cfg.TriggerLabelID != 0 && cfg.TriggerLabelName == "" {
			for _, l := range v.labels {
				if l.ID == cfg.TriggerLabelID {
					cfg.TriggerLabelName = l.Name
					break
				}
			}
		}
	}

	if v.trigger == core.WorkflowTriggerSecurity {
		cfg.ThreatAssessment = v.threatAssessment
		cfg.SecurityStatus = v.threatAssessment
	}

	for _, ck := range v.activeConds {
		switch ck {
		case CondThreat:
			cfg.ThreatAssessment = v.threatAssessment
			cfg.SecurityStatus = v.threatAssessment
		case CondDomainAuth:
			cfg.DomainAuth = v.domainAuth
		case CondSecurity:
			cfg.SecurityStatus = v.securityStatus
			cfg.ThreatAssessment = v.threatAssessment
		case CondFolder:
			cfg.FolderRole = v.folderRole
		case CondDays:
			cfg.Days = v.olderThanDays
		case CondLabel:
			cfg.LabelID = v.labelID
			cfg.LabelName = strings.TrimSpace(v.labelName)
			for _, l := range v.labels {
				if l.Name == cfg.LabelName {
					cfg.LabelID = l.ID
					break
				}
			}
			if cfg.LabelID != 0 && cfg.LabelName == "" {
				for _, l := range v.labels {
					if l.ID == cfg.LabelID {
						cfg.LabelName = l.Name
						break
					}
				}
			}
		case CondHasAtt:
			if v.hasAttachments == 1 {
				b := true
				cfg.HasAttachments = &b
			} else if v.hasAttachments == 2 {
				b := false
				cfg.HasAttachments = &b
			}
		case CondFrom:
			cfg.FromPattern = strings.TrimSpace(v.fromPattern)
		case CondSubject:
			cfg.SubjectPattern = strings.TrimSpace(v.subjectPattern)
			cfg.SubjectOp = v.subjectOp
		}
	}

	primaryAction := core.WorkflowActionArchive
	if len(v.activeActions) > 0 {
		var acts []string
		for _, a := range v.activeActions {
			acts = append(acts, a.Type)
		}
		primaryAction = strings.Join(acts, "+")
	}

	accID := v.accountID
	in := core.WorkflowInput{
		AccountID: accID,
		Name:      strings.TrimSpace(v.name),
		Trigger:   v.trigger,
		Action:    primaryAction,
		Config:    cfg,
		Enabled:   v.enabled,
	}

	hasPurge := false
	for _, act := range cfg.Actions {
		if act.Type == core.WorkflowActionPurge {
			hasPurge = true
			break
		}
	}
	if hasPurge {
		in.ConfirmPurge = true
		token, err := a.svc.AuthorizePurgeDefinition(core.WithUserAuth(context.Background()), in)
		if err != nil {
			a.toastErr(err)
			return
		}
		in.HumanApprovalToken = token
		in.PurgeProof = token
		in.Config.PurgeProof = token
	}

	id := v.editID
	v.builderOpen = false

	targetAccountIDs := v.builderAccountIDs
	if len(targetAccountIDs) == 0 {
		targetAccountIDs = []int64{v.accountID}
		if v.accountID == 0 && len(a.accounts) > 0 {
			targetAccountIDs = []int64{a.accounts[0].ID}
		}
	}

	a.writes.Chain("automations", func(ctx context.Context) error {
		if id == 0 {
			for _, targetAccID := range targetAccountIDs {
				accIn := in
				accIn.AccountID = targetAccID
				if _, err := a.svc.CreateWorkflow(ctx, accIn); err != nil {
					return err
				}
			}
			return nil
		}
		_, err := a.svc.UpdateWorkflow(ctx, id, in)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
		} else {
			a.toast(i18n.T("settings.automations.saved"))
			v.load()
		}
	})
}

func (v *automationsView) toggleEnabled(wf core.Workflow, enabled bool) {
	a := v.a
	id := wf.ID
	a.writes.Chain("automations", func(ctx context.Context) error {
		return a.svc.SetWorkflowEnabled(ctx, id, enabled)
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
		} else {
			v.load()
		}
	})
}

func (v *automationsView) updatePurgeDays(wf core.Workflow, days int) {
	a := v.a
	id := wf.ID
	cfg := wf.Config
	cfg.Days = days
	in := core.WorkflowInput{
		Name:    wf.Name,
		Trigger: wf.Trigger,
		Action:  wf.Action,
		Config:  cfg,
		Enabled: days > 0,
	}
	a.writes.Chain("automations", func(ctx context.Context) error {
		_, err := a.svc.UpdateWorkflow(ctx, id, in)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
		} else {
			v.load()
		}
	})
}

func (v *automationsView) deleteWorkflow(id int64) {
	a := v.a
	v.deleteOpen = false
	v.deleteTarget = nil

	a.writes.Chain("automations", func(ctx context.Context) error {
		return a.svc.DeleteWorkflow(ctx, id)
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
		} else {
			a.toast(i18n.T("settings.automations.deleted"))
			v.load()
		}
	})
}

func (v *automationsView) deleteDialog(c *ui.Context) {
	if !v.deleteOpen || v.deleteTarget == nil {
		return
	}
	wf := v.deleteTarget
	kit.Dialog(c, &v.deleteOpen, i18n.Tf("settings.automations.delete_confirm", wf.Name),
		kit.DialogOpts{
			Size:   kit.DialogSm,
			Alert:  true,
			Danger: true,
			Close:  i18n.T("common.close"),
		}, nil, func() {
			kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() {
				v.deleteOpen = false
				v.deleteTarget = nil
			})
			kit.Button(c, i18n.T("common.delete"), kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() {
				v.deleteWorkflow(wf.ID)
			})
		})
}

func triggerOptions() []kit.Option[string] {
	return []kit.Option[string]{
		{Value: "", Label: i18n.T("settings.automations.select_trigger")},
		{Value: core.WorkflowTriggerReceive, Label: i18n.T("settings.automations.trigger_receive_full")},
		{Value: core.WorkflowTriggerSecurity, Label: i18n.T("settings.automations.trigger_security_full")},
		{Value: core.WorkflowTriggerLabel, Label: i18n.T("settings.automations.trigger_label_full")},
		{Value: core.WorkflowTriggerDaily, Label: i18n.T("settings.automations.trigger_daily_full")},
	}
}

func scopeOptionsForTrigger(trigger string) []kit.Option[string] {
	if trigger == core.WorkflowTriggerDaily {
		return []kit.Option[string]{
			{Value: "inbox", Label: i18n.T("mail.folder.inbox")},
			{Value: "trash", Label: i18n.T("mail.folder.trash")},
			{Value: "junk", Label: i18n.T("mail.folder.junk")},
			{Value: "archive", Label: i18n.T("mail.folder.archive")},
		}
	}
	return []kit.Option[string]{
		{Value: "", Label: i18n.T("settings.automations.scope_all_folders")},
		{Value: "inbox", Label: i18n.T("mail.folder.inbox")},
		{Value: "archive", Label: i18n.T("mail.folder.archive")},
		{Value: "junk", Label: i18n.T("mail.folder.junk")},
		{Value: "trash", Label: i18n.T("mail.folder.trash")},
	}
}

func folderOptions() []kit.Option[string] {
	return []kit.Option[string]{
		{Value: "inbox", Label: i18n.T("mail.folder.inbox")},
		{Value: "trash", Label: i18n.T("mail.folder.trash")},
		{Value: "junk", Label: i18n.T("mail.folder.junk")},
		{Value: "archive", Label: i18n.T("mail.folder.archive")},
	}
}

func olderThanOptions() []kit.Option[int] {
	return []kit.Option[int]{
		{Value: 1, Label: i18n.T("settings.automations.older_than_one")}, // Älter als 1 Tag (singular)
		{Value: 7, Label: i18n.Tf("settings.automations.older_than", 7)},
		{Value: 14, Label: i18n.Tf("settings.automations.older_than", 14)},
		{Value: 30, Label: i18n.Tf("settings.automations.older_than", 30)},
		{Value: 60, Label: i18n.Tf("settings.automations.older_than", 60)},
		{Value: 90, Label: i18n.Tf("settings.automations.older_than", 90)},
	}
}

func threatOptions() []kit.Option[string] {
	return []kit.Option[string]{
		{Value: "warning", Label: i18n.T("settings.automations.field.threat_warning")},
		{Value: "suspicious", Label: i18n.T("settings.automations.field.threat_suspicious")},
		{Value: "clean", Label: i18n.T("settings.automations.field.threat_clean")},
	}
}

func threatChipLabel(threat string) string {
	switch threat {
	case "warning", "malicious":
		return i18n.T("settings.automations.threat_chip_warning")
	case "suspicious":
		return i18n.T("settings.automations.threat_chip_suspicious")
	case "clean":
		return i18n.T("settings.automations.threat_chip_clean")
	default:
		return threat
	}
}

func domainAuthOptions() []kit.Option[string] {
	return []kit.Option[string]{
		{Value: "verified", Label: i18n.T("settings.automations.field.domain_auth_verified")},
		{Value: "failed", Label: i18n.T("settings.automations.field.domain_auth_failed")},
		{Value: "unverified", Label: i18n.T("settings.automations.field.domain_auth_unverified")},
	}
}

func subjectOpOptions() []kit.Option[string] {
	return []kit.Option[string]{
		{Value: "contains", Label: i18n.T("settings.automations.subject_op_contains")},
		{Value: "equals", Label: i18n.T("settings.automations.subject_op_equals")},
		{Value: "starts_with", Label: i18n.T("settings.automations.subject_op_starts_with")},
	}
}

func securityOptions() []kit.Option[string] {
	return []kit.Option[string]{
		{Value: "warning", Label: i18n.T("settings.automations.field.security.warning")},
		{Value: "trusted", Label: i18n.T("settings.security.verified")},
	}
}

func attachmentOptions() []kit.Option[int] {
	return []kit.Option[int]{
		{Value: 1, Label: i18n.T("settings.automations.field.has_att")},
		{Value: 2, Label: i18n.T("settings.automations.field.no_att")},
	}
}

func purgeDayOptions() []kit.Option[int] {
	var opts []kit.Option[int]
	for _, d := range core.PurgeDays {
		opts = append(opts, kit.Option[int]{Value: d, Label: i18n.T("settings.account.purge." + strconv.Itoa(d))})
	}
	return opts
}

func folderLabel(role string) string {
	switch role {
	case "inbox":
		return i18n.T("mail.folder.inbox")
	case "trash":
		return i18n.T("mail.folder.trash")
	case "junk":
		return i18n.T("mail.folder.junk")
	case "archive":
		return i18n.T("mail.folder.archive")
	default:
		return role
	}
}

func actionLabel(action string) string {
	switch action {
	case core.WorkflowActionPurge:
		return i18n.T("settings.automations.action.purge")
	case core.WorkflowActionArchive:
		return i18n.T("settings.automations.action.archive")
	case core.WorkflowActionJunk:
		return i18n.T("settings.automations.action.junk")
	case core.WorkflowActionTrash, core.WorkflowActionDelete:
		return i18n.T("settings.automations.action.delete")
	case core.WorkflowActionMarkRead:
		return i18n.T("settings.automations.action.mark_read")
	case core.WorkflowActionMarkUnread:
		return i18n.T("settings.automations.action.mark_unread")
	case core.WorkflowActionStar:
		return i18n.T("settings.automations.action.star")
	case core.WorkflowActionUnstar:
		return i18n.T("settings.automations.action.unstar")
	case core.WorkflowActionApplyLabel:
		return i18n.T("settings.automations.action.apply_label")
	case core.WorkflowActionRemoveLabel:
		return i18n.T("settings.automations.action.remove_label")
	case core.WorkflowActionUnsubscribe:
		return i18n.T("settings.automations.action.unsubscribe")
	case core.WorkflowActionForward:
		return i18n.T("settings.automations.action.forward")
	case core.WorkflowActionExportEML:
		return i18n.T("settings.automations.action.export_eml")
	case core.WorkflowActionExportPDF:
		return i18n.T("settings.automations.action.export_pdf")
	case core.WorkflowActionExportAttachments:
		return i18n.T("settings.automations.action.export_attachments")
	case core.WorkflowActionWitDraft:
		return i18n.T("settings.automations.action.wit_draft")
	case core.WorkflowActionWitExtract:
		return i18n.T("settings.automations.action.wit_extract")
	default:
		return action
	}
}

func triggerLabel(trigger string) string {
	switch trigger {
	case core.WorkflowTriggerDaily:
		return i18n.T("settings.automations.trigger_daily_full")
	case core.WorkflowTriggerReceive:
		return i18n.T("settings.automations.trigger_receive_full")
	case core.WorkflowTriggerSecurity:
		return i18n.T("settings.automations.trigger_security_full")
	case core.WorkflowTriggerLabel:
		return i18n.T("settings.automations.trigger_label_full")
	default:
		return trigger
	}
}

func (v *automationsView) accountEmail(accID int64) string {
	for _, acc := range v.a.accounts {
		if acc.ID == accID {
			return acc.Email
		}
	}
	return ""
}

func (v *automationsView) renderHistory(c *ui.Context, p *settingsPage) {
	t, th := theme.Of(c), c.Theme()

	ui.Row(c).AlignItems(ui.Center).Justify(ui.SpaceBetween).FillWidth().Margin(0, 0, th.Space(3), 0).Children(func() {
		ui.Column(c).Gap(th.Space(0.5)).Children(func() {
			kit.Text(c, i18n.T("settings.automations.history.title"), t.Text.Body).FontWeight(600)
			kit.Text(c, i18n.T("settings.automations.history.sub"), t.Text.Sm).TextColor(t.Muted)
		})
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			settingButton(c, "↻ "+i18n.T("settings.automations.history.refresh"), kit.Outline, false).OnClick(func() {
				v.loadRuns()
			})
			if len(v.runs) > 0 {
				settingButton(c, i18n.T("settings.automations.history.clear"), kit.Ghost, false).OnClick(func() {
					v.clearRuns()
				})
			}
		})
	})

	if len(v.runs) == 0 {
		settingsSection(c, "", "", func() {
			ui.Row(c).FillWidth().Padding(th.Space(8), th.Space(4)).Justify(ui.Center).Children(func() {
				ui.Column(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
					ui.Icon(c, icons.Get("time")).Size(theme.IconLg, theme.IconLg).TextColor(t.Muted)
					kit.Text(c, i18n.T("settings.automations.history.empty"), t.Text.Body).TextColor(t.Muted)
				})
			})
		})
		return
	}

	ui.Column(c).Gap(th.Space(3)).FillWidth().Children(func() {
		for _, r := range v.runs {
			run := r
			v.renderHistoryItem(c, run)
		}
	})
}

func (v *automationsView) renderHistoryItem(c *ui.Context, run core.WorkflowRun) {
	t, th := theme.Of(c), c.Theme()

	ui.Column(c).Padding(th.Space(3.5)).Radius(t.Radius.Md).
		Background(t.Background).BorderColor(t.Border).BorderWidth(theme.Hairline).
		Gap(th.Space(2.5)).Children(func() {

		// 1. Top row: Workflow Name, Account, Date/Time, Status Badge
		ui.Row(c).AlignItems(ui.Center).Justify(ui.SpaceBetween).FillWidth().Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
				ui.Icon(c, icons.Get("sparkles")).Size(theme.IconSm, theme.IconSm).TextColor(t.Primary)
				kit.Text(c, run.WorkflowName, t.Text.Body).FontWeight(600)
				if run.AccountEmail != "" {
					ui.Row(c).AlignItems(ui.Center).Padding(th.Space(0.5), th.Space(1.5)).
						Radius(t.Radius.Sm).Background(t.Hover).Children(func() {
						kit.Text(c, run.AccountEmail, t.Text.Xs).TextColor(t.Foreground).FontWeight(500)
					})
				}
				kit.Text(c, "•", t.Text.Sm).TextColor(t.Muted)
				kit.Text(c, formatRunTime(run.RanAt), t.Text.Sm).TextColor(t.Muted)
			})

			renderStatusBadge(c, run)
		})

		// 2. Message row: Subject & Sender
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Children(func() {
			ui.Icon(c, icons.Get("mail")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
			kit.Text(c, run.Subject, t.Text.Sm).FontWeight(500)
			if run.FromAddr != "" {
				kit.Text(c, "("+run.FromAddr+")", t.Text.Sm).TextColor(t.Muted)
			}
		})

		// 3. Executed Actions row
		v.renderExecutedActionChips(c, run)

		// 4. Cause / Notice banner
		if run.SkipReason != "" || run.FailReason != "" {
			v.renderCauseBanner(c, run)
		}

		// 5. Quick Actions (Buttons)
		if run.DraftID > 0 || run.FilePath != "" || run.DirPath != "" {
			ui.Row(c).AlignItems(ui.Center).Gap(th.Space(2)).Margin(th.Space(1), 0, 0, 0).Children(func() {
				if run.DraftID > 0 {
					settingButton(c, "↗ "+i18n.T("settings.automations.history.open_draft"), kit.Outline, false).
						OnClick(func() {
							v.a.OpenDraft(run.DraftID)
						})
				}
				targetPath := run.DirPath
				if targetPath == "" && run.FilePath != "" {
					targetPath = filepath.Dir(run.FilePath)
				}
				if targetPath != "" {
					settingButton(c, "↗ "+i18n.T("settings.automations.history.open_finder"), kit.Outline, false).
						OnClick(func() {
							v.a.do(func(ctx context.Context) error {
								dir := targetPath
								if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
									dir = filepath.Dir(dir)
								}
								return v.a.plat.OpenPath(dir)
							}, nil)
						})
				}
			})
		}
	})
}

func (v *automationsView) renderExecutedActionChips(c *ui.Context, run core.WorkflowRun) {
	t, th := theme.Of(c), c.Theme()
	if run.Action == "" {
		return
	}

	parts := strings.Split(run.Action, "+")
	ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1.5)).Wrap().Children(func() {
		kit.Text(c, i18n.T("settings.automations.action")+":", t.Text.Xs).TextColor(t.Muted)
		for _, raw := range parts {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			lbl := actionChipLabel(raw)
			ui.Row(c).AlignItems(ui.Center).Padding(th.Space(0.5), th.Space(1.5)).
				Radius(t.Radius.Sm).Background(t.Hover).Children(func() {
				kit.Text(c, lbl, t.Text.Xs).FontWeight(500)
			})
		}
	})
}

func actionChipLabel(action string) string {
	switch {
	case strings.HasPrefix(action, "forward_skipped:"):
		return "Weiterleitung (übersprungen)"
	case strings.HasPrefix(action, "forward:"):
		return "Weitergeleitet"
	case action == "mark_read":
		return "Als gelesen"
	case action == "mark_unread":
		return "Als ungelesen"
	case action == "star":
		return "Markiert (Stern)"
	case action == "unstar":
		return "Entfernt (Stern)"
	case strings.HasPrefix(action, "wit_draft:"):
		return "Wit-Entwurf"
	case strings.HasPrefix(action, "wit_extract:"):
		return "Wit-Extraktion"
	case strings.HasPrefix(action, "export_pdf:"):
		return "PDF exportiert"
	case strings.HasPrefix(action, "export_eml:"):
		return "EML exportiert"
	case strings.HasPrefix(action, "export_attachments:"):
		return "Anhänge exportiert"
	case strings.HasPrefix(action, "apply_label:"):
		return "Label zugewiesen"
	case strings.HasPrefix(action, "remove_label:"):
		return "Label entfernt"
	case action == "purge":
		return "Bereinigt (Purge)"
	case action == "archive":
		return "Archiviert"
	case action == "trash":
		return "Papierkorb"
	case action == "junk":
		return "Spam"
	default:
		return action
	}
}

func (v *automationsView) renderCauseBanner(c *ui.Context, run core.WorkflowRun) {
	t, th := theme.Of(c), c.Theme()

	if run.SkipReason != "" {
		isLoop := strings.HasPrefix(run.SkipReason, "loop_prevention:")
		recipient := ""
		if isLoop {
			recipient = strings.TrimPrefix(run.SkipReason, "loop_prevention:")
		}

		msg := fmt.Sprintf(i18n.T("settings.automations.history.skip_loop_prevention"), recipient)
		if !isLoop {
			msg = i18n.T("settings.automations.history.skip_auto_generated")
		}

		ui.Row(c).AlignItems(ui.Start).Gap(th.Space(2)).Padding(th.Space(2), th.Space(3)).
			Radius(t.Radius.Sm).Background(t.WarningBg).BorderColor(t.WarningBorder).
			BorderWidth(theme.Hairline).FillWidth().Children(func() {
			ui.Icon(c, icons.Get("triangle-alert")).Size(theme.IconSm, theme.IconSm).TextColor(t.WarningText)
			ui.Column(c).Gap(th.Space(0.5)).Children(func() {
				kit.Text(c, msg, t.Text.Sm).TextColor(t.Foreground).FontWeight(500)
				techHint := fmt.Sprintf("Log: workflow forward skipped to prevent loop (sender: %s == recipient: %s)", run.FromAddr, recipient)
				kit.Text(c, techHint, t.Text.Xs).TextColor(t.Muted)
			})
		})
	}

	if run.FailReason != "" {
		errText := run.FailReason
		var userFriendlyMsg string
		if strings.Contains(errText, "operation not permitted") || strings.Contains(errText, "permission denied") {
			userFriendlyMsg = i18n.T("settings.automations.history.err_permission")
		} else if strings.Contains(errText, "no such file or directory") {
			userFriendlyMsg = i18n.T("settings.automations.history.err_not_found")
		} else if strings.Contains(errText, "message") && strings.Contains(errText, "not found") {
			userFriendlyMsg = i18n.T("settings.automations.history.err_msg_not_found")
		} else {
			userFriendlyMsg = fmt.Sprintf(i18n.T("settings.automations.history.err_generic"), errText)
		}

		ui.Row(c).AlignItems(ui.Start).Gap(th.Space(2)).Padding(th.Space(2), th.Space(3)).
			Radius(t.Radius.Sm).Background(t.DangerBg).BorderColor(t.DangerBorder).
			BorderWidth(theme.Hairline).FillWidth().Children(func() {
			ui.Icon(c, icons.Get("circle-x")).Size(theme.IconSm, theme.IconSm).TextColor(t.DangerText)
			ui.Column(c).Gap(th.Space(0.5)).Children(func() {
				kit.Text(c, userFriendlyMsg, t.Text.Sm).TextColor(t.Foreground).FontWeight(500)
				kit.Text(c, "Details: "+errText, t.Text.Xs).TextColor(t.Muted)
			})
		})
	}
}

func renderStatusBadge(c *ui.Context, run core.WorkflowRun) {
	t, th := theme.Of(c), c.Theme()

	switch {
	case run.Status == "success" && run.SkipReason == "":
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1)).Padding(th.Space(0.5), th.Space(2)).
			Radius(t.Radius.Sm).Background(t.SuccessBg).Children(func() {
			ui.Icon(c, icons.Get("check")).Size(theme.IconSm, theme.IconSm).TextColor(t.SuccessText)
			kit.Text(c, i18n.T("settings.automations.history.status_success"), t.Text.Sm).
				TextColor(t.SuccessText).FontWeight(600)
		})

	case run.Status == "success" && run.SkipReason != "":
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1)).Padding(th.Space(0.5), th.Space(2)).
			Radius(t.Radius.Sm).Background(t.WarningBg).Children(func() {
			ui.Icon(c, icons.Get("info")).Size(theme.IconSm, theme.IconSm).TextColor(t.WarningText)
			kit.Text(c, i18n.T("settings.automations.history.status_partial"), t.Text.Sm).
				TextColor(t.WarningText).FontWeight(600)
		})

	case run.Status == "failed":
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1)).Padding(th.Space(0.5), th.Space(2)).
			Radius(t.Radius.Sm).Background(t.DangerBg).Children(func() {
			ui.Icon(c, icons.Get("circle-x")).Size(theme.IconSm, theme.IconSm).TextColor(t.DangerText)
			kit.Text(c, i18n.T("settings.automations.history.status_failed"), t.Text.Sm).
				TextColor(t.DangerText).FontWeight(600)
		})

	case run.Status == "queued":
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1)).Padding(th.Space(0.5), th.Space(2)).
			Radius(t.Radius.Sm).Background(t.Hover).Children(func() {
			ui.Icon(c, icons.Get("time")).Size(theme.IconSm, theme.IconSm).TextColor(t.Muted)
			kit.Text(c, i18n.T("settings.automations.history.status_queued"), t.Text.Sm).
				TextColor(t.Muted).FontWeight(600)
		})

	case run.Status == "paused":
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1)).Padding(th.Space(0.5), th.Space(2)).
			Radius(t.Radius.Sm).Background(t.WarningBg).Children(func() {
			ui.Icon(c, icons.Get("info")).Size(theme.IconSm, theme.IconSm).TextColor(t.WarningText)
			kit.Text(c, i18n.T("settings.automations.history.status_paused"), t.Text.Sm).
				TextColor(t.WarningText).FontWeight(600)
		})

	default:
		ui.Row(c).AlignItems(ui.Center).Gap(th.Space(1)).Padding(th.Space(0.5), th.Space(2)).
			Radius(t.Radius.Sm).Background(t.Hover).Children(func() {
			kit.Text(c, run.Status, t.Text.Sm).TextColor(t.Muted).FontWeight(600)
		})
	}
}

func formatRunTime(dt time.Time) string {
	now := time.Now()
	if dt.Year() == now.Year() && dt.YearDay() == now.YearDay() {
		return fmt.Sprintf("Heute, %s Uhr", dt.Format("15:04"))
	}
	if dt.Year() == now.Year() && dt.YearDay() == now.YearDay()-1 {
		return fmt.Sprintf("Gestern, %s Uhr", dt.Format("15:04"))
	}
	return dt.Format("02.01.2006 15:04")
}
