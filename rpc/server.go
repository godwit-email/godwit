package rpc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// File names under the data directory.
const (
	SocketName  = "godwit.sock"
	UITokenName = "ui-token"
	TokenName   = "api-token"
)

// ServerOptions configures a Server. Zero fields take the defaults.
type ServerOptions struct {
	Conn     ConnOptions // per connection; Hello and EncodeError are set by the Server
	MaxConns int         // simultaneous connections, 16
	Version  string      // hello answers "godwit/<Version>"
	// UIStarted: the interface started this backend; hello tells the ui role
	// (Hello.UIStarted), which can then replace it on a version change.
	UIStarted bool
	// DisableExternal starts the Server with external access (RoleFull,
	// RoleMCP and api-token) off until SetExternal(true) turns it on.
	DisableExternal bool
	// Codec encodes errors (nil: every error is "internal error", whose
	// message for RoleUI is the error's own text). Every
	// string it sends, and every error logged, has the UI and API tokens and
	// then whatever Redact (nil = nothing more) finds replaced.
	Codec  *Codec
	Redact func(string) string
	// Audit, if set, runs before an authorized call is dispatched, with the
	// connection's role. An error stops the call and is answered and logged
	// like any error the call could have returned.
	Audit func(role, method string, params []json.RawMessage) error
	// UIDispatcher, if set, is the Dispatcher for RoleUI connections; nil uses d.
	UIDispatcher *Dispatcher
	// Context, if set, wraps an authenticated connection's request context for
	// its role.
	Context func(ctx context.Context, role string) context.Context
}

// Server serves a Dispatcher on <dataDir>/godwit.sock.
type Server struct {
	d           *Dispatcher
	opts        ServerOptions
	ln          net.Listener
	path        string
	uiTokenPath string
	uiToken     string
	tokenPath   string
	ctx         context.Context
	cancel      context.CancelFunc

	closeOnce sync.Once
	closeDone chan struct{} // closed when Close's cleanup finished
	closeErr  error

	mu       sync.Mutex
	closed   bool
	external bool
	apiToken string
	extConns map[chan struct{}]*conn
	conns    int
	wg       sync.WaitGroup
}

// authError is a hello failure, sent as it is.
type authError struct{ we *WireError }

func (e authError) Error() string { return e.we.Message }

func unauthenticated(msg string) error {
	return authError{&WireError{Code: CodeUnauthenticated, Message: msg, Data: &WireErrorData{Code: "unauthenticated"}}}
}

// maxSocketPath is the longest Unix socket path every platform takes
// (sun_path is 104 bytes on macOS and the BSDs, 108 on Linux).
const maxSocketPath = 103

// Listen listens on <dataDir>/godwit.sock (0600 on Unix), writes a fresh UI
// token to <dataDir>/ui-token (0600), and unless opts.DisableExternal is set,
// writes a fresh external token to <dataDir>/api-token (0600). The caller must
// already hold the data directory's owner lock (package ownerlock): that is
// what makes an existing socket file a leftover, which Listen removes. Any
// other file there is an error and stays.
func Listen(dataDir string, d *Dispatcher, opts ServerOptions) (*Server, error) {
	if opts.MaxConns <= 0 {
		opts.MaxConns = 16
	}
	path := filepath.Join(dataDir, SocketName)
	if runtime.GOOS != "windows" && len(path) > maxSocketPath {
		return nil, fmt.Errorf("rpc: socket path %s is %d bytes, longer than the %d the system allows; use a shorter data directory", path, len(path), maxSocketPath)
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("rpc: %s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	s := &Server{
		d:           d,
		opts:        opts,
		ln:          ln,
		path:        path,
		uiTokenPath: filepath.Join(dataDir, UITokenName),
		tokenPath:   filepath.Join(dataDir, TokenName),
		extConns:    map[chan struct{}]*conn{},
		closeDone:   make(chan struct{}),
	}
	if runtime.GOOS != "windows" {
		err = os.Chmod(path, 0o600)
	}
	if err == nil {
		s.uiToken, err = writeToken(s.uiTokenPath)
	}
	if err == nil {
		if opts.DisableExternal {
			err = removeFile(s.tokenPath)
		} else {
			s.apiToken, err = writeToken(s.tokenPath)
			s.external = err == nil
		}
	}
	if err != nil {
		ln.Close()
		_ = removeFile(s.uiTokenPath)
		_ = removeFile(s.tokenPath)
		return nil, err
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	return s, nil
}

// writeToken writes 32 random bytes as hex, atomically, 0600.
func writeToken(path string) (string, error) {
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*") // 0600
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(tok)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return tok, nil
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

var errExternalOff = errors.New("rpc: external access turned off")

// SetExternal enables or disables external access (RoleFull, RoleMCP and
// <dataDir>/api-token). Turning it off removes api-token and drops active
// external connections while keeping RoleUI connections open.
func (s *Server) SetExternal(on bool) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if on {
		if s.external {
			s.mu.Unlock()
			return nil
		}
		tok, err := writeToken(s.tokenPath)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		s.apiToken = tok
		s.external = true
		s.mu.Unlock()
		return nil
	}
	err := removeFile(s.tokenPath)
	s.external = false
	drop := maps.Clone(s.extConns)
	clear(s.extConns)
	s.mu.Unlock()
	for done, c := range drop {
		c.kill(errExternalOff)
		<-done
	}
	return err
}

// Path is the socket's path.
func (s *Server) Path() string { return s.path }

// Serve accepts connections until Close or until ctx ends, which closes the
// Server. It returns nil once closed and Close's cleanup has finished.
func (s *Server) Serve(ctx context.Context) error {
	defer context.AfterFunc(ctx, func() { s.Close() })()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				<-s.closeDone
				return s.closeErr
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return err
		}
		s.mu.Lock()
		if s.closed || s.conns >= s.opts.MaxConns {
			s.mu.Unlock()
			c.Close()
			continue
		}
		s.conns++
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			done := make(chan struct{})
			ServeConn(s.ctx, c, s.d, s.connOptions(done))
			s.mu.Lock()
			s.conns--
			delete(s.extConns, done)
			s.mu.Unlock()
			close(done)
		}()
	}
}

// Close stops accepting, drops every connection, waits for their streams to
// drain, and removes the socket and token files. Every call, concurrent ones
// included, returns once that has finished, with the same result.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.external = false
		clear(s.extConns)
		s.mu.Unlock()
		err := s.ln.Close()
		s.cancel()
		s.wg.Wait()
		for _, p := range []string{s.path, s.uiTokenPath, s.tokenPath} {
			if rerr := removeFile(p); rerr != nil {
				err = errors.Join(err, rerr)
			}
		}
		s.closeErr = err
		close(s.closeDone)
	})
	<-s.closeDone
	return s.closeErr
}

// redact removes the UI and API tokens, then whatever opts.Redact finds.
func (s *Server) redact(x string) string {
	if s.uiToken != "" {
		x = strings.ReplaceAll(x, s.uiToken, "[redacted]")
	}
	s.mu.Lock()
	apiTok := s.apiToken
	s.mu.Unlock()
	if apiTok != "" {
		x = strings.ReplaceAll(x, apiTok, "[redacted]")
	}
	if s.opts.Redact != nil {
		x = s.opts.Redact(x)
	}
	return x
}

// encode answers err: a hello failure as it is, anything else through the
// Codec; an unregistered error is logged, redacted.
func (s *Server) encode(err error) *WireError {
	var ae authError
	if errors.As(err, &ae) {
		return ae.we
	}
	we := internalError()
	if s.opts.Codec != nil {
		we = s.opts.Codec.Encode(err, s.redact)
	}
	if we.Code == CodeInternal {
		slog.Error("rpc: internal error", "err", s.redact(err.Error()))
	}
	return we
}

// encodeUI is encode for RoleUI, Godwit's own interface: an unregistered
// error keeps its redacted text, which the interface shows as it would in
// process.
func (s *Server) encodeUI(err error) *WireError {
	we := s.encode(err)
	if we.Code == CodeInternal {
		we.Message = s.redact(err.Error())
	}
	return we
}

func (s *Server) connOptions(done chan struct{}) ConnOptions {
	co := s.opts.Conn
	co.EncodeError = s.encode
	co.Hello = nil
	co.helloCtx = func(ctx context.Context, c *conn, params []json.RawMessage) (func(string, []json.RawMessage) error, context.Context, any, error) {
		return s.hello(ctx, c, done, params)
	}
	return co
}

// hello takes [token, role, client].
func (s *Server) hello(ctx context.Context, c *conn, done chan struct{}, params []json.RawMessage) (func(string, []json.RawMessage) error, context.Context, any, error) {
	var token, role, client string
	if len(params) != 3 || json.Unmarshal(params[0], &token) != nil || json.Unmarshal(params[1], &role) != nil ||
		json.Unmarshal(params[2], &client) != nil {
		return nil, nil, nil, unauthenticated("hello takes [token, role, client]")
	}
	var roleOK func(string, []json.RawMessage) bool
	switch role {
	case RoleUI:
		if s.uiToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.uiToken)) != 1 {
			return nil, nil, nil, unauthenticated("bad token")
		}
		roleOK = func(string, []json.RawMessage) bool { return true }
		if s.opts.UIDispatcher != nil {
			c.d = s.opts.UIDispatcher
		}
		if s.opts.Conn.MaxActive <= 0 {
			c.opts.MaxActive = 256
		}
		c.opts.EncodeError = s.encodeUI
	case RoleFull, RoleMCP:
		s.mu.Lock()
		ok := !s.closed && s.external && s.apiToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.apiToken)) == 1
		if ok && done != nil {
			s.extConns[done] = c
		}
		s.mu.Unlock()
		if !ok {
			return nil, nil, nil, unauthenticated("bad token")
		}
		roleOK = func(string, []json.RawMessage) bool { return true }
		if role == RoleMCP {
			roleOK = MCPAllowed
		}
	default:
		return nil, nil, nil, unauthenticated("role must be ui, full or mcp")
	}
	if s.opts.Context != nil {
		if w := s.opts.Context(ctx, role); w != nil {
			ctx = w
		}
	}
	allow := func(method string, params []json.RawMessage) error {
		if !roleOK(method, params) {
			return ErrForbidden
		}
		if s.opts.Audit != nil {
			return s.opts.Audit(role, method, params)
		}
		return nil
	}
	result := map[string]any{"server": "godwit/" + s.opts.Version, "role": role}
	if role == RoleUI && s.opts.UIStarted {
		result["uiStarted"] = true
	}
	return allow, ctx, result, nil
}

// Dial connects to the Server in dataDir with the token for role and says
// hello as role. codec (nil = none) decodes error responses.
func Dial(dataDir, role, client string, codec *Codec) (*Client, error) {
	tokenFile := TokenName
	if role == RoleUI {
		tokenFile = UITokenName
	}
	tok, err := os.ReadFile(filepath.Join(dataDir, tokenFile))
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("unix", filepath.Join(dataDir, SocketName))
	if err != nil {
		return nil, err
	}
	c := NewClient(conn)
	if codec != nil {
		c.DecodeError = codec.Decode
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Call(ctx, "hello", &c.hello, string(tok), role, client); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
