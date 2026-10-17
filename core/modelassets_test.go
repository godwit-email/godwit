package core

import (
	"context"
	"encoding/json"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/plugins"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestModelAssetsDefault(t *testing.T) {
	a := newModelAssets(nil)
	refs := map[string]string{"model": "4b-q4"}
	st, err := a.Status(context.Background(), "local", "release-digest", refs)
	require.NoError(t, err)
	require.Equal(t, ModelState("error"), st.State)
	require.Equal(t, "update_required", st.Error.Code)
	dirs, release, err := a.Prepare(context.Background(), "local", "release-digest", refs, func(ModelStatus) { t.Fatal("default must not report downloads") })
	var me *ModelError
	require.ErrorAs(t, err, &me)
	require.Equal(t, "update_required", me.Code)
	require.Nil(t, dirs)
	require.Nil(t, release)
	dirs, release, err = a.Verify(context.Background(), "local", "release-digest", refs)
	require.ErrorAs(t, err, &me)
	require.Nil(t, dirs)
	require.Nil(t, release)
	require.ErrorAs(t, a.Remove(context.Background(), "local", "release-digest", refs), &me)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = a.Status(ctx, "local", "release-digest", refs)
	require.ErrorIs(t, err, context.Canceled)
	_, _, err = a.Prepare(ctx, "local", "release-digest", refs, nil)
	require.ErrorIs(t, err, context.Canceled)
	_, _, err = a.Verify(ctx, "local", "release-digest", refs)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, a.Remove(ctx, "local", "release-digest", refs), context.Canceled)
}

type fakeModelAssets struct {
	mu                           sync.Mutex
	releases, verifies, prepares int
	started                      chan struct{}
	unblock                      chan struct{}
	once                         sync.Once
}

func (a *fakeModelAssets) Status(context.Context, string, string, map[string]string) (ModelStatus, error) {
	return ModelStatus{State: ModelDownloaded, TotalBytes: 4, DownloadedBytes: 4}, nil
}
func (a *fakeModelAssets) lease() func() {
	var once sync.Once
	return func() { once.Do(func() { a.mu.Lock(); a.releases++; a.mu.Unlock() }) }
}
func (a *fakeModelAssets) Prepare(ctx context.Context, plugin, release string, refs map[string]string, progress func(ModelStatus)) (map[string]string, func(), error) {
	a.mu.Lock()
	a.prepares++
	a.mu.Unlock()
	if progress != nil {
		progress(ModelStatus{State: ModelVerifying, TotalBytes: 4, DownloadedBytes: 4})
	}
	if a.started != nil {
		a.once.Do(func() { close(a.started) })
		select {
		case <-a.unblock:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	return map[string]string{"engine": "/verified/engine", "model": "/verified/" + refs["model"]}, a.lease(), nil
}
func (a *fakeModelAssets) Verify(ctx context.Context, plugin, release string, refs map[string]string) (map[string]string, func(), error) {
	a.mu.Lock()
	a.verifies++
	a.mu.Unlock()
	return map[string]string{"engine": "/verified/engine", "model": "/verified/" + refs["model"]}, a.lease(), nil
}
func (a *fakeModelAssets) Remove(context.Context, string, string, map[string]string) error {
	return nil
}

func nativeAssetService(t *testing.T, options ...map[string]any) (*Service, string) {
	t.Helper()
	e := newApprovalEnv(t)
	s := e.s
	s.nativeCmd = []string{os.Args[0], "plugin-host"}
	s.nativeEnv = []string{"PATH=" + os.Getenv("PATH"), "GODWIT_CORETEST_NATIVE=1"}
	dir := filepath.Join(t.TempDir(), "assetjudge")
	require.NoError(t, os.MkdirAll(dir, 0700))
	record := filepath.Join(t.TempDir(), "requests")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"name":"assetjudge","version":"1.0.0","native":{"abi":1},"capabilities":["models"]}`), 0600))
	spec := map[string]any{"describe": map[string]any{"name": "assetjudge", "version": "1.0.0", "abi": 1, "capabilities": []string{"models"}, "methods": []string{"models.list", "models.load", "models.judge"}}, "record": record, "models": []map[string]any{{"id": "a", "name": "A", "transport": "plugin", "operation": "judge", "assets": map[string]string{"model": "a"}}, {"id": "b", "name": "B", "transport": "plugin", "operation": "judge", "assets": map[string]string{"model": "b"}}}, "modelAnswers": []map[string]any{{"name": "q", "type": "predicate", "probability": 0.8}}}
	for _, option := range options {
		for k, v := range option {
			spec[k] = v
		}
	}
	b, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, plugins.LibraryName()), b, 0600))
	_, err = s.InstallPlugin(bg, dir)
	require.NoError(t, err)
	require.NoError(t, s.GrantPermission(bg, "assetjudge"))
	require.NoError(t, s.SetPluginEnabled(bg, "assetjudge", true))
	_, err = s.ListModels(bg)
	require.NoError(t, err)
	return s, record
}
func TestPrepareModelSwitch(t *testing.T) {
	s, record := nativeAssetService(t)
	a := &fakeModelAssets{}
	s.assets = a
	status, err := s.PrepareModel(bg, "assetjudge/a")
	require.NoError(t, err)
	require.Equal(t, ModelReady, status.State)
	st, err := s.GetSettings(bg)
	require.NoError(t, err)
	require.Equal(t, "assetjudge/a", st.JudgeModel)
	status, err = s.PrepareModel(bg, "assetjudge/b")
	require.NoError(t, err)
	require.Equal(t, ModelReady, status.State)
	st, err = s.GetSettings(bg)
	require.NoError(t, err)
	require.Equal(t, "assetjudge/b", st.JudgeModel)
	lines, err := os.ReadFile(record + ".models.load")
	require.NoError(t, err)
	rows := strings.Split(strings.TrimSpace(string(lines)), "\n")
	require.Len(t, rows, 2)
	require.JSONEq(t, `{"modelId":"a","assets":{"engine":"/verified/engine","model":"/verified/a"}}`, rows[0])
	require.JSONEq(t, `{"modelId":"b","assets":{"engine":"/verified/engine","model":"/verified/b"}}`, rows[1])
}
func TestPrepareModelLeaseLifetime(t *testing.T) {
	s, _ := nativeAssetService(t)
	a := &fakeModelAssets{}
	s.assets = a
	_, err := s.PrepareModel(bg, "assetjudge/a")
	require.NoError(t, err)
	a.mu.Lock()
	require.Zero(t, a.releases)
	a.mu.Unlock()
	require.NoError(t, s.RemoveModelFiles(bg, "assetjudge/a"))
	a.mu.Lock()
	require.Equal(t, 1, a.releases)
	a.mu.Unlock()
}
func TestPrepareModelSharedCancel(t *testing.T) {
	s, _ := nativeAssetService(t)
	a := &fakeModelAssets{started: make(chan struct{}), unblock: make(chan struct{})}
	s.assets = a
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() { _, err := s.PrepareModel(ctx, "assetjudge/a"); done <- err }()
	<-a.started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	close(a.unblock)
	st, err := s.GetSettings(bg)
	require.NoError(t, err)
	require.Empty(t, st.JudgeModel)
}

func TestPrepareModelSharedWaiters(t *testing.T) {
	s, _ := nativeAssetService(t)
	a := &fakeModelAssets{started: make(chan struct{}), unblock: make(chan struct{})}
	s.assets = a
	ctx, cancel := context.WithCancel(bg)
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := s.PrepareModel(ctx, "assetjudge/a"); first <- err }()
	<-a.started
	go func() { _, err := s.PrepareModel(bg, "assetjudge/a"); second <- err }()
	require.Eventually(t, func() bool {
		snap, err := s.captureModelSnapshot(bg, "assetjudge")
		if err != nil {
			return false
		}
		rt := snap.rp.modelJudge
		rt.mu.Lock()
		defer rt.mu.Unlock()
		for _, task := range rt.prepares {
			if len(task.waiters) == 2 {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-first, context.Canceled)
	close(a.unblock)
	require.NoError(t, <-second)
	a.mu.Lock()
	require.Equal(t, 1, a.prepares)
	a.mu.Unlock()
	st, err := s.GetSettings(bg)
	require.NoError(t, err)
	require.Equal(t, "assetjudge/a", st.JudgeModel)
}
func TestPrepareModelStaleRelease(t *testing.T) {
	s, _ := nativeAssetService(t)
	a := &fakeModelAssets{started: make(chan struct{}), unblock: make(chan struct{})}
	s.assets = a
	done := make(chan error, 1)
	go func() { _, err := s.PrepareModel(bg, "assetjudge/a"); done <- err }()
	<-a.started
	snap, err := s.captureModelSnapshot(bg, "assetjudge")
	require.NoError(t, err)
	_, err = s.db.Execute(bg, "UPDATE plugins SET config_version=config_version+1 WHERE name=?", snap.name)
	require.NoError(t, err)
	close(a.unblock)
	require.Error(t, <-done)
	st, err := s.GetSettings(bg)
	require.NoError(t, err)
	require.Empty(t, st.JudgeModel)
	a.mu.Lock()
	require.Equal(t, 1, a.releases)
	a.mu.Unlock()
}
func TestPrepareModelRollbackFailed(t *testing.T) {
	s, _ := nativeAssetService(t, map[string]any{"modelLoadFailAt": map[string]int{"a": 2, "b": 1}})
	a := &fakeModelAssets{}
	s.assets = a
	_, err := s.PrepareModel(bg, "assetjudge/a")
	require.NoError(t, err)
	snap, err := s.captureModelSnapshot(bg, "assetjudge")
	require.NoError(t, err)
	_, err = s.PrepareModel(bg, "assetjudge/b")
	require.Error(t, err)
	require.Nil(t, snap.rp.modelJudge.resident())
	a.mu.Lock()
	require.Equal(t, 3, a.releases)
	require.Equal(t, 1, a.verifies)
	a.mu.Unlock()
	st, err := s.GetSettings(bg)
	require.NoError(t, err)
	require.Equal(t, "assetjudge/a", st.JudgeModel)
}
func TestPrepareModelVerifyEachGeneration(t *testing.T) {
	s, _ := nativeAssetService(t)
	a := &fakeModelAssets{}
	s.assets = a
	_, err := s.PrepareModel(bg, "assetjudge/a")
	require.NoError(t, err)
	snap, err := s.captureModelSnapshot(bg, "assetjudge")
	require.NoError(t, err)
	rt := snap.rp.modelJudge
	rt.mu.Lock()
	gen := rt.generation
	rt.mu.Unlock()
	snap.rp.native.StopGeneration(gen)
	rt.releaseGeneration(gen)
	for range 2 {
		_, err = (pluginJudge{s: s, name: "assetjudge"}).Ask(bg, "test", []ai.Question{{Name: "q", Type: "predicate", Instructions: "Is this a test?"}})
		require.NoError(t, err)
	}
	a.mu.Lock()
	require.Equal(t, 1, a.verifies)
	a.mu.Unlock()
}

func TestJudgeModelRequiresPreparation(t *testing.T) {
	s, _ := nativeAssetService(t)
	st, err := s.GetSettings(bg)
	require.NoError(t, err)
	st.JudgeModel = "assetjudge/a"
	_, err = s.SetSettings(bg, st)
	var me *ModelError
	require.ErrorAs(t, err, &me)
	require.Equal(t, "model_not_ready", me.Code)
	_, err = s.db.Execute(bg, `UPDATE settings SET value=json_set(value,'$.JudgeModel','gone/') WHERE key='app'`)
	require.NoError(t, err)
	st, err = s.GetSettings(bg)
	require.NoError(t, err)
	st.Theme = "dark"
	_, err = s.SetSettings(bg, st)
	require.NoError(t, err)
	st.JudgeModel = "other/"
	_, err = s.SetSettings(bg, st)
	require.Error(t, err)
}

func TestJudgeModelStaleSettingsCannotReselectManagedModel(t *testing.T) {
	s, _ := nativeAssetService(t)
	s.assets = &fakeModelAssets{}
	_, err := s.PrepareModel(bg, "assetjudge/a")
	require.NoError(t, err)
	stale, err := s.GetSettings(bg)
	require.NoError(t, err)
	stale.Theme = "dark"

	s.settingsMu.Lock()
	unlock := sync.OnceFunc(s.settingsMu.Unlock)
	defer unlock()
	done := make(chan error, 1)
	go func() { _, err := s.SetSettings(bg, stale); done <- err }()
	// Wait for the stale writer's preflight to finish, without a timing-based sleep.
	require.Eventually(t, func() bool {
		buf := make([]byte, 1<<20)
		for _, stack := range strings.Split(string(buf[:runtime.Stack(buf, true)]), "\n\n") {
			if strings.Contains(stack, "[sync.Mutex.Lock]") && strings.Contains(stack, "(*Service).SetSettings(") {
				return true
			}
		}
		return false
	}, 5*time.Second, time.Millisecond)
	current := stale
	current.JudgeModel = ""
	_, err = s.storeSettings(bg, current) // another writer commits while holding settingsMu
	require.NoError(t, err)
	require.NoError(t, s.RemoveModelFiles(bg, "assetjudge/a"))
	unlock()

	select {
	case err := <-done:
		var me *ModelError
		require.ErrorAs(t, err, &me)
		require.Equal(t, "model_not_ready", me.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("stale settings writer did not finish")
	}
	stored, err := s.GetSettings(bg)
	require.NoError(t, err)
	require.Empty(t, stored.JudgeModel)
}

func TestPrepareModelCatalogInvalidatesResident(t *testing.T) {
	s, record := nativeAssetService(t, map[string]any{"modelsAfterList": []map[string]any{{"id": "a", "name": "A", "transport": "plugin", "operation": "judge", "assets": map[string]string{"model": "b"}}}})
	a := &fakeModelAssets{}
	s.assets = a
	_, err := s.PrepareModel(bg, "assetjudge/a")
	require.NoError(t, err)
	rows, err := s.RefreshModels(bg)
	require.NoError(t, err)
	rows = slices.DeleteFunc(rows, func(m ListedModel) bool { return m.Ref != "assetjudge/a" })
	require.Len(t, rows, 1)
	require.NotEqual(t, ModelReady, rows[0].Status.State, "new variant must not inherit old readiness")
	_, err = (pluginJudge{s: s, name: "assetjudge"}).Ask(bg, "public", []ai.Question{{Name: "q", Type: ai.Predicate, Instructions: "test?"}})
	require.NoError(t, err)
	loads, err := os.ReadFile(record + ".models.load")
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(loads)), "\n")
	require.Len(t, lines, 2)
	require.JSONEq(t, `{"modelId":"a","assets":{"engine":"/verified/engine","model":"/verified/b"}}`, lines[1])
}
