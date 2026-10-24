package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP connections: connecting (grant first), the stdio and http transports, closing.

// mcpClientOptions take forms (elicitation) only as input_required results,
// which callMCP turns into approvals (mcp_question.go): with no handler, a
// form sent in the middle of a call (protocol before 2026-07-28) is refused.
// No roots: Godwit has none to offer.
var mcpClientOptions = &mcp.ClientOptions{
	Capabilities:   &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}},
	MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
}

// mcpTestTransport, set in tests, connects to an in-memory server instead.
var mcpTestTransport func(MCPServer) mcp.Transport

// mcpConn is a live connection to a server version; proc is its stdio process.
type mcpConn struct {
	version int64
	session *mcp.ClientSession
	proc    *mcpProc
	tools   []mcpDef
	life    context.Context // ends at close: calls in flight end with it
	stop    context.CancelFunc
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

// close ends the calls in flight (the session's Close waits for them) and
// the session; a stdio process gets mcpGrace after stdin closes, then its
// process group is killed and reaped.
func (c *mcpConn) close() {
	c.stop()
	c.session.Close()
	if c.proc != nil {
		c.proc.Close()
	}
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
	c.life, c.stop = context.WithCancel(context.Background())
	var t mcp.Transport
	switch {
	case mcpTestTransport != nil:
		t = mcpTestTransport(srv)
	case srv.Transport == "stdio":
		t, c.proc, err = startMCPProcess(srv, env)
	default:
		t, err = s.mcpHTTP(srv, headers)
	}
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, mcpCallTimeout)
	defer cancel()
	fail := func(err error) (*mcpConn, error) {
		c.stop()
		if c.session != nil {
			c.session.Close()
		}
		if c.proc != nil {
			c.proc.Close()
		}
		return nil, c.withStderr(err)
	}
	c.session, err = mcp.NewClient(&mcp.Implementation{Name: "godwit", Version: "1"}, mcpClientOptions).Connect(cctx, t, nil)
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

// mcpHTTP is the Streamable HTTP transport to srv's URL: the plugin
// network's IP and proxy rules (loopback hosts dial loopback only),
// redirects only within the origin, the headers sent to that origin only,
// and every request and refused redirect audited (host:port only).
func (s *Service) mcpHTTP(srv MCPServer, headers map[string]string) (mcp.Transport, error) {
	u, err := mcpURL(srv.URL)
	if err != nil {
		return nil, err
	}
	tr := plugins.NewClient(s.pluginNet).Transport()
	if loopbackHost(u.Hostname()) {
		tr.DialContext = dialLoopback
	}
	audit := func(ctx context.Context, dest, decision, reason string) error {
		callID, _ := ctx.Value(mcpCallKey{}).(string)
		return s.gateway().Record(context.WithoutCancel(ctx), gateway.Entry{Source: gateway.SourceMCP,
			Extension: strconv.FormatInt(srv.ID, 10), Tool: "mcp:" + srv.Name + ":http", CallID: callID,
			Destinations: []string{dest}, Decision: decision, Reason: reason})
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
			resp, err := tr.RoundTrip(r)
			decision, reason := "allow", ""
			var ne *plugins.NetError
			switch {
			case err == nil:
			case errors.As(err, &ne):
				decision, reason = "deny", ne.Reason
			case r.Context().Err() != nil:
				decision, reason = "error", "cancelled"
			default:
				decision, reason = "error", plugins.FailReason(err)
			}
			if aerr := audit(r.Context(), plugins.HostPort(r.URL), decision, reason); aerr != nil { // never unaudited
				if resp != nil {
					resp.Body.Close()
				}
				return nil, errors.Join(err, aerr)
			}
			return resp, err
		}),
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			reason := ""
			switch {
			case !same(r.URL):
				reason = "host_not_allowed"
			case len(via) >= 5:
				reason = "too_many_redirects"
			default:
				return nil
			}
			return errors.Join(errors.New("redirect refused"), audit(r.Context(), plugins.HostPort(r.URL), "deny", reason))
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
		return nil, &plugins.NetError{Reason: "private_address", Host: addr}
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
}
