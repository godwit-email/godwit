package core

// Models a running plugin offers (capability "models", core/plugins/models.go).

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/godwit-email/godwit/core/plugins"
)

// PluginModels are a running plugin's models, or Err when it could not list them.
type PluginModels struct {
	Plugin string
	Models []plugins.ProviderModel
	Err    string
}

// modelCatalog is a plugin instance's model list for one config snapshot.
type modelCatalog struct {
	cfg    any // the snapshot it was listed with: a new one lists again
	models []plugins.ProviderModel
}

// ListPluginModels lists the models of every running plugin declaring models, by name.
func (s *Service) ListPluginModels(ctx context.Context) ([]PluginModels, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	s.pluginsMu.RLock()
	rps := map[string]*runningPlugin{}
	for name, rp := range s.running {
		if slices.Contains(rp.row.m.Capabilities, "models") {
			rps[name] = rp
		}
	}
	s.pluginsMu.RUnlock()
	var out []PluginModels
	for name, rp := range rps {
		ms, err := s.pluginModels(ctx, name, rp)
		pm := PluginModels{Plugin: name, Models: ms}
		if err != nil {
			pm.Err = s.cleanError(err).Error()
		}
		out = append(out, pm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plugin < out[j].Plugin })
	return out, nil
}

// cfgKey is the config snapshot rp's calls run with now.
func (rp *runningPlugin) cfgKey() any {
	if rp.native != nil {
		return rp.cfg.Load()
	}
	return rp.host.snapshot()
}

// pluginModels is rp's model list, listed again after a settings change.
func (s *Service) pluginModels(ctx context.Context, name string, rp *runningPlugin) ([]plugins.ProviderModel, error) {
	key := rp.cfgKey()
	if c := rp.models.Load(); c != nil && c.cfg == key {
		return c.models, nil
	}
	var raw []byte
	if rp.native != nil {
		res, err := rp.native.Call(ctx, "models.list", struct{}{})
		if err != nil {
			return nil, err
		}
		raw = res
	} else {
		v, err := rp.p.ListModels(ctx, s.modelInvocation(rp))
		if err != nil {
			return nil, err
		}
		raw, _ = json.Marshal(v)
	}
	var res plugins.ModelsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("plugin %s: the model list is not {models}", name)
	}
	if err := plugins.ValidateModels(res, rp.native != nil); err != nil {
		return nil, fmt.Errorf("plugin %s: %w", name, err)
	}
	rp.models.Store(&modelCatalog{key, res.Models})
	return res.Models, nil
}

// modelInvocation admits a script plugin's models call, as a translation is admitted.
func (s *Service) modelInvocation(rp *runningPlugin) plugins.Invocation {
	r := rp.row
	hosts, _ := plugins.EgressFor(plugins.KindModels, r.m, r.granted == r.permHash, true, true)
	return plugins.Invocation{ID: "models:" + rand.Text(), Kind: plugins.KindModels, EgressHosts: hosts, Config: rp.host.snapshot()}
}
