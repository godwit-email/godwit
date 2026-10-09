package rpc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// File names under the data directory.
const (
	SocketName = "godwit.sock"
	TokenName  = "api-token"
)

// ServerOptions configures a Server. Zero fields take the defaults.
type ServerOptions struct {
	Conn     ConnOptions // per connection; Hello is set by the Server
	MaxConns int         // simultaneous connections, 16
	Version  string      // hello answers "godwit/<Version>"
	// Audit, if set, runs before an authorized call is dispatched, with the
	// connection's role; an error refuses the call as forbidden.
	Audit func(role, method string, params []json.RawMessage) error
}

// Server serves a Dispatcher on <dataDir>/godwit.sock.
type Server struct {
	d         *Dispatcher
	opts      ServerOptions
	ln        net.Listener
	path      string
	tokenPath string
	token     string
	ctx       context.Context
	cancel    context.CancelFunc

	mu     sync.Mutex
	closed bool
	conns  int
	wg     sync.WaitGroup
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

// Listen listens on <dataDir>/godwit.sock (0600 on Unix) and writes a fresh
// token to <dataDir>/api-token (0600). The caller must already hold the data
// directory's owner lock (package ownerlock): that is what makes an existing
// socket file a leftover, which Listen removes. Any other file there is an
// error and stays.
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
	s := &Server{d: d, opts: opts, ln: ln, path: path, tokenPath: filepath.Join(dataDir, TokenName)}
	if runtime.GOOS != "windows" {
		err = os.Chmod(path, 0o600)
	}
	if err == nil {
		s.token, err = writeToken(s.tokenPath)
	}
	if err != nil {
		ln.Close()
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
	f, err := os.CreateTemp(filepath.Dir(path), "."+TokenName+"-*") // 0600
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

// Path is the socket's path.
func (s *Server) Path() string { return s.path }

// Serve accepts connections until Close or until ctx ends, which closes the
// Server. It returns nil once closed.
func (s *Server) Serve(ctx context.Context) error {
	defer context.AfterFunc(ctx, func() { s.Close() })()
	co := s.connOptions()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
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
			ServeConn(s.ctx, c, s.d, co)
			s.mu.Lock()
			s.conns--
			s.mu.Unlock()
		}()
	}
}

// Close stops accepting, drops every connection, waits for their streams to
// drain, and removes the socket and token files.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	err := s.ln.Close()
	s.cancel()
	s.wg.Wait()
	for _, p := range []string{s.path, s.tokenPath} {
		if rerr := os.Remove(p); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
	}
	return err
}

func (s *Server) connOptions() ConnOptions {
	co := s.opts.Conn
	enc := co.withDefaults().EncodeError
	co.EncodeError = func(err error) *WireError {
		var ae authError
		if errors.As(err, &ae) {
			return ae.we
		}
		return enc(err)
	}
	co.Hello = s.hello
	return co
}

// hello takes [token, role, client].
func (s *Server) hello(params []json.RawMessage) (func(string, []json.RawMessage) bool, any, error) {
	var token, role, client string
	if len(params) != 3 || json.Unmarshal(params[0], &token) != nil || json.Unmarshal(params[1], &role) != nil ||
		json.Unmarshal(params[2], &client) != nil {
		return nil, nil, unauthenticated("hello takes [token, role, client]")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) != 1 {
		return nil, nil, unauthenticated("bad token")
	}
	var roleOK func(string, []json.RawMessage) bool
	switch role {
	case RoleFull:
		roleOK = func(string, []json.RawMessage) bool { return true }
	case RoleMCP:
		roleOK = MCPAllowed
	default:
		return nil, nil, unauthenticated("role must be full or mcp")
	}
	allow := func(method string, params []json.RawMessage) bool {
		if !roleOK(method, params) {
			return false
		}
		return s.opts.Audit == nil || s.opts.Audit(role, method, params) == nil
	}
	return allow, map[string]string{"server": "godwit/" + s.opts.Version, "role": role}, nil
}

// Dial connects to the Server in dataDir with the token it wrote and says
// hello as role. codec (nil = none) decodes error responses.
func Dial(dataDir, role, client string, codec *Codec) (*Client, error) {
	tok, err := os.ReadFile(filepath.Join(dataDir, TokenName))
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
	if err := c.Call(ctx, "hello", nil, string(tok), role, client); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
