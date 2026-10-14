package plugins

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildEcho compiles testdata/native/echo.c into a shared library with the
// system C compiler (no cgo involved), and returns its path and SHA-256.
func buildEcho(t *testing.T) (string, string) {
	t.Helper()
	cc, err := exec.LookPath("cc")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("no cc to build the test library")
	}
	lib := filepath.Join(t.TempDir(), LibraryName())
	shared := []string{"-shared", "-fPIC"}
	if runtime.GOOS == "darwin" {
		shared = []string{"-dynamiclib"}
	}
	args := append(shared, "-I", filepath.Join("..", "..", "sdk", "native"), "-o", lib, filepath.Join("testdata", "native", "echo.c"))
	if out, err := exec.Command(cc, args...).CombinedOutput(); err != nil {
		t.Fatalf("cc: %v\n%s", err, out)
	}
	b, err := os.ReadFile(lib)
	require.NoError(t, err)
	sum := sha256.Sum256(b)
	return lib, hex.EncodeToString(sum[:])
}

func TestServeNativeDeniesBeforeLoad(t *testing.T) {
	lib, _ := buildEcho(t)
	marker := filepath.Join(t.TempDir(), "marker")
	t.Setenv("ECHO_MARKER", marker)
	var out, errw bytes.Buffer
	code := ServeNative([]string{"--library", lib, "--sha256", strings.Repeat("0", 64)}, strings.NewReader(""), &out, &errw)
	assert.Equal(t, 3, code)
	assert.Contains(t, errw.String(), "DENIED_BEFORE_DLOPEN")
	assert.NoFileExists(t, marker, "the library's constructor must not run")
}

func TestServeNativeLoadsAndServes(t *testing.T) {
	lib, sha := buildEcho(t)
	marker := filepath.Join(t.TempDir(), "marker")
	t.Setenv("ECHO_MARKER", marker)
	in := `{"id":1,"method":"describe","params":{}}` + "\n" + `{"id":2,"method":"nope","params":{}}` + "\n"
	var out, errw bytes.Buffer
	code := ServeNative([]string{"--library", lib, "--sha256", sha}, strings.NewReader(in), &out, &errw)
	require.Equal(t, 0, code, errw.String())
	assert.FileExists(t, marker)
	got := map[int64]map[string]any{}
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var line map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &line))
		got[int64(line["id"].(float64))] = line
	}
	require.Len(t, got, 2)
	assert.Equal(t, true, got[1]["ok"])
	assert.Equal(t, "echo", got[1]["result"].(map[string]any)["name"])
	assert.Equal(t, false, got[2]["ok"])
	assert.Equal(t, "invalid_request", got[2]["error"].(map[string]any)["code"])
}

func TestServeNativeABIMismatch(t *testing.T) {
	lib, sha := buildEcho(t)
	var out, errw bytes.Buffer
	code := ServeNative([]string{"--library", lib, "--sha256", sha, "--abi", "2"}, strings.NewReader(""), &out, &errw)
	assert.Equal(t, 4, code)
	assert.Contains(t, errw.String(), "incompatible ABI")
}

func TestServeNativeHostErrorLine(t *testing.T) {
	lib := filepath.Join(t.TempDir(), LibraryName())
	require.NoError(t, os.WriteFile(lib, []byte("x"), 0o600))
	sum := sha256.Sum256([]byte("x"))
	broken := func(string, uint32) (NativeCall, error) {
		return func(string, []byte) ([]byte, error) { return nil, errors.New("rc=-1") }, nil
	}
	var out, errw bytes.Buffer
	code := ServeNativeWith([]string{"--library", lib, "--sha256", hex.EncodeToString(sum[:])},
		strings.NewReader(`{"id":7,"method":"describe","params":{}}`+"\n"), &out, &errw, broken)
	require.Equal(t, 0, code)
	var line struct {
		ID        int64  `json:"id"`
		OK        bool   `json:"ok"`
		HostError string `json:"host_error"`
	}
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(out.Bytes()), &line))
	assert.Equal(t, int64(7), line.ID)
	assert.False(t, line.OK)
	assert.Contains(t, line.HostError, "rc=-1")
}
