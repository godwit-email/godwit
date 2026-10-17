package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
)

type loadedPluginModel struct {
	model         ProviderModel
	release       string
	configVersion int64
	version       int64
	generation    uint64
	dirs          map[string]string
}
type runtimeModelStatus struct {
	ModelStatus
	release       string
	configVersion int64
	version       int64
}

type modelPrepareTask struct {
	done    chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	waiters map[uint64]context.Context
	status  ModelStatus
	err     error
}

func (r *pluginJudgeRuntime) status(id string, snap modelSnapshot) (ModelStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.states[id]
	return v.ModelStatus, ok && v.release == snap.release && v.configVersion == snap.configVersion && v.version == snap.version
}
func (s *Service) setModelStatus(snap modelSnapshot, id string, status ModelStatus) {
	r := snap.rp.modelJudge
	r.mu.Lock()
	if r.states == nil {
		r.states = map[string]runtimeModelStatus{}
	}
	r.states[id] = runtimeModelStatus{ModelStatus: status, release: snap.release, configVersion: snap.configVersion, version: snap.version}
	r.mu.Unlock()
	s.bus.Publish(events.Event{Type: events.ModelsChanged})
}
func (r *pluginJudgeRuntime) resident() *loadedPluginModel {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loaded
}
func (r *pluginJudgeRuntime) keepLease(s *Service, session plugins.NativeSession, release func()) {
	r.mu.Lock()
	select {
	case <-session.Done:
		r.mu.Unlock()
		release()
		return
	default:
	}
	if r.leases == nil {
		r.leases = map[uint64][]func(){}
	}
	first := len(r.leases[session.Generation]) == 0
	r.leases[session.Generation] = append(r.leases[session.Generation], release)
	r.generation = session.Generation
	r.mu.Unlock()
	if first {
		go func() {
			<-session.Done
			r.releaseGeneration(session.Generation)
			s.bus.Publish(events.Event{Type: events.ModelsChanged})
		}()
	}
}
func (r *pluginJudgeRuntime) releaseGeneration(generation uint64) {
	r.releaseMu.Lock()
	defer r.releaseMu.Unlock()
	r.mu.Lock()
	leases := r.leases[generation]
	delete(r.leases, generation)
	if r.loaded != nil && r.loaded.generation == generation {
		r.loaded = nil
		for id, status := range r.states {
			if status.State == ModelReady || status.State == ModelLoading {
				status.State = ModelDownloaded
				r.states[id] = status
			}
		}
	}
	r.mu.Unlock()
	for _, release := range leases {
		release()
	}
}

func (s *Service) PrepareModel(ctx context.Context, ref string) (ModelStatus, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ModelStatus{}, err
	}
	defer end()
	snap, model, err := s.pluginModel(ctx, ref)
	if errors.Is(err, ErrModelServiceNotFound) {
		catalog, listErr := s.ListModels(ctx)
		if listErr != nil {
			return ModelStatus{}, listErr
		}
		for _, m := range catalog {
			if m.Ref == ref {
				return m.Status, nil
			}
		}
	}
	if err != nil {
		return ModelStatus{}, err
	}
	if model.Assets == nil {
		return ModelStatus{State: ModelReady}, nil
	}
	status, err := s.assets.Status(ctx, snap.name, snap.release, model.Assets)
	if err != nil {
		return ModelStatus{}, err
	}
	if status.Error != nil && status.Error.Code != "model_not_ready" && status.Error.Code != "verification_failed" {
		return status, status.Error
	}
	rt := snap.rp.modelJudge
	if resident := rt.resident(); resident != nil && resident.model.ID == model.ID && resident.configVersion == snap.configVersion && resident.version == snap.version {
		st, err := s.loadSettings(ctx)
		if err != nil {
			return status, err
		}
		if st.JudgeModel == ref {
			return ModelStatus{State: ModelReady, TotalBytes: status.TotalBytes, DownloadedBytes: status.DownloadedBytes}, nil
		}
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		return status, err
	}
	key := fmt.Sprintf("%s/%s/%d/%d", ref, snap.release, snap.configVersion, snap.version)
	rt.mu.Lock()
	if rt.prepares == nil {
		rt.prepares = map[string]*modelPrepareTask{}
	}
	task := rt.prepares[key]
	if task == nil {
		work, finish, err := s.begin(context.WithoutCancel(ctx), 0)
		if err != nil {
			rt.mu.Unlock()
			return status, err
		}
		taskCtx, cancel := context.WithCancel(work)
		task = &modelPrepareTask{ctx: taskCtx, cancel: cancel, done: make(chan struct{}), waiters: map[uint64]context.Context{}, status: status}
		rt.prepares[key] = task
		go func() {
			defer finish()
			defer cancel()
			select {
			case rt.prepareGate <- struct{}{}:
			case <-taskCtx.Done():
				rt.mu.Lock()
				task.err = taskCtx.Err()
				delete(rt.prepares, key)
				close(task.done)
				rt.mu.Unlock()
				return
			}
			result, runErr := s.runModelPrepare(work, task, snap, model, ref, st.JudgeModel)
			<-rt.prepareGate
			rt.mu.Lock()
			task.status, task.err = result, runErr
			delete(rt.prepares, key)
			close(task.done)
			rt.mu.Unlock()
			s.bus.Publish(events.Event{Type: events.ModelsChanged})
		}()
	}
	rt.nextWaiter++
	waiter := rt.nextWaiter
	task.waiters[waiter] = ctx
	rt.mu.Unlock()
	leave := func() {
		rt.mu.Lock()
		delete(task.waiters, waiter)
		if len(task.waiters) == 0 {
			task.cancel()
		}
		rt.mu.Unlock()
	}
	defer leave()
	select {
	case <-ctx.Done():
		status, _ := rt.status(model.ID, snap)
		return status, ctx.Err()
	case <-task.done:
		return task.status, task.err
	}
}

func (s *Service) runModelPrepare(work context.Context, task *modelPrepareTask, snap modelSnapshot, model ProviderModel, ref, previousSelection string) (ModelStatus, error) {
	rt := snap.rp.modelJudge
	err := snap.rp.native.WithProcess(work, func(session plugins.NativeSession) error {
		if err := task.ctx.Err(); err != nil {
			return err
		}
		dirs, release, err := s.assets.Prepare(task.ctx, snap.name, snap.release, model.Assets, func(status ModelStatus) { s.setModelStatus(snap, model.ID, status) })
		if err != nil {
			return err
		}
		if release == nil {
			return errors.New("asset provider returned no lease")
		}
		owned := true
		defer func() {
			if owned {
				release()
			}
		}()
		select {
		case rt.gate <- struct{}{}:
		case <-task.ctx.Done():
			return task.ctx.Err()
		}
		defer func() { <-rt.gate }()
		if !s.modelSnapshotCurrent(task.ctx, snap) {
			return modelUnavailable()
		}
		old := rt.resident()
		s.settingsMu.Lock()
		current, err := s.loadSettings(task.ctx)
		s.settingsMu.Unlock()
		if err != nil {
			return err
		}
		if current.JudgeModel != previousSelection {
			return modelUnavailable()
		}
		rt.keepLease(s, session, release)
		owned = false
		callCtx := context.WithValue(work, nativeModelCallKey{}, nativeModelCall{rp: snap.rp, session: session})
		err = s.loadPluginModel(callCtx, snap, model, session, dirs)
		if err == nil {
			s.settingsMu.Lock()
			current, err = s.loadSettings(work)
			rt.mu.Lock()
			live := false
			for _, waiter := range task.waiters {
				if waiter.Err() == nil {
					live = true
					break
				}
			}
			if err == nil && (!live || task.ctx.Err() != nil) {
				err = context.Canceled
			}
			if err == nil && (!s.modelSnapshotCurrent(work, snap) || current.JudgeModel != previousSelection) {
				err = modelUnavailable()
			}
			if err == nil {
				current.JudgeModel = ref
				_, err = s.storeSettings(work, current)
			}
			rt.mu.Unlock()
			s.settingsMu.Unlock()
		}
		if err == nil {
			return nil
		}
		// A native load may have mapped B even when it failed; keep both leases.
		recovery := context.WithoutCancel(work)
		if old != nil && old.generation == session.Generation && s.modelSnapshotCurrent(recovery, snap) {
			restoreDirs, restoreLease, restoreErr := s.assets.Verify(recovery, snap.name, old.release, old.model.Assets)
			if restoreErr == nil && restoreLease != nil {
				rt.keepLease(s, session, restoreLease)
				restoreErr = s.loadPluginModel(context.WithValue(recovery, nativeModelCallKey{}, nativeModelCall{rp: snap.rp, session: session}), snap, old.model, session, restoreDirs)
			}
			if restoreErr != nil || restoreLease == nil {
				snap.rp.native.StopGeneration(session.Generation)
				rt.releaseGeneration(session.Generation)
			}
		} else {
			snap.rp.native.StopGeneration(session.Generation)
			rt.releaseGeneration(session.Generation)
		}
		return err
	})
	status, ok := rt.status(model.ID, snap)
	if !ok {
		status = ModelStatus{State: ModelDownloaded}
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status.State = ModelDownloaded
			status.Error = nil
		} else {
			var me *ModelError
			if !errors.As(err, &me) {
				me = &ModelError{Code: "model_unavailable", Message: "The model could not be loaded", Retryable: true}
			}
			status.State = ModelFailed
			status.Error = me
		}
		s.setModelStatus(snap, model.ID, status)
	}
	return status, err
}

func (s *Service) loadPluginModel(ctx context.Context, snap modelSnapshot, model ProviderModel, session plugins.NativeSession, dirs map[string]string) error {
	rt := snap.rp.modelJudge
	s.setModelStatus(snap, model.ID, ModelStatus{State: ModelLoading})
	var result struct{}
	err := s.callPluginModel(ctx, snap, "models.load", plugins.ModelLoadRequest{ModelID: model.ID, Assets: dirs}, &result)
	if err != nil {
		return err
	}
	rt.mu.Lock()
	old := rt.loaded
	if old != nil && old.model.ID != model.ID {
		rt.states[old.model.ID] = runtimeModelStatus{ModelStatus: ModelStatus{State: ModelDownloaded}, release: old.release, configVersion: old.configVersion, version: old.version}
	}
	rt.loaded = &loadedPluginModel{model: model, release: snap.release, configVersion: snap.configVersion, generation: session.Generation, dirs: dirs, version: snap.version}
	rt.mu.Unlock()
	s.setModelStatus(snap, model.ID, ModelStatus{State: ModelReady})
	return s.pluginSucceeded(ctx, snap.name, snap.rp.gen)
}

func (s *Service) ensurePluginModel(ctx context.Context, snap modelSnapshot, model ProviderModel) error {
	if model.Assets == nil {
		return nil
	}
	pinned, ok := ctx.Value(nativeModelCallKey{}).(nativeModelCall)
	if !ok {
		return modelUnavailable()
	}
	rt := snap.rp.modelJudge
	loaded := rt.resident()
	if loaded != nil && loaded.generation == pinned.session.Generation && loaded.model.ID == model.ID && loaded.configVersion == snap.configVersion && loaded.version == snap.version {
		return nil
	}
	dirs, release, err := s.assets.Verify(ctx, snap.name, snap.release, model.Assets)
	if err != nil {
		return err
	}
	if release == nil {
		return errors.New("asset provider returned no lease")
	}
	rt.keepLease(s, pinned.session, release)
	if err = s.loadPluginModel(ctx, snap, model, pinned.session, dirs); err != nil {
		snap.rp.native.StopGeneration(pinned.session.Generation)
		rt.releaseGeneration(pinned.session.Generation)
	}
	return err
}

func (s *Service) RemoveModelFiles(ctx context.Context, ref string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	snap, model, err := s.pluginModel(ctx, ref)
	if err != nil {
		return err
	}
	if model.Assets == nil {
		return nil
	}
	rt := snap.rp.modelJudge
	select {
	case rt.gate <- struct{}{}:
	default:
		return modelUnavailable()
	}
	defer func() { <-rt.gate }()
	rt.mu.Lock()
	preparing := len(rt.prepares) > 0
	generation := rt.generation
	rt.mu.Unlock()
	if preparing {
		return modelUnavailable()
	}
	if generation != 0 {
		snap.rp.native.StopGeneration(generation)
		rt.releaseGeneration(generation)
	}
	if !s.modelSnapshotCurrent(ctx, snap) {
		return modelUnavailable()
	}
	if err = s.assets.Remove(ctx, snap.name, snap.release, model.Assets); err != nil {
		return err
	}
	status, err := s.assets.Status(ctx, snap.name, snap.release, model.Assets)
	if err == nil {
		s.setModelStatus(snap, model.ID, status)
	}
	return err
}
