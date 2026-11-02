package core

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	netmail "net/mail"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/gateway"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/mailsync"
	"github.com/godwit-email/godwit/core/store"
)

const (
	WorkflowKindNative = "native"
	WorkflowKindCustom = "custom"

	WorkflowTriggerDaily    = "daily"
	WorkflowTriggerReceive  = "on_receive"
	WorkflowTriggerLabel    = "on_label"
	WorkflowTriggerSecurity = "on_security"

	WorkflowActionPurge             = "purge"
	WorkflowActionArchive           = "archive"
	WorkflowActionJunk              = "junk"
	WorkflowActionTrash             = "trash"
	WorkflowActionDelete            = "delete"
	WorkflowActionMarkRead          = "mark_read"
	WorkflowActionMarkUnread        = "mark_unread"
	WorkflowActionStar              = "star"
	WorkflowActionUnstar            = "unstar"
	WorkflowActionApplyLabel        = "apply_label"
	WorkflowActionRemoveLabel       = "remove_label"
	WorkflowActionUnsubscribe       = "unsubscribe"
	WorkflowActionForward           = "forward"
	WorkflowActionExportEML         = "export_eml"
	WorkflowActionExportPDF         = "export_pdf"
	WorkflowActionExportAttachments = "export_attachments"
	WorkflowActionWitDraft          = "wit_draft"
	WorkflowActionWitExtract        = "wit_extract"

	PresetPurgeTrash   = "purge_trash"
	PresetPurgeJunk    = "purge_junk"
	PresetSecurityJunk = "security_junk"
)

// ActionPriority returns the canonical execution priority for a workflow action:
// 1. External action (unsubscribe, forward) -> priority 10
// 2. Status (mark_read, mark_unread, star, unstar) -> priority 20..21
// 3. AI / Wit actions (wit_draft, wit_extract) -> priority 25
// 4. File exports (export_eml, export_pdf, export_attachments) -> priority 27
// 5. Labels (apply_label, remove_label) -> priority 30..31
// 6. Destination action (archive, junk, trash, delete) -> priority 40..42
// 7. Permanent deletion (purge) -> priority 50
func ActionPriority(actionType string) int {
	switch actionType {
	case WorkflowActionUnsubscribe, WorkflowActionForward:
		return 10
	case WorkflowActionMarkRead, WorkflowActionMarkUnread:
		return 20
	case WorkflowActionStar, WorkflowActionUnstar:
		return 21
	case WorkflowActionWitDraft, WorkflowActionWitExtract:
		return 25
	case WorkflowActionExportEML, WorkflowActionExportPDF, WorkflowActionExportAttachments:
		return 27
	case WorkflowActionApplyLabel:
		return 30
	case WorkflowActionRemoveLabel:
		return 31
	case WorkflowActionArchive:
		return 40
	case WorkflowActionJunk:
		return 41
	case WorkflowActionTrash, WorkflowActionDelete:
		return 42
	case WorkflowActionPurge:
		return 50
	default:
		return 100
	}
}

// SortActions sorts action items into their deterministic, canonical execution order:
// Externe Aktion -> Status -> Labels -> Zielaktion -> Endgültiges Löschen
func SortActions(actions []WorkflowActionItem) []WorkflowActionItem {
	if len(actions) <= 1 {
		return actions
	}
	sorted := make([]WorkflowActionItem, len(actions))
	copy(sorted, actions)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := ActionPriority(sorted[i].Type), ActionPriority(sorted[j].Type)
		if pi != pj {
			return pi < pj
		}
		if sorted[i].LabelID != sorted[j].LabelID {
			return sorted[i].LabelID < sorted[j].LabelID
		}
		return sorted[i].LabelName < sorted[j].LabelName
	})
	return sorted
}

// ValidateWorkflowActions verifies safety rules and mutual exclusivity for workflow actions:
//   - Status: "Gelesen" und "Ungelesen" nicht in derselben Regel.
//   - Zielaktionen: Maximal eine Zielaktion (Archivieren, Papierkorb oder Spam).
//   - Labels: Dasselbe Label darf nicht gleichzeitig hinzugefügt und entfernt werden.
//   - Duplikatschutz: Keine identischen Aktionen mit identischen Parametern.
//     (Mehrere unterschiedliche Labels dürfen hinzugefügt oder entfernt werden).
//   - Purge: Ist eine eigenständige Aktion und nicht mit anderen Aktionen kombinierbar.
func ValidateWorkflowActions(actions []WorkflowActionItem) error {
	if len(actions) == 0 {
		return errors.New("mindestens eine Aktion ist erforderlich")
	}

	hasPurge := false
	hasRead := false
	hasUnread := false
	hasStar := false
	hasUnstar := false
	hasUnsub := false
	hasExportEML := false
	hasExportPDF := false
	hasExportAtt := false
	hasWitDraft := false
	hasWitExtract := false
	destCount := 0

	appliedIDs := make(map[int64]bool)
	removedIDs := make(map[int64]bool)
	appliedNames := make(map[string]bool)
	removedNames := make(map[string]bool)
	forwardAddrs := make(map[string]bool)

	for _, a := range actions {
		switch a.Type {
		case WorkflowActionPurge:
			if hasPurge {
				return errors.New("doppelte Aktion: „Endgültig löschen“ ist bereits vorhanden")
			}
			hasPurge = true
		case WorkflowActionArchive, WorkflowActionJunk, WorkflowActionTrash, WorkflowActionDelete:
			destCount++
		case WorkflowActionMarkRead:
			if hasRead {
				return errors.New("doppelte Aktion: „Als gelesen markieren“ ist bereits vorhanden")
			}
			hasRead = true
		case WorkflowActionMarkUnread:
			if hasUnread {
				return errors.New("doppelte Aktion: „Als ungelesen markieren“ ist bereits vorhanden")
			}
			hasUnread = true
		case WorkflowActionStar:
			if hasStar {
				return errors.New("doppelte Aktion: „Mit Stern markieren“ ist bereits vorhanden")
			}
			hasStar = true
		case WorkflowActionUnstar:
			if hasUnstar {
				return errors.New("doppelte Aktion: „Stern entfernen“ ist bereits vorhanden")
			}
			hasUnstar = true
		case WorkflowActionUnsubscribe:
			if hasUnsub {
				return errors.New("doppelte Aktion: „Newsletter-Abmeldung“ ist bereits vorhanden")
			}
			hasUnsub = true
		case WorkflowActionForward:
			addr := strings.ToLower(strings.TrimSpace(a.ForwardTo))
			if addr == "" {
				return errors.New("für die Weiterleitung ist eine Empfängeradresse erforderlich")
			}
			if _, err := netmail.ParseAddress(addr); err != nil {
				return fmt.Errorf("ungültige Weiterleitungsadresse %q", a.ForwardTo)
			}
			if forwardAddrs[addr] {
				return fmt.Errorf("doppelte Aktion: Weiterleitung an %q ist bereits vorhanden", a.ForwardTo)
			}
			forwardAddrs[addr] = true
		case WorkflowActionExportEML:
			if hasExportEML {
				return errors.New("doppelte Aktion: „E-Mail als .eml speichern“ ist bereits vorhanden")
			}
			hasExportEML = true
		case WorkflowActionExportPDF:
			if hasExportPDF {
				return errors.New("doppelte Aktion: „E-Mail als .pdf speichern“ ist bereits vorhanden")
			}
			hasExportPDF = true
		case WorkflowActionExportAttachments:
			if hasExportAtt {
				return errors.New("doppelte Aktion: „E-Mail-Anhänge speichern“ ist bereits vorhanden")
			}
			hasExportAtt = true
		case WorkflowActionWitDraft:
			if hasWitDraft {
				return errors.New("doppelte Aktion: „Antwortentwurf mit Wit erstellen“ ist bereits vorhanden")
			}
			hasWitDraft = true
		case WorkflowActionWitExtract:
			if hasWitExtract {
				return errors.New("doppelte Aktion: „Informationen mit Wit extrahieren“ ist bereits vorhanden")
			}
			hasWitExtract = true
		case WorkflowActionApplyLabel:
			if a.LabelID != 0 {
				if appliedIDs[a.LabelID] {
					lbl := a.LabelName
					if lbl == "" {
						lbl = fmt.Sprintf("ID %d", a.LabelID)
					}
					return fmt.Errorf("doppelte Aktion: Label %q wird bereits zugewiesen", lbl)
				}
				appliedIDs[a.LabelID] = true
			} else if a.LabelName != "" {
				nameLower := strings.ToLower(strings.TrimSpace(a.LabelName))
				if appliedNames[nameLower] {
					return fmt.Errorf("doppelte Aktion: Label %q wird bereits zugewiesen", a.LabelName)
				}
				appliedNames[nameLower] = true
			}
		case WorkflowActionRemoveLabel:
			if a.LabelID != 0 {
				if removedIDs[a.LabelID] {
					lbl := a.LabelName
					if lbl == "" {
						lbl = fmt.Sprintf("ID %d", a.LabelID)
					}
					return fmt.Errorf("doppelte Aktion: Label %q wird bereits entfernt", lbl)
				}
				removedIDs[a.LabelID] = true
			} else if a.LabelName != "" {
				nameLower := strings.ToLower(strings.TrimSpace(a.LabelName))
				if removedNames[nameLower] {
					return fmt.Errorf("doppelte Aktion: Label %q wird bereits entfernt", a.LabelName)
				}
				removedNames[nameLower] = true
			}
		}
	}

	if hasPurge && len(actions) > 1 {
		return errors.New("endgültiges Löschen (purge) ist eine eigenständige Aktion und kann nicht mit anderen Aktionen kombiniert werden")
	}
	if hasRead && hasUnread {
		return errors.New("„Gelesen“ und „Ungelesen“ können nicht in derselben Regel kombiniert werden")
	}
	if hasStar && hasUnstar {
		return errors.New("„Mit Stern markieren“ und „Stern entfernen“ können nicht in derselben Regel kombiniert werden")
	}
	if destCount > 1 {
		return errors.New("mehrere unterschiedliche Zielaktionen sind ausgeschlossen (nur maximal eine Zielaktion erlaubt)")
	}

	// Check if the same label is simultaneously applied and removed
	for id := range appliedIDs {
		if removedIDs[id] {
			return errors.New("dasselbe Label kann nicht gleichzeitig hinzugefügt und entfernt werden")
		}
	}
	for name := range appliedNames {
		if removedNames[name] {
			return errors.New("dasselbe Label kann nicht gleichzeitig hinzugefügt und entfernt werden")
		}
	}

	return nil
}

const purgeProofSecret = "godwit-purge-safety-proof-v2"

// ComputeNormalizedPurgeConfigHash hashes all execution-relevant parameters of a purge workflow definition.
// Any change to account, folder, retention days, condition logic, filters, safety limits, or actions
// alters this hash, invalidating any previously issued authorization.
func ComputeNormalizedPurgeConfigHash(in WorkflowInput) string {
	cfg := in.Config
	actions := SortActions(cfg.Actions)
	if len(actions) == 0 && in.Action != "" {
		actions = SortActions([]WorkflowActionItem{{Type: in.Action, LabelID: cfg.LabelID, LabelName: cfg.LabelName}})
	}
	var actStrs []string
	for _, a := range actions {
		actStrs = append(actStrs, fmt.Sprintf("%s:%d:%s", a.Type, a.LabelID, a.LabelName))
	}
	hasAttStr := "nil"
	if cfg.HasAttachments != nil {
		hasAttStr = fmt.Sprintf("%v", *cfg.HasAttachments)
	}
	isUnreadStr := "nil"
	if cfg.IsUnread != nil {
		isUnreadStr = fmt.Sprintf("%v", *cfg.IsUnread)
	}

	raw := fmt.Sprintf("acc:%d|role:%s|fid:%d|days:%d|logic:%s|from:%s|subj:%s:%s|threat:%s|auth:%s|att:%s|unread:%s|safety:%d|batch:%d|acts:%s",
		in.AccountID, cfg.FolderRole, cfg.FolderID, cfg.Days, strings.ToLower(cfg.CondLogic),
		strings.ToLower(strings.TrimSpace(cfg.FromPattern)),
		strings.ToLower(strings.TrimSpace(cfg.SubjectOp)),
		strings.ToLower(strings.TrimSpace(cfg.SubjectPattern)),
		cfg.EffectiveThreatAssessment(), cfg.DomainAuth, hasAttStr, isUnreadStr,
		cfg.EffectiveSafetyLimit(), cfg.EffectiveBatchSize(),
		strings.Join(actStrs, ","),
	)

	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

type userAuthContextKey struct{}

// WithUserAuth marks a context as originating from an authenticated human user session (e.g. settings UI confirmation modal).
func WithUserAuth(ctx context.Context) context.Context {
	return context.WithValue(ctx, userAuthContextKey{}, true)
}

// IsUserAuthenticated checks whether the context carries human user authorization.
func IsUserAuthenticated(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, ok := ctx.Value(userAuthContextKey{}).(bool)
	return ok && v
}

// AuthorizePurgeDefinition produces a human-authorized proof token bound to the normalized execution hash.
// This is called strictly from an authenticated human user action (e.g. settings UI confirmation modal).
func AuthorizePurgeDefinition(in WorkflowInput) string {
	hash := ComputeNormalizedPurgeConfigHash(in)
	mac := hmac.New(sha256.New, []byte(purgeProofSecret))
	mac.Write([]byte("human_purge_approval:" + hash))
	return hex.EncodeToString(mac.Sum(nil))
}

// GeneratePurgeProof provides backwards compatibility for tests setting up purge rules with standard parameters.
func GeneratePurgeProof(accountID int64, folderRole string, folderID int64, days int) string {
	return AuthorizePurgeDefinition(WorkflowInput{
		AccountID: accountID,
		Config: WorkflowConfig{
			FolderRole: folderRole,
			FolderID:   folderID,
			Days:       days,
			Actions:    []WorkflowActionItem{{Type: WorkflowActionPurge}},
		},
	})
}

// VerifyPurgeProof validates that the provided proof matches the human authorization
// for the exact normalized execution parameters of the input.
func VerifyPurgeProof(proof string, in WorkflowInput) bool {
	if strings.TrimSpace(proof) == "" {
		return false
	}
	expected := AuthorizePurgeDefinition(in)
	return hmac.Equal([]byte(proof), []byte(expected))
}

// AuthorizePurgeDefinition validates input and produces the human authorization proof.
// It strictly requires an authenticated human user session in ctx (WithUserAuth).
func (s *Service) AuthorizePurgeDefinition(ctx context.Context, in WorkflowInput) (string, error) {
	if !IsUserAuthenticated(ctx) {
		return "", &ValidationError{
			Code:       "ERR_UNAUTHORIZED_PURGE_AUTHORIZATION",
			Field:      "human_approval",
			Message:    "Purge-Regeln können nur über eine autorisierte Nutzersitzung (UI) freigegeben werden. Ein unberechtigter Aufruf ohne Nutzersitzung wird abgewiesen.",
			Resolution: "Bestätigen Sie die Löschung in der Godwit-Benutzeroberfläche.",
		}
	}
	if err := s.ValidateWorkflowInput(ctx, in); err != nil {
		return "", err
	}
	return AuthorizePurgeDefinition(in), nil
}

// ResumeWorkflow unpauses a workflow that was paused by the safety limit.
// It requires an authenticated user session, resets execution_state to 'active', and increments version.
func (s *Service) ResumeWorkflow(ctx context.Context, id int64, expectedVersion int64) (Workflow, error) {
	if !IsUserAuthenticated(ctx) {
		return Workflow{}, &ValidationError{
			Code:       "ERR_UNAUTHORIZED_RESUME",
			Field:      "human_approval",
			Message:    "Das Fortsetzen einer durch die Sicherheitsgrenze pausierten Regel erfordert eine autorisierte Nutzeraktion.",
			Resolution: "Geben Sie die Fortsetzung in der Benutzeroberfläche frei.",
		}
	}
	w, err := s.GetWorkflow(ctx, id)
	if err != nil {
		return Workflow{}, err
	}
	if w.ExecutionState != "paused_safety_limit" {
		return w, nil
	}
	now := s.clk().Now().Unix()
	var res sqlite.Result
	if expectedVersion > 0 {
		res, err = s.db.Execute(ctx, `UPDATE workflows 
			SET execution_state = 'active', version = version + 1, updated_at = ? 
			WHERE id = ? AND version = ?`, now, id, expectedVersion)
	} else {
		res, err = s.db.Execute(ctx, `UPDATE workflows 
			SET execution_state = 'active', version = version + 1, updated_at = ? 
			WHERE id = ?`, now, id)
	}
	if err != nil {
		return Workflow{}, err
	}
	if expectedVersion > 0 && res.Changes == 0 {
		return Workflow{}, &ValidationError{
			Code:       "ERR_VERSION_CONFLICT",
			Field:      "expected_version",
			Message:    fmt.Sprintf("Die Regel wurde zwischenzeitlich geändert (erwartete Version: %d). Bitte laden Sie die Regel neu.", expectedVersion),
			Resolution: "Laden Sie die Regel neu und wiederholen Sie die Freigabe.",
		}
	}
	return s.GetWorkflow(ctx, id)
}

// isCatchAllPattern returns true if the given pattern matches any sender address.
func isCatchAllPattern(p string) bool {
	return isTautologicalSenderPattern(p)
}

// isTautologicalSenderPattern evaluates whether a sender pattern matches all possible addresses.
func isTautologicalSenderPattern(p string) bool {
	clean := strings.ToLower(strings.TrimSpace(p))
	if clean == "" || clean == "*" || clean == "*@*" || clean == "*.*" || clean == "%" ||
		clean == "@*" || clean == "*@" || clean == "*@*.*" || clean == "?" || clean == "*?*" {
		return true
	}
	// Check if pattern contains only wildcards, dots, and at-signs
	stripped := strings.ReplaceAll(clean, "*", "")
	stripped = strings.ReplaceAll(stripped, "?", "")
	stripped = strings.ReplaceAll(stripped, "%", "")
	stripped = strings.ReplaceAll(stripped, "@", "")
	stripped = strings.ReplaceAll(stripped, ".", "")
	return len(stripped) == 0
}

// isTautologicalSubjectPattern evaluates whether a subject pattern matches any subject.
func isTautologicalSubjectPattern(p string) bool {
	clean := strings.ToLower(strings.TrimSpace(p))
	return clean == "" || clean == "*" || clean == "%" || clean == "*.*"
}

type filterBranchKind int

const (
	branchRestrictive filterBranchKind = iota
	branchTautological
)

// HasEffectiveInboxRestriction evaluates whether a daily rule on the inbox contains
// an effective limiting filter (age, metadata, content) to prevent unintended mass modifications.
// It normalizes each active filter branch and strictly evaluates the boolean logic:
// In an OR condition, any tautological / catch-all branch makes the entire group unrestricted!
func HasEffectiveInboxRestriction(cfg WorkflowConfig) bool {
	var branches []filterBranchKind

	// 1. Days / Age filter
	if cfg.Days > 0 {
		branches = append(branches, branchRestrictive)
	}

	// 2. Label filter
	if cfg.LabelID > 0 || strings.TrimSpace(cfg.LabelName) != "" {
		branches = append(branches, branchRestrictive)
	}

	// 3. Threat assessment
	if cfg.EffectiveThreatAssessment() != "" {
		branches = append(branches, branchRestrictive)
	}

	// 4. Domain authentication
	if cfg.DomainAuth != "" {
		branches = append(branches, branchRestrictive)
	}

	// 5. Attachments
	if cfg.HasAttachments != nil {
		branches = append(branches, branchRestrictive)
	}

	// 6. Unread flag
	if cfg.IsUnread != nil {
		branches = append(branches, branchRestrictive)
	}

	// 7. From pattern
	if strings.TrimSpace(cfg.FromPattern) != "" {
		if isTautologicalSenderPattern(cfg.FromPattern) {
			branches = append(branches, branchTautological)
		} else {
			branches = append(branches, branchRestrictive)
		}
	}

	// 8. Subject pattern
	if strings.TrimSpace(cfg.SubjectPattern) != "" {
		if isTautologicalSubjectPattern(cfg.SubjectPattern) {
			branches = append(branches, branchTautological)
		} else {
			branches = append(branches, branchRestrictive)
		}
	}

	if len(branches) == 0 {
		return false // No active filter branches at all
	}

	isOr := strings.EqualFold(cfg.CondLogic, "or")
	if isOr {
		// In an OR condition, if ANY branch is tautological (matches all),
		// the entire expression matches all emails!
		// Furthermore, EVERY active branch in an OR expression must be restrictive.
		for _, b := range branches {
			if b != branchRestrictive {
				return false
			}
		}
		return true
	}

	// In an AND condition (default), having at least one restrictive branch
	// effectively restricts the inbox modification.
	hasRestrictive := false
	for _, b := range branches {
		if b == branchRestrictive {
			hasRestrictive = true
			break
		}
	}
	return hasRestrictive
}

// ValidateWorkflowInput provides central validation used across UI, LLM tools, and execution engine.
func (s *Service) ValidateWorkflowInput(ctx context.Context, in WorkflowInput) *ValidationError {
	if in.AccountID == 0 {
		return &ValidationError{
			Code:       "ERR_ACCOUNT_REQUIRED",
			Field:      "account_id",
			Message:    "account_id ist erforderlich.",
			Resolution: "Geben Sie eine gültige account_id an.",
		}
	}
	if strings.TrimSpace(in.Name) == "" {
		return &ValidationError{
			Code:       "ERR_NAME_REQUIRED",
			Field:      "name",
			Message:    "Der Name der Regel darf nicht leer sein.",
			Resolution: "Geben Sie einen Namen für die Regel ein.",
		}
	}
	if in.Trigger != WorkflowTriggerDaily && in.Trigger != WorkflowTriggerReceive && in.Trigger != WorkflowTriggerLabel && in.Trigger != WorkflowTriggerSecurity {
		return &ValidationError{
			Code:       "ERR_TRIGGER_INVALID",
			Field:      "trigger",
			Message:    fmt.Sprintf("Ungültiger Auslöser: %q.", in.Trigger),
			Resolution: "Verwenden Sie einen gültigen Auslöser ('daily', 'on_receive', 'on_label', 'on_security').",
		}
	}

	actions := in.Config.Actions
	if len(actions) == 0 && in.Action != "" {
		actions = []WorkflowActionItem{{Type: in.Action, LabelID: in.Config.LabelID, LabelName: in.Config.LabelName}}
	}
	if len(actions) == 0 {
		return &ValidationError{
			Code:       "ERR_ACTIONS_REQUIRED",
			Field:      "actions",
			Message:    "Mindestens eine zulässige Aktion ist erforderlich.",
			Resolution: "Fügen Sie mindestens eine Aktion hinzu.",
		}
	}
	if err := ValidateWorkflowActions(actions); err != nil {
		return &ValidationError{
			Code:       "ERR_ACTIONS_INVALID",
			Field:      "actions",
			Message:    err.Error(),
			Resolution: "Prüfen Sie die Aktionen auf Konflikte (kein Gelesen+Ungelesen, maximal eine Zielaktion, keine gegensätzlichen Labels).",
		}
	}

	// Trigger specific validations
	switch in.Trigger {
	case WorkflowTriggerDaily:
		if in.Config.FolderRole == "" && in.Config.FolderID == 0 {
			return &ValidationError{
				Code:       "ERR_SCOPE_REQUIRED",
				Field:      "folder_role",
				Message:    "Ein konkreter Ordner als Geltungsbereich ist für den täglichen Zeitplan erforderlich.",
				Resolution: "Wählen Sie einen konkreten Ordner (z. B. 'inbox', 'trash', 'junk') als Geltungsbereich.",
			}
		}
	case WorkflowTriggerLabel:
		trigLabelID := in.Config.EffectiveTriggerLabelID()
		trigLabelName := strings.TrimSpace(in.Config.TriggerLabelName)
		if trigLabelID == 0 && trigLabelName == "" {
			return &ValidationError{
				Code:       "ERR_TRIGGER_LABEL_REQUIRED",
				Field:      "trigger_label_name",
				Message:    "Der Auslöser 'Bei Label-Zuweisung' erfordert ein Pflicht-Auslöser-Label.",
				Resolution: "Geben Sie ein gültiges Auslöser-Label an.",
			}
		}
		// Event loop check: cannot trigger on label X and apply label X
		for _, a := range actions {
			if a.Type == WorkflowActionApplyLabel {
				if trigLabelID != 0 && a.LabelID == trigLabelID {
					return &ValidationError{
						Code:       "ERR_EVENT_LOOP_DETECTED",
						Field:      "actions",
						Message:    "Eine Regel kann nicht durch ein Label ausgelöst werden und gleichzeitig dasselbe Label zuweisen (Gefahr einer Endlosschleife).",
						Resolution: "Verwenden Sie ein anderes Label für die Aktion oder ändern Sie den Auslöser.",
					}
				}
				if trigLabelName != "" && strings.EqualFold(a.LabelName, trigLabelName) {
					return &ValidationError{
						Code:       "ERR_EVENT_LOOP_DETECTED",
						Field:      "actions",
						Message:    "Eine Regel kann nicht durch ein Label ausgelöst werden und gleichzeitig dasselbe Label zuweisen (Gefahr einer Endlosschleife).",
						Resolution: "Verwenden Sie ein anderes Label für die Aktion oder ändern Sie den Auslöser.",
					}
				}
			}
		}
	case WorkflowTriggerSecurity:
		threat := in.Config.EffectiveThreatAssessment()
		if threat == "" {
			return &ValidationError{
				Code:       "ERR_TRIGGER_SECURITY_REQUIRED",
				Field:      "threat_assessment",
				Message:    "Der Auslöser 'Bei Bedrohungsbewertung' erfordert eine Bedrohungsstufe.",
				Resolution: "Wählen Sie eine Bedrohungsstufe (z. B. 'warning' / bösartig, 'suspicious', 'clean').",
			}
		}
	}

	hasPurge := false
	hasDestination := false
	for _, a := range actions {
		if a.Type == WorkflowActionPurge {
			hasPurge = true
		}
		switch a.Type {
		case WorkflowActionArchive, WorkflowActionTrash, WorkflowActionDelete, WorkflowActionJunk, WorkflowActionPurge:
			hasDestination = true
		}
	}

	// Purge validation rules
	if hasPurge {
		if in.Trigger != WorkflowTriggerDaily {
			return &ValidationError{
				Code:       "ERR_PURGE_TRIGGER_INVALID",
				Field:      "trigger",
				Message:    "Endgültiges Löschen (purge) ist ausschließlich mit dem zeitgesteuerten Auslöser 'daily' zulässig.",
				Resolution: "Ändern Sie den Auslöser auf 'daily'.",
			}
		}
		folderRole := in.Config.FolderRole
		if folderRole == "" && in.Config.FolderID != 0 && s != nil {
			fRows, _ := s.db.Query(ctx, "SELECT role FROM folders WHERE id = ?", in.Config.FolderID)
			if len(fRows.Values) > 0 {
				folderRole = store.Str(fRows.Values[0][0])
			}
		}
		if folderRole != "trash" && folderRole != "junk" {
			return &ValidationError{
				Code:       "ERR_PURGE_FOLDER_INVALID",
				Field:      "folder_role",
				Message:    "Endgültiges Löschen (purge) ist ausschließlich für die Ordner Papierkorb ('trash') oder Spam ('junk') zulässig.",
				Resolution: "Wählen Sie als Geltungsbereich entweder den Papierkorb oder Spam.",
			}
		}
		if in.Config.Days <= 0 {
			return &ValidationError{
				Code:       "ERR_PURGE_DAYS_REQUIRED",
				Field:      "days",
				Message:    "Endgültiges Löschen erfordert eine verpflichtende Aufbewahrungsfrist (Tage > 0).",
				Resolution: "Geben Sie eine Mindestaufbewahrungsdauer in Tagen an (z. B. 30 Tage).",
			}
		}
	}

	// Protection against accidental mass modifications on inbox
	if in.Trigger == WorkflowTriggerDaily && hasDestination {
		scopeRole := in.Config.FolderRole
		if scopeRole == "" && in.Config.FolderID != 0 && s != nil {
			fRows, _ := s.db.Query(ctx, "SELECT role FROM folders WHERE id = ?", in.Config.FolderID)
			if len(fRows.Values) > 0 {
				scopeRole = store.Str(fRows.Values[0][0])
			}
		}
		if scopeRole == "inbox" && !HasEffectiveInboxRestriction(in.Config) {
			return &ValidationError{
				Code:       "ERR_INBOX_MASS_MODIFICATION_BLOCKED",
				Field:      "filters",
				Message:    "Tägliche Regeln auf dem Posteingang mit Archivierungs-, Lösch- oder Spam-Aktionen erfordern mindestens eine wirksame Filtereinschränkung, um unbeabsichtigte Massenänderungen zu verhindern.",
				Resolution: "Fügen Sie eine wirksame Einschränkung hinzu (z. B. Mindestalter in Tagen, Absenderfilter, Betrefffilter oder Sicherheitsbewertung). Leere Felder, Wildcards wie '*' oder 'beliebige Anhänge' reichen nicht aus.",
			}
		}
	}

	for _, a := range actions {
		switch a.Type {
		case WorkflowActionExportEML, WorkflowActionExportPDF, WorkflowActionExportAttachments, WorkflowActionWitExtract:
			dir := a.TargetDirectory
			if dir == "" {
				dir = in.Config.TargetDirectory
			}
			if strings.TrimSpace(dir) == "" {
				return &ValidationError{
					Code:       "ERR_TARGET_DIRECTORY_REQUIRED",
					Field:      "target_directory",
					Message:    "Ein Zielordner auf dem Computer ist für Export- und Extraktions-Aktionen erforderlich.",
					Resolution: "Wählen Sie einen lokalen Speicherordner aus.",
				}
			}
		case WorkflowActionForward:
			to := a.ForwardTo
			if to == "" {
				to = in.Config.ForwardTo
			}
			if strings.TrimSpace(to) == "" {
				return &ValidationError{
					Code:       "ERR_INVALID_FORWARD_ADDRESS",
					Field:      "forward_to",
					Message:    "Für die Weiterleitung ist eine Empfänger-E-Mail-Adresse erforderlich.",
					Resolution: "Geben Sie eine gültige E-Mail-Adresse an.",
				}
			}
			if _, err := netmail.ParseAddress(strings.TrimSpace(to)); err != nil {
				return &ValidationError{
					Code:       "ERR_INVALID_FORWARD_ADDRESS",
					Field:      "forward_to",
					Message:    fmt.Sprintf("Ungültige Weiterleitungsadresse %q.", to),
					Resolution: "Geben Sie eine syntaktisch gültige E-Mail-Adresse an.",
				}
			}
		case WorkflowActionWitDraft:
			inst := a.Instructions
			if inst == "" {
				inst = in.Config.Instructions
			}
			if strings.TrimSpace(inst) == "" {
				return &ValidationError{
					Code:       "ERR_WIT_INSTRUCTIONS_REQUIRED",
					Field:      "instructions",
					Message:    "Anweisungen für den Wit-Antwortentwurf sind erforderlich.",
					Resolution: "Geben Sie an, wie Wit auf die Nachricht antworten soll.",
				}
			}
		}
	}

	// Validate label existence if DB is available
	if s != nil {
		checkLabelID := func(lblID int64, field string) *ValidationError {
			if lblID != 0 {
				lRows, err := s.db.Query(ctx, "SELECT id FROM labels WHERE id = ?", lblID)
				if err != nil || len(lRows.Values) == 0 {
					return &ValidationError{
						Code:       "ERR_LABEL_NOT_FOUND",
						Field:      field,
						Message:    fmt.Sprintf("Das Label mit ID %d existiert nicht.", lblID),
						Resolution: "Wählen Sie ein existierendes Label aus.",
					}
				}
			}
			return nil
		}

		if in.Trigger == WorkflowTriggerLabel {
			if in.Config.EffectiveTriggerLabelID() != 0 {
				if vErr := checkLabelID(in.Config.EffectiveTriggerLabelID(), "trigger_label_id"); vErr != nil {
					return vErr
				}
			}
		}
		if in.Config.LabelID != 0 {
			if vErr := checkLabelID(in.Config.LabelID, "label_id"); vErr != nil {
				return vErr
			}
		}
		for _, a := range actions {
			if (a.Type == WorkflowActionApplyLabel || a.Type == WorkflowActionRemoveLabel) && a.LabelID != 0 {
				if vErr := checkLabelID(a.LabelID, "actions"); vErr != nil {
					return vErr
				}
			}
		}
	}

	return nil
}

// ValidateWorkflowInput provides central validation for workflow inputs without DB queries.
func ValidateWorkflowInput(in WorkflowInput) *ValidationError {
	var s *Service
	return s.ValidateWorkflowInput(context.Background(), in)
}

// ThreatAssessmentState represents mutually exclusive threat evaluation categories.
type ThreatAssessmentState string

const (
	ThreatClean      ThreatAssessmentState = "clean"
	ThreatSuspicious ThreatAssessmentState = "suspicious"
	ThreatWarning    ThreatAssessmentState = "warning"
	ThreatPending    ThreatAssessmentState = "pending"
	ThreatUnknown    ThreatAssessmentState = "unknown"
)

// ResolveThreatState maps raw security scan fields to a deterministic, mutually exclusive state.
// "clean" is only produced when scanning is completed and zero fraud/spoofing/warning reasons exist.
func ResolveThreatState(secScannedAt int64, secStatus, secReason string) ThreatAssessmentState {
	if secScannedAt <= 0 {
		return ThreatPending
	}
	status := strings.ToLower(strings.TrimSpace(secStatus))
	reason := strings.ToLower(strings.TrimSpace(secReason))

	// High risk warning indicators (fraud, spoofing, phishing, malware)
	if status == "warning" || status == "malicious" ||
		reason == "ai_fraud" || reason == "spoofing" || reason == "phishing" ||
		reason == "email_in_name" || reason == "malicious" {
		return ThreatWarning
	}

	// Review / Suspicious indicators
	if status == "needs_review" || status == "suspicious" ||
		reason == "suspicious" || reason == "display_mismatch" {
		return ThreatSuspicious
	}

	// Clean ONLY if status is clean/pass and no warning or suspicious reasons exist
	if (status == "clean" || status == "pass" || status == "ok") && reason == "" {
		return ThreatClean
	}

	return ThreatUnknown
}

// DomainAuthState represents structured domain authentication states.
type DomainAuthState string

const (
	DomainAuthVerified   DomainAuthState = "verified"
	DomainAuthFailed     DomainAuthState = "failed"
	DomainAuthUnverified DomainAuthState = "unverified"
	DomainAuthPending    DomainAuthState = "pending"
)

// ResolveDomainAuthState evaluates RFC 8601 authentication results and checks
// domain alignment per RFC 7489 (DMARC).
func ResolveDomainAuthState(fromAddr, authResults, secStatus, secReason string, secScannedAt int64) DomainAuthState {
	if secScannedAt <= 0 && strings.TrimSpace(authResults) == "" {
		return DomainAuthPending
	}
	raw := strings.ToLower(authResults)
	reason := strings.ToLower(strings.TrimSpace(secReason))
	status := strings.ToLower(strings.TrimSpace(secStatus))

	// 1. Herkunftsnachweis aus dem vertrauenswürdigen Empfangspfad:
	// Wurde die Nachricht von der Empfangspipeline als nicht vertrauenswürdig (unverified)
	// oder neutral eingestuft, darf ein gefälschter, vom Absender nachgeahmter Header
	// (selbst mit passenden Domain- und Serverangaben wie mx.google.com / dkim=pass / dmarc=pass)
	// keinesfalls als DomainAuthVerified gewertet werden!
	if reason == "unverified" || (secScannedAt > 0 && status == "neutral") {
		return DomainAuthUnverified
	}
	if reason == "auth_failed" || status == "fraud" {
		return DomainAuthFailed
	}
	if status == "verified_domain" || status == "certified_brand" {
		return DomainAuthVerified
	}

	// 2. Auswertung vorhandener RFC-8601-Prüfergebnisse (bei noch nicht gescannten Entwürfen/Simulationen):
	trimmedAuth := strings.TrimSpace(authResults)
	if trimmedAuth != "" && strings.TrimSpace(fromAddr) != "" {
		hasAuthservID := false
		lowerAuth := strings.ToLower(trimmedAuth)
		if strings.HasPrefix(lowerAuth, "authentication-results:") {
			hasAuthservID = true
		} else if idx := strings.Index(trimmedAuth, ";"); idx > 0 {
			firstPart := strings.TrimSpace(trimmedAuth[:idx])
			if !strings.Contains(firstPart, "=") && (strings.Contains(firstPart, ".") || strings.Contains(firstPart, "-")) {
				hasAuthservID = true
			}
		}

		if hasAuthservID {
			rep := gomail.EvaluateSecurity(fromAddr, authResults)
			// RFC 7489 §6.6.2: DMARC is pass if aligned DKIM or aligned SPF passes
			if rep.DMARCPass || rep.Status == gomail.SecurityVerifiedDomain || rep.Status == gomail.SecurityCertifiedBrand {
				return DomainAuthVerified
			}
			if rep.DKIMPass || rep.BIMIPass {
				return DomainAuthVerified
			}
			if rep.Status == gomail.SecurityFraud || rep.WarningKind == gomail.WarnAuthFailed {
				return DomainAuthFailed
			}
			// Untrusted / unaligned / neutral RFC 8601 result (z. B. sender-injected header oder gefälschte Angaben)
			// darf nicht als verifiziert eingestuft werden und fällt nicht auf Substring-Matches zurück.
			return DomainAuthUnverified
		}
	}

	// 3. Token-based fallback when auth_results is shorthand tokens without authserv-id (e.g. test tokens)
	hasDmarcPass := strings.Contains(raw, "dmarc=pass") || reason == "dmarc_pass"
	hasDkimPass := strings.Contains(raw, "dkim=pass") || reason == "dkim_pass"
	hasSpfPass := strings.Contains(raw, "spf=pass") || reason == "spf_pass"
	hasBimiPass := strings.Contains(raw, "bimi=pass") || reason == "bimi_pass"

	hasDmarcFail := strings.Contains(raw, "dmarc=fail") || reason == "dmarc_fail"
	hasDkimFail := strings.Contains(raw, "dkim=fail") || reason == "dkim_fail"
	hasSpfFail := strings.Contains(raw, "spf=fail") || reason == "spf_fail"

	// RFC 7489: SPF failing alone does NOT fail domain authentication when DKIM passes!
	if hasDmarcFail || (hasDkimFail && hasSpfFail) || reason == "auth_failed" {
		return DomainAuthFailed
	}

	if (hasDmarcPass || hasDkimPass || hasBimiPass || hasSpfPass) && !hasDmarcFail {
		return DomainAuthVerified
	}

	return DomainAuthUnverified
}

func (s *Service) recordWorkflowRunWithOp(ctx context.Context, wfID, accID, msgID, ver int64, action, status, details string, opID *int64) {
	now := s.clk().Now().Unix()
	var opArg any
	if opID != nil && *opID > 0 {
		opArg = *opID
	}
	if _, err := s.db.Execute(ctx, `INSERT INTO workflow_runs(workflow_id, account_id, message_id, workflow_version, action, status, details, op_id, ran_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, wfID, accID, msgID, ver, action, status, details, opArg, now); err != nil {
		slog.Error("failed to record workflow run", "err", err, "wf_id", wfID, "msg_id", msgID)
	}
}

func (s *Service) recordWorkflowRun(ctx context.Context, wfID, accID, msgID, ver int64, action, status, details string) {
	s.recordWorkflowRunWithOp(ctx, wfID, accID, msgID, ver, action, status, details, nil)
}

func sameWorkflowConfig(a, b WorkflowConfig) bool {
	if a.Days != b.Days || a.FolderRole != b.FolderRole || a.FolderID != b.FolderID ||
		a.TriggerLabelID != b.TriggerLabelID || a.TriggerLabelName != b.TriggerLabelName ||
		a.LabelID != b.LabelID || a.LabelName != b.LabelName ||
		a.EffectiveThreatAssessment() != b.EffectiveThreatAssessment() ||
		a.DomainAuth != b.DomainAuth || a.FromPattern != b.FromPattern ||
		a.SubjectPattern != b.SubjectPattern || a.SubjectOp != b.SubjectOp ||
		!strings.EqualFold(a.CondLogic, b.CondLogic) {
		return false
	}
	if (a.HasAttachments == nil) != (b.HasAttachments == nil) {
		return false
	}
	if a.HasAttachments != nil && *a.HasAttachments != *b.HasAttachments {
		return false
	}
	if len(a.Actions) != len(b.Actions) {
		return false
	}
	for i := range a.Actions {
		if a.Actions[i].Type != b.Actions[i].Type ||
			a.Actions[i].LabelID != b.Actions[i].LabelID ||
			a.Actions[i].LabelName != b.Actions[i].LabelName {
			return false
		}
	}
	return true
}

// seedNativePresets ensures the 3 native preset workflows exist for the account.
func (s *Service) seedNativePresets(ctx context.Context, accountID int64) error {
	// Clean up legacy newsletter preset if present
	_, _ = s.db.Execute(ctx, "DELETE FROM workflows WHERE preset_id = 'newsletter_unsub' AND kind = 'native'")

	// Migrate legacy security_junk preset to modern on_security trigger and threat_assessment config
	_, _ = s.db.Execute(ctx, `UPDATE workflows SET trigger = ?, config = '{"threat_assessment":"warning"}' WHERE preset_id = ? AND account_id = ? AND (trigger != ? OR config NOT LIKE '%threat_assessment%')`,
		WorkflowTriggerSecurity, PresetSecurityJunk, accountID, WorkflowTriggerSecurity)

	rows, err := s.db.Query(ctx, "SELECT count(*) FROM workflows WHERE account_id = ?", accountID)
	if err != nil {
		return err
	}
	if len(rows.Values) > 0 && store.Int(rows.Values[0][0]) > 0 {
		return nil
	}

	// Read purge policies to sync trash and junk days
	purges, _ := s.Purges(ctx, accountID)
	trashDays := 30
	trashEnabled := false
	if p, ok := purges["trash"]; ok && p.Days > 0 {
		trashDays = p.Days
		trashEnabled = true
	}
	junkDays := 30
	junkEnabled := false
	if p, ok := purges["junk"]; ok && p.Days > 0 {
		junkDays = p.Days
		junkEnabled = true
	}

	now := s.clk().Now().Unix()
	presets := []struct {
		presetID string
		name     string
		trigger  string
		action   string
		config   WorkflowConfig
		enabled  bool
	}{
		{
			presetID: PresetPurgeTrash,
			name:     "mail_workflow_preset_purge_trash",
			trigger:  WorkflowTriggerDaily,
			action:   WorkflowActionPurge,
			config:   WorkflowConfig{Days: trashDays, FolderRole: "trash"},
			enabled:  trashEnabled,
		},
		{
			presetID: PresetPurgeJunk,
			name:     "mail_workflow_preset_purge_junk",
			trigger:  WorkflowTriggerDaily,
			action:   WorkflowActionPurge,
			config:   WorkflowConfig{Days: junkDays, FolderRole: "junk"},
			enabled:  junkEnabled,
		},
		{
			presetID: PresetSecurityJunk,
			name:     "mail_workflow_preset_security_junk",
			trigger:  WorkflowTriggerSecurity,
			action:   WorkflowActionJunk,
			config:   WorkflowConfig{ThreatAssessment: "warning"},
			enabled:  true,
		},
	}

	for _, p := range presets {
		cfgJSON, _ := json.Marshal(p.config)
		enVal := 0
		if p.enabled {
			enVal = 1
		}
		_, err := s.db.Execute(ctx, `INSERT INTO workflows(account_id, kind, preset_id, name, trigger, action, config, enabled, idempotency_key, version, execution_state, created_at, updated_at)
			VALUES (?, 'native', ?, ?, ?, ?, ?, ?, '', 1, 'active', ?, ?)`,
			accountID, p.presetID, p.name, p.trigger, p.action, string(cfgJSON), enVal, now, now)
		if err != nil {
			return err
		}
	}
	return nil
}

// ListWorkflows returns all workflows for the specified account, seeding native presets if missing.
func (s *Service) ListWorkflows(ctx context.Context, accountID int64) ([]Workflow, error) {
	_, _ = s.db.Execute(ctx, "DELETE FROM workflows WHERE preset_id = 'newsletter_unsub' AND kind = 'native'")
	if err := s.seedNativePresets(ctx, accountID); err != nil {
		return nil, err
	}

	// Sync purge policies from core into native presets
	purges, _ := s.Purges(ctx, accountID)

	rows, err := s.db.Query(ctx, `SELECT id, account_id, kind, preset_id, name, trigger, action, config, enabled, created_at, updated_at, idempotency_key, version, execution_state
		FROM workflows WHERE account_id = ? ORDER BY CASE WHEN kind = 'native' THEN 0 ELSE 1 END, id`, accountID)
	if err != nil {
		return nil, err
	}

	var out []Workflow
	for _, r := range rows.Values {
		w := Workflow{
			ID:        store.Int(r[0]),
			AccountID: store.Int(r[1]),
			Kind:      store.Str(r[2]),
			PresetID:  store.Str(r[3]),
			Name:      store.Str(r[4]),
			Trigger:   store.Str(r[5]),
			Action:    store.Str(r[6]),
			Enabled:   store.Int(r[8]) == 1,
			CreatedAt: time.Unix(store.Int(r[9]), 0),
			UpdatedAt: time.Unix(store.Int(r[10]), 0),
		}
		if len(r) > 11 {
			w.IdempotencyKey = store.Str(r[11])
		}
		if len(r) > 12 {
			w.Version = store.Int(r[12])
		}
		if w.Version == 0 {
			w.Version = 1
		}
		if len(r) > 13 {
			w.ExecutionState = store.Str(r[13])
		}
		if w.ExecutionState == "" {
			w.ExecutionState = "active"
		}
		if w.Enabled {
			w.Status = "enabled"
		} else {
			w.Status = "disabled"
		}
		w.CanEdit = w.Kind != WorkflowKindNative
		_ = json.Unmarshal([]byte(store.Str(r[7])), &w.Config)
		if len(w.Config.Actions) == 0 && w.Action != "" {
			w.Config.Actions = []WorkflowActionItem{{Type: w.Action, LabelID: w.Config.LabelID, LabelName: w.Config.LabelName}}
		}

		// Keep purge_trash and purge_junk synchronized with purge_policies
		if w.PresetID == PresetPurgeTrash {
			if p, ok := purges["trash"]; ok {
				if p.Days > 0 {
					w.Config.Days = p.Days
					w.Enabled = true
					w.Status = "enabled"
				} else {
					w.Enabled = false
					w.Status = "disabled"
				}
			}
		} else if w.PresetID == PresetPurgeJunk {
			if p, ok := purges["junk"]; ok {
				if p.Days > 0 {
					w.Config.Days = p.Days
					w.Enabled = true
					w.Status = "enabled"
				} else {
					w.Enabled = false
					w.Status = "disabled"
				}
			}
		}

		out = append(out, w)
	}
	return out, nil
}

// GetWorkflow fetches a single workflow by ID.
func (s *Service) GetWorkflow(ctx context.Context, id int64) (Workflow, error) {
	rows, err := s.db.Query(ctx, `SELECT id, account_id, kind, preset_id, name, trigger, action, config, enabled, created_at, updated_at, idempotency_key, version, execution_state
		FROM workflows WHERE id = ?`, id)
	if err != nil {
		return Workflow{}, err
	}
	if len(rows.Values) == 0 {
		return Workflow{}, fmt.Errorf("workflow %d not found", id)
	}
	r := rows.Values[0]
	w := Workflow{
		ID:        store.Int(r[0]),
		AccountID: store.Int(r[1]),
		Kind:      store.Str(r[2]),
		PresetID:  store.Str(r[3]),
		Name:      store.Str(r[4]),
		Trigger:   store.Str(r[5]),
		Action:    store.Str(r[6]),
		Enabled:   store.Int(r[8]) == 1,
		CreatedAt: time.Unix(store.Int(r[9]), 0),
		UpdatedAt: time.Unix(store.Int(r[10]), 0),
	}
	if len(r) > 11 {
		w.IdempotencyKey = store.Str(r[11])
	}
	if len(r) > 12 {
		w.Version = store.Int(r[12])
	}
	if w.Version == 0 {
		w.Version = 1
	}
	if len(r) > 13 {
		w.ExecutionState = store.Str(r[13])
	}
	if w.ExecutionState == "" {
		w.ExecutionState = "active"
	}
	if w.Enabled {
		w.Status = "enabled"
	} else {
		w.Status = "disabled"
	}
	w.CanEdit = w.Kind != WorkflowKindNative
	_ = json.Unmarshal([]byte(store.Str(r[7])), &w.Config)
	if len(w.Config.Actions) == 0 && w.Action != "" {
		w.Config.Actions = []WorkflowActionItem{{Type: w.Action, LabelID: w.Config.LabelID, LabelName: w.Config.LabelName}}
	}
	return w, nil
}

// CreateWorkflow creates a new user-defined custom workflow.
func (s *Service) CreateWorkflow(ctx context.Context, in WorkflowInput) (Workflow, error) {
	if in.IdempotencyKey != "" {
		rows, err := s.db.Query(ctx, "SELECT id, name, trigger, action, config FROM workflows WHERE account_id = ? AND idempotency_key = ?", in.AccountID, in.IdempotencyKey)
		if err == nil && len(rows.Values) > 0 {
			existingID := store.Int(rows.Values[0][0])
			existingName := store.Str(rows.Values[0][1])
			existingTrigger := store.Str(rows.Values[0][2])
			existingAction := store.Str(rows.Values[0][3])
			existingCfgRaw := store.Str(rows.Values[0][4])

			var existingCfg WorkflowConfig
			_ = json.Unmarshal([]byte(existingCfgRaw), &existingCfg)

			if existingName == in.Name && existingTrigger == in.Trigger && (existingAction == in.Action || in.Action == "") && sameWorkflowConfig(existingCfg, in.Config) {
				return s.GetWorkflow(ctx, existingID)
			}
			return Workflow{}, &ValidationError{
				Code:       "ERR_IDEMPOTENCY_CONFLICT",
				Field:      "idempotency_key",
				Message:    fmt.Sprintf("Ein Workflow mit dem Idempotency-Key %q existiert bereits, hat aber abweichende Parameter.", in.IdempotencyKey),
				Resolution: "Verwenden Sie einen neuen Idempotency-Key für geänderte Regeln oder übermitteln Sie identische Parameter.",
			}
		}
	}

	actions := in.Config.Actions
	if len(actions) == 0 && in.Action != "" {
		actions = []WorkflowActionItem{{Type: in.Action, LabelID: in.Config.LabelID, LabelName: in.Config.LabelName}}
	}
	actions = SortActions(actions)
	in.Config.Actions = actions

	if in.Trigger == "" {
		in.Trigger = WorkflowTriggerReceive
	}

	if valErr := s.ValidateWorkflowInput(ctx, in); valErr != nil {
		return Workflow{}, valErr
	}

	for _, a := range actions {
		if a.Type == WorkflowActionPurge {
			effectiveProof := cmp.Or(in.HumanApprovalToken, in.PurgeProof, in.Config.PurgeProof)
			if !in.ConfirmPurge && effectiveProof == "" {
				return Workflow{}, &ValidationError{
					Code:       "ERR_CONFIRM_PURGE_REQUIRED",
					Field:      "confirm_purge",
					Message:    "Endgültiges Löschen erfordert eine Bestätigung.",
					Resolution: "Bestätigen Sie die Löschung in der Benutzeroberfläche.",
				}
			}
			if effectiveProof == "" {
				return Workflow{}, &ValidationError{
					Code:       "ERR_PURGE_CONFIRMATION_REQUIRED",
					Field:      "purge_proof",
					Message:    "Endgültiges Löschen erfordert eine Freigabe durch den Nutzer über die Benutzeroberfläche. Ein Modellparameter confirm_purge: true allein ist keine ausreichende Nutzerbestätigung.",
					Resolution: "Rufen Sie zuerst preview_workflow auf und lassen Sie die Regel vom Nutzer über die Benutzeroberfläche freigeben.",
				}
			}
			if !VerifyPurgeProof(effectiveProof, in) {
				return Workflow{}, &ValidationError{
					Code:       "ERR_PURGE_CONFIRMATION_PROOF_INVALID",
					Field:      "purge_proof",
					Message:    "Der Bestätigungsnachweis ist ungültig oder passt nicht zur aktuellen Regeldefinition. Änderungen an Ordner, Frist, Filtern oder Schutzgrenzen machen den Nachweis ungültig.",
					Resolution: "Erstellen Sie eine neue Vorschau und lassen Sie die geänderten Parameter vom Nutzer bestätigen.",
				}
			}
		}
	}

	var actNames []string
	for _, a := range actions {
		actNames = append(actNames, a.Type)
	}
	in.Action = strings.Join(actNames, "+")

	cfgJSON, err := json.Marshal(in.Config)
	if err != nil {
		return Workflow{}, err
	}

	now := s.clk().Now().Unix()
	enVal := 0
	if in.Enabled {
		enVal = 1
	}

	res, err := s.db.Execute(ctx, `INSERT INTO workflows(account_id, kind, preset_id, name, trigger, action, config, enabled, idempotency_key, version, execution_state, created_at, updated_at)
		VALUES (?, 'custom', '', ?, ?, ?, ?, ?, ?, 1, 'active', ?, ?)`,
		in.AccountID, in.Name, in.Trigger, in.Action, string(cfgJSON), enVal, in.IdempotencyKey, now, now)
	if err != nil {
		if in.IdempotencyKey != "" {
			rows, qErr := s.db.Query(ctx, "SELECT id, name, trigger, action, config FROM workflows WHERE account_id = ? AND idempotency_key = ?", in.AccountID, in.IdempotencyKey)
			if qErr == nil && len(rows.Values) > 0 {
				existingID := store.Int(rows.Values[0][0])
				existingName := store.Str(rows.Values[0][1])
				existingTrigger := store.Str(rows.Values[0][2])
				existingAction := store.Str(rows.Values[0][3])
				existingCfgRaw := store.Str(rows.Values[0][4])

				var existingCfg WorkflowConfig
				_ = json.Unmarshal([]byte(existingCfgRaw), &existingCfg)

				if existingName == in.Name && existingTrigger == in.Trigger && (existingAction == in.Action || in.Action == "") && sameWorkflowConfig(existingCfg, in.Config) {
					return s.GetWorkflow(ctx, existingID)
				}
				return Workflow{}, &ValidationError{
					Code:       "ERR_IDEMPOTENCY_CONFLICT",
					Field:      "idempotency_key",
					Message:    fmt.Sprintf("Ein Workflow mit dem Idempotency-Key %q existiert bereits, hat aber abweichende Parameter.", in.IdempotencyKey),
					Resolution: "Verwenden Sie einen neuen Idempotency-Key für geänderte Regeln oder übermitteln Sie identische Parameter.",
				}
			}
		}
		return Workflow{}, err
	}

	return s.GetWorkflow(ctx, res.LastInsertID)
}

// UpdateWorkflow updates an existing workflow. Native workflows can only have trigger, config, and enabled updated.
func (s *Service) UpdateWorkflow(ctx context.Context, id int64, in WorkflowInput) (Workflow, error) {
	w, err := s.GetWorkflow(ctx, id)
	if err != nil {
		return Workflow{}, err
	}

	if in.ExpectedVersion > 0 && w.Version != in.ExpectedVersion {
		return Workflow{}, &ValidationError{
			Code:       "ERR_VERSION_CONFLICT",
			Field:      "expected_version",
			Message:    fmt.Sprintf("Die Regel wurde zwischenzeitlich geändert (aktuelle Version: %d, erwartete Version: %d). Bitte laden Sie die Regel neu.", w.Version, in.ExpectedVersion),
			Resolution: "Laden Sie die Regel mit list_workflows neu und wenden Sie Ihre Änderungen auf die aktuelle Version an.",
		}
	}

	if in.AccountID == 0 {
		in.AccountID = w.AccountID
	}

	actions := in.Config.Actions
	if len(actions) == 0 && in.Action != "" {
		actions = []WorkflowActionItem{{Type: in.Action, LabelID: in.Config.LabelID, LabelName: in.Config.LabelName}}
	}
	if len(actions) > 0 {
		actions = SortActions(actions)
		in.Config.Actions = actions
		var actNames []string
		for _, a := range actions {
			actNames = append(actNames, a.Type)
		}
		in.Action = strings.Join(actNames, "+")
	}

	if w.Kind != WorkflowKindNative {
		if valErr := s.ValidateWorkflowInput(ctx, in); valErr != nil {
			return Workflow{}, valErr
		}
		for _, a := range in.Config.Actions {
			if a.Type == WorkflowActionPurge {
				effectiveProof := cmp.Or(in.HumanApprovalToken, in.PurgeProof, in.Config.PurgeProof)
				if !in.ConfirmPurge && effectiveProof == "" {
					return Workflow{}, &ValidationError{
						Code:       "ERR_CONFIRM_PURGE_REQUIRED",
						Field:      "confirm_purge",
						Message:    "Endgültiges Löschen erfordert eine Bestätigung.",
						Resolution: "Bestätigen Sie die Löschung in der Benutzeroberfläche.",
					}
				}
				if effectiveProof == "" {
					return Workflow{}, &ValidationError{
						Code:       "ERR_PURGE_CONFIRMATION_REQUIRED",
						Field:      "purge_proof",
						Message:    "Endgültiges Löschen erfordert eine Freigabe durch den Nutzer über die Benutzeroberfläche. Ein Modellparameter confirm_purge: true allein ist keine ausreichende Nutzerbestätigung.",
						Resolution: "Rufen Sie zuerst preview_workflow auf und lassen Sie die Regel vom Nutzer über die Benutzeroberfläche freigeben.",
					}
				}
				if !VerifyPurgeProof(effectiveProof, in) {
					return Workflow{}, &ValidationError{
						Code:       "ERR_PURGE_CONFIRMATION_PROOF_INVALID",
						Field:      "purge_proof",
						Message:    "Der Bestätigungsnachweis ist ungültig oder passt nicht zur aktuellen Regeldefinition. Änderungen an Ordner, Frist, Filtern oder Schutzgrenzen machen den Nachweis ungültig.",
						Resolution: "Erstellen Sie eine neue Vorschau und lassen Sie die geänderten Parameter vom Nutzer bestätigen.",
					}
				}
			}
		}
	}

	cfgJSON, err := json.Marshal(in.Config)
	if err != nil {
		return Workflow{}, err
	}

	now := s.clk().Now().Unix()
	enVal := 0
	if in.Enabled {
		enVal = 1
	}

	if w.Kind == WorkflowKindNative {
		// Native preset: cannot rename, change action or preset_id
		// Sync with SetPurge if it's trash or junk purge
		if w.PresetID == PresetPurgeTrash {
			days := 0
			if in.Enabled {
				days = in.Config.Days
				if days == 0 {
					days = 30
				}
			}
			_ = s.SetPurge(ctx, w.AccountID, "trash", days)
		} else if w.PresetID == PresetPurgeJunk {
			days := 0
			if in.Enabled {
				days = in.Config.Days
				if days == 0 {
					days = 30
				}
			}
			_ = s.SetPurge(ctx, w.AccountID, "junk", days)
		}
	}

	newExecState := w.ExecutionState
	if w.ExecutionState == "paused_safety_limit" {
		// A rule paused by safety limit CANNOT be unpaused without authenticated human authorization!
		if in.ResetSafetyPause || (in.ExecutionState != "" && in.ExecutionState != "paused_safety_limit") {
			if !IsUserAuthenticated(ctx) {
				return Workflow{}, &ValidationError{
					Code:       "ERR_UNAUTHORIZED_RESUME",
					Field:      "execution_state",
					Message:    "Das Entsperren einer wegen Sicherheitsüberschreitung pausierten Regel erfordert eine autorisierte menschliche Nutzersitzung.",
					Resolution: "Bestätigen Sie die Fortsetzung der Regel über die Benutzeroberfläche (ResumeWorkflow) mit autorisierter Nutzersitzung.",
				}
			}
			newExecState = "active"
		} else {
			newExecState = "paused_safety_limit"
		}
	} else if in.ExecutionState != "" {
		newExecState = in.ExecutionState
	}

	// Atomic update with optimistic locking
	if in.ExpectedVersion > 0 {
		var res sqlite.Result
		if w.Kind == WorkflowKindNative {
			res, err = s.db.Execute(ctx, `UPDATE workflows SET trigger = ?, config = ?, enabled = ?, execution_state = ?, updated_at = ?, version = version + 1
				WHERE id = ? AND version = ?`, in.Trigger, string(cfgJSON), enVal, newExecState, now, id, in.ExpectedVersion)
		} else {
			res, err = s.db.Execute(ctx, `UPDATE workflows SET name = ?, trigger = ?, action = ?, config = ?, enabled = ?, execution_state = ?, updated_at = ?, version = version + 1
				WHERE id = ? AND version = ?`, in.Name, in.Trigger, in.Action, string(cfgJSON), enVal, newExecState, now, id, in.ExpectedVersion)
		}
		if err != nil {
			return Workflow{}, err
		}
		if res.Changes == 0 {
			return Workflow{}, &ValidationError{
				Code:       "ERR_VERSION_CONFLICT",
				Field:      "expected_version",
				Message:    fmt.Sprintf("Die Regel wurde zwischenzeitlich geändert (erwartete Version: %d). Bitte laden Sie die Regel neu.", in.ExpectedVersion),
				Resolution: "Laden Sie die Regel mit list_workflows neu und wenden Sie Ihre Änderungen auf die aktuelle Version an.",
			}
		}
	} else {
		if w.Kind == WorkflowKindNative {
			_, err = s.db.Execute(ctx, `UPDATE workflows SET trigger = ?, config = ?, enabled = ?, execution_state = ?, updated_at = ?, version = version + 1 WHERE id = ?`,
				in.Trigger, string(cfgJSON), enVal, newExecState, now, id)
		} else {
			_, err = s.db.Execute(ctx, `UPDATE workflows SET name = ?, trigger = ?, action = ?, config = ?, enabled = ?, execution_state = ?, updated_at = ?, version = version + 1 WHERE id = ?`,
				in.Name, in.Trigger, in.Action, string(cfgJSON), enVal, newExecState, now, id)
		}
		if err != nil {
			return Workflow{}, err
		}
	}

	return s.GetWorkflow(ctx, id)
}

// DeleteWorkflow removes a custom workflow. Native workflows cannot be deleted.
func (s *Service) DeleteWorkflow(ctx context.Context, id int64) error {
	w, err := s.GetWorkflow(ctx, id)
	if err != nil {
		return err
	}
	if w.Kind == WorkflowKindNative {
		return errors.New("cannot delete native preset workflow")
	}

	_, err = s.db.Execute(ctx, "DELETE FROM workflows WHERE id = ?", id)
	return err
}

// SetWorkflowEnabled activates or deactivates a workflow.
func (s *Service) SetWorkflowEnabled(ctx context.Context, id int64, enabled bool) error {
	return s.SetWorkflowEnabledWithVersion(ctx, id, enabled, 0)
}

// SetWorkflowEnabledWithVersion activates or deactivates a workflow with optimistic concurrency check.
func (s *Service) SetWorkflowEnabledWithVersion(ctx context.Context, id int64, enabled bool, expectedVersion int64) error {
	w, err := s.GetWorkflow(ctx, id)
	if err != nil {
		return err
	}

	if expectedVersion > 0 && w.Version != expectedVersion {
		return &ValidationError{
			Code:       "ERR_VERSION_CONFLICT",
			Field:      "expected_version",
			Message:    fmt.Sprintf("Die Regel wurde zwischenzeitlich geändert (aktuelle Version: %d, erwartete Version: %d). Bitte laden Sie die Regel neu.", w.Version, expectedVersion),
			Resolution: "Laden Sie die Regel mit list_workflows neu und prüfen Sie den aktuellen Zustand.",
		}
	}

	enVal := 0
	if enabled {
		enVal = 1
	}
	now := s.clk().Now().Unix()

	if expectedVersion > 0 {
		res, err := s.db.Execute(ctx, "UPDATE workflows SET enabled = ?, updated_at = ?, version = version + 1 WHERE id = ? AND version = ?", enVal, now, id, expectedVersion)
		if err != nil {
			return err
		}
		if res.Changes == 0 {
			return &ValidationError{
				Code:       "ERR_VERSION_CONFLICT",
				Field:      "expected_version",
				Message:    fmt.Sprintf("Die Regel wurde zwischenzeitlich geändert (erwartete Version: %d). Bitte laden Sie die Regel neu.", expectedVersion),
				Resolution: "Laden Sie die Regel mit list_workflows neu und prüfen Sie den aktuellen Zustand.",
			}
		}
	} else {
		if _, err := s.db.Execute(ctx, "UPDATE workflows SET enabled = ?, updated_at = ?, version = version + 1 WHERE id = ?", enVal, now, id); err != nil {
			return err
		}
	}

	// Keep purge policies synchronized
	if w.PresetID == PresetPurgeTrash {
		days := 0
		if enabled {
			days = w.Config.Days
			if days == 0 {
				days = 30
			}
		}
		_ = s.SetPurge(ctx, w.AccountID, "trash", days)
	} else if w.PresetID == PresetPurgeJunk {
		days := 0
		if enabled {
			days = w.Config.Days
			if days == 0 {
				days = 30
			}
		}
		_ = s.SetPurge(ctx, w.AccountID, "junk", days)
	}

	return nil
}

type messageCandidate struct {
	ID           int64
	Date         int64
	Subject      string
	From         string
	AttCount     int64
	Flags        string
	EnteredAt    int64
	AuthResults  string
	SecStatus    string
	SecReason    string
	SecScannedAt int64
}

func (s *Service) matchWorkflowFilters(ctx context.Context, cfg WorkflowConfig, hasPurge bool, nowTime int64, m messageCandidate) bool {
	type filterResult struct{ matched bool }
	var filters []filterResult

	// 1. Age filter (for non-purge rules)
	if cfg.Days > 0 && !hasPurge {
		ageDays := int((nowTime - m.Date) / 86400)
		filters = append(filters, filterResult{matched: ageDays >= cfg.Days})
	}

	// 2. Label filter
	if cfg.LabelID != 0 {
		hasLabelRows, _ := s.db.Query(ctx, "SELECT 1 FROM message_labels WHERE message_id = ? AND label_id = ? AND active = 1", m.ID, cfg.LabelID)
		filters = append(filters, filterResult{matched: len(hasLabelRows.Values) > 0})
	}

	// 3. Attachments
	if cfg.HasAttachments != nil {
		hasAtt := m.AttCount > 0
		filters = append(filters, filterResult{matched: hasAtt == *cfg.HasAttachments})
	}

	// 4. Unread flag
	if cfg.IsUnread != nil {
		unread := !strings.Contains(m.Flags, `\Seen`)
		filters = append(filters, filterResult{matched: unread == *cfg.IsUnread})
	}

	// 5. From pattern
	if cfg.FromPattern != "" {
		email := extractEmailAddress(m.From)
		filters = append(filters, filterResult{matched: matchSenderPattern(cfg.FromPattern, email)})
	}

	// 6. Subject pattern
	if cfg.SubjectPattern != "" {
		filters = append(filters, filterResult{matched: matchSubjectPattern(cfg.SubjectOp, cfg.SubjectPattern, m.Subject)})
	}

	// 7. Bedrohungsbewertung (Threat Assessment)
	threat := cfg.EffectiveThreatAssessment()
	if threat != "" {
		actualThreat := ResolveThreatState(m.SecScannedAt, m.SecStatus, m.SecReason)
		var matched bool
		switch threat {
		case "warning":
			matched = actualThreat == ThreatWarning
		case "suspicious":
			matched = actualThreat == ThreatSuspicious
		case "clean":
			matched = actualThreat == ThreatClean
		}
		filters = append(filters, filterResult{matched: matched})
	}

	// 8. Domain-Authentifizierung (RFC 7489 & RFC 8601)
	if cfg.DomainAuth != "" {
		actualAuth := ResolveDomainAuthState(m.From, m.AuthResults, m.SecStatus, m.SecReason, m.SecScannedAt)
		var matched bool
		switch cfg.DomainAuth {
		case "verified":
			matched = actualAuth == DomainAuthVerified
		case "failed":
			matched = actualAuth == DomainAuthFailed
		case "unverified":
			matched = actualAuth == DomainAuthUnverified
		}
		filters = append(filters, filterResult{matched: matched})
	}

	if len(filters) == 0 {
		return true
	}

	if cfg.CondLogic == "or" {
		for _, f := range filters {
			if f.matched {
				return true
			}
		}
		return false
	}

	for _, f := range filters {
		if !f.matched {
			return false
		}
	}
	return true
}

// PreviewWorkflow performs validation and simulates matching messages for a proposed workflow definition.
func (s *Service) PreviewWorkflow(ctx context.Context, in WorkflowInput) (WorkflowPreview, error) {
	if valErr := s.ValidateWorkflowInput(ctx, in); valErr != nil {
		return WorkflowPreview{
			Valid:   false,
			Summary: valErr.Error(),
		}, valErr
	}

	actions := in.Config.Actions
	if len(actions) == 0 && in.Action != "" {
		actions = []WorkflowActionItem{{Type: in.Action, LabelID: in.Config.LabelID, LabelName: in.Config.LabelName}}
	}
	actions = SortActions(actions)

	hasPurge := false
	hasTrash := false
	hasArchive := false
	for _, a := range actions {
		switch a.Type {
		case WorkflowActionPurge:
			hasPurge = true
		case WorkflowActionTrash, WorkflowActionDelete:
			hasTrash = true
		case WorkflowActionArchive:
			hasArchive = true
		}
	}

	// Plain text human-readable summary
	var summaryParts []string
	switch in.Trigger {
	case WorkflowTriggerDaily:
		role := in.Config.FolderRole
		if role == "" {
			role = "Ordner"
		}
		summaryParts = append(summaryParts, fmt.Sprintf("Auslöser: Täglich um 03:00 Uhr im Ordner '%s'", role))
	case WorkflowTriggerReceive:
		summaryParts = append(summaryParts, "Auslöser: Bei E-Mail-Eingang")
	case WorkflowTriggerLabel:
		lbl := in.Config.TriggerLabelName
		if lbl == "" && in.Config.EffectiveTriggerLabelID() != 0 {
			lbl = fmt.Sprintf("ID %d", in.Config.EffectiveTriggerLabelID())
		}
		summaryParts = append(summaryParts, fmt.Sprintf("Auslöser: Bei Zuweisung des Labels '%s'", lbl))
	case WorkflowTriggerSecurity:
		threat := in.Config.EffectiveThreatAssessment()
		summaryParts = append(summaryParts, fmt.Sprintf("Auslöser: Bei Bedrohungsbewertung (%s)", threat))
	}

	// Filter summary
	var filterDescs []string
	if in.Config.Days > 0 && !hasPurge {
		filterDescs = append(filterDescs, fmt.Sprintf("älter als %d Tage", in.Config.Days))
	}
	if in.Config.FromPattern != "" {
		filterDescs = append(filterDescs, fmt.Sprintf("Absender entspricht '%s'", in.Config.FromPattern))
	}
	if in.Config.SubjectPattern != "" {
		opText := "enthält"
		if in.Config.SubjectOp == "equals" {
			opText = "ist gleich"
		} else if in.Config.SubjectOp == "starts_with" {
			opText = "beginnt mit"
		}
		filterDescs = append(filterDescs, fmt.Sprintf("Betreff %s %q", opText, in.Config.SubjectPattern))
	}
	if in.Config.EffectiveThreatAssessment() != "" {
		filterDescs = append(filterDescs, fmt.Sprintf("Sicherheitsstatus: %s", in.Config.EffectiveThreatAssessment()))
	}
	if in.Config.DomainAuth != "" {
		filterDescs = append(filterDescs, fmt.Sprintf("Domain-Authentifizierung: %s", in.Config.DomainAuth))
	}
	if in.Config.HasAttachments != nil {
		if *in.Config.HasAttachments {
			filterDescs = append(filterDescs, "mit Anhängen")
		} else {
			filterDescs = append(filterDescs, "ohne Anhänge")
		}
	}
	if in.Config.LabelName != "" || in.Config.LabelID != 0 {
		lName := in.Config.LabelName
		if lName == "" {
			lName = fmt.Sprintf("ID %d", in.Config.LabelID)
		}
		filterDescs = append(filterDescs, fmt.Sprintf("mit Label '%s'", lName))
	}
	if len(filterDescs) > 0 {
		logic := " UND "
		if in.Config.CondLogic == "or" {
			logic = " ODER "
		}
		summaryParts = append(summaryParts, fmt.Sprintf("Filter: %s", strings.Join(filterDescs, logic)))
	} else {
		summaryParts = append(summaryParts, "Filter: Keine zusätzlichen Filter")
	}

	var actDescs []string
	for _, a := range actions {
		switch a.Type {
		case WorkflowActionPurge:
			actDescs = append(actDescs, fmt.Sprintf("Endgültig löschen (nach %d Tagen)", in.Config.Days))
		case WorkflowActionTrash, WorkflowActionDelete:
			actDescs = append(actDescs, "In den Papierkorb verschieben")
		case WorkflowActionArchive:
			actDescs = append(actDescs, "Archivieren")
		case WorkflowActionJunk:
			actDescs = append(actDescs, "Als Spam markieren")
		case WorkflowActionMarkRead:
			actDescs = append(actDescs, "Als gelesen markieren")
		case WorkflowActionMarkUnread:
			actDescs = append(actDescs, "Als ungelesen markieren")
		case WorkflowActionStar:
			actDescs = append(actDescs, "Mit Stern markieren")
		case WorkflowActionUnstar:
			actDescs = append(actDescs, "Stern entfernen")
		case WorkflowActionForward:
			to := a.ForwardTo
			if to == "" {
				to = in.Config.ForwardTo
			}
			actDescs = append(actDescs, fmt.Sprintf("Direkt weiterleiten an %s", to))
		case WorkflowActionExportEML:
			actDescs = append(actDescs, "E-Mail als .eml speichern")
		case WorkflowActionExportPDF:
			actDescs = append(actDescs, "E-Mail als .pdf speichern")
		case WorkflowActionExportAttachments:
			actDescs = append(actDescs, "E-Mail-Anhänge speichern")
		case WorkflowActionWitDraft:
			actDescs = append(actDescs, "Antwortentwurf mit Wit erstellen")
		case WorkflowActionWitExtract:
			actDescs = append(actDescs, "Daten mit Wit extrahieren (.md)")
		case WorkflowActionUnsubscribe:
			actDescs = append(actDescs, "Newsletter-Abmeldung")
		case WorkflowActionApplyLabel:
			lbl := a.LabelName
			if lbl == "" {
				lbl = fmt.Sprintf("ID %d", a.LabelID)
			}
			actDescs = append(actDescs, fmt.Sprintf("Label '%s' zuweisen", lbl))
		case WorkflowActionRemoveLabel:
			lbl := a.LabelName
			if lbl == "" {
				lbl = fmt.Sprintf("ID %d", a.LabelID)
			}
			actDescs = append(actDescs, fmt.Sprintf("Label '%s' entfernen", lbl))
		}
	}
	summaryParts = append(summaryParts, fmt.Sprintf("Aktionen: %s", strings.Join(actDescs, " -> ")))

	summary := strings.Join(summaryParts, " | ")

	// Query candidate messages in folder scope
	var conds []string
	var args []any
	conds = append(conds, "m.account_id = ?")
	args = append(args, in.AccountID)

	if in.Config.FolderRole != "" {
		conds = append(conds, "m.folder_id IN (SELECT id FROM folders WHERE role = ? AND account_id = ?)")
		args = append(args, in.Config.FolderRole, in.AccountID)
	} else if in.Config.FolderID != 0 {
		conds = append(conds, "m.folder_id = ?")
		args = append(args, in.Config.FolderID)
	}

	whereClause := strings.Join(conds, " AND ")
	q := fmt.Sprintf(`SELECT m.id, m.date, m.subject, m.from_addr,
		COALESCE((SELECT COUNT(*) FROM attachments WHERE message_id = m.id), 0),
		m.flags, COALESCE(m.entered_at, 0),
		COALESCE(m.auth_results, ''), COALESCE(m.security_status, ''), COALESCE(m.security_reason, ''), COALESCE(m.security_scanned_at, 0)
		FROM messages m WHERE %s ORDER BY m.date ASC`, whereClause)
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return WorkflowPreview{}, err
	}

	nowTime := s.clk().Now().Unix()
	var candidateIDs []int64
	var samples []WorkflowSampleMessage

	for _, r := range rows.Values {
		m := messageCandidate{
			ID:           store.Int(r[0]),
			Date:         store.Int(r[1]),
			Subject:      store.Str(r[2]),
			From:         store.Str(r[3]),
			AttCount:     store.Int(r[4]),
			Flags:        store.Str(r[5]),
			EnteredAt:    store.Int(r[6]),
			AuthResults:  store.Str(r[7]),
			SecStatus:    store.Str(r[8]),
			SecReason:    store.Str(r[9]),
			SecScannedAt: store.Int(r[10]),
		}

		if hasPurge {
			// Strict folder residence check: entered_at > 0 and days in folder
			if m.EnteredAt <= 0 || m.EnteredAt > nowTime-int64(in.Config.Days*86400) {
				continue
			}
		}

		if !s.matchWorkflowFilters(ctx, in.Config, hasPurge, nowTime, m) {
			continue
		}

		candidateIDs = append(candidateIDs, m.ID)
		if len(samples) < 5 {
			samples = append(samples, WorkflowSampleMessage{
				ID:      m.ID,
				Subject: m.Subject,
				From:    m.From,
				Date:    m.Date,
			})
		}
	}
	candidateCount := len(candidateIDs)

	isEvent := in.Trigger == WorkflowTriggerReceive || in.Trigger == WorkflowTriggerLabel || in.Trigger == WorkflowTriggerSecurity
	simNote := ""
	if isEvent {
		simNote = "Hinweis: Diese Vorschau zeigt bestehende Nachrichten, die den Bedingungen entsprechen. Bei ereignisbasierten Regeln handelt es sich um eine Simulation anhand vorhandener Daten, nicht um eine Vorhersage zukünftiger E-Mail-Eingänge."
	}

	var reqHumanApproval bool
	var confirmPrompt string
	var confirmReqID string
	var normalizedHash string
	confirmMsg := ""

	if hasPurge {
		confirmMsg = "ACHTUNG: Endgültiges Löschen entfernt Nachrichten unwiderruflich sowohl lokal als auch auf dem Server des Mailanbieters (IMAP). Eine Wiederherstellung ist ausgeschlossen."
		reqHumanApproval = true
		normalizedHash = ComputeNormalizedPurgeConfigHash(in)
		confirmReqID = fmt.Sprintf("req_purge_%s", normalizedHash[:16])
		confirmPrompt = "Endgültiges Löschen von Nachrichten in Trash/Junk erfordert die ausdrückliche Freigabe durch den Nutzer über die Benutzeroberfläche. Diese Freigabe kann nicht vom LLM erteilt werden."
	} else if hasTrash {
		confirmMsg = "Nachrichten werden in den Papierkorb verschoben. Sie verbleiben dort und können bei Bedarf wiederhergestellt werden."
	} else if hasArchive {
		confirmMsg = "Nachrichten werden archiviert und bleiben über die Suche sowie im Archivordner verfügbar."
	} else {
		confirmMsg = "Die Aktionen werden auf die zutreffenden E-Mails angewendet."
	}

	return WorkflowPreview{
		Valid:                 true,
		Summary:               summary,
		CandidateCount:        candidateCount,
		CandidateIDs:          candidateIDs,
		Samples:               samples,
		IsEventTrigger:        isEvent,
		SimulationNote:        simNote,
		IsIrreversible:        hasPurge,
		ConfirmationMessage:   confirmMsg,
		RequiresHumanApproval: reqHumanApproval,
		ConfirmationPrompt:    confirmPrompt,
		ConfirmationRequestID: confirmReqID,
		NormalizedConfigHash:  normalizedHash,
		PurgeProof:            "",
	}, nil
}

type workflowChainContextKey struct{}

func getWorkflowChain(ctx context.Context) []int64 {
	if v := ctx.Value(workflowChainContextKey{}); v != nil {
		if chain, ok := v.([]int64); ok {
			return chain
		}
	}
	return nil
}

// extractEmailAddress parses the email address from a From header value (e.g. "Name <user@domain.com>").
func extractEmailAddress(raw string) string {
	if a, err := netmail.ParseAddress(raw); err == nil && a.Address != "" {
		return strings.ToLower(strings.TrimSpace(a.Address))
	}
	if start := strings.Index(raw, "<"); start >= 0 {
		if end := strings.Index(raw[start:], ">"); end > 1 {
			return strings.ToLower(strings.TrimSpace(raw[start+1 : start+end]))
		}
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

// matchSenderPattern compares the extracted email address against an exact address or wildcard syntax.
func matchSenderPattern(pattern, email string) bool {
	pat := strings.ToLower(strings.TrimSpace(pattern))
	if pat == "" {
		return true
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if strings.HasPrefix(pat, "@") {
		return strings.HasSuffix(email, pat)
	}
	if strings.ContainsAny(pat, "*?") {
		var re strings.Builder
		re.WriteString("^")
		for i := 0; i < len(pat); i++ {
			c := pat[i]
			switch c {
			case '*':
				re.WriteString(".*")
			case '?':
				re.WriteString(".")
			case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
				re.WriteString("\\")
				re.WriteByte(c)
			default:
				re.WriteByte(c)
			}
		}
		re.WriteString("$")
		matched, err := regexp.MatchString(re.String(), email)
		return err == nil && matched
	}
	return email == pat
}

// matchSubjectPattern compares subject against pattern using explicit operators: contains, equals, starts_with.
func matchSubjectPattern(op, pattern, subject string) bool {
	subj := strings.ToLower(strings.TrimSpace(subject))
	pat := strings.ToLower(strings.TrimSpace(pattern))
	if pat == "" {
		return true
	}
	switch op {
	case "equals":
		return subj == pat
	case "starts_with":
		return strings.HasPrefix(subj, pat)
	default: // "contains"
		return strings.Contains(subj, pat)
	}
}

// sanitizeFilename cleans strings for safe filesystem usage across operating systems.
func sanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unbenannt"
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	res := strings.Trim(b.String(), " ._")
	if res == "" {
		res = "unbenannt"
	}
	runes := []rune(res)
	if len(runes) > 100 {
		runes = runes[:100]
	}
	return string(runes)
}

// writeUniqueFile writes data to dir/baseName+ext, incrementing (1), (2) if the file already exists.
func writeUniqueFile(dir, baseName, ext string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	cleanBase := sanitizeFilename(baseName)
	cleanExt := ext
	if !strings.HasPrefix(cleanExt, ".") && cleanExt != "" {
		cleanExt = "." + cleanExt
	}
	targetPath := filepath.Join(dir, cleanBase+cleanExt)
	counter := 1
	for {
		if _, err := os.Stat(targetPath); errors.Is(err, os.ErrNotExist) {
			break
		}
		targetPath = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", cleanBase, counter, cleanExt))
		counter++
		if counter > 1000 {
			break
		}
	}
	if err := os.WriteFile(targetPath, data, 0644); err != nil {
		return "", err
	}
	return targetPath, nil
}

// generateEmailPDF produces a clean, readable PDF 1.4 document containing email headers and body.
func generateEmailPDF(subject, from, to, dateStr, bodyText string) []byte {
	var content strings.Builder
	content.WriteString("BT\n/F1 14 Tf\n50 780 Td\n18 TL\n")

	escapePDF := func(s string) string {
		s = strings.ReplaceAll(s, "\\", "\\\\")
		s = strings.ReplaceAll(s, "(", "\\(")
		s = strings.ReplaceAll(s, ")", "\\)")
		return s
	}

	content.WriteString(fmt.Sprintf("(Betreff: %s) '\n", escapePDF(subject)))
	content.WriteString("/F1 10 Tf\n14 TL\n")
	content.WriteString(fmt.Sprintf("(Von: %s) '\n", escapePDF(from)))
	content.WriteString(fmt.Sprintf("(An: %s) '\n", escapePDF(to)))
	content.WriteString(fmt.Sprintf("(Datum: %s) '\n", escapePDF(dateStr)))
	content.WriteString("() '\n")

	lines := strings.Split(bodyText, "\n")
	y := 700
	for _, l := range lines {
		l = strings.TrimRight(l, "\r\n")
		for len(l) > 90 {
			chunk := l[:90]
			l = l[90:]
			content.WriteString(fmt.Sprintf("(%s) '\n", escapePDF(chunk)))
			y -= 14
			if y < 50 {
				break
			}
		}
		content.WriteString(fmt.Sprintf("(%s) '\n", escapePDF(l)))
		y -= 14
		if y < 50 {
			break
		}
	}
	content.WriteString("ET\n")

	stream := content.String()
	streamLen := len(stream)

	var doc strings.Builder
	doc.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")

	offsets := make([]int, 6)
	offsets[1] = doc.Len()
	doc.WriteString("1 0 obj\n<</Type /Catalog /Pages 2 0 R>>\nendobj\n")

	offsets[2] = doc.Len()
	doc.WriteString("2 0 obj\n<</Type /Pages /Kids [3 0 R] /Count 1>>\nendobj\n")

	offsets[3] = doc.Len()
	doc.WriteString("3 0 obj\n<</Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources <</Font <</F1 5 0 R>>>>>>\nendobj\n")

	offsets[4] = doc.Len()
	doc.WriteString(fmt.Sprintf("4 0 obj\n<</Length %d>>\nstream\n%sendstream\nendobj\n", streamLen, stream))

	offsets[5] = doc.Len()
	doc.WriteString("5 0 obj\n<</Type /Font /Subtype /Type1 /BaseFont /Helvetica>>\nendobj\n")

	xrefOffset := doc.Len()
	doc.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		doc.WriteString(fmt.Sprintf("%010d 00000 n \n", offsets[i]))
	}
	doc.WriteString(fmt.Sprintf("trailer\n<</Size 6 /Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", xrefOffset))

	return []byte(doc.String())
}

// resolveWorkflowCaseDir determines the directory for saving workflow files, creating a case subfolder if requested.
func (s *Service) resolveWorkflowCaseDir(ctx context.Context, act WorkflowActionItem, cfg WorkflowConfig, msgID int64, nowTime int64) (string, error) {
	baseDir := act.TargetDirectory
	if baseDir == "" {
		baseDir = cfg.TargetDirectory
	}
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return "", errors.New("kein Zielverzeichnis angegeben")
	}

	bundle := true
	if act.BundleSubfolder != nil {
		bundle = *act.BundleSubfolder
	} else if cfg.BundleSubfolder != nil {
		bundle = *cfg.BundleSubfolder
	}

	if !bundle {
		if err := os.MkdirAll(baseDir, 0755); err != nil {
			return "", err
		}
		return baseDir, nil
	}

	var dateUnix int64
	var fromAddr, subject string
	if s != nil && s.db != nil {
		rows, err := s.db.Query(ctx, "SELECT date, from_addr, subject FROM messages WHERE id = ?", msgID)
		if err == nil && len(rows.Values) > 0 {
			dateUnix = store.Int(rows.Values[0][0])
			fromAddr = store.Str(rows.Values[0][1])
			subject = store.Str(rows.Values[0][2])
		}
	}
	if dateUnix <= 0 {
		dateUnix = nowTime
	}
	if dateUnix <= 0 {
		dateUnix = time.Now().Unix()
	}

	dateStr := time.Unix(dateUnix, 0).Format("2006-01-02")
	cleanFrom := sanitizeFilename(extractEmailAddress(fromAddr))
	cleanSubj := sanitizeFilename(subject)

	subfolderName := fmt.Sprintf("%s - %s - %s", dateStr, cleanFrom, cleanSubj)
	caseDir := filepath.Join(baseDir, subfolderName)
	if err := os.MkdirAll(caseDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir case folder %s: %w", caseDir, err)
	}
	return caseDir, nil
}

func extractDomain(raw string) string {
	email := extractEmailAddress(raw)
	_, d, _ := strings.Cut(email, "@")
	return d
}

// executeWorkflowForward safely forwards an email to a recipient via direct SMTP/outbox with loop protection.
func (s *Service) executeWorkflowForward(ctx context.Context, workflowID, accountID, msgID int64, toAddr string) (string, error) {
	toAddr = strings.TrimSpace(toAddr)
	if toAddr == "" {
		return "", errors.New("keine Empfängeradresse für Weiterleitung")
	}

	raw, err := s.GetRawMessage(ctx, msgID)
	if err != nil {
		return "", fmt.Errorf("get raw message %d: %w", msgID, err)
	}

	rawStr := string(raw)
	if strings.Contains(rawStr, "X-Godwit-Automated-Forward") ||
		strings.Contains(rawStr, "Auto-Submitted: auto-generated") {
		slog.Info("workflow forward skipped to prevent loop: mail already marked auto-generated",
			"workflow_id", workflowID, "message_id", msgID)
		return "skipped_auto_generated", nil
	}

	rows, err := s.db.Query(ctx, "SELECT from_addr, subject FROM messages WHERE id = ?", msgID)
	if err != nil || len(rows.Values) == 0 {
		return "", fmt.Errorf("message %d not found", msgID)
	}
	fromAddr := store.Str(rows.Values[0][0])
	subject := store.Str(rows.Values[0][1])

	if strings.EqualFold(extractEmailAddress(fromAddr), extractEmailAddress(toAddr)) {
		slog.Info("workflow forward skipped to prevent loop: sender is forward recipient",
			"workflow_id", workflowID, "message_id", msgID, "recipient", toAddr)
		return "skipped_loop_prevention", nil
	}

	acc, err := s.account(ctx, accountID)
	if err != nil {
		return "", fmt.Errorf("account %d not found: %w", accountID, err)
	}

	fromHdr := acc.Email
	if acc.Name != "" {
		fromHdr = fmt.Sprintf("%s <%s>", acc.Name, acc.Email)
	}

	bodyHTML, _ := s.StoredHTML(ctx, msgID)
	if bodyHTML == "" {
		bodyHTML = fmt.Sprintf("<p>Weitergeleitete Nachricht von %s</p>", fromAddr)
	}

	fwdSubject := subject
	if !strings.HasPrefix(strings.ToLower(fwdSubject), "fwd:") && !strings.HasPrefix(strings.ToLower(fwdSubject), "wg:") {
		fwdSubject = "Fwd: " + fwdSubject
	}

	outgoing := gomail.Outgoing{
		From:    fromHdr,
		To:      []string{toAddr},
		Subject: fwdSubject,
		HTML:    bodyHTML,
	}

	fwdRaw, messageIDHdr, err := gomail.Build(outgoing, s.clk().Now())
	if err != nil {
		return "", fmt.Errorf("build forward message: %w", err)
	}

	loopHeader := fmt.Sprintf("Auto-Submitted: auto-generated\r\nX-Godwit-Automated-Forward: %d\r\n", workflowID)
	fwdRaw = append([]byte(loopHeader), fwdRaw...)

	rawPath := filepath.Join(s.dataDir, "outbox", fmt.Sprintf("fwd-%d-%d.eml", msgID, time.Now().UnixNano()))
	if err := store.WriteFileAtomic(rawPath, fwdRaw); err != nil {
		return "", fmt.Errorf("write outbox file: %w", err)
	}

	if _, err := s.db.Execute(ctx, `INSERT INTO outbox(account_id, message_id_hdr, raw_path, rcpts, state, subject, created_at, send_after)
		VALUES (?, ?, ?, ?, 'queued', ?, ?, ?)`,
		accountID, messageIDHdr, rawPath, encodeList([]string{toAddr}), fwdSubject, s.clk().Now().Unix(), s.clk().Now().UnixMilli()); err != nil {
		os.Remove(rawPath)
		return "", fmt.Errorf("insert outbox: %w", err)
	}

	s.bus.Publish(events.Event{Type: events.OutboxChanged, AccountID: accountID})
	s.wakeWorker(accountID)
	return "sent", nil
}

// executeWorkflowWitDraft prepares an AI-generated draft response in the Drafts folder.
func (s *Service) executeWorkflowWitDraft(ctx context.Context, accountID, msgID int64, instructions string) (int64, error) {
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		instructions = "Erstelle eine höfliche Eingangsbestätigung."
	}

	acc, err := s.account(ctx, accountID)
	if err != nil {
		return 0, fmt.Errorf("account %d not found: %w", accountID, err)
	}

	d, err := s.NewDraft(ctx, accountID, "reply", msgID)
	if err != nil {
		return 0, fmt.Errorf("new draft: %w", err)
	}

	m, err := s.mailForModel(ctx, msgID)
	var generatedReply string
	if err == nil {
		if model, _, rErr := s.resolveTask(ctx, "", false); rErr == nil {
			systemPrompt := `Du bist Wit, ein professioneller E-Mail-Assistent für Godwit.
Deine Aufgabe ist es, einen vollständigen, höflichen und stilsicheren Antwortentwurf auf die empfangene E-Mail zu verfassen.

Regeln:
- Schreibe direkt den E-Mail-Text inklusive passender Anrede und Grußformel.
- Antworte in der Sprache der empfangenen E-Mail (standardmäßig Deutsch).
- Halte dich präzise an die Anweisungen des Nutzers.
- Erfinde keine Fakten oder Zusagen, die nicht in den Anweisungen stehen.`

			senderInfo := fmt.Sprintf("Du verfasst den Entwurf im Namen dieses Nutzers:\n- Name: %s\n- E-Mail: %s", acc.Name, acc.Email)
			if acc.AIInstructions != "" {
				senderInfo += fmt.Sprintf("\n- Persönliche Richtlinien & Fakten des Kontos:\n%s", acc.AIInstructions)
			}

			userPrompt := fmt.Sprintf("%s\n\nAnweisungen für den Antwortentwurf:\n%s\n\nEmpfangene E-Mail:\nBetreff: %s\nAbsender: %s\nDatum: %s\n\nInhalt:\n%s",
				senderInfo, instructions, m.Subject, m.From, m.Date.Format("2006-01-02 15:04"), modelBody(m))

			req := ai.Request{
				Model:  model,
				System: systemPrompt,
				Prompt: userPrompt,
			}
			_ = s.gateway().Record(ctx, gateway.Entry{Source: gateway.SourceWit, Tool: "workflow:wit-draft", TargetKind: "message", TargetID: msgID, Decision: "run"})
			if rep, cErr := s.complete(ctx, featureCompose, req, 4<<10); cErr == nil && rep.Text != "" {
				generatedReply = strings.TrimSpace(rep.Restore(rep.Text))
			}
		}
	}

	if generatedReply != "" {
		var htmlParts []string
		for _, para := range strings.Split(generatedReply, "\n\n") {
			para = strings.TrimSpace(para)
			if para != "" {
				htmlParts = append(htmlParts, "<p>"+escapeLines(para)+"</p>")
			}
		}
		replyHTML := strings.Join(htmlParts, "")
		d.HTML = fmt.Sprintf("<div data-godwit=\"wit-draft\">%s</div><br/>%s", replyHTML, d.HTML)
	} else {
		d.HTML = fmt.Sprintf("<p>Guten Tag,</p><p><em>(Entwurf vorbereitet durch Wit: %s)</em></p><br/>%s",
			instructions, d.HTML)
	}

	savedDraft, err := s.SaveDraft(ctx, d)
	if err != nil {
		return 0, fmt.Errorf("save draft: %w", err)
	}
	return savedDraft.ID, nil
}

// executeWorkflowWitExtract extracts key information into a Markdown document in the case folder.
func (s *Service) executeWorkflowWitExtract(ctx context.Context, caseDir string, msgID int64, template string) (string, error) {
	m, err := s.mailForModel(ctx, msgID)
	if err != nil {
		return "", fmt.Errorf("message %d not found: %w", msgID, err)
	}

	fromAddr := m.From
	subject := m.Subject
	dateStr := m.Date.Format("2006-01-02 15:04")
	cleanBody := strings.TrimSpace(modelBody(m))
	if cleanBody == "" && m.Snippet != "" {
		cleanBody = strings.TrimSpace(m.Snippet)
	}
	cleanBody = strings.ToValidUTF8(cleanBody, "")

	focus := strings.TrimSpace(template)
	if focus == "" {
		focus = "Alle wichtigen Informationen extrahieren und in Bullet Points zusammenfassen."
	}

	lang := s.readingLanguage(ctx)

	// Attempt AI extraction via Wit
	var summary string
	model, _, rErr := s.resolveTask(ctx, "", false)
	if rErr == nil {
		systemPrompt := fmt.Sprintf(`Du bist Wit, ein intelligenter E-Mail-Assistent für Godwit.
Deine Aufgabe ist es, den Inhalt der vorliegenden E-Mail zu analysieren und gemäß der gewünschten Vorlage bzw. dem Fokus präzise zusammenzufassen.

Regeln:
- Schreibe die Zusammenfassung direkt auf %s (oder in der Hauptsprache der E-Mail), ohne Begrüßung, ohne Floskeln, ohne Einleitungssatz.
- Erstelle übersichtliche Aufzählungspunkte (Bullet Points mit "- "), die alle relevanten Kerninformationen, Termine, Fristen, Zahlen, Vereinbarungen, Kontaktdaten oder offenen Fragen erfassen.
- Halte die Aufzählungspunkte prägnant, strukturiert und aussagekräftig.
- Falls die Vorlage / der Fokus spezifische Schwerpunkte nennt, richte die Extraktion prioritär danach aus.
- Erfinde keine Fakten und beziehe dich strikt auf den Inhalt der E-Mail.`, lang)

		userPrompt := fmt.Sprintf("Vorlage / Fokus:\n%s\n\nBetreff: %s\nAbsender: %s\nDatum: %s\n\nE-Mail Inhalt:\n%s",
			focus, subject, fromAddr, dateStr, cleanBody)

		req := ai.Request{
			Model:  model,
			System: systemPrompt,
			Prompt: userPrompt,
		}
		_ = s.gateway().Record(ctx, gateway.Entry{Source: gateway.SourceWit, Tool: "workflow:wit-extract", TargetKind: "message", TargetID: msgID, Decision: "run"})
		if rep, cErr := s.complete(ctx, featureWit, req, 8<<10); cErr == nil && rep.Text != "" {
			summary = strings.TrimSpace(rep.Restore(rep.Text))
		}
	}

	var md strings.Builder
	md.WriteString("# Vorgangs-Zusammenfassung (Wit Extraktion)\n\n")
	md.WriteString(fmt.Sprintf("- **Datum:** %s\n", dateStr))
	md.WriteString(fmt.Sprintf("- **Absender:** %s\n", fromAddr))
	md.WriteString(fmt.Sprintf("- **Betreff:** %s\n", subject))
	md.WriteString(fmt.Sprintf("- **Vorlage / Fokus:** %s\n", focus))
	md.WriteString("\n## Extrahierte Kerndaten\n\n")
	md.WriteString("| Feld | Inhalt |\n|---|---|\n")
	md.WriteString(fmt.Sprintf("| Absender-Domain | `%s` |\n", extractDomain(fromAddr)))
	md.WriteString(fmt.Sprintf("| Betreff | %s |\n", subject))
	md.WriteString("| Status | Eingegangen & verarbeitet |\n\n")

	md.WriteString("## Zusammenfassung & Kernpunkte\n\n")
	if summary != "" {
		md.WriteString(summary + "\n\n")
	} else {
		md.WriteString("*(Hinweis: Keine automatische KI-Zusammenfassung verfügbar. Konfigurieren Sie ein Modell in den Godwit-Einstellungen unter 'KI & Modelle'.)*\n\n")
	}

	md.WriteString("## Vollständiger E-Mail-Inhalt\n\n")
	if cleanBody != "" {
		for _, line := range strings.Split(cleanBody, "\n") {
			md.WriteString("> " + line + "\n")
		}
		md.WriteString("\n")
	} else {
		md.WriteString("*(Kein Textinhalt vorhanden)*\n\n")
	}

	md.WriteString("---\n*Automatisch generiert durch Godwit Workflow-Automation mit Wit.*\n")

	filePath, err := writeUniqueFile(caseDir, "Zusammenfassung", ".md", []byte(md.String()))
	if err != nil {
		return "", err
	}
	return filePath, nil
}

// executeWorkflowAction executes an individual workflow action on a message.
func (s *Service) executeWorkflowAction(ctx context.Context, w Workflow, msgID int64, act WorkflowActionItem, now int64) (string, error) {
	switch act.Type {
	case WorkflowActionUnstar:
		err := s.SetFlags(ctx, []int64{msgID}, nil, []string{`\Flagged`})
		if err == nil {
			return act.Type, nil
		}
		return "", err

	case WorkflowActionForward:
		targetAddr := act.ForwardTo
		if targetAddr == "" {
			targetAddr = w.Config.ForwardTo
		}
		status, err := s.executeWorkflowForward(ctx, w.ID, w.AccountID, msgID, targetAddr)
		if err != nil {
			return "", err
		}
		if status == "skipped_loop_prevention" {
			return fmt.Sprintf("forward_skipped:loop_prevention:%s", targetAddr), nil
		}
		if status == "skipped_auto_generated" {
			return fmt.Sprintf("forward_skipped:auto_generated:%s", targetAddr), nil
		}
		return fmt.Sprintf("forward:%s", targetAddr), nil

	case WorkflowActionExportEML:
		caseDir, err := s.resolveWorkflowCaseDir(ctx, act, w.Config, msgID, now)
		if err != nil {
			return "", err
		}
		raw, err := s.GetRawMessage(ctx, msgID)
		if err != nil {
			return "", err
		}
		rows, _ := s.db.Query(ctx, "SELECT subject FROM messages WHERE id = ?", msgID)
		subj := "E-Mail"
		if len(rows.Values) > 0 {
			subj = store.Str(rows.Values[0][0])
		}
		savedPath, err := writeUniqueFile(caseDir, subj, ".eml", raw)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("export_eml:%s", savedPath), nil

	case WorkflowActionExportPDF:
		caseDir, err := s.resolveWorkflowCaseDir(ctx, act, w.Config, msgID, now)
		if err != nil {
			return "", err
		}
		rows, _ := s.db.Query(ctx, "SELECT subject, from_addr, to_addrs, date, snippet FROM messages WHERE id = ?", msgID)
		subj := "E-Mail"
		fromA := ""
		toA := ""
		dateUnix := now
		snippet := ""
		if len(rows.Values) > 0 {
			subj = store.Str(rows.Values[0][0])
			fromA = store.Str(rows.Values[0][1])
			toA = store.Str(rows.Values[0][2])
			dateUnix = store.Int(rows.Values[0][3])
			snippet = store.Str(rows.Values[0][4])
		}
		dateStr := time.Unix(dateUnix, 0).Format("2006-01-02 15:04")
		pdfBytes := generateEmailPDF(subj, fromA, toA, dateStr, snippet)
		savedPath, err := writeUniqueFile(caseDir, subj, ".pdf", pdfBytes)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("export_pdf:%s", savedPath), nil

	case WorkflowActionExportAttachments:
		caseDir, err := s.resolveWorkflowCaseDir(ctx, act, w.Config, msgID, now)
		if err != nil {
			return "", err
		}
		attRows, err := s.db.Query(ctx, "SELECT id, filename, inline FROM attachments WHERE message_id = ?", msgID)
		if err != nil {
			return "", err
		}
		count := 0
		for _, row := range attRows.Values {
			attID := store.Int(row[0])
			filename := store.Str(row[1])
			inline := store.Int(row[2]) == 1
			if inline {
				continue
			}
			srcPath, _, getErr := s.GetAttachment(ctx, attID)
			if getErr != nil {
				continue
			}
			data, readErr := os.ReadFile(srcPath)
			if readErr != nil {
				continue
			}
			ext := filepath.Ext(filename)
			stem := strings.TrimSuffix(filename, ext)
			if stem == "" {
				stem = "Anhang"
			}
			if _, wErr := writeUniqueFile(caseDir, stem, ext, data); wErr == nil {
				count++
			}
		}
		return fmt.Sprintf("export_attachments:%s:%d", caseDir, count), nil

	case WorkflowActionWitDraft:
		inst := act.Instructions
		if inst == "" {
			inst = w.Config.Instructions
		}
		draftID, err := s.executeWorkflowWitDraft(ctx, w.AccountID, msgID, inst)
		if err == nil {
			return fmt.Sprintf("wit_draft:%d", draftID), nil
		}
		return "", err

	case WorkflowActionWitExtract:
		caseDir, err := s.resolveWorkflowCaseDir(ctx, act, w.Config, msgID, now)
		if err != nil {
			return "", err
		}
		tmpl := act.ExtractTemplate
		if tmpl == "" {
			tmpl = w.Config.ExtractTemplate
		}
		savedPath, err := s.executeWorkflowWitExtract(ctx, caseDir, msgID, tmpl)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("wit_extract:%s", savedPath), nil

	case WorkflowActionMarkRead:
		err := s.SetFlags(ctx, []int64{msgID}, []string{`\Seen`}, nil)
		if err == nil {
			return act.Type, nil
		}
		return "", err

	case WorkflowActionMarkUnread:
		err := s.SetFlags(ctx, []int64{msgID}, nil, []string{`\Seen`})
		if err == nil {
			return act.Type, nil
		}
		return "", err

	case WorkflowActionStar:
		err := s.SetFlags(ctx, []int64{msgID}, []string{`\Flagged`}, nil)
		if err == nil {
			return act.Type, nil
		}
		return "", err

	case WorkflowActionApplyLabel:
		if act.LabelID != 0 {
			err := s.SetLabelWithSource(ctx, []int64{msgID}, act.LabelID, true, fmt.Sprintf("workflow:%d", w.ID))
			if err == nil {
				return fmt.Sprintf("apply_label:%d", act.LabelID), nil
			}
			return "", err
		}
		return "", nil

	case WorkflowActionRemoveLabel:
		if act.LabelID != 0 {
			err := s.SetLabelWithSource(ctx, []int64{msgID}, act.LabelID, false, fmt.Sprintf("workflow:%d", w.ID))
			if err == nil {
				return fmt.Sprintf("remove_label:%d", act.LabelID), nil
			}
			return "", err
		}
		return "", nil

	case WorkflowActionArchive:
		err := s.MoveToRole(ctx, []int64{msgID}, "archive")
		if err == nil {
			return act.Type, nil
		}
		return "", err

	case WorkflowActionJunk:
		err := s.MoveToRole(ctx, []int64{msgID}, "junk")
		if err == nil {
			return act.Type, nil
		}
		return "", err

	case WorkflowActionTrash, WorkflowActionDelete:
		err := s.Delete(ctx, []int64{msgID})
		if err == nil {
			return act.Type, nil
		}
		return "", err

	case WorkflowActionUnsubscribe:
		u, uErr := s.UnsubscribeMethod(ctx, msgID)
		if uErr != nil {
			slog.Warn("workflow unsubscribe lookup failed", "workflow_id", w.ID, "message_id", msgID, "err", uErr)
			return "unsubscribe:failed", uErr
		} else if u.Method == "" || u.Method == UnsubscribeWeb {
			return "unsubscribe:unsupported", nil
		} else {
			res, unsubErr := s.Unsubscribe(ctx, msgID)
			if unsubErr != nil {
				return "unsubscribe:failed", unsubErr
			}
			return fmt.Sprintf("unsubscribe:success(%s)", res.Method), nil
		}

	case WorkflowActionPurge:
		pRows, pErr := s.db.Query(ctx, `SELECT m.folder_id, COALESCE(m.entered_at, 0), f.role
			FROM messages m JOIN folders f ON f.id = m.folder_id WHERE m.id = ?`, msgID)
		if pErr == nil && len(pRows.Values) > 0 {
			pEntAt := store.Int(pRows.Values[0][1])
			pRole := store.Str(pRows.Values[0][2])
			if (pRole == "trash" || pRole == "junk") && pEntAt > 0 && pEntAt <= now-int64(w.Config.Days*86400) {
				delErr := s.DeleteForever(ctx, []int64{msgID})
				if delErr == nil {
					return act.Type, nil
				}
				return "", delErr
			}
		}
		return act.Type, nil

	default:
		return "", fmt.Errorf("unbekannter Aktionstyp %q", act.Type)
	}
}

type workflowLabelCycleKey struct{}

// EvaluateWorkflows runs matching workflows for a message under the specified trigger.
func (s *Service) EvaluateWorkflows(ctx context.Context, accountID int64, trigger string, msgID int64) error {
	workflows, err := s.ListWorkflows(ctx, accountID)
	if err != nil {
		return err
	}

	chain := getWorkflowChain(ctx)
	modifiedLabels, _ := ctx.Value(workflowLabelCycleKey{}).([]int64)

	// Read initial message details including security scanner results and domain auth
	rows, err := s.db.Query(ctx, `SELECT m.id, m.folder_id, m.subject, m.from_addr, m.date, m.flags,
		m.has_attachments, m.security_status, m.security_reason, COALESCE(m.security_scanned_at, 0), COALESCE(m.auth_results, ''),
		COALESCE(m.entered_at, 0)
		FROM messages m WHERE m.id = ? AND m.account_id = ?`, msgID, accountID)
	if err != nil || len(rows.Values) == 0 {
		return err
	}

	r := rows.Values[0]
	folderID := store.Int(r[1])
	subject := store.Str(r[2])
	fromAddr := store.Str(r[3])
	msgDate := store.Int(r[4])
	flags := store.Str(r[5])
	attCount := int(store.Int(r[6]))
	secStatus := store.Str(r[7])
	secReason := store.Str(r[8])
	secScannedAt := store.Int(r[9])
	authResults := store.Str(r[10])
	enteredAt := store.Int(r[11])

	// Lookup folder role
	folderRows, err := s.db.Query(ctx, "SELECT role FROM folders WHERE id = ?", folderID)
	folderRole := ""
	if err == nil && len(folderRows.Values) > 0 {
		folderRole = store.Str(folderRows.Values[0][0])
	}

	// Lookup applied label IDs
	labelRows, err := s.db.Query(ctx, "SELECT label_id FROM message_labels WHERE message_id = ? AND active = 1", msgID)
	var labelIDs []int64
	if err == nil {
		for _, lr := range labelRows.Values {
			labelIDs = append(labelIDs, store.Int(lr[0]))
		}
	}

	now := s.clk().Now().Unix()

	for _, w := range workflows {
		if !w.Enabled || w.Trigger != trigger {
			continue
		}

		// Bei E-Mail-Eingang (on_receive): Gilt ausschließlich für neu eingehende E-Mails!
		// Historische E-Mails, die vor der Erstellung des Workflows empfangen wurden,
		// dürfen bei Re-Scans oder Neu-Analysen NIEMALS rückwirkend benutzerdefinierte on_receive-Workflows auslösen.
		if trigger == WorkflowTriggerReceive && w.Kind == WorkflowKindCustom {
			createdCutoff := w.CreatedAt.Unix() - 60
			if msgDate < createdCutoff || (enteredAt > 0 && enteredAt < createdCutoff) {
				continue
			}
		}

		// Bei E-Mail-Eingang (on_receive) oder Bedrohungsbewertung (on_security):
		// Gilt ausschließlich für neu eingehende / neu eingegangene E-Mails!
		// Historische E-Mails, die vor der Erstellung des Workflows empfangen wurden,
		// dürfen bei Re-Scans oder Neu-Analysen NIEMALS rückwirkend benutzerdefinierte Workflows auslösen.
		if (trigger == WorkflowTriggerReceive || trigger == WorkflowTriggerSecurity) && w.Kind == WorkflowKindCustom {
			createdCutoff := w.CreatedAt.Unix() - 60
			if msgDate < createdCutoff || (enteredAt > 0 && enteredAt < createdCutoff) {
				continue
			}
		}

		// Runtime validation: verify rule validity before execution
		if valErr := s.ValidateWorkflowInput(ctx, WorkflowInput{
			AccountID: accountID,
			Name:      w.Name,
			Trigger:   w.Trigger,
			Action:    w.Action,
			Config:    w.Config,
			Enabled:   w.Enabled,
		}); valErr != nil {
			slog.Warn("workflow validation failed at execution time, skipping", "workflow_id", w.ID, "err", valErr)
			s.recordWorkflowRun(ctx, w.ID, accountID, msgID, w.UpdatedAt.Unix(), w.Action, "skipped",
				fmt.Sprintf("skipped:invalid_workflow_configuration: %v", valErr))
			continue
		}

		// Schleifen- und Rekursionsschutz: Workflow darf nicht mehrfach in derselben Kaskade aufgerufen werden
		if slices.Contains(chain, w.ID) {
			continue
		}
		if len(chain) >= 5 {
			continue // Kaskadentiefe begrenzt
		}

		// Wiederholte Zustellung desselben Ereignisses darf keine unbeabsichtigten doppelten Wirkungen erzeugen:
		// Check if already executed for this workflow and message
		runRows, err := s.db.Query(ctx, "SELECT id FROM workflow_runs WHERE workflow_id = ? AND message_id = ?", w.ID, msgID)
		if err == nil && len(runRows.Values) > 0 {
			continue // Already processed
		}

		// Atomare Vorabprüfung vor jeder Regelausführung:
		// Nachrichtenzustand und Ordner erneut prüfen, falls eine vorherige Regel die Nachricht verschoben oder gelöscht hat
		curRows, curErr := s.db.Query(ctx, `SELECT m.folder_id, m.flags, COALESCE(m.entered_at, 0)
			FROM messages m WHERE m.id = ? AND m.account_id = ?`, msgID, accountID)
		if curErr != nil || len(curRows.Values) == 0 {
			// Nachricht wurde von einer vorherigen Regel unwiderruflich gelöscht (purge) -> Auswertung beenden
			break
		}
		folderID = store.Int(curRows.Values[0][0])
		flags = store.Str(curRows.Values[0][1])
		enteredAt = store.Int(curRows.Values[0][2])
		curFolderRows, _ := s.db.Query(ctx, "SELECT role FROM folders WHERE id = ?", folderID)
		folderRole = ""
		if len(curFolderRows.Values) > 0 {
			folderRole = store.Str(curFolderRows.Values[0][0])
		}

		// Aktuelle Labels neu einlesen
		curLabelRows, _ := s.db.Query(ctx, "SELECT label_id FROM message_labels WHERE message_id = ? AND active = 1", msgID)
		labelIDs = nil
		for _, clr := range curLabelRows.Values {
			labelIDs = append(labelIDs, store.Int(clr[0]))
		}

		cfg := w.Config

		// Auslöser-Validierung bei on_label: Das Auslöser-Label ist Pflichtparameter des Triggers
		// und nimmt nicht an der UND-/ODER-Filterverknüpfung teil.
		if trigger == WorkflowTriggerLabel {
			trigLabelID := cfg.EffectiveTriggerLabelID()
			if trigLabelID == 0 && cfg.TriggerLabelName != "" {
				lRow, _ := s.db.Query(ctx, "SELECT id FROM labels WHERE name = ? AND account_id = ?", cfg.TriggerLabelName, accountID)
				if len(lRow.Values) > 0 {
					trigLabelID = store.Int(lRow.Values[0][0])
				}
			}
			if trigLabelID == 0 || !slices.Contains(labelIDs, trigLabelID) {
				continue // Auslöser-Label nicht erfüllt
			}

			// Label-Schleifenerkennung über mehrere Regeln hinweg:
			// Wenn dieses Label bereits in der aktuellen Kaskade von einer vorherigen Regel zugewiesen wurde,
			// liegt eine zirkuläre Abhängigkeit vor.
			if slices.Contains(modifiedLabels, trigLabelID) {
				slog.Warn("cross-rule label cycle detected, skipping workflow cascade",
					"workflow_id", w.ID, "trigger_label_id", trigLabelID)
				continue
			}
		}

		// Auslöser-Validierung bei on_security: Bedrohungsstufe prüfen
		if trigger == WorkflowTriggerSecurity {
			threat := cfg.EffectiveThreatAssessment()
			actualThreat := ResolveThreatState(secScannedAt, secStatus, secReason)
			var matched bool
			switch threat {
			case "warning", "malicious":
				matched = actualThreat == ThreatWarning
			case "suspicious":
				matched = actualThreat == ThreatSuspicious
			case "clean":
				matched = actualThreat == ThreatClean
			}
			if !matched {
				continue // Auslöser-Bedrohungsstufe nicht erfüllt
			}
		}

		// 1. Geltungsbereich (Scope) prüfen: MUSS zwingend zutreffen (hartes UND, unabhängig von Filterlogik)
		if cfg.FolderRole != "" && cfg.FolderRole != folderRole {
			continue
		}
		if cfg.FolderID != 0 && cfg.FolderID != folderID {
			continue
		}

		// Aktionen bestimmen und in festgelegte Ausführungsreihenfolge bringen:
		// Externe Aktion -> Status -> Labels -> Zielaktion -> Endgültiges Löschen
		actionsToRun := SortActions(cfg.Actions)
		if len(actionsToRun) == 0 && w.Action != "" {
			actionsToRun = SortActions([]WorkflowActionItem{{Type: w.Action, LabelID: cfg.LabelID, LabelName: cfg.LabelName}})
		}

		// 2. Sicherheitsregeln für purge prüfen: Dürfen niemals durch Filter-ODER umgangen werden
		hasPurge := false
		for _, a := range actionsToRun {
			if a.Type == WorkflowActionPurge {
				hasPurge = true
				break
			}
		}
		if hasPurge {
			// Purge ist ausnahmslos nur in Papierkorb oder Spam mit Aufbewahrungsfrist zulässig
			if folderRole != "trash" && folderRole != "junk" {
				continue
			}
			// Verweildauer im Ordner (entered_at) prüfen, nicht nur das Nachrichtenalter!
			// Falls der Zeitpunkt nicht zuverlässig bekannt ist (entered_at <= 0), darf nicht gelöscht werden!
			if enteredAt <= 0 {
				continue
			}
			residenceDays := int((now - enteredAt) / 86400)
			if cfg.Days <= 0 || residenceDays < cfg.Days {
				continue
			}
		}

		// 3. Filter prüfen: Hier (und NUR hier) gilt die Auswahl UND / ODER
		type filterResult struct{ matched bool }
		var filters []filterResult

		if cfg.Days > 0 && !hasPurge {
			ageDays := int((now - msgDate) / 86400)
			filters = append(filters, filterResult{matched: ageDays >= cfg.Days})
		}

		// Zusätzliches Filter-Label prüfen (unterscheidet sich vom Auslöser-Label)
		filterLabelID := cfg.LabelID
		if trigger == WorkflowTriggerLabel && filterLabelID == cfg.EffectiveTriggerLabelID() {
			filterLabelID = 0
		}
		if filterLabelID != 0 {
			hasLabel := slices.Contains(labelIDs, filterLabelID)
			filters = append(filters, filterResult{matched: hasLabel})
		} else if cfg.LabelName != "" && (trigger != WorkflowTriggerLabel || cfg.LabelName != cfg.TriggerLabelName) {
			lRow, _ := s.db.Query(ctx, "SELECT id FROM labels WHERE name = ? AND account_id = ?", cfg.LabelName, accountID)
			if len(lRow.Values) > 0 {
				filters = append(filters, filterResult{matched: slices.Contains(labelIDs, store.Int(lRow.Values[0][0]))})
			}
		}

		// 1. Bedrohungsbewertung (Threat Assessment) - Deterministic state machine
		threat := cfg.EffectiveThreatAssessment()
		if threat != "" {
			actualThreat := ResolveThreatState(secScannedAt, secStatus, secReason)
			var matched bool
			switch threat {
			case "warning":
				matched = actualThreat == ThreatWarning
			case "suspicious":
				matched = actualThreat == ThreatSuspicious
			case "clean":
				matched = actualThreat == ThreatClean
			}
			filters = append(filters, filterResult{matched: matched})
		}

		// 2. Domain-Authentifizierung (Domain Authentication) - RFC 7489 & RFC 8601 aligned evaluation
		if cfg.DomainAuth != "" {
			actualAuth := ResolveDomainAuthState(fromAddr, authResults, secStatus, secReason, secScannedAt)
			var matched bool
			switch cfg.DomainAuth {
			case "verified":
				matched = actualAuth == DomainAuthVerified
			case "failed":
				matched = actualAuth == DomainAuthFailed
			case "unverified":
				matched = actualAuth == DomainAuthUnverified
			}
			filters = append(filters, filterResult{matched: matched})
		}

		// 3. Anhänge
		if cfg.HasAttachments != nil {
			has := attCount > 0
			filters = append(filters, filterResult{matched: has == *cfg.HasAttachments})
		}

		// 4. Ungelesen
		if cfg.IsUnread != nil {
			unread := !strings.Contains(flags, `\Seen`)
			filters = append(filters, filterResult{matched: unread == *cfg.IsUnread})
		}

		// 5. Absender
		if cfg.FromPattern != "" {
			email := extractEmailAddress(fromAddr)
			filters = append(filters, filterResult{matched: matchSenderPattern(cfg.FromPattern, email)})
		}

		// 6. Betreff
		if cfg.SubjectPattern != "" {
			filters = append(filters, filterResult{matched: matchSubjectPattern(cfg.SubjectOp, cfg.SubjectPattern, subject)})
		}

		filterMatched := true
		if len(filters) > 0 {
			if cfg.CondLogic == "or" {
				filterMatched = false
				for _, f := range filters {
					if f.matched {
						filterMatched = true
						break
					}
				}
			} else {
				for _, f := range filters {
					if !f.matched {
						filterMatched = false
						break
					}
				}
			}
		}
		if !filterMatched {
			continue
		}

		// Track modified labels across cascade for loop prevention
		var nextModifiedLabels []int64
		copy(nextModifiedLabels, modifiedLabels)
		for _, a := range actionsToRun {
			if a.Type == WorkflowActionApplyLabel && a.LabelID != 0 {
				nextModifiedLabels = append(nextModifiedLabels, a.LabelID)
			}
		}

		subCtx := context.WithValue(ctx, workflowChainContextKey{}, append(chain, w.ID))
		subCtx = context.WithValue(subCtx, workflowLabelCycleKey{}, nextModifiedLabels)

		var executedActions []string
		var stepFailed bool
		var failErr error

		for _, act := range actionsToRun {
			actRes, actErr := s.executeWorkflowAction(subCtx, w, msgID, act, now)
			if actErr != nil {
				stepFailed = true
				failErr = actErr
				slog.Error("workflow action failed, halting subsequent actions for message",
					"workflow_id", w.ID, "action", act.Type, "message_id", msgID, "err", actErr)
				break
			}
			if actRes != "" {
				executedActions = append(executedActions, actRes)
			}
		}

		runStatus := "success"
		runDetails := strings.Join(executedActions, "+")
		if stepFailed {
			runStatus = "failed"
			runDetails = fmt.Sprintf("failed: %v (ausgeführt: %s)", failErr, runDetails)
		}

		if len(executedActions) > 0 || stepFailed {
			actionSummary := strings.Join(executedActions, "+")
			_, _ = s.db.Execute(ctx, `INSERT INTO workflow_runs(workflow_id, account_id, message_id, workflow_version, action, status, details, ran_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, w.ID, accountID, msgID, w.UpdatedAt.Unix(), actionSummary, runStatus, runDetails, now)
		}
	}

	return nil
}

// RunDailyWorkflows executes scheduled/daily workflows for an account.
func (s *Service) RunDailyWorkflows(ctx context.Context, accountID int64) (int, error) {
	if rErr := s.ReconcilePurgeWorkflowRuns(ctx, accountID); rErr != nil {
		slog.Warn("reconcile purge workflow runs encountered error before daily execution", "err", rErr, "account_id", accountID)
	}
	workflows, err := s.ListWorkflows(ctx, accountID)
	if err != nil {
		return 0, err
	}

	totalActions := 0
	for _, w := range workflows {
		if !w.Enabled || w.Trigger != WorkflowTriggerDaily {
			continue
		}
		if w.ExecutionState == "paused_safety_limit" {
			slog.Warn("workflow execution paused awaiting human approval: safety limit was previously exceeded",
				"workflow_id", w.ID)
			continue
		}
		if w.Kind == WorkflowKindNative && (w.PresetID == PresetPurgeTrash || w.PresetID == PresetPurgeJunk) {
			// Purges are handled via mailsync.QueuePurge during sync
			continue
		}

		// Runtime validation: verify rule configuration before daily execution
		if valErr := s.ValidateWorkflowInput(ctx, WorkflowInput{
			AccountID: accountID,
			Name:      w.Name,
			Trigger:   w.Trigger,
			Action:    w.Action,
			Config:    w.Config,
			Enabled:   w.Enabled,
		}); valErr != nil {
			slog.Warn("daily workflow validation failed at runtime, skipping", "workflow_id", w.ID, "err", valErr)
			s.recordWorkflowRun(ctx, w.ID, accountID, 0, w.UpdatedAt.Unix(), "", "skipped",
				fmt.Sprintf("skipped:invalid_configuration: %v", valErr))
			continue
		}

		actionsToRun := SortActions(w.Config.Actions)
		if len(actionsToRun) == 0 && w.Action != "" {
			actionsToRun = SortActions([]WorkflowActionItem{{Type: w.Action, LabelID: w.Config.LabelID, LabelName: w.Config.LabelName}})
		}

		hasPurge := false
		for _, a := range actionsToRun {
			if a.Type == WorkflowActionPurge {
				hasPurge = true
				break
			}
		}

		// Resolve exact target folder ID
		var targetFolderID int64
		if w.Config.FolderID != 0 {
			targetFolderID = w.Config.FolderID
		} else if w.Config.FolderRole != "" {
			fRow, _ := s.db.Query(ctx, "SELECT id FROM folders WHERE role = ? AND account_id = ?", w.Config.FolderRole, accountID)
			if len(fRow.Values) > 0 {
				targetFolderID = store.Int(fRow.Values[0][0])
			}
		}
		if targetFolderID == 0 {
			slog.Warn("daily workflow target folder not found, skipping", "workflow_id", w.ID, "role", w.Config.FolderRole)
			s.recordWorkflowRun(ctx, w.ID, accountID, 0, w.UpdatedAt.Unix(), "", "skipped", "skipped:folder_not_found")
			continue
		}

		// Query candidate messages in the exact target folder
		conds := []string{"m.account_id = ?", "m.folder_id = ?"}
		args := []any{accountID, targetFolderID}

		if hasPurge {
			// Mandatory hard retention period check on folder residence time (entered_at > 0)
			conds = append(conds, "m.entered_at > 0 AND m.entered_at <= unixepoch() - ?")
			args = append(args, w.Config.Days*86400)
		} else if w.Config.Days > 0 && w.Config.CondLogic != "or" {
			// In AND logic, date constraint can safely optimize SQL query
			conds = append(conds, "m.date <= unixepoch() - ?")
			args = append(args, w.Config.Days*86400)
		}

		if w.Config.IsUnread != nil && *w.Config.IsUnread && w.Config.CondLogic != "or" {
			conds = append(conds, "instr(m.flags, '\\Seen') = 0")
		}

		// Exclude already processed messages
		conds = append(conds, "m.id NOT IN (SELECT message_id FROM workflow_runs WHERE workflow_id = ?)")
		args = append(args, w.ID)

		query := fmt.Sprintf(`SELECT m.id, m.date, m.from_addr, m.subject,
			COALESCE((SELECT COUNT(*) FROM attachments WHERE message_id = m.id), 0),
			m.flags, COALESCE(m.entered_at, 0),
			COALESCE(m.auth_results, ''), COALESCE(m.security_status, ''), COALESCE(m.security_reason, ''), COALESCE(m.security_scanned_at, 0)
			FROM messages m WHERE %s ORDER BY m.date ASC`, strings.Join(conds, " AND "))
		rows, err := s.db.Query(ctx, query, args...)
		if err != nil {
			continue
		}

		nowTime := s.clk().Now().Unix()
		version := w.Version
		if version == 0 {
			version = 1
		}

		// Evaluate actual matching messages against complete filter expression
		var matchingIDs []int64
		for _, r := range rows.Values {
			mID := store.Int(r[0])
			mDate := store.Int(r[1])
			fromAddr := store.Str(r[2])
			subject := store.Str(r[3])
			attCount := store.Int(r[4])
			flags := store.Str(r[5])
			enteredAt := store.Int(r[6])
			authResults := store.Str(r[7])
			secStatus := store.Str(r[8])
			secReason := store.Str(r[9])
			secScannedAt := store.Int(r[10])

			if hasPurge {
				if enteredAt <= 0 || enteredAt > nowTime-int64(w.Config.Days*86400) {
					continue
				}
			}

			m := messageCandidate{
				ID:           mID,
				Date:         mDate,
				Subject:      subject,
				From:         fromAddr,
				AttCount:     attCount,
				Flags:        flags,
				EnteredAt:    enteredAt,
				AuthResults:  authResults,
				SecStatus:    secStatus,
				SecReason:    secReason,
				SecScannedAt: secScannedAt,
			}

			if s.matchWorkflowFilters(ctx, w.Config, hasPurge, nowTime, m) {
				matchingIDs = append(matchingIDs, mID)
			}
		}

		if len(matchingIDs) == 0 {
			continue
		}

		safetyLimit := w.Config.EffectiveSafetyLimit()
		batchSize := w.Config.EffectiveBatchSize()

		// ── SICHERHEITSGRENZE (SAFETY LIMIT) ──
		// Wird die zulässige Trefferzahl überschritten, beginnt KEINE Veränderung.
		// Der Lauf pausiert vorab, 0 Nachrichten werden modifiziert.
		// Persistenter Sperrzustand 'paused_safety_limit' blockiert Folgeläufe bis zur expliziten Nutzerfreigabe.
		if len(matchingIDs) > safetyLimit {
			slog.Warn("daily workflow halted before any modification: safety limit exceeded",
				"workflow_id", w.ID, "version", version, "matches", len(matchingIDs), "safety_limit", safetyLimit)

			res, pErr := s.db.Execute(ctx, `UPDATE workflows 
				SET execution_state = 'paused_safety_limit', version = version + 1, updated_at = ? 
				WHERE id = ? AND version = ? AND execution_state != 'paused_safety_limit'`,
				nowTime, w.ID, version)
			if pErr != nil {
				slog.Error("failed to persist workflow safety pause", "err", pErr, "workflow_id", w.ID)
				return totalActions, fmt.Errorf("failed to persist safety limit pause: %w", pErr)
			}
			if res.Changes == 0 {
				// Konkurrierende Änderung: Die Regel wurde in der Zwischenzeit geändert oder bereits pausiert.
				// Die veraltete Pausen-Entscheidung wird verworfen; es werden keine falschen Audit-Einträge geschrieben.
				slog.Warn("safety pause skipped: workflow version changed concurrently", "workflow_id", w.ID, "evaluated_version", version)
				continue
			}

			newVer := version + 1
			for _, id := range matchingIDs {
				s.recordWorkflowRun(ctx, w.ID, accountID, id, newVer, "", "paused",
					fmt.Sprintf("paused:safety_limit_exceeded (Gefunden: %d Treffer, Sicherheitsgrenze: %d. Ausführung vor jeder Änderung blockiert)",
						len(matchingIDs), safetyLimit))
			}
			continue
		}

		// ── VERARBEITUNGSGRÖSSE (BATCH SIZE / CHUNKING) ──
		// Erlaubter Lauf: Verarbeitet Nachrichten kontrolliert in technischen Teilmengen.
		for i := 0; i < len(matchingIDs); i += batchSize {
			end := i + batchSize
			if end > len(matchingIDs) {
				end = len(matchingIDs)
			}
			chunk := matchingIDs[i:end]

			if hasPurge {
				for _, id := range chunk {
					checkRows, err := s.db.Query(ctx, `SELECT m.id, m.folder_id, m.entered_at, f.role
						FROM messages m JOIN folders f ON f.id = m.folder_id
						WHERE m.id = ? AND m.account_id = ?`, id, accountID)
					if err != nil {
						s.recordWorkflowRun(ctx, w.ID, accountID, id, version, "purge", "failed", fmt.Sprintf("failed:query_error: %v", err))
						continue
					}
					if len(checkRows.Values) == 0 {
						s.recordWorkflowRun(ctx, w.ID, accountID, id, version, "purge", "skipped", "skipped:message_not_found")
						continue
					}
					mFolderID := store.Int(checkRows.Values[0][1])
					mEnteredAt := store.Int(checkRows.Values[0][2])
					mRole := store.Str(checkRows.Values[0][3])

					if mFolderID != targetFolderID {
						s.recordWorkflowRun(ctx, w.ID, accountID, id, version, "purge", "skipped",
							fmt.Sprintf("skipped:folder_mismatch (aktuell: %d, erwartet: %d)", mFolderID, targetFolderID))
						continue
					}
					if mRole != "trash" && mRole != "junk" {
						s.recordWorkflowRun(ctx, w.ID, accountID, id, version, "purge", "skipped",
							fmt.Sprintf("skipped:invalid_role (aktuell: %s)", mRole))
						continue
					}
					if mEnteredAt <= 0 {
						s.recordWorkflowRun(ctx, w.ID, accountID, id, version, "purge", "skipped", "skipped:unreliable_entered_at")
						continue
					}
					if mEnteredAt > nowTime-int64(w.Config.Days*86400) {
						s.recordWorkflowRun(ctx, w.ID, accountID, id, version, "purge", "skipped",
							fmt.Sprintf("skipped:retention_not_met (entered_at: %d)", mEnteredAt))
						continue
					}

					// Atomare Einreihung des Purge-Auftrags mit Deduplikation
					opID, alreadyQueued, qErr := s.queueWorkflowPurge(ctx, accountID, id)
					if qErr != nil {
						slog.Error("failed to queue workflow purge", "err", qErr, "message_id", id, "workflow_id", w.ID)
						s.recordWorkflowRun(ctx, w.ID, accountID, id, version, "purge", "failed", fmt.Sprintf("failed:provider_queue_error: %v", qErr))
						continue
					}

					var details string
					if alreadyQueued {
						slog.Info("purge operation already pending in ops queue", "op_id", opID, "message_id", id)
						// Check if workflow_runs already recorded a queued run for this purge
						existingRun, _ := s.db.Query(ctx, "SELECT id FROM workflow_runs WHERE workflow_id = ? AND message_id = ? AND action = 'purge' AND status = 'queued' LIMIT 1", w.ID, id)
						if len(existingRun.Values) > 0 {
							// Already recorded in workflow_runs; do not insert redundant duplicate audit rows
							continue
						}
						details = fmt.Sprintf("op_id:%d:already_queued", opID)
					} else {
						details = fmt.Sprintf("op_id:%d:queued_for_provider_purge", opID)
					}
					s.recordWorkflowRunWithOp(ctx, w.ID, accountID, id, version, "purge", "queued", details, &opID)
					totalActions++
				}
				continue
			}

			// Non-purge chunk processing
			var executedActions []string
			var stepFailed bool
			var failErr error

			for _, act := range actionsToRun {
				var actErr error
				switch act.Type {
				case WorkflowActionUnsubscribe:
					for _, id := range chunk {
						u, uErr := s.UnsubscribeMethod(ctx, id)
						if uErr != nil {
							slog.Warn("daily workflow unsubscribe lookup error", "workflow_id", w.ID, "id", id, "err", uErr)
							continue
						}
						if u.Method == "" || u.Method == UnsubscribeWeb {
							slog.Info("daily workflow unsubscribe skipped: unsupported", "workflow_id", w.ID, "id", id)
							continue
						}
						_, unsubErr := s.Unsubscribe(ctx, id)
						if unsubErr != nil {
							actErr = fmt.Errorf("unsubscribe request failed: %w", unsubErr)
							slog.Warn("daily workflow unsubscribe failed", "workflow_id", w.ID, "id", id, "err", unsubErr)
							break
						}
					}
				case WorkflowActionMarkRead:
					actErr = s.SetFlags(ctx, chunk, []string{`\Seen`}, nil)
				case WorkflowActionMarkUnread:
					actErr = s.SetFlags(ctx, chunk, nil, []string{`\Seen`})
				case WorkflowActionStar:
					actErr = s.SetFlags(ctx, chunk, []string{`\Flagged`}, nil)
				case WorkflowActionUnstar:
					actErr = s.SetFlags(ctx, chunk, nil, []string{`\Flagged`})
				case WorkflowActionForward, WorkflowActionExportEML, WorkflowActionExportPDF, WorkflowActionExportAttachments, WorkflowActionWitDraft, WorkflowActionWitExtract:
					for _, id := range chunk {
						_, actErr = s.executeWorkflowAction(ctx, w, id, act, nowTime)
						if actErr != nil {
							break
						}
					}
				case WorkflowActionApplyLabel:
					if act.LabelID != 0 {
						actErr = s.SetLabelWithSource(ctx, chunk, act.LabelID, true, fmt.Sprintf("workflow:%d", w.ID))
					}
				case WorkflowActionRemoveLabel:
					if act.LabelID != 0 {
						actErr = s.SetLabelWithSource(ctx, chunk, act.LabelID, false, fmt.Sprintf("workflow:%d", w.ID))
					}
				case WorkflowActionArchive:
					actErr = s.MoveToRole(ctx, chunk, "archive")
				case WorkflowActionJunk:
					actErr = s.MoveToRole(ctx, chunk, "junk")
				case WorkflowActionTrash, WorkflowActionDelete:
					actErr = s.Delete(ctx, chunk)
				}

				if actErr != nil {
					stepFailed = true
					failErr = actErr
					slog.Error("daily workflow action failed, halting subsequent actions for chunk",
						"workflow_id", w.ID, "action", act.Type, "err", actErr)
					break
				} else {
					executedActions = append(executedActions, act.Type)
				}
			}

			runStatus := "success"
			runDetails := strings.Join(executedActions, "+")
			if stepFailed {
				runStatus = "failed"
				runDetails = fmt.Sprintf("failed: %v (ausgeführt: %s)", failErr, runDetails)
			}

			for _, id := range chunk {
				s.recordWorkflowRun(ctx, w.ID, accountID, id, version, strings.Join(executedActions, "+"), runStatus, runDetails)
			}
			if !stepFailed {
				totalActions += len(chunk)
			}
		}
	}

	return totalActions, nil
}

// queueWorkflowPurge atomically queues a permanent delete op for a message in Trash or Junk.
// It checks for existing active ops under s.editMu, respects idx_ops_unique_active_purge,
// and returns the op ID and whether it was already queued.
func (s *Service) queueWorkflowPurge(ctx context.Context, accountID, messageID int64) (opID int64, alreadyQueued bool, err error) {
	s.editMu.Lock()
	defer s.editMu.Unlock()

	// 1. Check if an active purge op already exists in the queue (pending, running, awaiting_claim)
	opRows, qErr := s.db.Query(ctx, `SELECT id FROM ops 
		WHERE message_id = ? AND kind = 'purge' AND state IN ('pending', 'running', 'awaiting_claim') 
		ORDER BY id DESC LIMIT 1`, messageID)
	if qErr != nil {
		return 0, false, fmt.Errorf("query active purge op: %w", qErr)
	}
	if len(opRows.Values) > 0 {
		return store.Int(opRows.Values[0][0]), true, nil
	}

	// 2. Validate folder role: message must be in trash or junk
	trash, _ := s.roleFolder(ctx, accountID, "trash")
	junk, _ := s.roleFolder(ctx, accountID, "junk")
	if trash == 0 && junk == 0 {
		return 0, false, &ErrNoRoleFolder{Role: "trash"}
	}
	mRows, mErr := s.db.Query(ctx, "SELECT folder_id, thread_id FROM messages WHERE id = ? AND account_id = ?", messageID, accountID)
	if mErr != nil {
		return 0, false, fmt.Errorf("query message %d: %w", messageID, mErr)
	}
	if len(mRows.Values) == 0 {
		return 0, false, fmt.Errorf("message %d not found", messageID)
	}
	folderID := store.Int(mRows.Values[0][0])
	var threadID int64
	if mRows.Values[0][1] != nil {
		threadID = store.Int(mRows.Values[0][1])
	}
	role := ""
	if trash != 0 && folderID == trash {
		role = "trash"
	} else if junk != 0 && folderID == junk {
		role = "junk"
	} else {
		return 0, false, fmt.Errorf("message %d is not in trash or junk", messageID)
	}

	// 3. Insert purge op with provider references
	stmts := []sqlite.Statement{{
		SQL: `INSERT INTO ops(account_id, message_id, kind, dst_folder_id, payload, depends_on, created_at)
			VALUES (?, ?, 'purge', ?, ?, ` + latestOp + `, unixepoch())`,
		Args: []any{accountID, messageID, folderID, mailsync.PurgeNowRole(role), messageID},
	}}

	if _, txErr := s.db.Transaction(ctx, stmts); txErr != nil {
		// If UNIQUE constraint collision on idx_ops_unique_active_purge:
		opRows2, qErr2 := s.db.Query(ctx, `SELECT id FROM ops 
			WHERE message_id = ? AND kind = 'purge' AND state IN ('pending', 'running', 'awaiting_claim') 
			ORDER BY id DESC LIMIT 1`, messageID)
		if qErr2 == nil && len(opRows2.Values) > 0 {
			return store.Int(opRows2.Values[0][0]), true, nil
		}
		return 0, false, fmt.Errorf("insert purge op: %w", txErr)
	}

	// Publish message change event and wake worker
	s.bus.Publish(events.Event{
		Type:       events.MessagesChanged,
		AccountID:  accountID,
		MessageIDs: []int64{messageID},
		ThreadIDs:  []int64{threadID},
	})
	s.wakeWorker(accountID)

	// Fetch newly created op id
	opRowsNew, qErrNew := s.db.Query(ctx, `SELECT id FROM ops 
		WHERE message_id = ? AND kind = 'purge' AND state IN ('pending', 'running', 'awaiting_claim') 
		ORDER BY id DESC LIMIT 1`, messageID)
	if qErrNew != nil || len(opRowsNew.Values) == 0 {
		return 0, false, fmt.Errorf("failed to retrieve created purge op for message %d", messageID)
	}
	return store.Int(opRowsNew.Values[0][0]), false, nil
}

// ReconcilePurgeWorkflowRuns reconciles queued purge runs against the persistent ops table and message existence.
// If the provider operation succeeded (message expunged or state='done'), it transitions status to 'success'.
// If the message was rescued (moved out of trash/junk), it transitions status to 'cancelled'.
// If the provider operation failed (state='failed'), it transitions status to 'failed'.
func (s *Service) ReconcilePurgeWorkflowRuns(ctx context.Context, accountID int64) error {
	runs, err := s.db.Query(ctx, `SELECT r.id, r.message_id, r.op_id 
		FROM workflow_runs r 
		WHERE r.account_id = ? AND r.status = 'queued' AND r.action = 'purge'`, accountID)
	if err != nil {
		return fmt.Errorf("reconcile purge workflow runs query: %w", err)
	}
	var errs []error
	for _, row := range runs.Values {
		runID := store.Int(row[0])
		msgID := store.Int(row[1])
		var opID int64
		if row[2] != nil {
			opID = store.Int(row[2])
		}

		var opState, lastErr string
		var opFound bool
		if opID > 0 {
			opRows, oErr := s.db.Query(ctx, "SELECT state, COALESCE(last_error, '') FROM ops WHERE id = ?", opID)
			if oErr != nil {
				slog.Error("failed to query op for purge workflow run", "err", oErr, "op_id", opID, "run_id", runID)
				errs = append(errs, fmt.Errorf("query op %d for run %d: %w", opID, runID, oErr))
				continue
			}
			if len(opRows.Values) > 0 {
				opFound = true
				opState = store.Str(opRows.Values[0][0])
				lastErr = store.Str(opRows.Values[0][1])
			}
		}
		if !opFound {
			opRows, oErr := s.db.Query(ctx, "SELECT id, state, COALESCE(last_error, '') FROM ops WHERE message_id = ? AND kind = 'purge' ORDER BY id DESC LIMIT 1", msgID)
			if oErr != nil {
				slog.Error("failed to query ops by message for purge workflow run", "err", oErr, "msg_id", msgID, "run_id", runID)
				errs = append(errs, fmt.Errorf("query ops for message %d run %d: %w", msgID, runID, oErr))
				continue
			}
			if len(opRows.Values) > 0 {
				opFound = true
				if opID == 0 {
					opID = store.Int(opRows.Values[0][0])
					if _, uErr := s.db.Execute(ctx, "UPDATE workflow_runs SET op_id = ? WHERE id = ?", opID, runID); uErr != nil {
						slog.Error("failed to backfill op_id in workflow_runs", "err", uErr, "op_id", opID, "run_id", runID)
						errs = append(errs, fmt.Errorf("backfill op_id %d for run %d: %w", opID, runID, uErr))
						continue
					}
				}
				opState = store.Str(opRows.Values[0][1])
				lastErr = store.Str(opRows.Values[0][2])
			}
		}

		msgRows, mErr := s.db.Query(ctx, "SELECT m.folder_id, f.role FROM messages m JOIN folders f ON f.id = m.folder_id WHERE m.id = ?", msgID)
		if mErr != nil {
			slog.Error("failed to query message existence in reconcile", "err", mErr, "msg_id", msgID, "run_id", runID)
			errs = append(errs, fmt.Errorf("query message %d for run %d: %w", msgID, runID, mErr))
			continue
		}
		msgExists := len(msgRows.Values) > 0

		if opFound {
			switch opState {
			case "done":
				// Der Status 'success' (provider_purge_completed) wird AUSSCHLIESSLICH aus einem
				// eindeutig zugeordneten Abschlussnachweis des Providers (ops.state = 'done') abgeleitet.
				if _, uErr := s.db.Execute(ctx, "UPDATE workflow_runs SET status = 'success', details = 'provider_purge_completed' WHERE id = ?", runID); uErr != nil {
					slog.Error("failed to update workflow run to success", "err", uErr, "run_id", runID)
					errs = append(errs, fmt.Errorf("update run %d to success: %w", runID, uErr))
				}
				continue
			case "cancelled":
				if _, uErr := s.db.Execute(ctx, "UPDATE workflow_runs SET status = 'cancelled', details = 'cancelled_by_rescue' WHERE id = ?", runID); uErr != nil {
					slog.Error("failed to update workflow run to cancelled", "err", uErr, "run_id", runID)
					errs = append(errs, fmt.Errorf("update run %d to cancelled: %w", runID, uErr))
				}
				continue
			case "failed":
				details := "provider_purge_failed"
				if lastErr != "" {
					details += ": " + lastErr
				}
				if _, uErr := s.db.Execute(ctx, "UPDATE workflow_runs SET status = 'failed', details = ? WHERE id = ?", details, runID); uErr != nil {
					slog.Error("failed to update workflow run to failed", "err", uErr, "run_id", runID)
					errs = append(errs, fmt.Errorf("update run %d to failed: %w", runID, uErr))
				}
				continue
			case "pending", "running", "awaiting_claim":
				// Der Löschauftrag ist beim Anbieter noch ausstehend bzw. in Bearbeitung.
				// Auch wenn die lokale Nachrichtenzeile fehlen sollte (z. B. durch vorzeitiges Löschen
				// oder unvollständigen Sync), darf daraus KEIN vorzeitiger Erfolg abgeleitet werden.
				// Der Lauf verbleibt im Status 'queued', bis der Provider den Abschluss bestätigt.
				continue
			}
		}

		// Falls die Nachricht lokal noch existiert, aber vor Bestätigung aus dem Papierkorb gerettet wurde:
		if msgExists {
			role := store.Str(msgRows.Values[0][1])
			if role != "trash" && role != "junk" {
				if _, uErr := s.db.Execute(ctx, "UPDATE workflow_runs SET status = 'cancelled', details = 'cancelled_by_rescue' WHERE id = ?", runID); uErr != nil {
					slog.Error("failed to update workflow run to cancelled by rescue", "err", uErr, "run_id", runID)
					errs = append(errs, fmt.Errorf("update run %d to cancelled by rescue: %w", runID, uErr))
				}
				continue
			}
		}
	}
	return errors.Join(errs...)
}

// ListWorkflowRuns returns recent execution log entries for an account (or all accounts if accountID <= 0), with associated workflow and message details.
func (s *Service) ListWorkflowRuns(ctx context.Context, accountID int64, limit int) ([]WorkflowRun, error) {
	if limit <= 0 {
		limit = 50
	}
	var (
		rows sqlite.Rows
		err  error
	)
	if accountID > 0 {
		rows, err = s.db.Query(ctx, `SELECT 
			r.id, r.workflow_id, COALESCE(w.name, ''), r.workflow_version, 
			r.account_id, r.message_id, COALESCE(m.subject, ''), COALESCE(m.from_addr, ''), 
			r.action, r.status, r.details, r.op_id, r.ran_at,
			COALESCE(a.email, ''), COALESCE(a.name, '')
			FROM workflow_runs r
			LEFT JOIN workflows w ON w.id = r.workflow_id
			LEFT JOIN messages m ON m.id = r.message_id
			LEFT JOIN accounts a ON a.id = r.account_id
			WHERE r.account_id = ?
			ORDER BY r.ran_at DESC, r.id DESC
			LIMIT ?`, accountID, limit)
	} else {
		rows, err = s.db.Query(ctx, `SELECT 
			r.id, r.workflow_id, COALESCE(w.name, ''), r.workflow_version, 
			r.account_id, r.message_id, COALESCE(m.subject, ''), COALESCE(m.from_addr, ''), 
			r.action, r.status, r.details, r.op_id, r.ran_at,
			COALESCE(a.email, ''), COALESCE(a.name, '')
			FROM workflow_runs r
			LEFT JOIN workflows w ON w.id = r.workflow_id
			LEFT JOIN messages m ON m.id = r.message_id
			LEFT JOIN accounts a ON a.id = r.account_id
			ORDER BY r.ran_at DESC, r.id DESC
			LIMIT ?`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}

	var items []WorkflowRun
	for _, row := range rows.Values {
		item := WorkflowRun{
			ID:              store.Int(row[0]),
			WorkflowID:      store.Int(row[1]),
			WorkflowName:    store.Str(row[2]),
			WorkflowVersion: store.Int(row[3]),
			AccountID:       store.Int(row[4]),
			MessageID:       store.Int(row[5]),
			Subject:         store.Str(row[6]),
			FromAddr:        store.Str(row[7]),
			Action:          store.Str(row[8]),
			Status:          store.Str(row[9]),
			Details:         store.Str(row[10]),
			RanAt:           time.Unix(store.Int(row[12]), 0),
			AccountEmail:    store.Str(row[13]),
			AccountName:     store.Str(row[14]),
		}
		if row[11] != nil {
			opID := store.Int(row[11])
			item.OpID = &opID
		}
		if item.WorkflowName == "" {
			item.WorkflowName = fmt.Sprintf("Workflow #%d", item.WorkflowID)
		}
		if item.Subject == "" {
			item.Subject = fmt.Sprintf("E-Mail #%d", item.MessageID)
		}

		parseWorkflowRunDetails(&item)
		items = append(items, item)
	}
	return items, nil
}

// ClearWorkflowRuns deletes non-pending execution log entries for an account (or all accounts if accountID <= 0).
func (s *Service) ClearWorkflowRuns(ctx context.Context, accountID int64) error {
	if accountID > 0 {
		_, err := s.db.Execute(ctx, "DELETE FROM workflow_runs WHERE account_id = ? AND op_id IS NULL", accountID)
		return err
	}
	_, err := s.db.Execute(ctx, "DELETE FROM workflow_runs WHERE op_id IS NULL")
	return err
}

func parseWorkflowRunDetails(run *WorkflowRun) {
	allText := run.Action + " " + run.Details

	// Parse draft ID: wit_draft:(\d+)
	if idx := strings.Index(allText, "wit_draft:"); idx != -1 {
		rest := allText[idx+len("wit_draft:"):]
		end := strings.IndexAny(rest, "+ \t\n(),:")
		if end == -1 {
			end = len(rest)
		}
		if dID, err := strconv.ParseInt(strings.TrimSpace(rest[:end]), 10, 64); err == nil && dID > 0 {
			run.DraftID = dID
		}
	}

	// Parse file paths or directory paths
	for _, prefix := range []string{"wit_extract:", "export_pdf:", "export_eml:"} {
		if idx := strings.Index(allText, prefix); idx != -1 {
			rest := allText[idx+len(prefix):]
			end := strings.IndexAny(rest, "+\t\n()")
			if end == -1 {
				end = len(rest)
			}
			p := strings.TrimSpace(rest[:end])
			if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.Contains(p, `:\`) {
				run.FilePath = p
				run.DirPath = filepath.Dir(p)
				break
			}
		}
	}
	if run.DirPath == "" {
		if idx := strings.Index(allText, "export_attachments:"); idx != -1 {
			rest := allText[idx+len("export_attachments:"):]
			end := strings.LastIndex(rest, ":")
			if end != -1 {
				p := strings.TrimSpace(rest[:end])
				if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.Contains(p, `:\`) {
					run.DirPath = p
				}
			}
		}
	}
	// Fallback: search for "/Users/" in details
	if run.DirPath == "" {
		if idx := strings.Index(allText, "/Users/"); idx != -1 {
			rest := allText[idx:]
			end := strings.IndexAny(rest, ":\t\n()")
			if end == -1 {
				end = len(rest)
			}
			p := strings.TrimSpace(rest[:end])
			run.DirPath = filepath.Dir(p)
		}
	}

	// Parse skip reasons
	if idx := strings.Index(allText, "forward_skipped:loop_prevention:"); idx != -1 {
		rest := allText[idx+len("forward_skipped:loop_prevention:"):]
		end := strings.IndexAny(rest, "+ \t\n()")
		if end == -1 {
			end = len(rest)
		}
		run.SkipReason = "loop_prevention:" + strings.TrimSpace(rest[:end])
	} else if idx := strings.Index(allText, "forward_skipped:auto_generated:"); idx != -1 {
		rest := allText[idx+len("forward_skipped:auto_generated:"):]
		end := strings.IndexAny(rest, "+ \t\n()")
		if end == -1 {
			end = len(rest)
		}
		run.SkipReason = "auto_generated:" + strings.TrimSpace(rest[:end])
	}

	// Parse failure reason
	if run.Status == "failed" {
		fReason := run.Details
		if strings.HasPrefix(fReason, "failed: ") {
			fReason = strings.TrimPrefix(fReason, "failed: ")
		}
		if idx := strings.Index(fReason, " (ausgeführt:"); idx != -1 {
			fReason = strings.TrimSpace(fReason[:idx])
		}
		run.FailReason = fReason
	}
}
