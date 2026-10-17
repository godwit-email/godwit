package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/store"
)

type modelSnapshot struct {
	name, slug, release    string
	rp                     *runningPlugin
	config                 *plugins.ConfigSnapshot
	configVersion, version int64
	models                 []ProviderModel
	life                   context.Context
	cancel                 context.CancelFunc
}
type modelCatalog struct {
	snap         modelSnapshot
	checked      time.Time
	refreshError string
}

func (s *Service) ListModels(ctx context.Context) ([]ListedModel, error) {
	return s.listModels(ctx, false)
}
func (s *Service) RefreshModels(ctx context.Context) ([]ListedModel, error) {
	return s.listModels(ctx, true)
}

func (s *Service) listModels(ctx context.Context, refresh bool) ([]ListedModel, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	services, err := s.ListModelServices(ctx)
	if err != nil {
		return nil, err
	}
	result := []ListedModel{}
	for _, svc := range services {
		protocol := map[string]string{ai.KindOpenAI: "openai-completions", ai.KindResponses: "openai-responses", ai.KindAnthropic: "anthropic-message"}[svc.Kind]
		if protocol != "" {
			for _, id := range svc.Models {
				result = append(result, ListedModel{Ref: svc.Slug + "/" + id, Model: ProviderModel{ID: id, Name: id, Transport: "http", Protocol: protocol, BaseURL: svc.BaseURL}, Status: ModelStatus{State: ModelReady}})
			}
		}
		judgeProtocol := map[string]string{string(ai.FormatDecisions): "openai-decisions", string(ai.FormatSystemOne): "typesafe-systemone"}[svc.JudgeFormat]
		if judgeProtocol != "" && svc.JudgeModel != "" {
			result = append(result, ListedModel{Ref: svc.Slug + "/" + svc.JudgeModel, Model: ProviderModel{ID: svc.JudgeModel, Name: svc.JudgeModel, Transport: "http", Protocol: judgeProtocol, BaseURL: svc.BaseURL}, Status: ModelStatus{State: ModelReady}})
		}
	}
	s.pluginsMu.RLock()
	names := []string{}
	for name, rp := range s.running {
		if slices.Contains(rp.row.m.Capabilities, "models") {
			names = append(names, name)
		}
	}
	s.pluginsMu.RUnlock()
	sort.Strings(names)
	for _, name := range names {
		snap, err := s.modelCatalog(ctx, name, refresh)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		for _, m := range snap.models {
			status := ModelStatus{State: ModelReady}
			if m.Assets != nil {
				status, err = s.assets.Status(ctx, name, snap.release, m.Assets)
				if err != nil {
					if ctx.Err() != nil {
						return nil, ctx.Err()
					}
					status = ModelStatus{State: ModelFailed, Error: &ModelError{Code: "model_unavailable", Message: "Model status is unavailable", Retryable: true}}
				}
			}
			if runtimeStatus, ok := snap.rp.modelJudge.status(m.ID, snap); ok && m.Assets != nil {
				status = runtimeStatus
			}
			result = append(result, ListedModel{Ref: snap.slug + "/" + m.ID, Model: m, Status: status})
		}
	}
	// The cache never shares mutable maps with UI or plugin callers.
	b, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var copied []ListedModel
	err = json.Unmarshal(b, &copied)
	return copied, err
}

func (s *Service) captureModelSnapshot(ctx context.Context, name string) (modelSnapshot, error) {
	s.pluginsMu.RLock()
	defer s.pluginsMu.RUnlock()
	rp := s.running[name]
	if rp == nil || !slices.Contains(rp.row.m.Capabilities, "models") {
		return modelSnapshot{}, modelUnavailable()
	}
	snap := modelSnapshot{name: name, rp: rp}
	if rp.host != nil {
		snap.config = rp.host.snapshot()
		if snap.config == nil || snap.config.Err != nil {
			return modelSnapshot{}, modelUnavailable()
		}
		snap.configVersion = snap.config.Version
	} else {
		_, ver, _, err := s.storedConfig(ctx, name)
		if err != nil {
			return modelSnapshot{}, err
		}
		snap.configVersion = ver
	}
	return snap, nil
}
func modelUnavailable() *ModelError {
	return &ModelError{Code: "model_unavailable", Message: "The model is unavailable", Retryable: true}
}
func (s *Service) modelSnapshotCurrent(ctx context.Context, snap modelSnapshot) bool {
	now, err := s.captureModelSnapshot(ctx, snap.name)
	return err == nil && now.rp == snap.rp && now.configVersion == snap.configVersion && (snap.life == nil || snap.life.Err() == nil)
}

func (s *Service) modelProviderSlug(ctx context.Context, name string) (string, error) {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	rows, err := s.db.Query(ctx, "SELECT slug FROM plugin_model_providers WHERE plugin = ?", name)
	if err != nil {
		return "", err
	}
	if len(rows.Values) > 0 {
		return store.Str(rows.Values[0][0]), nil
	}
	slug, err := s.newSlug(ctx, name, 0)
	if err != nil {
		return "", err
	}
	_, err = s.db.Execute(ctx, "INSERT INTO plugin_model_providers(plugin,slug) VALUES(?,?)", name, slug)
	return slug, err
}

func (s *Service) modelCatalog(ctx context.Context, name string, refresh bool) (modelSnapshot, error) {
	if err := plugins.CheckModelReentry(ctx, name); err != nil {
		return modelSnapshot{}, err
	}
	snap, err := s.captureModelSnapshot(ctx, name)
	if err != nil {
		return modelSnapshot{}, err
	}
	s.pluginModelsMu.Lock()
	old, exists := s.pluginModels[name]
	if exists && old.snap.rp == snap.rp && old.snap.configVersion == snap.configVersion && !refresh && time.Since(old.checked) < time.Hour {
		s.pluginModelsMu.Unlock()
		return old.snap, nil
	}
	s.pluginModelsMu.Unlock()
	key := fmt.Sprintf("%s/%d/%d", name, snap.rp.gen, snap.configVersion)
	done := s.modelRefresh.DoChan(key, func() (any, error) {
		work, end, err := s.begin(context.WithoutCancel(ctx), 0)
		if err != nil {
			return nil, err
		}
		defer end()
		snap.slug, err = s.modelProviderSlug(work, name)
		if err != nil {
			return nil, err
		}
		snap.release = s.modelRelease(snap)
		var r plugins.ModelsResult
		err = s.callPluginModel(work, snap, "models.list", struct{}{}, &r)
		if err == nil {
			err = plugins.ValidateModels(r, snap.rp.native != nil, snap.rp.p != nil || s.nativeModelMethod(snap, "models.judge"), s.nativeModelMethod(snap, "models.load"))
		}
		s.pluginsMu.RLock()
		defer s.pluginsMu.RUnlock()
		if s.running[name] != snap.rp {
			return nil, modelUnavailable()
		}
		// Config writes also hold pluginsMu, so the check and publish are atomic.
		ver := snap.configVersion
		if snap.rp.host != nil {
			if cfg := snap.rp.host.snapshot(); cfg != nil {
				ver = cfg.Version
			}
		} else {
			_, ver, _, _ = s.storedConfig(work, name)
		}
		if ver != snap.configVersion {
			return nil, modelUnavailable()
		}
		changed := false
		s.pluginModelsMu.Lock()
		defer func() {
			s.pluginModelsMu.Unlock()
			if changed {
				s.forgetPluginEngine(name)
			}
		}()
		if s.pluginModels == nil {
			s.pluginModels = map[string]modelCatalog{}
		}
		previous := s.pluginModels[name]
		if err != nil {
			code := "internal"
			var me *ModelError
			if errors.As(err, &me) {
				code = me.Code
			}
			if previous.snap.rp != snap.rp || previous.snap.configVersion != snap.configVersion {
				previous = modelCatalog{snap: snap}
			}
			previous.checked = time.Now()
			previous.refreshError = code
			s.pluginModels[name] = previous
			return previous.snap, nil
		}
		snap.models = r.Models
		before, _ := json.Marshal(previous.snap.models)
		after, _ := json.Marshal(r.Models)
		if previous.snap.rp == snap.rp && previous.snap.configVersion == snap.configVersion && previous.snap.release == snap.release && bytes.Equal(before, after) {
			snap.version = previous.snap.version
		} else {
			s.pluginModelVersion++
			snap.version = s.pluginModelVersion
		}
		changed = previous.snap.version > 0 && previous.snap.version != snap.version
		if changed && previous.snap.cancel != nil {
			previous.snap.cancel()
		}
		if !changed && previous.snap.life != nil && previous.snap.life.Err() == nil {
			snap.life, snap.cancel = previous.snap.life, previous.snap.cancel
		} else {
			snap.life, snap.cancel = context.WithCancel(context.Background())
		}
		s.pluginModels[name] = modelCatalog{snap: snap, checked: time.Now()}
		return snap, nil
	})
	select {
	case <-ctx.Done():
		return modelSnapshot{}, ctx.Err()
	case r := <-done:
		if r.Err != nil {
			return modelSnapshot{}, r.Err
		}
		return r.Val.(modelSnapshot), nil
	}
}

func (s *Service) modelRelease(snap modelSnapshot) string {
	if s.opts.Registry == nil {
		return ""
	}
	reg, err := s.cachedRegistry(s.opts.Registry)
	if err != nil {
		return ""
	}
	for _, p := range reg.Plugins {
		if p.Name != snap.name || !s.isReviewedPackage(snap.name, snap.rp.row, p) {
			continue
		}
		for _, rel := range p.Releases {
			if rel.Version == snap.rp.row.m.Version {
				b, err := json.Marshal(rel)
				if err != nil {
					return ""
				}
				digest := sha256.Sum256(b)
				key := hex.EncodeToString(digest[:])
				s.pluginModelsMu.Lock()
				if s.modelReleases == nil {
					s.modelReleases = map[string][]byte{}
				}
				s.modelReleases[key] = b
				s.pluginModelsMu.Unlock()
				return key
			}
		}
	}
	return ""
}
func (s *Service) nativeModelMethod(snap modelSnapshot, method string) bool {
	methods := snap.rp.nativeMethods.Load()
	return methods != nil && (*methods)[method]
}

func (s *Service) callPluginModel(ctx context.Context, snap modelSnapshot, method string, request, out any) error {
	input, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if len(input) > plugins.MaxResultBytes {
		return plugins.ErrTooLarge
	}
	if err := plugins.CheckModelReentry(ctx, snap.name); err != nil {
		return err
	}
	if !s.modelSnapshotCurrent(ctx, snap) {
		return modelUnavailable()
	}
	if method != "models.list" {
		if err := s.markHoldsMail(ctx, snap.name); err != nil {
			return err
		}
	}
	err = nil
	if snap.rp.native != nil {
		var raw json.RawMessage
		if pinned, ok := ctx.Value(nativeModelCallKey{}).(nativeModelCall); ok && pinned.rp == snap.rp {
			raw, err = pinned.session.Call(ctx, method, request)
		} else {
			raw, err = snap.rp.native.Call(ctx, method, request)
		}
		if err == nil {
			if len(raw) > plugins.MaxResultBytes {
				err = plugins.ErrTooLarge
			} else {
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.DisallowUnknownFields()
				err = dec.Decode(out)
				if err == nil && method == "models.list" {
					err = plugins.ValidateModels(*out.(*plugins.ModelsResult), true, s.nativeModelMethod(snap, "models.judge"), s.nativeModelMethod(snap, "models.load"))
				}
			}
			if err != nil {
				err = fmt.Errorf("%w: invalid model result", plugins.ErrNativeABI)
			}
		}
	} else {
		hosts, _ := plugins.EgressFor(plugins.InvocationKind(method), snap.rp.row.m, true, true, true)
		inv := plugins.Invocation{ID: method + ":" + rand.Text(), Kind: plugins.InvocationKind(method), EgressHosts: hosts, Config: snap.config}
		switch method {
		case "models.list":
			*out.(*plugins.ModelsResult), err = snap.rp.p.ListModels(ctx, inv)
		case "models.prepare":
			*out.(*plugins.ModelHTTPRequest), err = snap.rp.p.PrepareModel(ctx, inv, request.(plugins.PrepareModelRequest))
		case "models.judge":
			*out.(*plugins.JudgeModelResult), err = snap.rp.p.JudgeModel(ctx, inv, request.(plugins.JudgeModelRequest))
		default:
			return modelUnavailable()
		}
	}
	if err == nil {
		if method == "models.list" {
			return s.pluginSucceeded(ctx, snap.name, snap.rp.gen)
		}
		return nil
	}
	var business *plugins.NativeError
	if errors.As(err, &business) {
		if !strings.Contains("|invalid_request|model_unavailable|model_not_ready|insufficient_memory|unsupported_platform|update_required|verification_failed|model_reentry|rate_limited|upstream|internal|", "|"+business.Code+"|") {
			_ = s.pluginFailed(context.WithoutCancel(ctx), snap.name, snap.rp.gen)
			return fmt.Errorf("%w: invalid model error", plugins.ErrNativeABI)
		}
		msg := plugins.ScrubPaths(s.secrets.Redact(business.Message))
		if len(msg) > 1024 {
			msg = "The model request failed"
		}
		return &ModelError{Code: business.Code, Message: msg, Retryable: business.Retryable}
	}
	if ctx.Err() == nil {
		_ = s.pluginFailed(context.WithoutCancel(ctx), snap.name, snap.rp.gen)
	}
	return err
}

// pluginModel resolves an exact provider/model identity, refreshing at most once.
func (s *Service) pluginModel(ctx context.Context, ref string) (modelSnapshot, ProviderModel, error) {
	slug, id, ok := strings.Cut(ref, "/")
	if !ok || id == "" {
		return modelSnapshot{}, ProviderModel{}, modelUnavailable()
	}
	rows, err := s.db.Query(ctx, "SELECT plugin FROM plugin_model_providers WHERE slug = ?", slug)
	if err != nil {
		return modelSnapshot{}, ProviderModel{}, err
	}
	if len(rows.Values) == 0 {
		return modelSnapshot{}, ProviderModel{}, ErrModelServiceNotFound
	}
	name := store.Str(rows.Values[0][0])
	s.pluginModelsMu.Lock()
	before := s.pluginModels[name].checked
	s.pluginModelsMu.Unlock()
	for _, refresh := range []bool{false, true} {
		if refresh {
			s.pluginModelsMu.Lock()
			refreshed := s.pluginModels[name].checked != before
			s.pluginModelsMu.Unlock()
			if refreshed {
				break
			}
		}
		snap, err := s.modelCatalog(ctx, name, refresh)
		if err != nil {
			return snap, ProviderModel{}, err
		}
		for _, m := range snap.models {
			if m.ID == id {
				return snap, m, nil
			}
		}
	}
	return modelSnapshot{}, ProviderModel{}, modelUnavailable()
}

func (s *Service) currentPluginModelVersion(name string) (int64, error) {
	s.pluginModelsMu.Lock()
	defer s.pluginModelsMu.Unlock()
	snap := s.pluginModels[name].snap
	if snap.rp == nil || !snap.rp.active.Load() {
		return 0, modelUnavailable()
	}
	version := snap.rp.modelConfigVersion.Load()
	if snap.rp.host != nil {
		cfg := snap.rp.host.snapshot()
		if cfg == nil || cfg.Err != nil {
			return 0, modelUnavailable()
		}
		version = cfg.Version
	}
	if version != snap.configVersion {
		return 0, modelUnavailable()
	}
	return snap.version, nil
}
func (s *Service) forgetPluginModels(name string) {
	s.pluginModelsMu.Lock()
	if snap := s.pluginModels[name].snap; snap.cancel != nil {
		snap.cancel()
	}
	s.pluginModelsMu.Unlock()
	s.forgetPluginEngine(name)
}
func (s *Service) forgetPluginEngine(name string) {
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	if s.ai != nil {
		s.ai.ForgetProvider(name)
	}
}
