package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP tools in Wit runs: names, definitions, trust, calls and results.

// mcpDef is a listed tool with its input schema.
type mcpDef struct {
	MCPTool
	schema map[string]any
}

var unsafeToolChars = regexp.MustCompile(`[^A-Za-z0-9_]`)

// mcpToolName is a tool's name in Wit runs: mcp_<server id>_<tool>_<hash>,
// the tool's name with every other character as "_", cut so the whole name
// fits 64 characters; the definition hash's first 6 hex digits tell apart
// tools that look alike.
func mcpToolName(id int64, tool, hash string) string {
	prefix := fmt.Sprintf("mcp_%d_", id)
	name := unsafeToolChars.ReplaceAllString(tool, "_")
	if n := 64 - len(prefix) - 7; len(name) > n {
		name = name[:max(n, 1)]
	}
	return prefix + name + "_" + hash[:6]
}

// mcpDefs are the listed tools with their definition hashes (SHA-256 of
// their whole JSON definition); two tools with the same Wit name are an error.
func mcpDefs(id int64, tools []*mcp.Tool) ([]mcpDef, error) {
	seen := map[string]string{}
	out := make([]mcpDef, 0, len(tools))
	for _, t := range tools {
		b, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		d := mcpDef{MCPTool: MCPTool{Name: t.Name, Description: t.Description, DefinitionHash: hex.EncodeToString(sum[:]),
			ReadOnlyHint: t.Annotations != nil && t.Annotations.ReadOnlyHint}}
		if b, err = json.Marshal(t.InputSchema); err == nil {
			err = json.Unmarshal(b, &d.schema)
		}
		if err != nil {
			return nil, fmt.Errorf("tool %q has an invalid input schema", t.Name)
		}
		if d.schema == nil {
			d.schema = map[string]any{"type": "object"}
		}
		n := mcpToolName(id, t.Name, d.DefinitionHash)
		if other, ok := seen[n]; ok {
			return nil, fmt.Errorf("tools %q and %q get the same name", other, t.Name)
		}
		seen[n] = t.Name
		out = append(out, d)
	}
	return out, nil
}

// registerMCPTools offers c's tools to Wit runs, trusted ones as read tools,
// replacing what the server offered before. The caller holds mcpMu.
func (s *Service) registerMCPTools(ctx context.Context, srv MCPServer, c *mcpConn) error {
	trust, err := s.mcpTrust(ctx, srv.ID)
	if err != nil {
		return err
	}
	s.unregisterMCPTools(srv.ID)
	for _, d := range c.tools {
		if err := s.registerTool(s.mcpToolDesc(srv, d, trust[d.Name] == d.DefinitionHash)); err != nil {
			s.unregisterMCPTools(srv.ID)
			return err
		}
	}
	return nil
}

func (s *Service) unregisterMCPTools(id int64) {
	ext := strconv.FormatInt(id, 10)
	s.toolsMu.Lock()
	defer s.toolsMu.Unlock()
	for n, t := range s.tools {
		if t.Source == gateway.SourceMCP && t.Extension == ext {
			delete(s.tools, n)
		}
	}
}

// mcpTools are the enabled servers' tools for a Wit run, untrusted ones
// (write tools: every call approved) only with writes. A granted, enabled
// server not connected yet, and not failed, is connected first.
func (s *Service) mcpTools(ctx context.Context, writes bool) []toolDesc {
	list, err := s.mcpRows(ctx, 0)
	if err != nil {
		slog.Default().Warn("could not list MCP servers", "err", err)
	}
	for _, srv := range list {
		if st := s.mcpGet(srv.ID); srv.Enabled && srv.Granted && st.conn == nil && st.err == "" {
			if _, err := s.mcpLive(ctx, srv.ID); err != nil {
				slog.Default().Warn("could not connect an MCP server", "server", srv.ID, "err", err)
			}
		}
	}
	s.toolsMu.Lock()
	var out []toolDesc
	for _, t := range s.tools {
		if t.Source == gateway.SourceMCP && (writes || !t.Write) {
			out = append(out, t)
		}
	}
	s.toolsMu.Unlock()
	slices.SortFunc(out, func(a, b toolDesc) int { return strings.Compare(a.Spec.Name, b.Spec.Name) })
	return out
}

// mcpToolDesc is the Wit tool for def of srv. An untrusted tool is a write
// tool: every call is approved first and runs once approved, bound to the
// server version and the definition. A trusted one runs in the run, unless
// the run read mail (egress to "mcp:<name>"): then it is approved too.
func (s *Service) mcpToolDesc(srv MCPServer, def mcpDef, trusted bool) toolDesc {
	tool, hash, dest := def.Name, def.DefinitionHash, "mcp:"+srv.Name
	spec := ai.ToolSpec{Name: mcpToolName(srv.ID, tool, hash), Description: s.extToolDescription("mcp "+srv.Name, def.Description),
		Schema: def.schema}
	if trusted {
		spec.Run = func(ctx context.Context, args json.RawMessage) (any, error) {
			if beforeMCPToolRun != nil {
				beforeMCPToolRun(ctx)
			}
			// Decided again now, failing closed: since the gate allowed the
			// call the run may have read mail, or the trust been revoked.
			why := ""
			if runOf(ctx).scope.ReadMail() {
				why = "this run has read mail"
			} else if ok, err := s.mcpTrusted(ctx, srv.ID, tool, hash); err != nil || !ok {
				why = "the tool is no longer trusted"
			}
			if why != "" {
				return nil, errors.New(mcpUntrusted(srv.Name, "not run: "+why+", so the call needs approval"))
			}
			text, err := s.callMCP(ctx, mcpCall{id: srv.ID, version: srv.Version, tool: tool, hash: hash,
				callID: "tool:" + ai.CallID(ctx)}, args)
			if ni := (*needsInput)(nil); errors.As(err, &ni) {
				return nil, s.askMCP(ctx, srv, hash, args, ni.in, "Answer MCP server "+srv.Name+"'s question")
			}
			if err != nil {
				return nil, cleanErr{mcpUntrusted(srv.Name, err.Error()), err}
			}
			return mcpUntrusted(srv.Name, text), nil
		}
	}
	return toolDesc{Spec: spec, Source: gateway.SourceMCP, Extension: strconv.FormatInt(srv.ID, 10), Write: !trusted, Mode: modeExternal,
		Egress: func(*gateway.Scope) []string { return []string{dest} },
		Prepare: func(_ context.Context, _ Conversation, args json.RawMessage) (prepared, error) {
			return prepared{Args: args, Summary: "Run " + s.pluginText(tool) + " of MCP server " + srv.Name}, nil
		},
		Run: func(ctx context.Context, a approvalRow) (any, error) {
			if beforeMCPApprovedRun != nil {
				beforeMCPApprovedRun()
			}
			// srv.Version is the version this tool belongs to, the one the
			// binding just checked: callMCP refuses any other.
			call := mcpCall{id: srv.ID, version: srv.Version, tool: tool, hash: hash, callID: fmt.Sprintf("approval:%d", a.ID)}
			if a.input != "" {
				call.input = new(mcpInput)
				if err := json.Unmarshal([]byte(a.input), call.input); err != nil {
					return nil, err
				}
			}
			return s.callMCP(ctx, call, a.Args)
		},
		Binding: func(ctx context.Context) (string, error) {
			c, err := s.mcpLive(ctx, srv.ID)
			if err != nil {
				return "", err
			}
			d := c.def(tool)
			if d == nil {
				return "", fmt.Errorf("MCP server %s no longer has tool %q", srv.Name, tool)
			}
			return fmt.Sprintf("%d:%s", c.version, d.DefinitionHash), nil
		},
		Timeout: mcpCallTimeout,
	}
}

// mcpUntrusted is text from or about server name's tool call as the model gets it.
func mcpUntrusted(name, text string) string {
	return ai.Untrusted("mcp:"+name, text, ai.SuspectInjection(text))
}

// mcpCall is one call: the tool of server id at version, with its
// definition hash; callID ties its HTTP requests' audit rows to it. input,
// if set, is the form it asked before, answered: it is called again with it.
type mcpCall struct {
	id, version        int64
	tool, hash, callID string
	input              *mcpInput
}

// Test hooks, nil in production: beforeMCPToolRun runs as a trusted tool's
// in-run call starts, beforeMCPApprovedRun as an approved call starts.
var (
	beforeMCPToolRun     func(ctx context.Context)
	beforeMCPApprovedRun func()
)

// mcpTrusted reports whether the server's tool is trusted at definition hash.
func (s *Service) mcpTrusted(ctx context.Context, id int64, tool, hash string) (bool, error) {
	trust, err := s.mcpTrust(ctx, id)
	return err == nil && trust[tool] == hash, err
}

type mcpCallKey struct{}

// callMCP calls the tool on a connection to call.version, if its definition
// is still call.hash, and returns its text content, cleaned and then capped
// at maxMCPResult; a result marked as an error is an error. Another version
// is ErrStaleApproval: nothing is sent. A call that times out stops the server.
func (s *Service) callMCP(ctx context.Context, call mcpCall, args json.RawMessage) (string, error) {
	id, tool, hash := call.id, call.tool, call.hash
	ctx = context.WithValue(ctx, mcpCallKey{}, call.callID) // for its HTTP requests' audit rows, a reconnect's too
	c, err := s.mcpLive(ctx, id)
	if err != nil {
		return "", s.mcpError(err)
	}
	if c.version != call.version {
		return "", ErrStaleApproval
	}
	if d := c.def(tool); d == nil || d.DefinitionHash != hash {
		return "", fmt.Errorf("the MCP tool %q changed; refresh the server's tools", s.pluginText(tool))
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	rctx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer cancel()
	defer context.AfterFunc(c.life, cancel)() // the connection closed: the call ends
	params := &mcp.CallToolParams{Name: tool, Arguments: args}
	if in := call.input; in != nil && in.Answer != nil {
		params.InputResponses, params.RequestState = mcp.InputResponseMap{in.ID: in.Answer}, in.State
	}
	res, err := c.session.CallTool(rctx, params)
	if err != nil {
		if errors.Is(rctx.Err(), context.DeadlineExceeded) {
			s.dropMCP(id, c, "stopped after a call timed out; refresh to start it again")
		}
		err = s.mcpError(c.withStderr(err))
		if !mcpAnswered(err) {
			err = fmt.Errorf("%w: %w", errMCPMaybeRan, err)
		}
		return "", err
	}
	if res.NeedsInput() { // it asks the user: nothing ran
		in, err := mcpInputOf(res, call.input)
		if err != nil {
			return "", err
		}
		return "", &needsInput{in}
	}
	text := capMCP(s.clean(mcpText(res))) // cleaned whole: a cut cannot split a secret
	if res.IsError {
		return "", errors.New(text)
	}
	return text, nil
}

// errMCPMaybeRan: the call may have reached the server, but no answer came
// back (the connection closed or failed, the session was stopped, the call
// was cut off): it may have run, and is never run again.
var errMCPMaybeRan = errors.New("the MCP call may have run, but its answer was lost")

// mcpAnswered reports whether err is the server's own JSON-RPC error answer,
// a definite failure; anything else after CallTool started may have run.
// The SDK's local closing errors (-32003 client, -32004 server closing) are
// not answers.
func mcpAnswered(err error) bool {
	var we *jsonrpc.Error
	return errors.As(err, &we) && we.Code != -32003 && we.Code != -32004
}

// mcpText joins a result's text content; other content shows as [<type>].
func mcpText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for i, c := range res.Content {
		if i > 0 {
			b.WriteByte('\n')
		}
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
			continue
		}
		var w struct{ Type string }
		if raw, err := c.MarshalJSON(); err == nil {
			json.Unmarshal(raw, &w)
		}
		b.WriteString("[" + w.Type + "]")
		if b.Len() > 2*maxMCPResult { // whole items only: none is split before cleaning
			break
		}
	}
	return b.String()
}

const cutMark = "\n[cut at 64 KB]"

// capMCP cuts cleaned text to maxMCPResult bytes, the mark included.
func capMCP(text string) string {
	if len(text) <= maxMCPResult {
		return text
	}
	return strings.ToValidUTF8(text[:maxMCPResult-len(cutMark)], "") + cutMark
}
