package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakeNativeEnv = "GODWIT_PLUGINS_FAKE_NATIVE"

// TestMain: the test binary is also a fake plugin-host for NativeProcess.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "plugin-host" && os.Getenv(fakeNativeEnv) != "" {
		os.Exit(ServeNativeWith(os.Args[2:], os.Stdin, os.Stdout, os.Stderr, FakeNativeOpener(os.Stderr)))
	}
	os.Exit(m.Run())
}

// fakeLib writes a fake library (FakeNativeOpener's JSON) and returns its path and SHA-256.
func fakeLib(t *testing.T, spec map[string]any) (string, string) {
	t.Helper()
	if spec["describe"] == nil {
		spec["describe"] = map[string]any{"name": "fake", "version": "1", "abi": 1, "capabilities": []string{"translate"}}
	}
	b, err := json.Marshal(spec)
	require.NoError(t, err)
	lib := filepath.Join(t.TempDir(), LibraryName())
	require.NoError(t, os.WriteFile(lib, b, 0o600))
	sum := sha256.Sum256(b)
	return lib, hex.EncodeToString(sum[:])
}

func newFake(t *testing.T, lib, sha string, timeout time.Duration, redact func(string) string) *NativeProcess {
	t.Helper()
	n := NewNativeProcess(NativeOptions{Command: []string{os.Args[0], "plugin-host"}, Env: []string{"PATH=" + os.Getenv("PATH"), fakeNativeEnv + "=1"},
		Library: lib, SHA256: sha, Timeout: timeout, Redact: redact})
	t.Cleanup(n.Close)
	return n
}

func TestNativeCall(t *testing.T) {
	lib, sha := fakeLib(t, map[string]any{})
	n := newFake(t, lib, sha, 0, nil)
	res, err := n.Call(context.Background(), "describe", struct{}{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"name":"fake","version":"1","abi":1,"capabilities":["translate"]}`, string(res))
	res, err = n.Call(context.Background(), "translate", map[string]any{"texts": []string{"a", "b"}, "source": "", "target": "zh-CN"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"texts":["a","b"],"source":"en"}`, string(res))
}

func TestNativeBusinessError(t *testing.T) {
	lib, sha := fakeLib(t, map[string]any{})
	_, err := newFake(t, lib, sha, 0, nil).Call(context.Background(), "fail", struct{}{})
	var ne *NativeError
	require.ErrorAs(t, err, &ne)
	assert.Equal(t, "rate_limited", ne.Code)
	assert.True(t, ne.Retryable)
	assert.False(t, IsNativeFailure(err), "an error envelope is the plugin's answer, not a failure")
}

func TestNativeTimeoutRestarts(t *testing.T) {
	lib, sha := fakeLib(t, map[string]any{})
	n := newFake(t, lib, sha, 500*time.Millisecond, nil)
	start := time.Now()
	_, err := n.Call(context.Background(), "probe.sleep", map[string]any{"ms": 5000})
	assert.Less(t, time.Since(start), 2*time.Second)
	assert.ErrorIs(t, err, ErrTimeout)
	assert.True(t, IsNativeFailure(err))
	_, err = n.Call(context.Background(), "describe", struct{}{})
	assert.NoError(t, err, "the next call starts a new process")
}

func TestNativeCrashRestarts(t *testing.T) {
	lib, sha := fakeLib(t, map[string]any{"echoStderr": "boom-secret"})
	n := newFake(t, lib, sha, 0, func(s string) string { return strings.ReplaceAll(s, "secret", "***") })
	_, err := n.Call(context.Background(), "probe.crash", struct{}{})
	require.ErrorIs(t, err, ErrNativeExited)
	assert.True(t, IsNativeFailure(err))
	assert.Contains(t, err.Error(), "boom-***")
	assert.NotContains(t, err.Error(), "boom-secret")
	_, err = n.Call(context.Background(), "describe", struct{}{})
	assert.NoError(t, err)
}

func TestNativeStartupExit(t *testing.T) {
	lib, _ := fakeLib(t, map[string]any{})
	n := newFake(t, lib, strings.Repeat("0", 64), 0, nil)
	start := time.Now()
	_, err := n.Call(context.Background(), "describe", struct{}{})
	assert.Less(t, time.Since(start), 5*time.Second)
	require.ErrorIs(t, err, ErrNativeExited)
	assert.Contains(t, err.Error(), "DENIED_BEFORE_DLOPEN")
}

func TestNativeInitFailure(t *testing.T) {
	lib, sha := fakeLib(t, map[string]any{"initFail": true, "echoStderr": "init said no"})
	_, err := newFake(t, lib, sha, 0, nil).Call(context.Background(), "describe", struct{}{})
	require.ErrorIs(t, err, ErrNativeExited)
	assert.Contains(t, err.Error(), "init said no")
}

func TestNativeInFlightLimit(t *testing.T) {
	lib, sha := fakeLib(t, map[string]any{})
	n := newFake(t, lib, sha, 0, nil)
	_, err := n.Call(context.Background(), "describe", struct{}{}) // started before timing
	require.NoError(t, err)
	start := time.Now()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := n.Call(context.Background(), "probe.sleep", map[string]any{"ms": 300})
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	assert.GreaterOrEqual(t, time.Since(start), 600*time.Millisecond, "4 at a time")
}

func TestNativeClose(t *testing.T) {
	lib, sha := fakeLib(t, map[string]any{})
	n := newFake(t, lib, sha, 0, nil)
	_, err := n.Call(context.Background(), "describe", struct{}{})
	require.NoError(t, err)
	n.Close()
	_, err = n.Call(context.Background(), "describe", struct{}{})
	assert.True(t, errors.Is(err, ErrClosed))
}
