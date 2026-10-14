package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fakeNativeEnv = "GODWIT_PLUGINS_FAKE_NATIVE"
	realNativeEnv = "GODWIT_PLUGINS_REAL_NATIVE" // the real ServeNative, with a library built by buildEcho
)

// TestMain: the test binary is also a fake plugin-host for NativeProcess.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "plugin-host" && os.Getenv(fakeNativeEnv) != "" {
		os.Exit(ServeNativeWith(os.Args[2:], os.Stdin, os.Stdout, os.Stderr, FakeNativeOpener(os.Stderr)))
	}
	if len(os.Args) > 1 && os.Args[1] == "plugin-host" && os.Getenv(realNativeEnv) != "" {
		os.Exit(ServeNative(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
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
	assert.JSONEq(t, `{"texts":["[zh-CN] a","[zh-CN] b"],"source":"en"}`, string(res))
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

// A process that closed its stdin but lives on must not hold a call (or the
// caller's locks) until it happens to exit.
func TestNativeStdinClosedDoesNotHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	// The first call's line goes into the pipe while it is open; 0.2 s later the
	// process closes stdin and lives on, so the second call's write must fail.
	n := NewNativeProcess(NativeOptions{Command: []string{"/bin/sh", "-c", "sleep 0.2; exec 0<&-; sleep 20", "plugin-host"}, Library: "x", SHA256: "x", Timeout: 30 * time.Second})
	t.Cleanup(n.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := n.Call(ctx, "describe", struct{}{})
	require.ErrorIs(t, err, context.DeadlineExceeded, "written, never answered")
	start := time.Now()
	_, err = n.Call(context.Background(), "describe", struct{}{})
	assert.ErrorIs(t, err, ErrNativeExited)
	assert.Less(t, time.Since(start), 5*time.Second)
}

// What a library prints to stdout must not break the JSON lines on it.
func TestNativeLibraryStdoutKeptOffProtocol(t *testing.T) {
	lib, sha := buildEcho(t)
	n := NewNativeProcess(NativeOptions{Command: []string{os.Args[0], "plugin-host"}, Env: []string{"PATH=" + os.Getenv("PATH"), realNativeEnv + "=1"},
		Library: lib, SHA256: sha, Timeout: 3 * time.Second})
	t.Cleanup(n.Close)
	_, err := n.Call(context.Background(), "noisy", struct{}{})
	require.NoError(t, err)
	res, err := n.Call(context.Background(), "describe", struct{}{})
	require.NoError(t, err)
	assert.Contains(t, string(res), `"echo"`)
}

// newStarted is newFake with an OnStart that counts its runs (after delay) and fails while fail returns an error.
func newStarted(t *testing.T, idle, delay time.Duration, fail func() error) (*NativeProcess, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	lib, sha := fakeLib(t, map[string]any{})
	var runs atomic.Int32
	var started atomic.Bool
	n := NewNativeProcess(NativeOptions{Command: []string{os.Args[0], "plugin-host"}, Env: []string{"PATH=" + os.Getenv("PATH"), fakeNativeEnv + "=1"},
		Library: lib, SHA256: sha, Idle: idle,
		OnStart: func(ctx context.Context, call func(context.Context, string, any) (json.RawMessage, error)) error {
			time.Sleep(delay)
			runs.Add(1)
			if fail != nil {
				if err := fail(); err != nil {
					return err
				}
			}
			if _, err := call(ctx, "configure", map[string]any{"config": map[string]any{}}); err != nil {
				return err
			}
			started.Store(true)
			return nil
		}})
	t.Cleanup(n.Close)
	return n, &runs, &started
}

func TestNativeOnStartGatesCalls(t *testing.T) {
	n, runs, started := newStarted(t, 0, 300*time.Millisecond, nil)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			_, err := n.Call(context.Background(), "describe", struct{}{})
			assert.NoError(t, err)
			assert.True(t, started.Load(), "no call goes out before OnStart is done")
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), runs.Load())
}

func TestNativeOnStartAfterCrash(t *testing.T) {
	n, runs, _ := newStarted(t, 0, 0, nil)
	_, err := n.Call(context.Background(), "describe", struct{}{})
	require.NoError(t, err)
	_, err = n.Call(context.Background(), "probe.crash", struct{}{})
	require.ErrorIs(t, err, ErrNativeExited)
	_, err = n.Call(context.Background(), "describe", struct{}{})
	require.NoError(t, err)
	assert.Equal(t, int32(2), runs.Load(), "the new process was set up again")
}

func TestNativeOnStartError(t *testing.T) {
	var once atomic.Bool
	n, runs, _ := newStarted(t, 0, 0, func() error {
		if once.CompareAndSwap(false, true) {
			return errors.New("nope")
		}
		return nil
	})
	_, err := n.Call(context.Background(), "describe", struct{}{})
	assert.ErrorContains(t, err, "nope")
	_, err = n.Call(context.Background(), "describe", struct{}{})
	assert.NoError(t, err)
	assert.Equal(t, int32(2), runs.Load())
}

func TestNativeIdleExit(t *testing.T) {
	n, runs, _ := newStarted(t, 200*time.Millisecond, 0, nil)
	require.NoError(t, n.Start(context.Background()))
	time.Sleep(700 * time.Millisecond)
	_, err := n.Call(context.Background(), "describe", struct{}{})
	require.NoError(t, err)
	assert.Equal(t, int32(2), runs.Load(), "it exited when idle and started again")
}

func TestNativeIdleWaitsForCalls(t *testing.T) {
	n, runs, _ := newStarted(t, 200*time.Millisecond, 0, nil)
	_, err := n.Call(context.Background(), "probe.sleep", map[string]any{"ms": 600})
	require.NoError(t, err)
	_, err = n.Call(context.Background(), "describe", struct{}{})
	require.NoError(t, err)
	assert.Equal(t, int32(1), runs.Load(), "a call in flight is not idle")
}
