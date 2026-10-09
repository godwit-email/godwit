package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/store"
	"golang.org/x/net/http/httpguts"
)

// MCP servers are programs (stdio) or remote services (http) the user
// configures and grants; their tools join Ask and Assist runs. Godwit cannot
// limit what a server does itself, so every call is approved unless the user
// trusts that exact tool definition, and a trusted one is still approved once
// the run read mail (egress to "mcp:<name>").

var ErrMCPNotFound = errors.New("MCP server not found")

// MCPServerInput configures a server. Transport is "stdio" (Command, Args,
// Env) or "http" (URL, Headers). Env and Header values go to the keychain;
// an empty value in an update keeps the stored one, a key left out is removed.
type MCPServerInput struct {
	Name, Transport, Command string
	Args                     []string
	Env                      map[string]string
	URL                      string
	Headers                  map[string]string
}

// MCPServer is a configured server. Granted means the current Version is
// granted; Tools are the last listed for this version; Error is why it last
// failed to connect or was stopped.
type MCPServer struct {
	ID                        int64
	Name, Transport, Command  string
	Args, EnvKeys, HeaderKeys []string
	URL                       string
	Version                   int64
	Enabled, Granted          bool
	Tools                     []MCPTool
	Error                     string
}

// MCPTool is a tool of a server. ReadOnlyHint is the server's own claim, for
// display only; Trusted holds while the user trusted this DefinitionHash.
type MCPTool struct {
	Name, Description     string
	ReadOnlyHint, Trusted bool
	DefinitionHash        string
}

const (
	maxMCPResult   = 64 << 10 // text handed back from one call
	maxMCPStderr   = 64 << 10 // stderr kept of a stdio server
	mcpStderrShown = 1 << 10  // the stderr tail an error shows
	mcpGrace       = 2 * time.Second
)

// mcpCallTimeout bounds a connect and a call (tests shorten it).
var mcpCallTimeout = 60 * time.Second

// mcpState is what the Service knows of a server besides its row. A conn is
// only kept for a server that is enabled and granted at that version: every
// change to the row closes it first.
type mcpState struct {
	conn    *mcpConn
	version int64    // the version tools were listed for
	tools   []mcpDef // last listed
	err     string   // cleaned
}

func (s *Service) mcpGet(id int64) mcpState {
	s.mcpStMu.Lock()
	defer s.mcpStMu.Unlock()
	if st := s.mcpSt[id]; st != nil {
		return *st
	}
	return mcpState{}
}

func (s *Service) mcpSet(id int64, f func(st *mcpState)) {
	s.mcpStMu.Lock()
	defer s.mcpStMu.Unlock()
	if s.mcpSt == nil {
		s.mcpSt = map[int64]*mcpState{}
	}
	if s.mcpSt[id] == nil {
		s.mcpSt[id] = &mcpState{}
	}
	f(s.mcpSt[id])
}

func (s *Service) mcpRows(ctx context.Context, id int64) ([]MCPServer, error) {
	q := `SELECT id, name, transport, command, args, env_keys, url, header_keys, version, enabled, granted_version FROM mcp_servers`
	var args []any
	if id != 0 {
		q += " WHERE id = ?"
		args = append(args, id)
	}
	rows, err := s.db.Query(ctx, q+" ORDER BY id", args...)
	if err != nil {
		return nil, err
	}
	out := make([]MCPServer, 0, len(rows.Values))
	for _, r := range rows.Values {
		srv := MCPServer{ID: store.Int(r[0]), Name: store.Str(r[1]), Transport: store.Str(r[2]), Command: store.Str(r[3]),
			URL: store.Str(r[6]), Version: store.Int(r[8]), Enabled: store.Int(r[9]) != 0}
		srv.Granted = store.Int(r[10]) == srv.Version
		if err := errors.Join(json.Unmarshal([]byte(store.Str(r[4])), &srv.Args), json.Unmarshal([]byte(store.Str(r[5])), &srv.EnvKeys),
			json.Unmarshal([]byte(store.Str(r[7])), &srv.HeaderKeys)); err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, nil
}

func (s *Service) mcpRow(ctx context.Context, id int64) (MCPServer, error) {
	rows, err := s.mcpRows(ctx, id)
	if err == nil && len(rows) == 0 {
		err = ErrMCPNotFound
	}
	if err != nil {
		return MCPServer{}, err
	}
	return rows[0], nil
}

// mcpTrust is the server's trusted tools: name → definition hash.
func (s *Service) mcpTrust(ctx context.Context, id int64) (map[string]string, error) {
	rows, err := s.db.Query(ctx, "SELECT tool, definition_hash FROM mcp_tool_trust WHERE server_id = ?", id)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows.Values {
		out[store.Str(r[0])] = store.Str(r[1])
	}
	return out, nil
}

// withState adds what the Service knows of srv: its tools (if listed for
// this version, with trust) and its last error.
func (s *Service) withState(ctx context.Context, srv MCPServer) (MCPServer, error) {
	st := s.mcpGet(srv.ID)
	srv.Error = st.err
	if st.version != srv.Version {
		return srv, nil
	}
	trust, err := s.mcpTrust(ctx, srv.ID)
	if err != nil {
		return srv, err
	}
	srv.Tools = []MCPTool{}
	for _, d := range st.tools {
		t := d.MCPTool
		t.Trusted = trust[t.Name] == t.DefinitionHash
		srv.Tools = append(srv.Tools, t)
	}
	return srv, nil
}

// ListMCPServers lists the configured servers by ID.
func (s *Service) ListMCPServers(ctx context.Context) ([]MCPServer, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	list, err := s.mcpRows(ctx, 0)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i], err = s.withState(ctx, list[i]); err != nil {
			return nil, err
		}
	}
	return list, nil
}

func (s *Service) mcpServer(ctx context.Context, id int64) (MCPServer, error) {
	srv, err := s.mcpRow(ctx, id)
	if err != nil {
		return srv, err
	}
	return s.withState(ctx, srv)
}

// checkMCPInput validates in for a new server (old nil) or an update of old.
// Errors never quote values.
func checkMCPInput(in *MCPServerInput, old *MCPServer) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 64 || strings.ContainsFunc(in.Name, unicode.IsControl) {
		return errors.New("an MCP server needs a name of up to 64 characters")
	}
	switch in.Transport {
	case "stdio":
		if strings.TrimSpace(in.Command) == "" || in.URL != "" || len(in.Headers) > 0 {
			return errors.New("a stdio MCP server needs a command and no URL or headers")
		}
	case "http":
		if in.Command != "" || len(in.Args) > 0 || len(in.Env) > 0 {
			return errors.New("an http MCP server takes no command, arguments or environment")
		}
		if _, err := mcpURL(in.URL); err != nil {
			return err
		}
	default:
		return errors.New(`an MCP server's transport is "stdio" or "http"`)
	}
	values := func(kind string, m map[string]string, had []string, validKey, validValue func(string) bool) error {
		for k, v := range m {
			switch {
			case !validKey(k):
				return fmt.Errorf("invalid %s name %q", kind, k)
			case v == "" && (old == nil || !slices.Contains(had, k)):
				return fmt.Errorf("%s %s needs a value", kind, k)
			case v != "" && utf8.RuneCountInString(v) < 8:
				return fmt.Errorf("%s %s: a value must be at least 8 characters", kind, k)
			case !validValue(v):
				return fmt.Errorf("%s %s: the value has invalid characters", kind, k)
			}
		}
		return nil
	}
	var oldEnv, oldHeaders []string
	if old != nil {
		oldEnv, oldHeaders = old.EnvKeys, old.HeaderKeys
	}
	return errors.Join(
		values("environment variable", in.Env, oldEnv,
			func(k string) bool { return k != "" && !strings.ContainsAny(k, "=\x00") },
			func(v string) bool { return !strings.Contains(v, "\x00") }),
		values("header", in.Headers, oldHeaders, httpguts.ValidHeaderFieldName, httpguts.ValidHeaderFieldValue))
}

// mcpURL is an http server's URL: https, or http to a loopback host; no userinfo.
func mcpURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || u.Opaque != "":
		return nil, errors.New("an http MCP server needs a valid URL")
	case u.User != nil:
		return nil, errors.New("an MCP server URL cannot hold credentials; use a header")
	case u.Scheme != "https" && (u.Scheme != "http" || !loopbackHost(u.Hostname())):
		return nil, errors.New("an MCP server URL must use https (http only on this computer)")
	}
	return u, nil
}

func loopbackHost(h string) bool {
	ip := net.ParseIP(h)
	return strings.EqualFold(h, "localhost") || ip != nil && ip.IsLoopback()
}

func encodeKeys(m map[string]string) string { return mcpList(slices.Sorted(maps.Keys(m))) }

// mcpList is l as a JSON list, never null.
func mcpList(l []string) string {
	if l == nil {
		l = []string{}
	}
	return encodeList(l)
}

// AddMCPServer adds a server, disabled and not granted.
func (s *Service) AddMCPServer(ctx context.Context, in MCPServerInput) (MCPServer, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return MCPServer{}, err
	}
	defer end()
	defer s.bus.Publish(events.Event{Type: events.MCPChanged})
	if err := checkMCPInput(&in, nil); err != nil {
		return MCPServer{}, err
	}
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	r, err := s.db.Execute(ctx, `INSERT INTO mcp_servers(name, transport, command, args, env_keys, url, header_keys, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, in.Name, in.Transport, in.Command, mcpList(in.Args), encodeKeys(in.Env), in.URL,
		encodeKeys(in.Headers), time.Now().Unix())
	if err != nil {
		return MCPServer{}, err
	}
	id := r.LastInsertID
	if err := errors.Join(s.saveMCPSecrets(id, "env", in.Env), s.saveMCPSecrets(id, "header", in.Headers)); err != nil {
		_, derr1 := s.dropMCPSecrets(id, "env", slices.Collect(maps.Keys(in.Env)))
		_, derr2 := s.dropMCPSecrets(id, "header", slices.Collect(maps.Keys(in.Headers)))
		derr := errors.Join(derr1, derr2)
		if derr == nil { // else the row, disabled, keeps the keys listed for RemoveMCPServer
			_, derr = s.db.Execute(context.WithoutCancel(ctx), "DELETE FROM mcp_servers WHERE id = ?", id)
		}
		return MCPServer{}, errors.Join(err, derr)
	}
	return s.mcpServer(ctx, id)
}

// UpdateMCPServer changes a server: its version goes up, its connection
// closes, its grant and tool trust are cleared, it is disabled, and its
// pending approvals become stale.
func (s *Service) UpdateMCPServer(ctx context.Context, id int64, in MCPServerInput) (MCPServer, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return MCPServer{}, err
	}
	defer end()
	defer s.bus.Publish(events.Event{Type: events.MCPChanged})
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	old, err := s.mcpRow(ctx, id)
	if err != nil {
		return MCPServer{}, err
	}
	if err := checkMCPInput(&in, &old); err != nil {
		return MCPServer{}, err
	}
	// First, in one step: a new version, disabled, without grant or trust,
	// so nothing bound to the old one stays valid; and old ∪ new keys
	// listed, so every keychain entry touched below is found by a retry or
	// RemoveMCPServer whatever happens.
	s.disconnectMCP(id, "")
	newEnv, newHeaders := slices.Collect(maps.Keys(in.Env)), slices.Collect(maps.Keys(in.Headers))
	if _, err := s.db.Transaction(ctx, []sqlite.Statement{
		{SQL: "UPDATE mcp_servers SET version = version + 1, enabled = 0, granted_version = 0 WHERE id = ?", Args: []any{id}},
		{SQL: "DELETE FROM mcp_tool_trust WHERE server_id = ?", Args: []any{id}},
		listMCPKeys(id, slices.Concat(old.EnvKeys, newEnv), slices.Concat(old.HeaderKeys, newHeaders)),
	}); err != nil {
		return MCPServer{}, err
	}
	if err := s.staleMCPApprovals(ctx, id); err != nil {
		return MCPServer{}, err
	}
	// The new config is saved only once the keychain has every new value;
	// the lists are pruned to the new keys, plus those whose delete failed.
	if err := errors.Join(s.saveMCPSecrets(id, "env", in.Env), s.saveMCPSecrets(id, "header", in.Headers)); err != nil {
		return MCPServer{}, err
	}
	dropped := func(oldKeys []string, now map[string]string) []string {
		return slices.DeleteFunc(slices.Clone(oldKeys), func(k string) bool { _, ok := now[k]; return ok })
	}
	failedEnv, err1 := s.dropMCPSecrets(id, "env", dropped(old.EnvKeys, in.Env))
	failedHeaders, err2 := s.dropMCPSecrets(id, "header", dropped(old.HeaderKeys, in.Headers))
	stmts := []sqlite.Statement{listMCPKeys(id, slices.Concat(newEnv, failedEnv), slices.Concat(newHeaders, failedHeaders))}
	if err := errors.Join(err1, err2); err != nil {
		_, terr := s.db.Transaction(ctx, stmts) // the old config stays
		return MCPServer{}, errors.Join(err, terr)
	}
	stmts = append(stmts, sqlite.Statement{SQL: "UPDATE mcp_servers SET name = ?, transport = ?, command = ?, args = ?, url = ? WHERE id = ?",
		Args: []any{in.Name, in.Transport, in.Command, mcpList(in.Args), in.URL, id}})
	if _, err := s.db.Transaction(ctx, stmts); err != nil {
		return MCPServer{}, err
	}
	return s.mcpServer(ctx, id)
}

// RemoveMCPServer stops a server and deletes it with its keychain entries;
// its pending approvals become stale.
func (s *Service) RemoveMCPServer(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	defer s.bus.Publish(events.Event{Type: events.MCPChanged})
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	old, err := s.mcpRow(ctx, id)
	if err != nil {
		return err
	}
	// Stopped and disabled first; then the secrets: if one cannot be deleted
	// the server stays, disabled, listing only the keys left, for a retry.
	if _, err := s.db.Execute(ctx, "UPDATE mcp_servers SET enabled = 0 WHERE id = ?", id); err != nil {
		return err
	}
	s.disconnectMCP(id, "")
	failedEnv, err1 := s.dropMCPSecrets(id, "env", old.EnvKeys)
	failedHeaders, err2 := s.dropMCPSecrets(id, "header", old.HeaderKeys)
	if err := errors.Join(err1, err2); err != nil {
		_, lerr := s.db.Transaction(ctx, []sqlite.Statement{listMCPKeys(id, failedEnv, failedHeaders)})
		return errors.Join(err, lerr)
	}
	if _, err := s.db.Execute(ctx, "DELETE FROM mcp_servers WHERE id = ?", id); err != nil {
		return err
	}
	s.mcpStMu.Lock()
	delete(s.mcpSt, id)
	s.mcpStMu.Unlock()
	return s.staleMCPApprovals(ctx, id)
}

// GrantMCPServer grants the server's current version: it may then be started.
func (s *Service) GrantMCPServer(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	defer s.bus.Publish(events.Event{Type: events.MCPChanged})
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	r, err := s.db.Execute(ctx, "UPDATE mcp_servers SET granted_version = version WHERE id = ?", id)
	if err == nil && r.Changes == 0 {
		err = ErrMCPNotFound
	}
	return err
}

// SetMCPServerEnabled starts (connects and lists tools) or stops a server.
// Starting needs its current version granted (ErrNeedsGrant).
func (s *Service) SetMCPServerEnabled(ctx context.Context, id int64, enabled bool) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	defer s.bus.Publish(events.Event{Type: events.MCPChanged})
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	srv, err := s.mcpRow(ctx, id)
	if err != nil {
		return err
	}
	if !enabled {
		s.disconnectMCP(id, "")
		_, err := s.db.Execute(ctx, "UPDATE mcp_servers SET enabled = 0 WHERE id = ?", id)
		return err
	}
	if !srv.Granted {
		return ErrNeedsGrant
	}
	if s.mcpGet(id).conn == nil {
		if _, err := s.connectMCP(ctx, srv, true); err != nil {
			return err
		}
	}
	if _, err := s.db.Execute(ctx, "UPDATE mcp_servers SET enabled = 1 WHERE id = ?", id); err != nil {
		s.disconnectMCP(id, "")
		return err
	}
	return nil
}

// RefreshMCPTools connects to the server again and lists its tools; a
// disabled server is disconnected afterwards. Needs the current version granted.
func (s *Service) RefreshMCPTools(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	defer s.bus.Publish(events.Event{Type: events.MCPChanged})
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	srv, err := s.mcpRow(ctx, id)
	if err != nil {
		return err
	}
	if !srv.Granted {
		return ErrNeedsGrant
	}
	s.disconnectMCP(id, "")
	_, err = s.connectMCP(ctx, srv, srv.Enabled)
	return err
}

// SetMCPToolTrusted trusts (or stops trusting) a tool as listed now: trust
// is bound to its definition hash, so any change to the definition ends it.
func (s *Service) SetMCPToolTrusted(ctx context.Context, id int64, tool string, trusted bool) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	defer s.bus.Publish(events.Event{Type: events.MCPChanged})
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	srv, err := s.mcpRow(ctx, id)
	if err != nil {
		return err
	}
	st := s.mcpGet(id)
	var def *mcpDef
	if st.version == srv.Version {
		def = findDef(st.tools, tool)
	}
	if def == nil {
		return fmt.Errorf("MCP server %s has no tool %q listed; refresh its tools", srv.Name, tool)
	}
	if trusted {
		_, err = s.db.Execute(ctx, "INSERT OR REPLACE INTO mcp_tool_trust(server_id, tool, definition_hash) VALUES (?, ?, ?)",
			id, tool, def.DefinitionHash)
	} else {
		_, err = s.db.Execute(ctx, "DELETE FROM mcp_tool_trust WHERE server_id = ? AND tool = ?", id, tool)
	}
	if err == nil && st.conn != nil {
		err = s.registerMCPTools(ctx, srv, st.conn)
	}
	return err
}

// staleMCPApprovals ends the server's pending approvals as stale and resumes
// the runs left with nothing open.
func (s *Service) staleMCPApprovals(ctx context.Context, id int64) error {
	ctx = context.WithoutCancel(ctx)
	as, err := s.approvalRows(ctx, "a.state = 'pending' AND a.source = ? AND a.extension = ?", gateway.SourceMCP, strconv.FormatInt(id, 10))
	if err != nil || len(as) == 0 {
		return err
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	var errs []error
	runs := map[int64]int64{}
	for _, a := range as {
		_, err := s.db.Transaction(ctx, []sqlite.Statement{{SQL: "UPDATE approvals SET state = 'stale', result = ?, updated_at = ? WHERE id = ? AND state = 'pending'",
			Args: []any{msgStale, time.Now().Unix(), a.ID}}, a.audit("stale")})
		errs = append(errs, err)
		runs[a.RunID] = a.ConversationID
	}
	for run, conv := range runs {
		s.bus.Publish(events.Event{Type: events.ApprovalsChanged, ConversationID: conv})
		errs = append(errs, s.resumeRun(ctx, run))
	}
	return errors.Join(errs...)
}
