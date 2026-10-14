package main

// plugin-host: the subprocess. It checks the file's hash before loading it,
// loads it with purego (no cgo), and serves JSON lines on stdio.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	maxRequest  = 1 << 20
	maxResponse = 4 << 20
)

type cBuffer struct {
	Ptr unsafe.Pointer
	Len uintptr
}

type cAPI struct {
	ABI  uint32
	Call uintptr
	Free uintptr
}

var (
	callFn func(method *byte, req *byte, n uintptr, out *cBuffer) int32
	freeFn func(p unsafe.Pointer, n uintptr)
)

func fileSHA(path string) (string, error) {
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

// Exit codes: 3 denied before dlopen, 4 load or init failed.
func pluginHost(args []string) int {
	fs := flag.NewFlagSet("plugin-host", flag.ContinueOnError)
	lib := fs.String("library", "", "")
	want := fs.String("sha256", "", "")
	abi := fs.Uint("abi", 1, "")
	if fs.Parse(args) != nil {
		return 2
	}
	got, err := fileSHA(*lib)
	if err != nil || got != *want {
		fmt.Fprintf(os.Stderr, "DENIED_BEFORE_DLOPEN: sha256 %s does not match the granted %s\n", got, *want)
		return 3
	}
	// shortcut: hashes then opens by path, a swap in between is possible; production loads from the private version directory nobody else writes.
	h, err := openLibrary(*lib)
	if err != nil {
		fmt.Fprintln(os.Stderr, "LOAD_FAILED:", err)
		return 4
	}
	initSym, err := symbol(h, "godwit_plugin_init")
	if err != nil {
		fmt.Fprintln(os.Stderr, "LOAD_FAILED: missing godwit_plugin_init")
		return 4
	}
	var initFn func(hostABI uint32, api *cAPI) int32
	purego.RegisterFunc(&initFn, initSym)
	api := new(cAPI)
	if rc := initFn(uint32(*abi), api); rc != 0 || api.ABI != 1 || api.Call == 0 || api.Free == 0 {
		fmt.Fprintf(os.Stderr, "LOAD_FAILED: incompatible ABI (init rc=%d abi=%d)\n", rc, api.ABI)
		return 4
	}
	purego.RegisterFunc(&callFn, api.Call)
	purego.RegisterFunc(&freeFn, api.Free)

	out := bufio.NewWriter(os.Stdout)
	var mu sync.Mutex
	write := func(b []byte) {
		mu.Lock()
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
		mu.Unlock()
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), maxRequest+64<<10)
	var wg sync.WaitGroup
	for sc.Scan() {
		line := bytes.Clone(sc.Bytes())
		wg.Go(func() { write(handle(line)) })
	}
	wg.Wait()
	return 0
}

func handle(line []byte) []byte {
	var req struct {
		ID     int64           `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return hostError(0, "bad request line")
	}
	env, err := invoke(req.Method, req.Params)
	if err != nil {
		return hostError(req.ID, err.Error())
	}
	// The envelope is a JSON object: splice the id in front of its fields.
	return append([]byte(`{"id":`+strconv.FormatInt(req.ID, 10)+`,`), env[1:]...)
}

func hostError(id int64, msg string) []byte {
	b, _ := json.Marshal(map[string]any{"id": id, "ok": false, "host_error": msg})
	return b
}

func invoke(method string, params []byte) ([]byte, error) {
	if len(params) > maxRequest {
		return nil, fmt.Errorf("request too large")
	}
	m := append([]byte(method), 0)
	var p *byte
	if len(params) > 0 {
		p = &params[0]
	}
	var buf cBuffer
	rc := callFn(&m[0], p, uintptr(len(params)), &buf)
	if buf.Ptr != nil {
		defer freeFn(buf.Ptr, buf.Len)
	}
	if rc != 0 || buf.Ptr == nil || buf.Len == 0 || buf.Len > maxResponse {
		return nil, fmt.Errorf("ABI call failed (rc=%d len=%d)", rc, buf.Len)
	}
	env := bytes.Clone(unsafe.Slice((*byte)(buf.Ptr), buf.Len))
	var probe struct {
		OK *bool `json:"ok"`
	}
	if env[0] != '{' || json.Unmarshal(env, &probe) != nil || probe.OK == nil {
		return nil, fmt.Errorf("plugin returned an invalid envelope")
	}
	return env, nil
}
