package core_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assetEnv is dlx installed from a registry whose release has an engine (one
// variant, fixed) and two models (4b needs 8 GB, 2b 6 GB).
type assetEnv struct {
	rs      *coretest.RegistryServer
	s       *core.Service
	a       core.ModelAssets
	release string
	files   map[string][]byte // served path → contents
	mu      sync.Mutex
	hits    map[string]int // served path → requests
}

func (e *assetEnv) count(path string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hits[path]
}

var (
	ref4b = map[string]string{"model": "4b-q4"}
	ref2b = map[string]string{"model": "2b-q8"}
)

func assetFile(e *assetEnv, path, name string, body []byte) map[string]any {
	e.files[path] = body
	e.rs.Serve(path, body)
	sum := sha256.Sum256(body)
	return map[string]any{"name": name, "url": e.rs.URL(path), "sha256": hex.EncodeToString(sum[:]), "size": len(body)}
}

func (e *assetEnv) assets(engineFor []string, twoBSHA string) []any {
	engine := map[string]any{"name": "engine", "variants": []any{map[string]any{"id": "b1",
		"files": []any{assetFile(e, "/a/lib", "libllama", []byte(strings.Repeat("L", 3000)))}}}}
	if engineFor != nil {
		engine["platforms"] = engineFor
	}
	twoB := assetFile(e, "/a/2b", "model.gguf", []byte(strings.Repeat("2", 4000)))
	if twoBSHA != "" {
		twoB["sha256"] = twoBSHA
	}
	return []any{engine, map[string]any{"name": "model", "variants": []any{
		map[string]any{"id": "4b-q4", "minMemoryGB": 8, "files": []any{
			assetFile(e, "/a/4b", "model.gguf", []byte(strings.Repeat("4", 200_000))),
			assetFile(e, "/a/4b-cfg", "decision_config.json", []byte(`{"t":1}`))}},
		map[string]any{"id": "2b-q8", "minMemoryGB": map[string]any{"default": 6}, "files": []any{twoB}}}}}
}

func (e *assetEnv) publish(t *testing.T, at time.Time, assets []any) {
	rel := e.rs.NativeRelease(t, "dlx", "0.1.0", nil)
	rel["assets"] = assets
	e.rs.Publish(t, coretest.Registry(at, plugin("dlx", rel)))
}

func newAssetEnv(t *testing.T) *assetEnv {
	t.Helper()
	core.SetPhysicalMemoryForTest(t, 16)
	e := &assetEnv{rs: coretest.NewRegistryServer(t), files: map[string][]byte{}, hits: map[string]int{}}
	e.rs.Intercept(func(_ http.ResponseWriter, req *http.Request, _ []byte) bool {
		e.mu.Lock()
		e.hits[req.URL.Path]++
		e.mu.Unlock()
		return false
	})
	e.publish(t, t0, e.assets(nil, ""))
	e.s = regService(t, e.rs, t.TempDir())
	require.NoError(t, e.s.RefreshPluginRegistry(ctx))
	_, err := e.s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	e.a = e.s.ModelAssetsForTest()
	e.release = e.s.PluginAssetsDigestForTest(t, "dlx")
	require.NotEmpty(t, e.release, "the install records its release's assets")
	return e
}

func TestModelAssetsPrepareThenVerify(t *testing.T) {
	e := newAssetEnv(t)
	st, err := e.a.Status(ctx, "dlx", e.release, ref4b)
	require.NoError(t, err)
	assert.Equal(t, core.ModelNotDownloaded, st.State)
	assert.Equal(t, "model_not_ready", st.Error.Code)
	assert.EqualValues(t, 3000+200_000+7, st.TotalBytes, "the engine comes with the model")
	assert.Zero(t, st.DownloadedBytes)

	var states []core.ModelState
	var last core.ModelStatus
	dirs, release, err := e.a.Prepare(ctx, "dlx", e.release, ref4b, func(s core.ModelStatus) {
		if len(states) == 0 || states[len(states)-1] != s.State {
			states = append(states, s.State)
		}
		last = s
	})
	require.NoError(t, err)
	release()
	release() // twice is fine
	assert.Equal(t, []core.ModelState{core.ModelDownloading, core.ModelVerifying}, states)
	assert.Equal(t, last.TotalBytes, last.DownloadedBytes)
	require.Len(t, dirs, 2)
	b, err := os.ReadFile(filepath.Join(dirs["model"], "model.gguf"))
	require.NoError(t, err)
	assert.Equal(t, e.files["/a/4b"], b)
	assert.FileExists(t, filepath.Join(dirs["engine"], "libllama"))
	assert.Zero(t, e.count("/a/2b"), "a model not chosen is never downloaded")

	st, err = e.a.Status(ctx, "dlx", e.release, ref4b)
	require.NoError(t, err)
	assert.Equal(t, core.ModelDownloaded, st.State)
	assert.Nil(t, st.Error)

	got, release, err := e.a.Verify(ctx, "dlx", e.release, ref4b)
	require.NoError(t, err)
	release()
	assert.Equal(t, dirs, got)
	before := e.count("/a/4b")
	_, release, err = e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.NoError(t, err)
	release()
	assert.Equal(t, before, e.count("/a/4b"), "nothing downloaded twice")

	// The other model shares the engine; only its own file is fetched.
	_, release, err = e.a.Prepare(ctx, "dlx", e.release, ref2b, nil)
	require.NoError(t, err)
	release()
	assert.Equal(t, 1, e.count("/a/lib"))
}

func TestModelAssetsVerifyNeedsADownload(t *testing.T) {
	e := newAssetEnv(t)
	_, _, err := e.a.Verify(ctx, "dlx", e.release, ref4b)
	assert.Equal(t, "model_not_ready", core.ModelErrorCode(err))
	assert.Zero(t, e.count("/a/4b"), "Verify never downloads")
}

func TestModelAssetsMemoryAndPlatform(t *testing.T) {
	e := newAssetEnv(t)
	core.SetPhysicalMemoryForTest(t, 7)
	st, err := e.a.Status(ctx, "dlx", e.release, ref4b)
	require.NoError(t, err)
	assert.Equal(t, core.ModelErrorState, st.State)
	assert.Equal(t, "insufficient_memory", st.Error.Code)
	_, _, err = e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
	assert.Equal(t, "insufficient_memory", core.ModelErrorCode(err))
	assert.Zero(t, e.count("/a/4b"))
	st, err = e.a.Status(ctx, "dlx", e.release, ref2b)
	require.NoError(t, err)
	assert.Equal(t, core.ModelNotDownloaded, st.State, "the smaller model still fits")

	_, err = e.a.Status(ctx, "dlx", e.release, map[string]string{"model": "9b"})
	require.NoError(t, err)
	st, _ = e.a.Status(ctx, "dlx", e.release, map[string]string{"model": "9b"})
	assert.Equal(t, "model_unavailable", st.Error.Code, "a variant the release does not have")
	st, _ = e.a.Status(ctx, "dlx", "not-a-release", ref2b)
	assert.Equal(t, "model_unavailable", st.Error.Code)
}

func TestModelAssetsUnsupportedPlatform(t *testing.T) {
	e := newAssetEnv(t)
	other := "linux-arm64"
	if plugins.PlatformKey() == other {
		other = "darwin-arm64"
	}
	e.publish(t, t0.Add(time.Hour), e.assets([]string{other}, ""))
	require.NoError(t, e.s.RefreshPluginRegistry(ctx))
	_, err := e.s.InstallPluginFromRegistry(ctx, "dlx") // the new release's assets, still listed
	require.NoError(t, err)
	release := e.s.PluginAssetsDigestForTest(t, "dlx")
	st, err := e.a.Status(ctx, "dlx", release, ref4b)
	require.NoError(t, err)
	assert.Equal(t, "unsupported_platform", st.Error.Code, "the engine is not built for this platform")
}

func TestModelAssetsResume(t *testing.T) {
	e := newAssetEnv(t)
	body := e.files["/a/4b"]
	var ranges []string
	cut := true
	e.rs.Intercept(func(w http.ResponseWriter, req *http.Request, b []byte) bool {
		if req.URL.Path != "/a/4b" {
			return false
		}
		e.mu.Lock()
		ranges = append(ranges, req.Header.Get("Range"))
		e.mu.Unlock()
		if cut { // half the file, then the connection drops
			cut = false
			w.Header().Set("Content-Length", "200000")
			w.Write(b[:100_000])
			return true
		}
		return false
	})
	_, _, err := e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.Error(t, err)
	st, err := e.a.Status(ctx, "dlx", e.release, ref4b)
	require.NoError(t, err)
	assert.EqualValues(t, 3000+100_000, st.DownloadedBytes, "the partial file is kept (the config file comes after it)")

	dirs, release, err := e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.NoError(t, err)
	release()
	assert.Equal(t, []string{"", "bytes=100000-"}, ranges)
	b, err := os.ReadFile(filepath.Join(dirs["model"], "model.gguf"))
	require.NoError(t, err)
	assert.Equal(t, body, b)
}

// A server that ignores the range (200), or answers 416 or a wrong range,
// still ends with the right file.
func TestModelAssetsResumeFallbacks(t *testing.T) {
	for name, answer := range map[string]func(w http.ResponseWriter, b []byte){
		"200": func(w http.ResponseWriter, b []byte) { w.Write(b) },
		"416": func(w http.ResponseWriter, _ []byte) { w.WriteHeader(http.StatusRequestedRangeNotSatisfiable) },
		"wrong range": func(w http.ResponseWriter, b []byte) {
			w.Header().Set("Content-Range", "bytes 5-199999/200000")
			w.WriteHeader(http.StatusPartialContent)
			w.Write(b[5:])
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newAssetEnv(t)
			n := 0
			e.rs.Intercept(func(w http.ResponseWriter, req *http.Request, b []byte) bool {
				if req.URL.Path != "/a/4b" {
					return false
				}
				n++
				switch {
				case n == 1:
					w.Header().Set("Content-Length", "200000")
					w.Write(b[:100_000])
				case req.Header.Get("Range") != "":
					answer(w, b)
				default:
					return false
				}
				return true
			})
			_, _, err := e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
			require.Error(t, err)
			dirs, release, err := e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
			require.NoError(t, err)
			release()
			b, _ := os.ReadFile(filepath.Join(dirs["model"], "model.gguf"))
			assert.Equal(t, e.files["/a/4b"], b)
		})
	}
}

func TestModelAssetsMismatchDiscarded(t *testing.T) {
	e := newAssetEnv(t)
	e.rs.Serve("/a/4b", []byte(strings.Repeat("x", 200_000))) // not the registry's file
	_, _, err := e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.ErrorIs(t, err, core.ErrRegistryMismatch)
	st, _ := e.a.Status(ctx, "dlx", e.release, ref4b)
	assert.EqualValues(t, 3000, st.DownloadedBytes, "nothing kept of the bad file")

	e.rs.Serve("/a/4b", []byte(strings.Repeat("4", 300_000))) // larger than the registry says
	_, _, err = e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.ErrorIs(t, err, core.ErrRegistryMismatch)
}

func TestModelAssetsStall(t *testing.T) {
	core.SetAssetStallForTest(t, 200*time.Millisecond)
	e := newAssetEnv(t)
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	e.rs.Intercept(func(w http.ResponseWriter, req *http.Request, b []byte) bool {
		if req.URL.Path != "/a/4b" {
			return false
		}
		w.Header().Set("Content-Length", "200000")
		w.Write(b[:1000])
		w.(http.Flusher).Flush()
		select {
		case <-hang:
		case <-req.Context().Done():
		}
		return true
	})
	start := time.Now()
	_, _, err := e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	st, _ := e.a.Status(ctx, "dlx", e.release, ref4b)
	assert.EqualValues(t, 3000+1000, st.DownloadedBytes, "a stalled download resumes later")
}

func TestModelAssetsVerifyFailures(t *testing.T) {
	e := newAssetEnv(t)
	dirs, release, err := e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.NoError(t, err)
	release()
	model := filepath.Join(dirs["model"], "model.gguf")
	good, _ := os.ReadFile(model)
	check := func(t *testing.T, code string) {
		t.Helper()
		_, _, err := e.a.Verify(ctx, "dlx", e.release, ref4b)
		assert.Equal(t, code, core.ModelErrorCode(err))
		st, _ := e.a.Status(ctx, "dlx", e.release, ref4b)
		assert.NotEqual(t, core.ModelReady, st.State)
	}

	require.NoError(t, os.WriteFile(model, []byte(strings.Repeat("5", len(good))), 0o600)) // same size and name
	check(t, "verification_failed")
	_, release, err = e.a.Prepare(ctx, "dlx", e.release, ref4b, nil) // an explicit download repairs it
	require.NoError(t, err)
	release()
	b, _ := os.ReadFile(model)
	assert.Equal(t, good, b)

	require.NoError(t, os.WriteFile(filepath.Join(dirs["model"], "extra"), nil, 0o600))
	check(t, "verification_failed")
	require.NoError(t, os.Remove(filepath.Join(dirs["model"], "extra")))

	if runtime.GOOS != "windows" {
		require.NoError(t, os.Rename(model, model+".real"))
		require.NoError(t, os.Symlink(model+".real", model))
		check(t, "verification_failed")
		require.NoError(t, os.Remove(model))
		require.NoError(t, os.Rename(model+".real", model))
	}

	require.NoError(t, os.Remove(model))
	check(t, "model_not_ready")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = e.a.Verify(cancelled, "dlx", e.release, ref2b)
	assert.Error(t, err)
}

func TestModelAssetsLeasesAndRemove(t *testing.T) {
	e := newAssetEnv(t)
	dirs, release, err := e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.NoError(t, err)
	err = e.a.Remove(ctx, "dlx", e.release, ref4b)
	assert.Equal(t, "model_unavailable", core.ModelErrorCode(err), "in use")
	assert.FileExists(t, filepath.Join(dirs["model"], "model.gguf"), "nothing removed")
	release()

	require.NoError(t, e.a.Remove(ctx, "dlx", e.release, ref4b))
	assert.NoDirExists(t, dirs["model"])
	assert.FileExists(t, filepath.Join(dirs["engine"], "libllama"), "the engine is shared")
	st, _ := e.a.Status(ctx, "dlx", e.release, ref4b)
	assert.Equal(t, core.ModelNotDownloaded, st.State)

	_, _, err = e.a.Verify(ctx, "dlx", e.release, ref4b)
	assert.Equal(t, "model_not_ready", core.ModelErrorCode(err), "a failed Verify keeps no lease")
	require.NoError(t, e.a.Remove(ctx, "dlx", e.release, ref4b))
}

// Two waiters share one download; one giving up does not stop it for the
// other, and the last one giving up stops it.
func TestModelAssetsSharedDownload(t *testing.T) {
	e := newAssetEnv(t)
	gate := make(chan struct{})
	var started atomic.Int32
	e.rs.Intercept(func(w http.ResponseWriter, req *http.Request, b []byte) bool {
		if req.URL.Path != "/a/4b" {
			return false
		}
		started.Add(1)
		select {
		case <-gate:
		case <-req.Context().Done():
			return true
		}
		return false
	})
	quitter, quit := context.WithCancel(ctx)
	errs := make(chan error, 2)
	for _, c := range []context.Context{quitter, ctx} {
		go func() {
			_, release, err := e.a.Prepare(c, "dlx", e.release, ref4b, nil)
			if err == nil {
				release()
			}
			errs <- err
		}()
	}
	require.Eventually(t, func() bool { return started.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond) // both are waiting on it
	quit()
	assert.ErrorIs(t, <-errs, context.Canceled)
	close(gate)
	require.NoError(t, <-errs)
	assert.EqualValues(t, 1, started.Load(), "one download")

	// Everyone gives up: the download stops.
	require.NoError(t, e.a.Remove(ctx, "dlx", e.release, ref4b))
	stopped := make(chan struct{})
	e.rs.Intercept(func(w http.ResponseWriter, req *http.Request, b []byte) bool {
		if req.URL.Path != "/a/4b" {
			return false
		}
		<-req.Context().Done()
		close(stopped)
		return true
	})
	c, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	_, _, err := e.a.Prepare(c, "dlx", e.release, ref4b, nil)
	assert.ErrorIs(t, err, context.Canceled)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the download kept going with no one waiting")
	}
}

// Changing a variant the user never downloaded revokes the installed release.
func TestModelAssetsRevocation(t *testing.T) {
	e := newAssetEnv(t)
	assert.Equal(t, "listed", entry(t, e.s, "dlx").InstalledTrust)
	e.publish(t, t0.Add(time.Hour), e.assets(nil, strings.Repeat("0", 64)))
	require.NoError(t, e.s.RefreshPluginRegistry(ctx))
	p := nativeInfo(t, e.s, "dlx")
	assert.Equal(t, "trust_revoked", p.DisabledReason)
	st, _ := e.a.Status(ctx, "dlx", e.release, ref4b)
	assert.Equal(t, "model_unavailable", st.Error.Code, "the installed release is gone from the registry")
}

// Uninstalling removes the plugin's model files that nothing else holds.
func TestModelAssetsUninstallSweeps(t *testing.T) {
	e := newAssetEnv(t)
	dirs, release, err := e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.NoError(t, err)
	release()
	require.NoError(t, e.s.UninstallPlugin(ctx, "dlx"))
	assert.NoDirExists(t, dirs["model"])
	assert.NoDirExists(t, dirs["engine"])
}

// A lease survives a sweep: a model in use stays even after its plugin goes.
func TestModelAssetsSweepKeepsLeased(t *testing.T) {
	e := newAssetEnv(t)
	dirs, release, err := e.a.Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.NoError(t, err)
	require.NoError(t, e.s.UninstallPlugin(ctx, "dlx"))
	assert.DirExists(t, dirs["model"])
	release()
}

// The whole path: a release with assets installed from the registry; until
// the user downloads the model the judge is unavailable; after, the judge
// verifies the files and loads them into the plugin with their directories.
func TestPluginJudgeWithHostedAssets(t *testing.T) {
	core.SetPhysicalMemoryForTest(t, 16)
	e := &assetEnv{rs: coretest.NewRegistryServer(t), files: map[string][]byte{}, hits: map[string]int{}}
	record := filepath.Join(t.TempDir(), "record")
	rel := e.rs.NativeModelsRelease(t, "dlx", "0.1.0", record, []any{
		map[string]any{"id": "decision-4b-q4", "name": "4B", "transport": "plugin", "operation": "judge", "assets": ref4b}})
	rel["assets"] = e.assets(nil, "")
	e.rs.Publish(t, coretest.Registry(t0, plugin("dlx", rel)))
	e.s = regService(t, e.rs, t.TempDir())
	require.NoError(t, e.s.RefreshPluginRegistry(ctx))
	_, err := e.s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	e.release = e.s.PluginAssetsDigestForTest(t, "dlx")
	st, err := e.s.GetSettings(ctx)
	require.NoError(t, err)
	st.JudgeService = "plugin/dlx/decision-4b-q4"
	_, err = e.s.SetSettings(ctx, st)
	require.NoError(t, err)

	j, err := e.s.JudgeForTest(ctx)
	require.NoError(t, err)
	qs := []ai.Question{{Name: "bill", Type: ai.Predicate, Instructions: "Is this a bill?"}}
	_, err = j.Ask(ctx, "mail", qs)
	require.ErrorIs(t, err, ai.ErrJudgeUnavailable)
	assert.Equal(t, "model_not_ready", core.ModelErrorCode(err), "not downloaded: nothing is fetched behind the user's back")
	assert.Zero(t, e.count("/a/4b"))

	dirs, release, err := e.s.ModelAssetsForTest().Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.NoError(t, err)
	release()
	got, err := j.Ask(ctx, "mail", qs)
	require.NoError(t, err)
	require.Len(t, got, 1)
	loads, err := os.ReadFile(record + ".models.load")
	require.NoError(t, err)
	var load struct {
		ModelID string            `json:"modelId"`
		Assets  map[string]string `json:"assets"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.SplitN(string(loads), "\n", 2)[0]), &load))
	assert.Equal(t, "decision-4b-q4", load.ModelID)
	assert.Equal(t, dirs, load.Assets, "the engine and the model, verified")
}

// hostedJudgeEnv is dlx installed from the registry with a judge model per
// variant of the release's "model" asset.
func hostedJudgeEnv(t *testing.T) *assetEnv {
	t.Helper()
	core.SetPhysicalMemoryForTest(t, 16)
	e := &assetEnv{rs: coretest.NewRegistryServer(t), files: map[string][]byte{}, hits: map[string]int{}}
	rel := e.rs.NativeModelsRelease(t, "dlx", "0.1.0", filepath.Join(t.TempDir(), "record"), []any{
		map[string]any{"id": "decision-4b-q4", "name": "4B", "transport": "plugin", "operation": "judge", "assets": ref4b},
		map[string]any{"id": "decision-2b-q8", "name": "2B", "transport": "plugin", "operation": "judge", "assets": ref2b}})
	rel["assets"] = e.assets(nil, "")
	e.rs.Publish(t, coretest.Registry(t0, plugin("dlx", rel)))
	e.s = regService(t, e.rs, t.TempDir())
	require.NoError(t, e.s.RefreshPluginRegistry(ctx))
	_, err := e.s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	e.release = e.s.PluginAssetsDigestForTest(t, "dlx")
	return e
}

func files(t *testing.T, s *core.Service) map[string]core.ModelStatus {
	t.Helper()
	pms, err := s.ListPluginModels(ctx)
	require.NoError(t, err)
	require.Len(t, pms, 1)
	return pms[0].Files
}

// The model list shows each local model's files; the user downloads one with
// progress and removes it again; the list and an event follow each change.
func TestPrepareAndRemoveModelFiles(t *testing.T) {
	e := hostedJudgeEnv(t)
	f := files(t, e.s)
	assert.Equal(t, core.ModelNotDownloaded, f["decision-4b-q4"].State)
	assert.EqualValues(t, 3000+200_000+7, f["decision-4b-q4"].TotalBytes)
	assert.Equal(t, core.ModelNotDownloaded, f["decision-2b-q8"].State)

	evs, stop := e.s.Subscribe()
	defer stop()
	var states []core.ModelState
	st, err := e.s.PrepareModelWithProgress(ctx, "plugin/dlx/decision-4b-q4", func(p core.ModelStatus) {
		if len(states) == 0 || states[len(states)-1] != p.State {
			states = append(states, p.State)
		}
	})
	require.NoError(t, err)
	assert.Equal(t, core.ModelDownloaded, st.State)
	assert.Equal(t, []core.ModelState{core.ModelDownloading, core.ModelVerifying}, states)
	assert.Equal(t, core.ModelDownloaded, files(t, e.s)["decision-4b-q4"].State)
	assert.Equal(t, core.ModelNotDownloaded, files(t, e.s)["decision-2b-q8"].State, "the other model is not fetched")
	assert.Zero(t, e.count("/a/2b"))
	waitEvent(t, evs, events.ModelsChanged)

	require.NoError(t, e.s.RemoveModelFiles(ctx, "plugin/dlx/decision-4b-q4"))
	assert.Equal(t, core.ModelNotDownloaded, files(t, e.s)["decision-4b-q4"].State)
	waitEvent(t, evs, events.ModelsChanged)

	core.SetPhysicalMemoryForTest(t, 7)
	assert.Equal(t, "insufficient_memory", files(t, e.s)["decision-4b-q4"].Error.Code)
	_, err = e.s.PrepareModel(ctx, "plugin/dlx/decision-4b-q4")
	assert.Equal(t, "insufficient_memory", core.ModelErrorCode(err))

	_, err = e.s.PrepareModel(ctx, "plugin/dlx/nope")
	assert.ErrorIs(t, err, core.ErrModelUnavailable)
}

func waitEvent(t *testing.T, ch <-chan events.Event, typ string) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type == typ {
				return
			}
		case <-timeout:
			t.Fatalf("no %s event", typ)
		}
	}
}

// judgeWith asks plugin model ref one question.
func judgeWith(t *testing.T, s *core.Service, ref string) {
	t.Helper()
	st, err := s.GetSettings(ctx)
	require.NoError(t, err)
	st.JudgeService = ref
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
	j, err := s.JudgeForTest(ctx)
	require.NoError(t, err)
	_, err = j.Ask(ctx, "mail", []ai.Question{{Name: "bill", Type: ai.Predicate, Instructions: "Is this a bill?"}})
	require.NoError(t, err)
}

// A model the plugin loaded holds its files only while it may still be
// resident: until another model is loaded in its place, or the process exits
// for being idle.
func TestLoadedModelsReleaseTheirFiles(t *testing.T) {
	core.SetNativeIdleForTest(t, 300*time.Millisecond)
	e := hostedJudgeEnv(t)
	for _, ref := range []string{"plugin/dlx/decision-4b-q4", "plugin/dlx/decision-2b-q8"} {
		_, err := e.s.PrepareModel(ctx, ref)
		require.NoError(t, err)
	}
	judgeWith(t, e.s, "plugin/dlx/decision-4b-q4")
	require.Error(t, e.s.RemoveModelFiles(ctx, "plugin/dlx/decision-4b-q4"), "loaded: in use")
	judgeWith(t, e.s, "plugin/dlx/decision-2b-q8")
	require.NoError(t, e.s.RemoveModelFiles(ctx, "plugin/dlx/decision-4b-q4"), "2B replaced it")
	require.Eventually(t, func() bool { return e.s.RemoveModelFiles(ctx, "plugin/dlx/decision-2b-q8") == nil }, 10*time.Second, 50*time.Millisecond,
		"the idle process let its model go")
	f := files(t, e.s)
	assert.Equal(t, core.ModelNotDownloaded, f["decision-4b-q4"].State)
	assert.Equal(t, core.ModelNotDownloaded, f["decision-2b-q8"].State)
}

// assetDirs are the variant directories in dir's asset store.
func assetDirs(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(dir, "plugin-assets"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, e := range ents {
		if e.Name() != ".partial" {
			out = append(out, e.Name())
		}
	}
	return out
}

// An upgrade whose release has other model files removes the old ones it
// no longer names, and keeps the engine both share.
func TestUpgradeSweepsOldModelFiles(t *testing.T) {
	core.SetPhysicalMemoryForTest(t, 16)
	dir := t.TempDir()
	e := &assetEnv{rs: coretest.NewRegistryServer(t), files: map[string][]byte{}, hits: map[string]int{}}
	models := []any{map[string]any{"id": "decision-4b-q4", "name": "4B", "transport": "plugin", "operation": "judge", "assets": ref4b}}
	rel := e.rs.NativeModelsRelease(t, "dlx", "0.1.0", filepath.Join(t.TempDir(), "record"), models)
	rel["assets"] = e.assets(nil, "")
	e.rs.Publish(t, coretest.Registry(t0, plugin("dlx", rel)))
	e.s = regService(t, e.rs, dir)
	require.NoError(t, e.s.RefreshPluginRegistry(ctx))
	_, err := e.s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	_, err = e.s.PrepareModel(ctx, "plugin/dlx/decision-4b-q4")
	require.NoError(t, err)
	before := assetDirs(t, dir)
	require.Len(t, before, 2, "the engine and 4B")

	rel2 := e.rs.NativeModelsRelease(t, "dlx", "0.2.0", filepath.Join(t.TempDir(), "record"), models)
	assets := e.assets(nil, "")
	fourB := assets[1].(map[string]any)["variants"].([]any)[0].(map[string]any)
	fourB["files"] = []any{assetFile(e, "/a/4b-new", "model.gguf", []byte(strings.Repeat("5", 1000)))}
	rel2["assets"] = assets
	e.rs.Publish(t, coretest.Registry(t0.Add(time.Hour), plugin("dlx", rel2, rel)))
	require.NoError(t, e.s.RefreshPluginRegistry(ctx))
	_, err = e.s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	after := assetDirs(t, dir)
	require.Len(t, after, 1, "only the engine is left")
	assert.Contains(t, before, after[0])
}

// A download that ends after its plugin was removed is not reported as done,
// and its files go.
func TestStaleDownloadIsRejected(t *testing.T) {
	core.SetPhysicalMemoryForTest(t, 16)
	dir := t.TempDir()
	e := &assetEnv{rs: coretest.NewRegistryServer(t), files: map[string][]byte{}, hits: map[string]int{}}
	rel := e.rs.NativeModelsRelease(t, "dlx", "0.1.0", filepath.Join(t.TempDir(), "record"), []any{
		map[string]any{"id": "decision-4b-q4", "name": "4B", "transport": "plugin", "operation": "judge", "assets": ref4b}})
	rel["assets"] = e.assets(nil, "")
	e.rs.Publish(t, coretest.Registry(t0, plugin("dlx", rel)))
	e.s = regService(t, e.rs, dir)
	require.NoError(t, e.s.RefreshPluginRegistry(ctx))
	_, err := e.s.InstallPluginFromRegistry(ctx, "dlx")
	require.NoError(t, err)
	started, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.rs.Intercept(func(_ http.ResponseWriter, req *http.Request, _ []byte) bool {
		if req.URL.Path == "/a/4b" {
			once.Do(func() { close(started) })
			<-resume
		}
		return false
	})
	done := make(chan error, 1)
	go func() { _, err := e.s.PrepareModel(ctx, "plugin/dlx/decision-4b-q4"); done <- err }()
	<-started
	require.NoError(t, e.s.UninstallPlugin(ctx, "dlx"))
	close(resume)
	err = <-done
	require.Error(t, err)
	assert.Equal(t, "model_unavailable", core.ModelErrorCode(err))
	assert.Empty(t, assetDirs(t, dir))
}

// A file a judge call finds changed (same size) shows as verification_failed
// in the model list until it is downloaded again.
func TestVerificationFailureShows(t *testing.T) {
	e := hostedJudgeEnv(t)
	dirs, release, err := e.s.ModelAssetsForTest().Prepare(ctx, "dlx", e.release, ref4b, nil)
	require.NoError(t, err)
	release()
	weights := filepath.Join(dirs["model"], "model.gguf")
	require.NoError(t, os.WriteFile(weights, []byte(strings.Repeat("x", 200_000)), 0o600))
	assert.Equal(t, core.ModelDownloaded, files(t, e.s)["decision-4b-q4"].State, "sizes alone look right")

	st, err := e.s.GetSettings(ctx)
	require.NoError(t, err)
	st.JudgeService = "plugin/dlx/decision-4b-q4"
	_, err = e.s.SetSettings(ctx, st)
	require.NoError(t, err)
	j, err := e.s.JudgeForTest(ctx)
	require.NoError(t, err)
	_, err = j.Ask(ctx, "mail", []ai.Question{{Name: "bill", Type: ai.Predicate, Instructions: "Is this a bill?"}})
	require.Error(t, err)
	f := files(t, e.s)["decision-4b-q4"]
	require.NotNil(t, f.Error)
	assert.Equal(t, "verification_failed", f.Error.Code)

	_, err = e.s.PrepareModel(ctx, "plugin/dlx/decision-4b-q4")
	require.NoError(t, err)
	assert.Equal(t, core.ModelDownloaded, files(t, e.s)["decision-4b-q4"].State)
}
