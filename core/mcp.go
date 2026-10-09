package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zalando/go-keyring"
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

// mcpTestTransport, set in tests, connects to an in-memory server instead.
var mcpTestTransport func(MCPServer) mcp.Transport

// mcpDef is a listed tool with its input schema.
type mcpDef struct {
	MCPTool
	schema map[string]any
}

// mcpConn is a live connection to a server version; proc is its stdio process.
type mcpConn struct {
	version int64
	session *mcp.ClientSession
	proc    *mcpProc
	tools   []mcpDef
}

func (c *mcpConn) def(tool string) *mcpDef { return findDef(c.tools, tool) }

func findDef(tools []mcpDef, name string) *mcpDef {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

// close ends the session; a stdio process gets mcpGrace after stdin closes,
// then its process group is killed and reaped.
func (c *mcpConn) close() {
	c.session.Close()
	if c.proc != nil {
		c.proc.Close()
	}
}

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

// Env and header values live in the keychain as <install-id>/mcp:<id>:<kind>:<key>,
// each its own owner in the secret set.
func (s *Service) mcpSecretUser(id int64, kind, key string) string {
	return s.installID + "/" + mcpSecretOwner(id, kind, key)
}

func mcpSecretOwner(id int64, kind, key string) string {
	return fmt.Sprintf("mcp:%d:%s:%s", id, kind, key)
}

// saveMCPSecrets stores the non-empty values (redacted from then on).
func (s *Service) saveMCPSecrets(id int64, kind string, values map[string]string) error {
	for k, v := range values {
		if v == "" {
			continue
		}
		s.secrets.Set(mcpSecretOwner(id, kind, k), v)
		if err := keychainSet(s.mcpSecretUser(id, kind, k), v); err != nil {
			return fmt.Errorf("save MCP %s %s: %w", kind, k, err)
		}
	}
	return nil
}

// dropMCPSecrets deletes the keys' entries (missing ones are fine). A key
// whose entry cannot be deleted stays redacted.
func (s *Service) dropMCPSecrets(id int64, kind string, keys []string) error {
	var errs []error
	for _, k := range keys {
		if err := keychainDelete(s.mcpSecretUser(id, kind, k)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			errs = append(errs, fmt.Errorf("delete MCP %s %s: %w", kind, k, err))
			continue
		}
		s.secrets.RemoveOwner(mcpSecretOwner(id, kind, k))
	}
	return errors.Join(errs...)
}

// mcpValues reads the keys' values and puts them in the secret set.
func (s *Service) mcpValues(id int64, kind string, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		v, err := keychainGet(s.mcpSecretUser(id, kind, k))
		if err != nil {
			return nil, fmt.Errorf("read MCP %s %s: %w", kind, k, err)
		}
		s.secrets.Set(mcpSecretOwner(id, kind, k), v)
		out[k] = v
	}
	return out, nil
}

// loadMCPSecrets puts every server's values in the secret set at startup;
// one that cannot be read is read again when its server connects.
func (s *Service) loadMCPSecrets(ctx context.Context) error {
	list, err := s.mcpRows(ctx, 0)
	if err != nil {
		return err
	}
	for _, srv := range list {
		_, err1 := s.mcpValues(srv.ID, "env", srv.EnvKeys)
		_, err2 := s.mcpValues(srv.ID, "header", srv.HeaderKeys)
		if err := errors.Join(err1, err2); err != nil {
			slog.Default().Warn("could not load an MCP server's secrets", "server", srv.ID, "err", err)
		}
	}
	return nil
}

// AddMCPServer adds a server, disabled and not granted.
func (s *Service) AddMCPServer(ctx context.Context, in MCPServerInput) (MCPServer, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return MCPServer{}, err
	}
	defer end()
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
		err = errors.Join(err, s.dropMCPSecrets(id, "env", slices.Collect(maps.Keys(in.Env))),
			s.dropMCPSecrets(id, "header", slices.Collect(maps.Keys(in.Headers))))
		_, derr := s.db.Execute(context.WithoutCancel(ctx), "DELETE FROM mcp_servers WHERE id = ?", id)
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
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	old, err := s.mcpRow(ctx, id)
	if err != nil {
		return MCPServer{}, err
	}
	if err := checkMCPInput(&in, &old); err != nil {
		return MCPServer{}, err
	}
	s.disconnectMCP(id, "")
	if _, err := s.db.Transaction(ctx, []sqlite.Statement{
		{SQL: `UPDATE mcp_servers SET name = ?, transport = ?, command = ?, args = ?, env_keys = ?, url = ?, header_keys = ?,
			version = version + 1, enabled = 0, granted_version = 0 WHERE id = ?`,
			Args: []any{in.Name, in.Transport, in.Command, mcpList(in.Args), encodeKeys(in.Env), in.URL, encodeKeys(in.Headers), id}},
		{SQL: "DELETE FROM mcp_tool_trust WHERE server_id = ?", Args: []any{id}},
	}); err != nil {
		return MCPServer{}, err
	}
	// ponytail: a failed keychain write leaves the new version with a value
	// missing (its connect fails until saved again); no rollback.
	dropped := func(oldKeys []string, now map[string]string) []string {
		return slices.DeleteFunc(slices.Clone(oldKeys), func(k string) bool { _, ok := now[k]; return ok })
	}
	err = errors.Join(s.staleMCPApprovals(ctx, id),
		s.saveMCPSecrets(id, "env", in.Env), s.saveMCPSecrets(id, "header", in.Headers),
		s.dropMCPSecrets(id, "env", dropped(old.EnvKeys, in.Env)), s.dropMCPSecrets(id, "header", dropped(old.HeaderKeys, in.Headers)))
	if err != nil {
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
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	old, err := s.mcpRow(ctx, id)
	if err != nil {
		return err
	}
	s.disconnectMCP(id, "")
	// Secrets first: if one cannot be deleted the server stays, for a retry.
	if err := errors.Join(s.dropMCPSecrets(id, "env", old.EnvKeys), s.dropMCPSecrets(id, "header", old.HeaderKeys)); err != nil {
		return err
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

// disconnectMCP closes the server's connection, if any, and takes its tools
// out of Wit runs; reason, if set, becomes its error.
func (s *Service) disconnectMCP(id int64, reason string) {
	var c *mcpConn
	s.mcpSet(id, func(st *mcpState) { c, st.conn, st.err = st.conn, nil, reason })
	s.unregisterMCPTools(id)
	if c != nil {
		c.close()
	}
}

// dropMCP disconnects c if it is still the server's connection (a call on it timed out).
func (s *Service) dropMCP(id int64, c *mcpConn, reason string) {
	s.mcpStMu.Lock()
	st := s.mcpSt[id]
	mine := st != nil && st.conn == c
	if mine {
		st.conn, st.err = nil, reason
	}
	s.mcpStMu.Unlock()
	if mine {
		s.unregisterMCPTools(id)
		c.close()
	}
}

// stopMCP closes every connection (Close).
func (s *Service) stopMCP() {
	s.mcpStMu.Lock()
	var cs []*mcpConn
	for _, st := range s.mcpSt {
		if st.conn != nil {
			cs = append(cs, st.conn)
			st.conn = nil
		}
	}
	s.mcpStMu.Unlock()
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Go(c.close)
	}
	wg.Wait()
}

// connectMCP connects to srv and lists its tools. With keep the connection
// stays and its tools join Wit runs; else it is closed. Every path that
// starts a process or opens a connection comes here, and the grant is
// checked first. The caller holds mcpMu.
func (s *Service) connectMCP(ctx context.Context, srv MCPServer, keep bool) (*mcpConn, error) {
	if !srv.Granted {
		return nil, ErrNeedsGrant
	}
	c, err := s.dialMCP(ctx, srv)
	if err != nil {
		err = s.mcpError(err)
		s.mcpSet(srv.ID, func(st *mcpState) { st.err = err.Error() })
		return nil, err
	}
	s.mcpSet(srv.ID, func(st *mcpState) { st.version, st.tools, st.err = srv.Version, c.tools, "" })
	if !keep {
		c.close()
		return c, nil
	}
	s.mcpSet(srv.ID, func(st *mcpState) { st.conn = c })
	if err := s.registerMCPTools(ctx, srv, c); err != nil {
		s.disconnectMCP(srv.ID, s.clean(err.Error()))
		return nil, err
	}
	return c, nil
}

func (s *Service) dialMCP(ctx context.Context, srv MCPServer) (*mcpConn, error) {
	env, err := s.mcpValues(srv.ID, "env", srv.EnvKeys)
	if err != nil {
		return nil, err
	}
	headers, err := s.mcpValues(srv.ID, "header", srv.HeaderKeys)
	if err != nil {
		return nil, err
	}
	c := &mcpConn{version: srv.Version}
	var t mcp.Transport
	switch {
	case mcpTestTransport != nil:
		t = mcpTestTransport(srv)
	case srv.Transport == "stdio":
		t, c.proc, err = startMCPProcess(srv, env)
	default:
		t, err = s.mcpHTTP(srv.URL, headers)
	}
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer cancel()
	fail := func(err error) (*mcpConn, error) {
		if c.session != nil {
			c.session.Close()
		}
		if c.proc != nil {
			c.proc.Close()
		}
		return nil, c.withStderr(err)
	}
	c.session, err = mcp.NewClient(&mcp.Implementation{Name: "godwit", Version: "1"}, nil).Connect(cctx, t, nil)
	if err != nil {
		return fail(err)
	}
	var tools []*mcp.Tool
	for t, err := range c.session.Tools(cctx, nil) {
		if err != nil {
			return fail(err)
		}
		tools = append(tools, t)
	}
	if c.tools, err = mcpDefs(srv.ID, tools); err != nil {
		return fail(err)
	}
	return c, nil
}

// withStderr adds the tail of a stdio server's stderr to err, cleaned
// before it is cut so no secret is shown in part.
func (c *mcpConn) withStderr(err error) error {
	if c.proc == nil {
		return err
	}
	tail := c.proc.stderr.String()
	if tail == "" {
		return err
	}
	return stderrErr{err, tail}
}

// stderrErr is an error with a stdio server's stderr; Error shows only the
// tail of it, and the Service cleans it before it leaves.
type stderrErr struct {
	err    error
	stderr string
}

func (e stderrErr) Error() string { return e.err.Error() + "; stderr: " + e.stderr }
func (e stderrErr) Unwrap() error { return e.err }

// mcpError cleans err, cutting a stderr tail to mcpStderrShown after cleaning.
func (s *Service) mcpError(err error) error {
	var se stderrErr
	if !errors.As(err, &se) {
		return s.cleanError(err)
	}
	tail := s.clean(se.stderr)
	if len(tail) > mcpStderrShown {
		tail = "…" + strings.ToValidUTF8(tail[len(tail)-mcpStderrShown:], "")
	}
	return cleanErr{s.clean(se.err.Error()) + "; stderr: " + tail, err}
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

// mcpLive is the server's connection, connecting it if it is enabled and granted.
func (s *Service) mcpLive(ctx context.Context, id int64) (*mcpConn, error) {
	if c := s.mcpGet(id).conn; c != nil {
		return c, nil
	}
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	if c := s.mcpGet(id).conn; c != nil {
		return c, nil
	}
	srv, err := s.mcpRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if !srv.Enabled {
		return nil, fmt.Errorf("MCP server %s is disabled", srv.Name)
	}
	return s.connectMCP(ctx, srv, true)
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
			if runOf(ctx).scope.ReadMail() { // read since the gate allowed it: fail closed
				return nil, errors.New(mcpUntrusted(srv.Name, "not run: this run has read mail, so the call needs approval"))
			}
			text, err := s.callMCP(ctx, srv.ID, tool, hash, args)
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
		Run: func(ctx context.Context, a Approval) (any, error) { return s.callMCP(ctx, srv.ID, tool, hash, a.Args) },
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

// callMCP calls the server's tool if its definition is still hash and
// returns its text content, cleaned and capped at maxMCPResult; a result
// marked as an error is an error. A call that times out stops the server.
func (s *Service) callMCP(ctx context.Context, id int64, tool, hash string, args json.RawMessage) (string, error) {
	c, err := s.mcpLive(ctx, id)
	if err != nil {
		return "", s.mcpError(err)
	}
	if d := c.def(tool); d == nil || d.DefinitionHash != hash {
		return "", fmt.Errorf("the MCP tool %q changed; refresh the server's tools", s.pluginText(tool))
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	rctx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer cancel()
	res, err := c.session.CallTool(rctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		if errors.Is(rctx.Err(), context.DeadlineExceeded) {
			s.dropMCP(id, c, "stopped after a call timed out; refresh to start it again")
		}
		return "", s.mcpError(c.withStderr(err))
	}
	text := s.clean(mcpText(res))
	if res.IsError {
		return "", errors.New(text)
	}
	return text, nil
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
		if b.Len() > maxMCPResult {
			break
		}
	}
	text := b.String()
	if len(text) > maxMCPResult {
		text = strings.ToValidUTF8(text[:maxMCPResult], "") + "\n[cut at 64 KB]"
	}
	return text
}

// mcpHTTP is the Streamable HTTP transport to rawURL: the plugin network's
// IP and proxy rules (loopback hosts dial loopback only), redirects only
// within the origin, and the headers sent to that origin only.
func (s *Service) mcpHTTP(rawURL string, headers map[string]string) (mcp.Transport, error) {
	u, err := mcpURL(rawURL)
	if err != nil {
		return nil, err
	}
	tr := plugins.NewClient(s.pluginNet).Transport()
	if loopbackHost(u.Hostname()) {
		tr.DialContext = dialLoopback
	}
	same := func(v *url.URL) bool { return v.Scheme == u.Scheme && plugins.HostPort(v) == plugins.HostPort(u) }
	client := &http.Client{
		Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
			if same(r.URL) && len(headers) > 0 {
				r = r.Clone(r.Context())
				for k, v := range headers {
					r.Header.Set(k, v)
				}
			}
			return tr.RoundTrip(r)
		}),
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if !same(r.URL) || len(via) >= 5 {
				return errors.New("redirect refused")
			}
			return nil
		},
	}
	return &mcp.StreamableClientTransport{Endpoint: u.String(), HTTPClient: client, DisableStandaloneSSE: true}, nil
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// dialLoopback dials a loopback address only ("localhost" is 127.0.0.1).
func dialLoopback(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, errors.New("not a loopback address")
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
}

// mcpProc is a stdio server's process, in its own process group; it is the
// transport's writer, so closing the connection closes it.
type mcpProc struct {
	stdin  *os.File
	stderr *tailBuffer
	kill   func() // the whole group
	exited chan struct{}
	once   sync.Once
}

func (p *mcpProc) Write(b []byte) (int, error) { return p.stdin.Write(b) }

// Close closes stdin, gives the server mcpGrace to exit, then kills its
// process group (children it left too) and reaps it.
func (p *mcpProc) Close() error {
	p.once.Do(func() {
		p.stdin.Close()
		select {
		case <-p.exited:
		case <-time.After(mcpGrace):
		}
		p.kill()
		<-p.exited
	})
	return nil
}

// startMCPProcess starts srv's command with only PATH and its Env in its
// environment. The user granted running it: it is not sandboxed.
func startMCPProcess(srv MCPServer, env map[string]string) (mcp.Transport, *mcpProc, error) {
	cmd := exec.Command(srv.Command, srv.Args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	p := &mcpProc{stderr: &tailBuffer{max: maxMCPStderr}, exited: make(chan struct{})}
	cmd.Stderr = p.stderr
	setProcessGroup(cmd)
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return nil, nil, err
	}
	cmd.Stdin, cmd.Stdout, p.stdin = inR, outW, inW
	err = cmd.Start()
	inR.Close() // the child's ends
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		return nil, nil, err
	}
	p.kill = groupKiller(cmd)
	go func() { cmd.Wait(); close(p.exited) }()
	return &mcp.IOTransport{Reader: outR, Writer: p}, p, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.ToValidUTF8(string(t.b), "")
}
