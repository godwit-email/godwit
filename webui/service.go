package webui

import (
	"context"
	"encoding/json"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
)

// Service is the subset of the mail engine the web UI pages call.
type Service interface {
	AddAttachmentAt(ctx context.Context, draftID, revision int64, srcPath string) (core.DraftAttachment, int64, error)
	AnswerQuestion(ctx context.Context, id int64, round int, answers [][]string) error
	ApplyEditedProposal(ctx context.Context, approvalID int64, edited string) error
	AskAgain(ctx context.Context, conversationID int64, turn string, input string, quoted []int64) (<-chan core.WitEvent, error)
	AskWith(ctx context.Context, conversationID int64, input string, quoted []int64) (<-chan core.WitEvent, error)
	AssistConversation(ctx context.Context, draftID int64, create bool) (int64, error)
	AssistSkill(ctx context.Context, draftID int64, skill string, opts core.SkillOptions) (<-chan core.WitEvent, error)
	AssistWith(ctx context.Context, draftID int64, input string, quoted []int64) (<-chan core.WitEvent, error)
	CachedImage(ctx context.Context, src string) (core.CachedImageFile, error)
	CachedImageDims(src string) core.ImageDims
	CancelRun(ctx context.Context, runID int64) error
	CompactConversation(ctx context.Context, id int64) error
	DeleteDraftIfEmpty(ctx context.Context, id, revision int64) (deleted bool, err error)
	DraftForApproval(ctx context.Context, approvalID int64) (int64, error)
	EditProposedRule(ctx context.Context, id int64) (core.Rule, error)
	FailQuestion(ctx context.Context, conv int64, question string, quoted []int64, reason string) error
	ForgetImageFailure(urls []string)
	GetConversation(ctx context.Context, id int64) (core.Conversation, error)
	GetDraft(ctx context.Context, id int64) (core.Draft, error)
	GetMessageLazyImages(ctx context.Context, id int64, loadRemoteImages bool) (core.Message, error)
	GetSettings(ctx context.Context) (core.Settings, error)
	ImagesAllowed(ctx context.Context, message int64) (bool, error)
	InlineImages(ctx context.Context, id int64, each func(cid string, a mail.Attachment)) error
	ListAccounts(ctx context.Context) ([]core.Account, error)
	ListApprovals(ctx context.Context, conversationID int64) ([]core.Approval, error)
	ListConversations(ctx context.Context, kind string) ([]core.Conversation, error)
	ListModelServices(ctx context.Context) ([]core.ModelService, error)
	ListPluginModels(ctx context.Context) ([]core.PluginModels, error)
	ListSkills(ctx context.Context) ([]core.SkillInfo, error)
	ListTranslators(ctx context.Context) ([]core.TranslatorInfo, error)
	MessageExists(ctx context.Context, id int64) (bool, error)
	ModelName(ctx context.Context, ref string) string
	NewConversation(ctx context.Context, kind string, draftID int64) (core.Conversation, error)
	NewMailtoDraft(ctx context.Context, accountID int64, link string) (core.Draft, error)
	ReadingLanguage(ctx context.Context) string
	RemoveAttachmentAt(ctx context.Context, draftID, attachmentID, revision int64) (int64, error)
	ResolveAll(ctx context.Context, runID int64, accept bool) error
	ResolveApproval(ctx context.Context, id int64, accept bool) error
	ResolveApprovalForConversation(ctx context.Context, id int64) error
	ResolveApprovalWithArgs(ctx context.Context, id int64, accept bool, args json.RawMessage) error
	RewriteSelection(ctx context.Context, draftID int64, sel core.Selection, instruction string) (<-chan core.WitEvent, error)
	RunConversation(ctx context.Context, runID int64) (int64, error)
	SaveDraft(ctx context.Context, d core.Draft) (core.Draft, error)
	SearchWith(ctx context.Context, q core.SearchQuery, limit int) ([]core.MessageSummary, error)
	SendAcknowledgedRevision(ctx context.Context, draftID, revision int64, digest string) error
	SendLater(ctx context.Context, draftID, revision int64, at time.Time, digest string) error
	SendRevision(ctx context.Context, draftID, revision int64) error
	SenderAvatar(ctx context.Context, messageID int64, allowRemote bool) (core.Avatar, error)
	SetConversationModel(ctx context.Context, id int64, ref, effort string) error
	SetDraftAccount(ctx context.Context, id, accountID, revision int64) (core.Draft, error)
	SetDraftModel(ctx context.Context, draftID int64, model string) error
	SignatureEditor(ctx context.Context, id int64) (string, error)
	SignatureShown(ctx context.Context, block string) (string, error)
	SubscribeConversation(id int64) (<-chan core.WitEvent, func())
	SystemLanguage() string
	SystemTranslatorLanguages() []string
	UndoLastAssist(ctx context.Context, draftID int64) error
	UpdateSignature(ctx context.Context, id int64, p core.SignaturePatch) (core.Signature, error)
	CompleteContacts(ctx context.Context, query string, limit int) ([]core.ContactCompletion, error)
}

var _ Service = (*core.Service)(nil)
