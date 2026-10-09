package rpc

import "encoding/json"

// Roles a connection declares in hello.
const (
	RoleFull = "full" // every exposed method, for the user's own programs
	RoleMCP  = "mcp"  // read and draft only, for godwit mcp
)

// mcpMethods is the mcp role's allowlist: reads that touch only stored data,
// and drafts. Nothing here sends, deletes, changes accounts, settings,
// extensions or model services, decides approvals or downloads attachments.
var mcpMethods = map[string]bool{
	"ListAccounts": true, "ListFolders": true, "ListThreads": true, "GetThread": true,
	"ListMessages": true, "GetMessageText": true, "Search": true,
	"ListDrafts": true, "GetDraft": true, "NewDraft": true, "SaveDraft": true,
}

// mcpDraftModes are the NewDraft modes the mcp role may use; forward is not
// one, since it copies (and may download) the source's attachments.
var mcpDraftModes = map[string]bool{"new": true, "reply": true, "reply_all": true}

// MCPAllowed reports whether the mcp role may call method with params.
func MCPAllowed(method string, params []json.RawMessage) bool {
	if !mcpMethods[method] {
		return false
	}
	if method == "NewDraft" {
		var mode string
		return len(params) >= 2 && json.Unmarshal(params[1], &mode) == nil && mcpDraftModes[mode]
	}
	return true
}
