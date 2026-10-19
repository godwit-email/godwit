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
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/godwit-email/godwit/core/skills"
	"github.com/godwit-email/godwit/core/store"
)

// PluginModels are a running plugin's models, or Err when it could not list
// them. Files are the file states of its models with registry-hosted assets, by model ID.
type PluginModels struct {
	Plugin string
	Models []plugins.ProviderModel
	Files  map[string]ModelStatus
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
		ms, _, err := s.pluginModels(ctx, name, rp)
		pm := PluginModels{Plugin: name, Models: ms, Files: s.modelStatuses(ctx, name, rp, ms)}
		if err != nil {
			pm.Err = s.cleanError(err).Error()
		}
		out = append(out, pm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plugin < out[j].Plugin })
	return out, nil
}

// cfgKey is the config snapshot rp's calls run with now.
// A native plugin's is the settings its process acknowledged (configure
// returned), so a list made while it reconfigures is not filed under them.
func (rp *runningPlugin) cfgKey() any {
	if rp.native != nil {
		return rp.acked.Load()
	}
	return rp.host.snapshot()
}

// pluginModels is rp's model list, listed again after a settings change.
// cfg is the settings snapshot the list belongs to.
func (s *Service) pluginModels(ctx context.Context, name string, rp *runningPlugin) (_ []plugins.ProviderModel, cfg any, _ error) {
	key := rp.cfgKey()
	if c := rp.models.Load(); c != nil && c.cfg == key {
		return c.models, key, nil
	}
	var res plugins.ModelsResult
	err := s.callModelPlugin(ctx, name, rp, func(ctx context.Context) error {
		var err error
		if rp.native != nil {
			v, cerr := rp.native.Call(ctx, "models.list", struct{}{})
			err = decodeAnswer(v, cerr, &res, "models.list")
		} else {
			v, cerr := rp.p.ListModels(ctx, s.modelInvocation(rp, key))
			err = decodeAnswer(v, cerr, &res, "listModels")
		}
		if err == nil {
			if verr := plugins.ValidateModels(res, rp.native != nil); verr != nil {
				err = fmt.Errorf("%w: %s", errPluginAnswer, verr)
			}
		}
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	rp.models.Store(&modelCatalog{key, res.Models})
	return res.Models, key, nil
}

// modelInvocation admits a script plugin's models call, as a translation is
// admitted, with cfg (a cfgKey) as its ctx.config: the snapshot the model was
// listed with, never newer settings with an older model.
func (s *Service) modelInvocation(rp *runningPlugin, cfg any) plugins.Invocation {
	r := rp.row
	hosts, _ := plugins.EgressFor(plugins.KindModels, r.m, r.granted == r.permHash, true, true)
	return plugins.Invocation{ID: "models:" + rand.Text(), Kind: plugins.KindModels, EgressHosts: hosts, Config: cfg.(*plugins.ConfigSnapshot)}
}

// pluginModel is the model ref ("plugin/<name>/<model id>") names, its
// plugin, and the settings snapshot its listing belongs to: a client or judge
// made from it refuses once the plugin's settings are other.
func (s *Service) pluginModel(ctx context.Context, ref string) (string, *runningPlugin, plugins.ProviderModel, any, error) {
	name, id, _ := strings.Cut(strings.TrimPrefix(ref, translatorPrefix), "/")
	s.pluginsMu.RLock()
	rp := s.running[name]
	s.pluginsMu.RUnlock()
	if rp == nil || !slices.Contains(rp.row.m.Capabilities, "models") {
		return "", nil, plugins.ProviderModel{}, nil, fmt.Errorf("%w: %s", errNoPluginModel, ref)
	}
	ms, cfg, err := s.pluginModels(ctx, name, rp)
	if errors.Is(err, errOwnModel) {
		return "", nil, plugins.ProviderModel{}, nil, err
	}
	if err != nil {
		return "", nil, plugins.ProviderModel{}, nil, errors.Join(fmt.Errorf("%w: %s", ErrModelUnavailable, ref), err)
	}
	if i := slices.IndexFunc(ms, func(m plugins.ProviderModel) bool { return m.ID == id }); i >= 0 {
		return name, rp, ms[i], cfg, nil
	}
	return "", nil, plugins.ProviderModel{}, nil, fmt.Errorf("%w: %s", errNoPluginModel, ref)
}

// errNoPluginModel: no running plugin offers the model, as its list says;
// unlike a list that failed, a legacy service ref may then be meant (resolveRef).
var errNoPluginModel = fmt.Errorf("%w", ErrModelUnavailable)

// isPluginRef reports whether ref is a well-formed "plugin/<name>/<model id>".
func isPluginRef(ref string) bool {
	rest, ok := strings.CutPrefix(ref, translatorPrefix)
	name, id, _ := strings.Cut(rest, "/")
	return ok && skills.ValidName(name) && id != ""
}

// protocolKinds are the engine kinds of the generation protocols.
var protocolKinds = map[string]string{"openai-completions": ai.KindOpenAI, "openai-responses": ai.KindResponses, "anthropic-message": ai.KindAnthropic}

// resolvePluginModel resolves a plugin generation model's ref for the engine.
func (s *Service) resolvePluginModel(ctx context.Context, ref string) (ai.ResolvedModel, error) {
	name, rp, m, cfg, err := s.pluginModel(ctx, ref)
	if err != nil {
		return ai.ResolvedModel{}, err
	}
	if m.Judges() {
		return ai.ResolvedModel{}, fmt.Errorf("%w: %s is a judge model", ErrModelNotFound, ref)
	}
	// Version follows the plugin's code and settings, as cache keys built from it must.
	rows, err := s.db.Query(ctx, "SELECT config_version FROM plugins WHERE name = ?", name)
	if err != nil || len(rows.Values) == 0 {
		return ai.ResolvedModel{}, cmp.Or(err, fmt.Errorf("%w: %s", ErrModelUnavailable, ref))
	}
	h := fnv.New64a()
	fmt.Fprint(h, rp.hash, "\x00", store.Int(rows.Values[0][0]))
	return ai.ResolvedModel{ServiceID: pluginServiceID(ref), Version: int64(h.Sum64() >> 1), Kind: protocolKinds[m.Protocol], BaseURL: strings.TrimRight(m.BaseURL, "/"),
		Model: m.ID, Client: s.pluginModelClient(name, rp, m.ID, cfg)}, nil
}

// pluginServiceID is a plugin model's engine ServiceID: negative, a hash of
// its ref, so it never names a stored service, and stable across runs for
// the cache keys built from it.
func pluginServiceID(ref string) int64 {
	h := fnv.New64a()
	h.Write([]byte(ref))
	return -int64(h.Sum64()>>1) - 1
}

// forgetPluginModels ends the engine instances of the models rp ran, as
// stopping it does, so their runs in flight end like a removed service's.
func (s *Service) forgetPluginModels(name string, rp *runningPlugin) {
	rp.clients.Range(func(k, _ any) bool {
		s.forgetModel(pluginServiceID(translatorPrefix + name + "/" + k.(clientKey).id))
		return true
	})
}

// judgeFormats are the judge formats of the HTTP judge protocols.
var judgeFormats = map[string]ai.JudgeFormat{"typesafe-systemone": ai.FormatSystemOne, "openai-decisions": ai.FormatDecisions}

// pluginJudge is the judge a plugin judge model's ref names.
func (s *Service) pluginJudge(ctx context.Context, ref string) (ai.Judge, error) {
	name, rp, m, cfg, err := s.pluginModel(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !m.Judges() {
		return nil, fmt.Errorf("%w: %s is not a judge model", ErrModelNotFound, ref)
	}
	if m.Transport == "plugin" {
		return pluginModelJudge{s, name, rp, m, cfg}, nil
	}
	c := s.pluginModelClient(name, rp, m.ID, cfg)
	j, err := ai.NewJudge(c, judgeFormats[m.Protocol], m.BaseURL, "", m.ID)
	return pluginHTTPJudge{j, c.Transport.(*pluginModelTransport)}, err
}

// pluginModelClient is the client of rp's model id: one per instance, model
// and settings snapshot, so the engine rebuilds its Genkit instance for a
// restarted or reconfigured plugin.
func (s *Service) pluginModelClient(name string, rp *runningPlugin, id string, cfg any) *http.Client {
	key := clientKey{id, cfg}
	if c, ok := rp.clients.Load(key); ok {
		return c.(*http.Client)
	}
	t := &pluginModelTransport{s: s, name: name, rp: rp, model: id, cfg: cfg, base: cmp.Or(s.modelHTTP, ai.NoRedirectClient).Transport}
	if rp.native == nil {
		t.base = rp.host.net.Transport()
	}
	if t.base == nil {
		t.base = http.DefaultTransport
	}
	// LoadOrStore: concurrent first resolutions share one client, else the
	// engine would take the second for a changed service and end the first's run.
	c, _ := rp.clients.LoadOrStore(key, &http.Client{Transport: t, CheckRedirect: ai.NoRedirectClient.CheckRedirect})
	return c.(*http.Client)
}

// clientKey names a plugin model's client: its model id and settings snapshot.
type clientKey struct {
	id  string
	cfg any
}

// pluginModelTransport sends the requests an SDK encoded for a plugin's model:
// the plugin's prepareRequest (models.prepare) adjusts each one first, if it
// has one, and the response goes back to the SDK as it came.
type pluginModelTransport struct {
	s     *Service
	name  string
	rp    *runningPlugin
	model string
	cfg   any               // the settings snapshot it was resolved with (cfgKey)
	base  http.RoundTripper // native: the model client's; script: the plugin network's
}

// maxModelRequest caps a request handed to the plugin.
const maxModelRequest = 8 << 20

func (t *pluginModelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if req.Body != nil {
		defer req.Body.Close()
	}
	if err := t.stale(); err != nil { // a client resolved before never sends again
		return nil, err
	}
	if t.rp.native == nil {
		if err := t.s.markHoldsMail(ctx, t.name); err != nil { // the request holds mail, prepared or not
			return nil, err
		}
	}
	out, err := t.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	if t.rp.native == nil {
		r := t.rp.row
		hosts, _ := plugins.EgressFor(plugins.KindModels, r.m, r.granted == r.permHash, true, true)
		var resp *http.Response
		err := t.rp.host.net.Allowed(out.URL, hosts)
		if err == nil {
			resp, err = t.base.RoundTrip(out)
		}
		// Audited once its outcome is known, as net.fetch's hops are: host and port only.
		e := gateway.Entry{Source: gateway.SourcePlugin, Extension: t.name, Tool: "plugin:" + t.name + ":models",
			Decision: "allow", Destinations: []string{plugins.HostPort(out.URL)}}
		var ne *plugins.NetError
		switch {
		case err == nil:
		case errors.As(err, &ne):
			e.Decision, e.Reason = "deny", ne.Reason
		case ctx.Err() != nil:
			e.Decision, e.Reason = "error", "cancelled"
		default:
			e.Decision, e.Reason = "error", plugins.FailReason(err)
		}
		if aerr := t.s.gateway().Record(context.WithoutCancel(ctx), e); aerr != nil {
			if resp != nil {
				resp.Body.Close()
			}
			return nil, errors.Join(err, aerr)
		}
		return resp, err
	}
	return t.base.RoundTrip(out)
}

// stale is ErrModelUnavailable once the plugin stopped, was replaced or
// took other settings since t was made.
func (t *pluginModelTransport) stale() error {
	t.s.pluginsMu.RLock()
	stale := t.s.running[t.name] != t.rp || t.rp.cfgKey() != t.cfg
	t.s.pluginsMu.RUnlock()
	if stale {
		return fmt.Errorf("%w: plugin/%s/%s", ErrModelUnavailable, t.name, t.model)
	}
	return nil
}

// pluginHTTPJudge is ai.NewJudge on a plugin model's client: a stale client
// is ErrModelUnavailable, not the judge's generic "could not reach".
type pluginHTTPJudge struct {
	ai.Judge
	t *pluginModelTransport
}

func (j pluginHTTPJudge) Ask(ctx context.Context, input string, qs []ai.Question) ([]ai.Answer, error) {
	if err := j.t.stale(); err != nil {
		return nil, err
	}
	return j.Judge.Ask(ctx, input, qs)
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
	var res struct { // pointers: a missing field is a wrong answer, an empty one may be right
		URL     string             `json:"url"`
		Headers *map[string]string `json:"headers"`
		Body    *string            `json:"body"`
	}
	var u *url.URL
	err := t.s.callModelPlugin(ctx, t.name, t.rp, func(ctx context.Context) error {
		var err error
		if t.rp.native != nil {
			v, cerr := t.rp.native.Call(ctx, "models.prepare", in)
			err = decodeAnswer(v, cerr, &res, "models.prepare")
		} else {
			v, cerr := t.rp.p.PrepareRequest(ctx, t.s.modelInvocation(t.rp, t.cfg), in)
			err = decodeAnswer(v, cerr, &res, "prepareRequest")
		}
		if err == nil {
			if u, err = url.Parse(res.URL); err != nil || u.Host == "" || res.Headers == nil || res.Body == nil { // a wrong answer: it counts
				err = fmt.Errorf("%w: prepareRequest must return a valid url, headers and body", errPluginAnswer)
			}
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	out, err := http.NewRequestWithContext(ctx, req.Method, u.String(), bytes.NewReader([]byte(*res.Body)))
	if err != nil {
		return nil, err
	}
	for k, v := range *res.Headers {
		out.Header.Set(k, v)
	}
	return out, nil
}

// errOwnModel: a script plugin's own call (llm.complete, llm.decide) uses one of its models.
var errOwnModel = errors.New("a plugin cannot use its own model from its own calls; pick another default or judge model")

// errPluginAnswer: a model plugin's answer is not what the call returns.
var errPluginAnswer = errors.New("the model plugin answered wrongly")

// decodeAnswer decodes a call's answer v (a native call's JSON or a script's
// value) into out; what names the call in the error.
func decodeAnswer[T any](v T, err error, out any, what string) error {
	if err != nil {
		return err
	}
	raw, ok := any(v).(json.RawMessage)
	if !ok {
		raw, _ = json.Marshal(v)
	}
	if json.Unmarshal(raw, out) != nil {
		return fmt.Errorf("%w: %s", errPluginAnswer, what)
	}
	return nil
}

// callModelPlugin runs one call into a model plugin and keeps the plugin's
// failure count as callTranslator does: a script plugin's error counts, a
// native one's error envelope is its answer, and a wrong answer
// (errPluginAnswer) always counts.
func (s *Service) callModelPlugin(ctx context.Context, name string, rp *runningPlugin, call func(context.Context) error) error {
	if rp.native == nil && ctx.Value(pluginCallKey{}) == name {
		return errOwnModel // its VM runs one call at a time: this one would wait for the call waiting for it
	}
	err := call(ctx)
	if ctx.Err() != nil || errors.Is(err, plugins.ErrClosed) {
		return errors.Join(ctx.Err(), err) // cancelled or stopped: not the plugin's failure
	}
	if err != nil {
		var count error
		if rp.native == nil || plugins.IsNativeFailure(err) || errors.Is(err, errPluginAnswer) {
			count = s.pluginFailed(ctx, name, rp.gen)
		} else {
			count = s.pluginSucceeded(ctx, name, rp.gen) // an envelope is an answer: failures are counted in a row
		}
		return errors.Join(fmt.Errorf("model plugin %s: %w", name, s.cleanError(err)), count)
	}
	return s.pluginSucceeded(ctx, name, rp.gen)
}
