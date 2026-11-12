package rpc

import "encoding/json"

// Roles a connection declares in hello.
const (
	RoleUI   = "ui"   // every exposed method plus UI-only methods (rpcbind/testdata/ui_methods.txt), for Godwit's own interface
	RoleFull = "full" // every method exposed to external programs (rpcbind/testdata/full_methods.txt), for the user's own programs
	RoleMCP  = "mcp"  // read, draft and request a send, for godwit mcp
)

// mcpMethods is the mcp role's allowlist: reads that touch only stored data,
// drafts, and RequestMCPSend, whose outcome core decides by the user's
// 通过 MCP 发送 setting. Nothing else sends, and nothing here deletes, changes
// accounts, settings, extensions or model services, decides approvals or
// downloads attachments.
var mcpMethods = map[string]bool{
	"ListAccounts": true, "ListFolders": true, "ListThreads": true, "GetThread": true,
	"ListMessages": true, "GetMessageText": true, "Search": true,
	"ListDrafts": true, "GetDraft": true, "NewDraft": true, "SaveDraft": true,
	"RequestMCPSend": true,
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
