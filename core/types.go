package core

import (
	"fmt"
	"time"

	"github.com/godwit-email/godwit/core/conn"
)

// Security is the transport protection of an IMAP or SMTP connection.
type Security = conn.Security

const (
	SecurityTLS      = conn.TLS
	SecurityStartTLS = conn.StartTLS
	SecurityNone     = conn.None
)

// AccountInput is what the user supplies to add or test an account.
type AccountInput struct {
	Email, Name, Alias, Signature, Username, Password string
	IsDefault                                         bool
	Sync                                              *SyncRange // nil: the newest 2000 per folder
	IMAPHost                                          string
	IMAPPort                                          int
	IMAPSecurity                                      Security
	SMTPHost                                          string
	SMTPPort                                          int
	SMTPSecurity                                      Security
	Provider                                          string // auth.Provider ID; empty is stored as other
	// OAuthFlowID names a finished AwaitOAuth flow: the account signs in with its
	// tokens instead of Password, and Email must be the signed-in address.
	OAuthFlowID   string
	oauthClientID string // the flow's own client, set by AddAccount
}

// Account is a stored account. The password lives only in the system keychain.
// Status is ok; reauth_required when the password is missing from the keychain
// (StatusDetail credentials_missing) or the IMAP or SMTP server refuses it
// (auth_rejected) or the provider revoked an OAuth grant (token_revoked) or
// refuses the user's own OAuth client (client_rejected); or
// error when the keychain is unavailable (keychain_unavailable). An OAuth
// account's keychain entry holds its refresh token; access tokens stay in memory.
type Account struct {
	ID                                      int64
	Email, Name, Alias, Signature, Username string // Signature is SignatureID's (Signature.HTML), "" for none
	SignatureID                             int64  // its default signature, 0 none
	Icon                                    string // '' the provider's mark, label:<name>, brand:<provider id> or image:<n> (AccountIcon)
	AIInstructions                          string // the user's facts and guidelines for Wit drafting from this account
	IsDefault                               bool
	Sync                                    SyncRange
	Folders                                 int // folders synced so far: 0 before the first sync
	AuthType, Status                        string
	IMAPHost                                string
	IMAPPort                                int
	IMAPSecurity                            Security
	SMTPHost                                string
	SMTPPort                                int
	SMTPSecurity                            Security
	Provider, StatusDetail                  string
	OAuthClientID                           string // the user's own OAuth client (OAuthClient), "" the provider's
}

// Folder is a mailbox of an account. Role is inbox, sent, trash, drafts, junk,
// archive or empty.
type Folder struct {
	ID, AccountID int64
	Name, Role    string
	Delim         string // the server's hierarchy delimiter in Name, "" when flat
	Unread        int
	Total         int
}

// ServerSettings replaces an account's username and IMAP and SMTP servers. An
// empty Username means the email address.
type ServerSettings struct {
	Username     string
	IMAPHost     string
	IMAPPort     int
	IMAPSecurity Security
	SMTPHost     string
	SMTPPort     int
	SMTPSecurity Security
}

// SyncRange is how much of each folder a sync fetches: the newest Limit
// messages, the mail of the last Months months, or (both 0) all of it.
// Narrowing it only stops fetching older mail; nothing stored is deleted.
type SyncRange struct{ Limit, Months int }

// MaxAIInstructions is the most runes an account's AI instructions have:
// every draft from the account sends them to the model.
const MaxAIInstructions = 2000

// AccountPatch changes the account settings that are not nil. Signature
// rewrites the account's default signature (for every account using it),
// making one named as the account if it has none; SignatureID picks it, 0
// for none.
type AccountPatch struct {
	Name, Alias, Signature *string
	SignatureID            *int64
	IsDefault              *bool
	Sync                   *SyncRange
	Servers                *ServerSettings
	Icon                   *string // '', label:<name> or brand:<provider id>
	IconImage              []byte  // an uploaded png, jpeg, gif, webp or svg: Icon becomes image:<n>
	AIInstructions         *string // trimmed, at most MaxAIInstructions runes
}

// AccountIcon is an account's uploaded icon image.
type AccountIcon struct {
	ContentType string
	Data        []byte
}

// WorkflowActionItem represents an action with its specific configuration.
type WorkflowActionItem struct {
	Type            string `json:"type"` // "archive", "junk", "trash", "purge", "mark_read", "mark_unread", "star", "unstar", "apply_label", "remove_label", "unsubscribe", "forward", "export_eml", "export_pdf", "export_attachments", "wit_draft", "wit_extract"
	LabelID         int64  `json:"label_id,omitempty"`
	LabelName       string `json:"label_name,omitempty"`
	ForwardTo       string `json:"forward_to,omitempty"`
	TargetDirectory string `json:"target_directory,omitempty"`
	BundleSubfolder *bool  `json:"bundle_subfolder,omitempty"`
	ExtractTemplate string `json:"extract_template,omitempty"`
	Instructions    string `json:"instructions,omitempty"`
}

// Workflow represents an automated mail workflow / rule.
type Workflow struct {
	ID             int64          `json:"id"`
	AccountID      int64          `json:"account_id"`
	Kind           string         `json:"kind"`                // "native" or "custom"
	PresetID       string         `json:"preset_id,omitempty"` // "purge_trash", "purge_junk", "security_junk", "newsletter_unsub"
	Name           string         `json:"name"`
	Trigger        string         `json:"trigger"` // "daily", "on_receive", "on_label"
	Action         string         `json:"action"`  // primary action or summary
	Config         WorkflowConfig `json:"config"`
	Enabled        bool           `json:"enabled"`
	Status         string         `json:"status"`          // "enabled" or "disabled"
	Version        int64          `json:"version"`         // integer sequence number (increments monotonically on every change)
	ExecutionState string         `json:"execution_state"` // "active" or "paused_safety_limit"
	CanEdit        bool           `json:"can_edit"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// WorkflowConfig defines conditions and parameters for workflow evaluation.
type WorkflowConfig struct {
	Days             int                  `json:"days,omitempty"`
	FolderRole       string               `json:"folder_role,omitempty"`
	FolderID         int64                `json:"folder_id,omitempty"`
	TriggerLabelID   int64                `json:"trigger_label_id,omitempty"`
	TriggerLabelName string               `json:"trigger_label_name,omitempty"`
	LabelID          int64                `json:"label_id,omitempty"`
	LabelName        string               `json:"label_name,omitempty"`
	ThreatAssessment string               `json:"threat_assessment,omitempty"` // "warning", "suspicious", "clean"
	DomainAuth       string               `json:"domain_auth,omitempty"`       // "verified", "failed", "unverified"
	SecurityStatus   string               `json:"security_status,omitempty"`   // legacy alias for ThreatAssessment
	HasAttachments   *bool                `json:"has_attachments,omitempty"`
	IsUnread         *bool                `json:"is_unread,omitempty"`
	FromPattern      string               `json:"from_pattern,omitempty"`
	SubjectPattern   string               `json:"subject_pattern,omitempty"`
	SubjectOp        string               `json:"subject_op,omitempty"`   // "contains" (default), "equals", "starts_with"
	CondLogic        string               `json:"cond_logic,omitempty"`   // "and" (default) or "or"
	BatchLimit       int                  `json:"batch_limit,omitempty"`  // legacy alias for SafetyLimit
	SafetyLimit      int                  `json:"safety_limit,omitempty"` // max candidate matches before halting without modification (default 250)
	BatchSize        int                  `json:"batch_size,omitempty"`   // chunk processing size (default 50)
	PurgeProof       string               `json:"purge_proof,omitempty"`  // cryptographic server-side user confirmation proof for purge
	TargetDirectory  string               `json:"target_directory,omitempty"`
	BundleSubfolder  *bool                `json:"bundle_subfolder,omitempty"`
	ForwardTo        string               `json:"forward_to,omitempty"`
	Instructions     string               `json:"instructions,omitempty"`
	ExtractTemplate  string               `json:"extract_template,omitempty"`
	Actions          []WorkflowActionItem `json:"actions,omitempty"` // multiple combined actions
}

// EffectiveThreatAssessment returns ThreatAssessment or falls back to SecurityStatus for backwards compatibility.
func (c WorkflowConfig) EffectiveThreatAssessment() string {
	if c.ThreatAssessment != "" {
		return c.ThreatAssessment
	}
	return c.SecurityStatus
}

// EffectiveSafetyLimit returns SafetyLimit, falling back to legacy BatchLimit or default 250.
func (c WorkflowConfig) EffectiveSafetyLimit() int {
	if c.SafetyLimit > 0 {
		return c.SafetyLimit
	}
	if c.BatchLimit > 0 {
		return c.BatchLimit
	}
	return 250
}

// EffectiveBatchSize returns BatchSize, falling back to default 50.
func (c WorkflowConfig) EffectiveBatchSize() int {
	if c.BatchSize > 0 {
		return c.BatchSize
	}
	return 50
}

// EffectiveTriggerLabelID returns TriggerLabelID or falls back to LabelID for backwards compatibility.
func (c WorkflowConfig) EffectiveTriggerLabelID() int64 {
	if c.TriggerLabelID != 0 {
		return c.TriggerLabelID
	}
	return c.LabelID
}

// ValidationError represents a structured workflow validation failure.
type ValidationError struct {
	Code       string `json:"code"`
	Field      string `json:"field,omitempty"`
	Message    string `json:"message"`
	Resolution string `json:"resolution,omitempty"`
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("[%s] %s: %s", e.Code, e.Field, e.Message)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// WorkflowSampleMessage holds preview details for a candidate message.
type WorkflowSampleMessage struct {
	ID      int64  `json:"id"`
	Subject string `json:"subject"`
	From    string `json:"from"`
	Date    int64  `json:"date"`
}

// WorkflowPreview contains validation summary and candidate simulation.
type WorkflowPreview struct {
	Valid                 bool                    `json:"valid"`
	Summary               string                  `json:"summary"`
	CandidateCount        int                     `json:"candidate_count"`
	CandidateIDs          []int64                 `json:"candidate_ids,omitempty"`
	Samples               []WorkflowSampleMessage `json:"samples"`
	IsEventTrigger        bool                    `json:"is_event_trigger"`
	SimulationNote        string                  `json:"simulation_note,omitempty"`
	IsIrreversible        bool                    `json:"is_irreversible"`
	ConfirmationMessage   string                  `json:"confirmation_message"`
	RequiresHumanApproval bool                    `json:"requires_human_approval,omitempty"`
	ConfirmationPrompt    string                  `json:"confirmation_prompt,omitempty"`
	ConfirmationRequestID string                  `json:"confirmation_request_id,omitempty"`
	NormalizedConfigHash  string                  `json:"normalized_config_hash,omitempty"`
	PurgeProof            string                  `json:"purge_proof,omitempty"`
}

// WorkflowInput is the input for creating or updating a workflow.
type WorkflowInput struct {
	AccountID          int64          `json:"account_id"`
	Name               string         `json:"name"`
	Trigger            string         `json:"trigger"`
	Action             string         `json:"action"`
	Config             WorkflowConfig `json:"config"`
	Enabled            bool           `json:"enabled"`
	IdempotencyKey     string         `json:"idempotency_key,omitempty"`
	ExpectedVersion    int64          `json:"expected_version,omitempty"`
	ExecutionState     string         `json:"execution_state,omitempty"`
	ResetSafetyPause   bool           `json:"reset_safety_pause,omitempty"`
	ConfirmPurge       bool           `json:"confirm_purge,omitempty"`
	HumanApprovalToken string         `json:"human_approval_token,omitempty"`
	PurgeProof         string         `json:"purge_proof,omitempty"`
}

// WorkflowRun records the execution history of a workflow on a message.
type WorkflowRun struct {
	ID              int64     `json:"id"`
	WorkflowID      int64     `json:"workflow_id"`
	AccountID       int64     `json:"account_id"`
	MessageID       int64     `json:"message_id"`
	WorkflowVersion int64     `json:"workflow_version"`
	Action          string    `json:"action"`
	Status          string    `json:"status"` // "success", "queued", "paused", "failed", "skipped", "cancelled"
	Details         string    `json:"details,omitempty"`
	OpID            *int64    `json:"op_id,omitempty"`
	RanAt           time.Time `json:"ran_at"`

	// Parsed / computed helper fields for UI
	WorkflowName string `json:"workflow_name,omitempty"`
	AccountEmail string `json:"account_email,omitempty"`
	AccountName  string `json:"account_name,omitempty"`
	Subject      string `json:"subject,omitempty"`
	FromAddr     string `json:"from_addr,omitempty"`
	DraftID      int64  `json:"draft_id,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
	DirPath      string `json:"dir_path,omitempty"`
	SkipReason   string `json:"skip_reason,omitempty"`
	FailReason   string `json:"fail_reason,omitempty"`
}
