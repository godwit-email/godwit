package plugins

// The plugin-host side of a native plugin (spec 4.5): the child process
// `godwit plugin-host` checks the library's SHA-256 before anything loads it
// (loading runs its initializers), loads it, and serves JSON lines on stdio.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// ABI request and response caps (spec 4.4).
const (
	maxNativeRequest  = 1 << 20
	maxNativeResponse = 4 << 20
)

// NativeCall calls a loaded library: it returns the plugin's response
// envelope as is; an error is a failure below the envelope.
type NativeCall func(method string, req []byte) ([]byte, error)

// NativeOpener loads the library at path, speaking hostABI.
type NativeOpener func(path string, hostABI uint32) (NativeCall, error)

// ServeNative is `godwit plugin-host`. Exit codes: 3 the file is not the one
// granted (nothing was loaded), 4 it did not load or init, 0 stdin closed.
func ServeNative(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return ServeNativeWith(args, stdin, stdout, stderr, openNative)
}

// ServeNativeWith is ServeNative with another loader (tests).
func ServeNativeWith(args []string, stdin io.Reader, stdout, stderr io.Writer, open NativeOpener) int {
	fs := flag.NewFlagSet("plugin-host", flag.ContinueOnError)
	fs.SetOutput(stderr)
	lib := fs.String("library", "", "absolute path of the shared library")
	want := fs.String("sha256", "", "SHA-256 the user or the plugin directory allowed")
	abi := fs.Uint("abi", uint(NativeABI), "")
	if fs.Parse(args) != nil {
		return 2
	}
	got, err := FileSHA256(*lib)
	if err != nil || got != *want {
		fmt.Fprintf(stderr, "DENIED_BEFORE_DLOPEN: %s is not the granted file\n", *lib)
		return 3
	}
	// shortcut: hashes then opens by path, so a swap in between loads the new file; the
	// version directory is Godwit's own and written once, upgrade if that stops holding.
	call, err := open(*lib, uint32(*abi))
	if err != nil {
		fmt.Fprintln(stderr, "LOAD_FAILED:", err)
		return 4
	}
	var mu sync.Mutex
	out := bufio.NewWriter(stdout)
	write := func(b []byte) {
		mu.Lock()
		defer mu.Unlock()
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
	}
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 64<<10), maxNativeRequest+64<<10)
	var wg sync.WaitGroup
	for sc.Scan() {
		line := bytes.Clone(sc.Bytes())
		wg.Go(func() { write(serveLine(call, line)) })
	}
	wg.Wait()
	return 0
}

// FileSHA256 is the hex SHA-256 of a file's contents.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func serveLine(call NativeCall, line []byte) []byte {
	var req struct {
		ID     int64           `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return hostErrorLine(0, "bad request line")
	}
	if len(req.Params) > maxNativeRequest {
		return hostErrorLine(req.ID, "request too large")
	}
	env, err := call(req.Method, req.Params)
	if err == nil {
		err = checkEnvelope(env)
	}
	if err != nil {
		return hostErrorLine(req.ID, err.Error())
	}
	// env is a JSON object: put the id in front of its fields.
	return append([]byte(`{"id":`+strconv.FormatInt(req.ID, 10)+`,`), env[1:]...)
}

func checkEnvelope(env []byte) error {
	var probe struct {
		OK *bool `json:"ok"`
	}
	if len(env) == 0 || len(env) > maxNativeResponse || env[0] != '{' || json.Unmarshal(env, &probe) != nil || probe.OK == nil {
		return errors.New("the plugin returned an invalid envelope")
	}
	return nil
}

func hostErrorLine(id int64, msg string) []byte {
	b, _ := json.Marshal(map[string]any{"id": id, "ok": false, "host_error": msg})
	return b
}

// The C structs of spec 4.4, as Go lays them out on 64-bit platforms.
type cBuffer struct {
	Ptr unsafe.Pointer
	Len uintptr
}

type cAPI struct {
	ABI  uint32
	Call uintptr
	Free uintptr
}

// openNative loads a library with purego (no cgo) and binds its function table.
func openNative(path string, hostABI uint32) (NativeCall, error) {
	h, err := openLibrary(path)
	if err != nil {
		return nil, err
	}
	sym, err := librarySymbol(h, "godwit_plugin_init")
	if err != nil {
		return nil, errors.New("missing godwit_plugin_init")
	}
	var initFn func(hostABI uint32, api *cAPI) int32
	purego.RegisterFunc(&initFn, sym)
	api := new(cAPI)
	if rc := initFn(hostABI, api); rc != 0 || api.ABI != NativeABI || api.Call == 0 || api.Free == 0 {
		return nil, fmt.Errorf("incompatible ABI (init returned %d, abi %d)", rc, api.ABI)
	}
	var callFn func(method *byte, req *byte, n uintptr, out *cBuffer) int32
	var freeFn func(p unsafe.Pointer, n uintptr)
	purego.RegisterFunc(&callFn, api.Call)
	purego.RegisterFunc(&freeFn, api.Free)
	return func(method string, req []byte) ([]byte, error) {
		m := append([]byte(method), 0)
		var p *byte
		if len(req) > 0 {
			p = &req[0]
		}
		var buf cBuffer
		rc := callFn(&m[0], p, uintptr(len(req)), &buf)
		if buf.Ptr != nil {
			defer freeFn(buf.Ptr, buf.Len)
		}
		if rc != 0 || buf.Ptr == nil || buf.Len == 0 || buf.Len > maxNativeResponse {
			return nil, fmt.Errorf("ABI call failed (rc=%d, %d bytes)", rc, buf.Len)
		}
		return bytes.Clone(unsafe.Slice((*byte)(buf.Ptr), buf.Len)), nil
	}, nil
}
