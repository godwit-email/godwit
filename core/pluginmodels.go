package core

// Models a running plugin offers (capability "models", core/plugins/models.go):
// their catalog, their "plugin/<name>/<model id>" references, and the HTTP
// client that runs an HTTP model through the SDK of its protocol, the plugin
// adjusting each request the SDK encoded.

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/skills"
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

// pluginModel is the model ref ("plugin/<name>/<model id>") names, and its plugin.
func (s *Service) pluginModel(ctx context.Context, ref string) (string, *runningPlugin, plugins.ProviderModel, error) {
	name, id, _ := strings.Cut(strings.TrimPrefix(ref, translatorPrefix), "/")
	s.pluginsMu.RLock()
	rp := s.running[name]
	s.pluginsMu.RUnlock()
	if rp == nil || !slices.Contains(rp.row.m.Capabilities, "models") {
		return "", nil, plugins.ProviderModel{}, fmt.Errorf("%w: %s", ErrModelUnavailable, ref)
	}
	ms, err := s.pluginModels(ctx, name, rp)
	if err != nil {
		return "", nil, plugins.ProviderModel{}, errors.Join(fmt.Errorf("%w: %s", ErrModelUnavailable, ref), err)
	}
	if i := slices.IndexFunc(ms, func(m plugins.ProviderModel) bool { return m.ID == id }); i >= 0 {
		return name, rp, ms[i], nil
	}
	return "", nil, plugins.ProviderModel{}, fmt.Errorf("%w: %s", ErrModelUnavailable, ref)
}

// isPluginRef reports whether ref is a well-formed "plugin/<name>/<model id>".
func isPluginRef(ref string) bool {
	rest, ok := strings.CutPrefix(ref, translatorPrefix)
	name, id, _ := strings.Cut(rest, "/")
	return ok && skills.ValidName(name) && id != ""
}

// protocolKinds are the engine kinds of the generation protocols.
var protocolKinds = map[string]string{"openai-completions": ai.KindOpenAI, "openai-responses": ai.KindResponses, "anthropic-message": ai.KindAnthropic}

// resolvePluginModel resolves a plugin generation model's ref for the engine.
// Its ServiceID is negative, a hash of ref, so it never names a stored
// service, and is stable across runs for the cache keys built from it.
func (s *Service) resolvePluginModel(ctx context.Context, ref string) (ai.ResolvedModel, error) {
	name, rp, m, err := s.pluginModel(ctx, ref)
	if err != nil {
		return ai.ResolvedModel{}, err
	}
	if m.Judges() {
		return ai.ResolvedModel{}, fmt.Errorf("%w: %s is a judge model", ErrModelNotFound, ref)
	}
	h := fnv.New64a()
	h.Write([]byte(ref))
	return ai.ResolvedModel{ServiceID: -int64(h.Sum64()>>1) - 1, Kind: protocolKinds[m.Protocol], BaseURL: strings.TrimRight(m.BaseURL, "/"),
		Model: m.ID, Client: s.pluginModelClient(name, rp, m.ID)}, nil
}

// judgeFormats are the judge formats of the HTTP judge protocols.
var judgeFormats = map[string]ai.JudgeFormat{"typesafe-systemone": ai.FormatSystemOne, "openai-decisions": ai.FormatDecisions}

// pluginJudge is the judge a plugin judge model's ref names.
func (s *Service) pluginJudge(ctx context.Context, ref string) (ai.Judge, error) {
	name, rp, m, err := s.pluginModel(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !m.Judges() {
		return nil, fmt.Errorf("%w: %s is not a judge model", ErrModelNotFound, ref)
	}
	return ai.NewJudge(s.pluginModelClient(name, rp, m.ID), judgeFormats[m.Protocol], m.BaseURL, "", m.ID)
}

// pluginModelClient is the client of rp's model id: one per instance and
// model, so the engine rebuilds its Genkit instance for a restarted plugin.
func (s *Service) pluginModelClient(name string, rp *runningPlugin, id string) *http.Client {
	if c, ok := rp.clients.Load(id); ok {
		return c.(*http.Client)
	}
	t := &pluginModelTransport{s: s, name: name, rp: rp, model: id, base: cmp.Or(s.modelHTTP, ai.NoRedirectClient).Transport}
	if rp.native == nil {
		t.base = rp.host.net.Transport()
	}
	if t.base == nil {
		t.base = http.DefaultTransport
	}
	c, _ := rp.clients.LoadOrStore(id, &http.Client{Transport: t, CheckRedirect: ai.NoRedirectClient.CheckRedirect})
	return c.(*http.Client)
}

// pluginModelTransport sends the requests an SDK encoded for a plugin's model:
// the plugin's prepareRequest (models.prepare) adjusts each one first, if it
// has one, and the response goes back to the SDK as it came.
type pluginModelTransport struct {
	s     *Service
	name  string
	rp    *runningPlugin
	model string
	base  http.RoundTripper // native: the model client's; script: the plugin network's
}

// maxModelRequest caps a request handed to the plugin.
const maxModelRequest = 8 << 20

func (t *pluginModelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if req.Body != nil {
		defer req.Body.Close()
	}
	out, err := t.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	if t.rp.native == nil {
		r := t.rp.row
		hosts, _ := plugins.EgressFor(plugins.KindModels, r.m, r.granted == r.permHash, true, true)
		if err := t.rp.host.net.Allowed(out.URL, hosts); err != nil {
			return nil, fmt.Errorf("plugin %s: %w", t.name, err)
		}
	}
	return t.base.RoundTrip(out)
}

// prepare is req as the plugin returns it, or req itself when it adjusts none.
func (t *pluginModelTransport) prepare(ctx context.Context, req *http.Request) (*http.Request, error) {
	prepares := t.rp.p != nil && t.rp.p.Prepares()
	if t.rp.native != nil {
		prepares = slices.Contains(*cmp.Or(t.rp.methods.Load(), &[]string{}), "models.prepare")
	}
	if !prepares {
		return req, nil
	}
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(io.LimitReader(req.Body, maxModelRequest+1))
		if err != nil {
			return nil, err
		}
		if len(b) > maxModelRequest {
			return nil, ErrTooLarge
		}
		body = b
	}
	in := plugins.ModelRequest{ModelID: t.model, Method: req.Method, URL: req.URL.String(), Headers: map[string]string{}, Body: string(body)}
	for k, v := range req.Header {
		in.Headers[k] = strings.Join(v, ", ")
	}
	var res struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	err := t.s.callModelPlugin(ctx, t.name, t.rp, "models.prepare", func(ctx context.Context) (any, error) {
		if t.rp.native != nil {
			return t.rp.native.Call(ctx, "models.prepare", in)
		}
		if err := t.s.markHoldsMail(ctx, t.name); err != nil { // the request holds mail
			return nil, err
		}
		return t.rp.p.PrepareRequest(ctx, t.s.modelInvocation(t.rp), in)
	}, &res)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(res.URL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: prepareRequest returned no valid url", errPluginAnswer)
	}
	out, err := http.NewRequestWithContext(ctx, req.Method, u.String(), bytes.NewReader([]byte(res.Body)))
	if err != nil {
		return nil, err
	}
	for k, v := range res.Headers {
		out.Header.Set(k, v)
	}
	return out, nil
}

// errPluginAnswer: a model plugin's answer is not what the call returns.
var errPluginAnswer = errors.New("the model plugin answered wrongly")

// callModelPlugin runs one call into a model plugin, decodes its answer into
// out and keeps the plugin's failure count as callTranslator does: a script
// plugin's error counts, a native one's error envelope is its answer, and an
// answer out cannot hold always counts.
func (s *Service) callModelPlugin(ctx context.Context, name string, rp *runningPlugin, method string, call func(context.Context) (any, error), out any) error {
	v, err := call(ctx)
	if err == nil {
		raw, ok := v.(json.RawMessage)
		if !ok {
			raw, _ = json.Marshal(v)
		}
		if json.Unmarshal(raw, out) != nil {
			err = fmt.Errorf("%w: %s", errPluginAnswer, method)
		}
	}
	if ctx.Err() != nil || errors.Is(err, plugins.ErrClosed) {
		return errors.Join(ctx.Err(), err) // cancelled or stopped: not the plugin's failure
	}
	if err != nil {
		var failed error
		if rp.native == nil || plugins.IsNativeFailure(err) || errors.Is(err, errPluginAnswer) {
			failed = s.pluginFailed(ctx, name, rp.gen)
		}
		return errors.Join(fmt.Errorf("model plugin %s: %w", name, s.cleanError(err)), failed)
	}
	return s.pluginSucceeded(ctx, name, rp.gen)
}
