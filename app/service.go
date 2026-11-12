package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/events"
)

// Service is the subset of the mail engine the native UI calls.
type Service interface {
	AccountIcon(ctx context.Context, id int64) (core.AccountIcon, error)
	AddAccount(ctx context.Context, in core.AccountInput) (core.Account, error)
	AddLocalArchive(ctx context.Context, name string) (core.Account, error)
	AddMCPServer(ctx context.Context, in core.MCPServerInput) (core.MCPServer, error)
	AddModelService(ctx context.Context, in core.ModelServiceInput) (core.ModelService, error)
	AllAccountLabels(ctx context.Context) (map[int64][]core.Label, error)
	ArchiveMessages(ctx context.Context, ids []int64, folderID int64) (core.ArchiveUndo, error)
	ArchiveSender(ctx context.Context, addr string) (core.SenderArchive, error)
	ArchiveThreads(ctx context.Context, threadIDs []int64) (core.ArchiveUndo, error)
	ArchiveUsage(ctx context.Context, accountID int64) (int64, error)
	AttachmentCacheUsage(ctx context.Context, accountID int64) (int64, error)
	AwaitOAuth(ctx context.Context, flowID string) (email string, err error)
	BeginOAuth(ctx context.Context, providerID, loginHint string, client core.OAuthClient) (flowID, authURL string, err error)
	BeginPluginLogin(ctx context.Context, name string) (string, error)
	BlockSender(ctx context.Context, from string) error
	BlockedSenders(ctx context.Context) ([]string, error)
	CancelOAuth(flowID string)
	CancelOutbox(ctx context.Context, id int64) error
	CancelPluginLogin(ctx context.Context, name, flow string) error
	CheckAccount(ctx context.Context, in core.AccountInput) (core.CheckReport, error)
	CheckModelService(ctx context.Context, id int64, in core.ModelServiceInput) (core.ServiceCheck, error)
	ClearAttachmentCache(ctx context.Context, accountID int64) error
	ClearImageCache() error
	ClearMaskingLog(ctx context.Context) error
	ContactDetail(ctx context.Context, addr string) (core.ContactDetail, error)
	ContactMail(ctx context.Context, addr, cursor string, limit int) (core.MessagePage, error)
	Contacts(ctx context.Context) ([]core.Contact, error)
	CreateLabel(ctx context.Context, in core.LabelInput) (core.Label, error)
	CreateLocalFolder(ctx context.Context, accountID int64, name string) (core.Folder, error)
	CreateSignature(ctx context.Context, name string) (core.Signature, error)
	Delete(ctx context.Context, ids []int64) error
	DeleteConversation(ctx context.Context, id int64) error
	DeleteDraft(ctx context.Context, id int64) error
	DeleteForever(ctx context.Context, ids []int64) error
	DeleteLabel(ctx context.Context, id int64) error
	DeleteLocalFolder(ctx context.Context, folderID int64) error
	DeleteRule(ctx context.Context, id int64) error
	DeleteSignature(ctx context.Context, id int64) error
	DeleteView(ctx context.Context, id int64) error
	DetectProvider(ctx context.Context, email string) (auth.Provider, auth.Source, error)
	DownloadRecognitionModel(ctx context.Context, progress func(got, total int64)) error
	DraftFromQuickReply(ctx context.Context, messageID int64, r core.QuickReply) (core.Draft, error)
	EmptyFolder(ctx context.Context, folderID int64) ([]int64, error)
	ExportAccountsFile(ctx context.Context) (string, error)
	FetchAttachments(ctx context.Context, attachmentIDs []int64, fn func(path string, info core.AttachmentInfo) error) (err error)
	Find(ctx context.Context, q core.FindQuery) (core.FindResult, error)
	FirstTimeRecipients(ctx context.Context, addrs []string) ([]string, error)
	GetAttachment(ctx context.Context, attachmentID int64) (string, core.AttachmentInfo, error)
	GetDraft(ctx context.Context, id int64) (core.Draft, error)
	GetMessage(ctx context.Context, id int64, loadRemoteImages bool) (core.Message, error)
	GetMessageLazyImages(ctx context.Context, id int64, loadRemoteImages bool) (core.Message, error)
	GetMessageText(ctx context.Context, id int64) (core.MessageText, error)
	GetPluginConfig(ctx context.Context, name string) ([]core.ConfigValue, error)
	RawMessageFile(ctx context.Context, messageID int64) (string, error)
	GetSettings(ctx context.Context) (core.Settings, error)
	GetThread(ctx context.Context, id int64) (core.Thread, error)
	GrantMCPServer(ctx context.Context, id int64) error
	GrantPermission(ctx context.Context, name string) error
	ImageCacheSize() int64
	ImagesAllowed(ctx context.Context, message int64) (bool, error)
	ImportAccounts(ctx context.Context, data []byte) (core.AccountImportResult, error)
	ImportEML(ctx context.Context, accountID, folderID int64, paths []string) (core.ImportSummary, error)
	InlineSignatureImages(ctx context.Context, html string) (string, error)
	InstallPlugin(ctx context.Context, srcDir string) (core.PluginInfo, error)
	InstallPluginFromRegistryWithProgress(ctx context.Context, name string, progress func(core.PluginInstallProgress)) (core.PluginInfo, error)
	KeychainStatus(ctx context.Context) error
	LabelModelReady(ctx context.Context) bool
	ListAccounts(ctx context.Context) ([]core.Account, error)
	ListActions(ctx context.Context, kind string) ([]core.ActionInfo, error)
	ListConversations(ctx context.Context, kind string) ([]core.Conversation, error)
	ListDrafts(ctx context.Context, accountID int64) ([]core.Draft, error)
	ListFolders(ctx context.Context, accountID int64) ([]core.Folder, error)
	ListLabels(ctx context.Context) ([]core.Label, error)
	ListMCPServers(ctx context.Context) ([]core.MCPServer, error)
	ListMessages(ctx context.Context, v core.View, cursor string, limit int) (core.MessagePage, error)
	ListModelServices(ctx context.Context) ([]core.ModelService, error)
	ListOutbox(ctx context.Context, accountID int64) ([]core.OutboxItem, error)
	ListPluginModels(ctx context.Context) ([]core.PluginModels, error)
	ListPluginRegistry(ctx context.Context) (core.RegistryView, error)
	ListPlugins(ctx context.Context) ([]core.PluginInfo, error)
	ListProviders() []auth.Provider
	ListSignatures(ctx context.Context) ([]core.Signature, error)
	ListSkills(ctx context.Context) ([]core.SkillInfo, error)
	ListThreads(ctx context.Context, v core.View, cursor string, limit int) (core.ThreadPage, error)
	ListTranslators(ctx context.Context) ([]core.TranslatorInfo, error)
	LoadOlder(ctx context.Context, folderID int64) (more bool, err error)
	MarkAllAsRead(ctx context.Context, v core.View) (int, error)
	MarkViewRead(ctx context.Context, id int64) (int, error)
	MaskingLog(ctx context.Context) ([]core.MaskRecord, error)
	MessageGist(ctx context.Context, messageID int64) (string, error)
	MessageLabels(ctx context.Context, ids []int64) (map[int64][]core.AppliedLabel, error)
	ModelServiceAvatar(ctx context.Context, id int64) (core.Avatar, error)
	MoveFrom(ctx context.Context, ids []int64, from, folderID int64) error
	MoveToRole(ctx context.Context, ids []int64, role string) error
	NewDraft(ctx context.Context, accountID int64, mode string, sourceMessageID int64) (core.Draft, error)
	NewMailtoDraft(ctx context.Context, accountID int64, link string) (core.Draft, error)
	PendingLabels(ctx context.Context) (int, error)
	PendingMCPSends(ctx context.Context) ([]core.MCPSend, error)
	PluginCards(ctx context.Context, messageID int64) ([]core.Card, error)
	PluginLoginStatus(ctx context.Context, name string) (core.PluginLogin, error)
	PluginLogout(ctx context.Context, name string) error
	PluginReadme(ctx context.Context, name, lang string) (string, error)
	PluginTags(ctx context.Context, messageID int64) ([]core.Tag, error)
	PollPluginLogin(ctx context.Context, name, flow string) (core.PluginLoginState, error)
	PrepareModelWithProgress(ctx context.Context, ref string, progress func(core.ModelStatus)) (core.ModelStatus, error)
	PreviewAccountImport(ctx context.Context, data []byte) (core.AccountImportPreview, error)
	PreviewMasking(ctx context.Context, st core.Settings, text string) (string, error)
	PreviewRule(ctx context.Context, d core.RuleDoc, limit int) (core.RulePreview, error)
	PreviewView(ctx context.Context, d core.SavedViewDoc, viewID int64, limit int) (core.ViewContents, error)
	ProposedRuleSaved(ctx context.Context, id int64, r core.Rule) error
	Purges(ctx context.Context, accountID int64) (map[string]core.Purge, error)
	QuickReplies(ctx context.Context, messageID int64) ([]core.QuickReply, error)
	Reauthorize(ctx context.Context, id int64, flowID string) error
	RecentLog() string
	RecentSignInCodes(ctx context.Context, now time.Time) ([]core.SignInCode, error)
	RecheckSending(ctx context.Context, id int64) (core.CheckReport, error)
	RecognitionModelBackend(ctx context.Context) (*core.RecognitionModelBackend, error)
	RecognitionModelError() error
	RecognitionModelInstalled() bool
	RecordCapStatus(ctx context.Context, id, version int64, c string, st core.CapStatus) error
	RefreshMCPTools(ctx context.Context, id int64) error
	RefreshPluginRegistry(ctx context.Context) error
	RemoveAccount(ctx context.Context, id int64) error
	RemoveMCPServer(ctx context.Context, id int64) error
	RemoveModelFiles(ctx context.Context, ref string) error
	RemoveModelService(ctx context.Context, id int64) error
	RenameLocalFolder(ctx context.Context, folderID int64, name string) error
	ReorderLabels(ctx context.Context, ids []int64) error
	ReorderRules(ctx context.Context, ids []int64) error
	RescanLabels(ctx context.Context, days int, all bool) (int, error)
	RescanSecurity(ctx context.Context, days int, all bool) (int, error)
	ResolveMCPSend(ctx context.Context, id int64, accept bool) error
	ResolveMCPSendAcknowledged(ctx context.Context, id int64, digest string, send bool) error
	ResolveOutbox(ctx context.Context, id int64, action string) error
	RetryRuleRun(ctx context.Context, runID int64) error
	RuleHitCounts(ctx context.Context) (map[int64]int, error)
	RuleHits(ctx context.Context, ruleID int64) ([]core.RuleHit, error)
	Rules(ctx context.Context) ([]core.Rule, error)
	RulesInBackground(ctx context.Context) (bool, error)
	RunAction(ctx context.Context, id string, t core.Target, opts core.RunActionOptions) (core.ActionResult, error)
	RunRuleOnExisting(ctx context.Context, ruleID int64) (int, error)
	RunSkill(ctx context.Context, name string, t core.Target) (<-chan ai.Event, error)
	SaveDraft(ctx context.Context, d core.Draft) (core.Draft, error)
	SaveRule(ctx context.Context, r core.Rule) (int64, error)
	SaveView(ctx context.Context, b core.SavedView) (int64, error)
	SavedViews(ctx context.Context) ([]core.SavedView, error)
	SearchWith(ctx context.Context, q core.SearchQuery, limit int) ([]core.MessageSummary, error)
	SecurityProgress(ctx context.Context) (core.SecurityScanProgress, error)
	SendAcknowledgedRevision(ctx context.Context, draftID, revision int64, digest string) error
	SendRevision(ctx context.Context, draftID, revision int64) error
	SenderAvatar(ctx context.Context, messageID int64, allowRemote bool) (core.Avatar, error)
	SetFlags(ctx context.Context, ids []int64, add, remove []string) error
	SetLabel(ctx context.Context, messageIDs []int64, labelID int64, on bool) error
	SetMCPServerEnabled(ctx context.Context, id int64, enabled bool) error
	SetMCPToolTrusted(ctx context.Context, id int64, tool string, trusted bool) error
	SetMessageSecurity(ctx context.Context, messageIDs []int64, reason string) error
	SetPluginConfig(ctx context.Context, name string, values map[string]json.RawMessage) error
	SetPluginEnabled(ctx context.Context, name string, enabled bool) error
	SetPurge(ctx context.Context, accountID int64, role string, days int) error
	SetRoleFolder(ctx context.Context, accountID int64, role string, folderID int64) error
	SetRuleEnabled(ctx context.Context, id int64, on bool) error
	SetSettings(ctx context.Context, st core.Settings) (core.Settings, error)
	SetSkillEnabled(ctx context.Context, name string, enabled bool) error
	SetUIForeground(ctx context.Context, on bool) error
	SetViewException(ctx context.Context, id, threadID int64, kind string) error
	SignatureCode(ctx context.Context, id int64) (string, error)
	SignatureEditor(ctx context.Context, id int64) (string, error)
	StarredCounts(ctx context.Context) (map[int64]int, int, error)
	StopRescan(ctx context.Context) error
	StopSecurityRescan(ctx context.Context) error
	StoredHTML(ctx context.Context, id int64) (string, error)
	Subscribe() (<-chan events.Event, func())
	SyncNow(ctx context.Context, accountID int64) error
	TaskModel(ctx context.Context, skill string) (string, error)
	TestCapability(ctx context.Context, id int64, in core.ModelServiceInput, c string) (core.CapStatus, error)
	TestPluginModel(ctx context.Context, ref string) (core.CapStatus, error)
	ThreadLabels(ctx context.Context, threadIDs []int64) (map[int64][]core.AppliedLabel, error)
	TrustImageSender(ctx context.Context, from string) error
	TrustedImageSenders(ctx context.Context) ([]string, error)
	UnblockSender(ctx context.Context, addr string) error
	UndoArchive(ctx context.Context, a core.SenderArchive) error
	UndoArchiveThreads(ctx context.Context, u core.ArchiveUndo) error
	UndoPurge(ctx context.Context, ids []int64) (gone int, err error)
	UndoRuleRun(ctx context.Context, runID int64) (kept int, err error)
	UninstallPlugin(ctx context.Context, name string) error
	Unsubscribe(ctx context.Context, messageID int64) (core.UnsubscribeResult, error)
	UnsubscribeMethod(ctx context.Context, messageID int64) (core.Unsubscribe, error)
	UntrustImageSender(ctx context.Context, addr string) error
	UpdateAccount(ctx context.Context, id int64, p core.AccountPatch) (core.Account, error)
	UpdateLabel(ctx context.Context, id int64, in core.LabelInput) (core.Label, error)
	UpdateMCPServer(ctx context.Context, id int64, in core.MCPServerInput) (core.MCPServer, error)
	UpdateModelService(ctx context.Context, id int64, in core.ModelServiceInput) (core.ModelService, error)
	UpdatePassword(ctx context.Context, id int64, password string) error
	UpdateSignature(ctx context.Context, id int64, p core.SignaturePatch) (core.Signature, error)
	ViewContents(ctx context.Context, id int64, limit int) (core.ViewContents, error)
	ViewExceptions(ctx context.Context, id int64) ([]core.ViewException, error)
	ViewGroupPage(ctx context.Context, id int64, groupID, cursor string, limit int) (core.ViewGroupPage, error)
	ViewTemplates(ctx context.Context) ([]core.ViewTemplate, error)
	ViewWhy(ctx context.Context, id, threadID int64) (core.ViewReason, error)
	AddAddressBook(ctx context.Context, in core.AddressBookInput) (core.AddressBook, error)
	ContactAvatar(ctx context.Context, addr string) (core.Avatar, error)
	ListAddressBookPresets() []core.AddressBookPreset
	ListAddressBooks(ctx context.Context) ([]core.AddressBook, error)
	RemoveAddressBook(ctx context.Context, id int64) error
	RenameAddressBook(ctx context.Context, id int64, name string) error
	ReorderAccountEntries(ctx context.Context, entries []core.AccountEntry) error
	SetAddressBookCollectionEnabled(ctx context.Context, collectionID int64, enabled bool) error
	SyncAddressBook(ctx context.Context, id int64) error
	UpdateAddressBookAuth(ctx context.Context, id int64, password, oauthFlowID string, client core.OAuthClient) (core.AddressBook, error)
}

var _ Service = (*core.Service)(nil)
