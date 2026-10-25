package plugins

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Calcium-Ion/moejs"
	"github.com/Calcium-Ion/moejs/engine"
	"github.com/godwit-email/godwit/core/ai"
)

// Host is what the core gives a plugin's ctx. Permissions are checked before
// any of it is called. It has no way to send mail, and must never get one.
type Host interface {
	StorageGet(ctx context.Context, key string) (string, bool, error)
	StorageSet(ctx context.Context, key, value string) error
	StorageDelete(ctx context.Context, key string) error
	MailGet(ctx context.Context, id int64) (MailMessage, error)
	MailSearch(ctx context.Context, q string, limit int) ([]MailSummary, error)
	MailSetFlags(ctx context.Context, ids []int64, add, remove []string) error
	MailMove(ctx context.Context, ids []int64, folderID int64) error
	MailCreateDraft(ctx context.Context, d DraftInput) (int64, error)
	MailUpdateDraft(ctx context.Context, u DraftUpdate) (int64, error)
	LLMComplete(ctx context.Context, prompt string) (string, error)
	LLMDecide(ctx context.Context, input string, qs []Question) ([]Answer, error)
	Fetch(ctx context.Context, inv Invocation, req FetchRequest) (FetchResponse, error)
	AuditEgress(ctx context.Context, inv Invocation, dest, decision, reason string) error
}

// Host-side data limits.
const (
	MaxBodyBytes     = 256 << 10 // each of a message's text and html, as a plugin gets it
	MaxStorageValue  = 64 << 10
	MaxStorageTotal  = 1 << 20  // keys and values of one plugin
	MaxLLMBytes      = 64 << 10 // llm.complete prompt and answer; llm.decide input and question text
	MaxQuestions     = 128      // per llm.decide call: rejected beyond, never split
	MaxFetchBody     = 1 << 20
	maxSearch        = 50
	defaultSearch    = 20
	EventReceived    = "message.received"
	EventBeforeSend  = "message.beforeSend"
	permMailRead     = "mail:read"
	permMailWrite    = "mail:write"
	permLLM          = "llm"
	errOnlyDuringRun = "only during initialization"
)

var (
	toolNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	actionIDRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,40}$`)
	actionOn   = []string{"message", "thread", "selection", "draft"}
)

// Question and Answer are typed judge questions and answers, as llm.decide
// and Wit's classify_mail take and give them: the OpenAI Decisions API's
// shape (predicate, choice, score; a refusal answer), whatever the judge.
type (
	Question = ai.Question
	Answer   = ai.Answer
)

// ToolInfo is a registered tool; Write defaults to true.
type ToolInfo struct {
	Name, Description string
	Schema            map[string]any
	Write             bool
}

type ActionInfo struct {
	ID    string
	Label Text
	On    []string
}

// Plugin is a loaded plugin: its VM and what it registered during
// initialization. Registrations never change after Load. Its JS values are
// only touched on the VM's loop; a zero one is "none".
type Plugin struct {
	Name string
	m    Manifest
	h    Host
	vm   *VM

	events  map[string]moejs.Value
	tools   []tool
	actions []action
	card    *card

	// ctx.config is read once per call.
	cfgCall    *CallScope
	cfgVal     moejs.Value
	translator moejs.Value    // ctx.translators.register's translate
	models     *modelProvider // ctx.models.register's (models.go)
}

type tool struct {
	info ToolInfo
	run  moejs.Value
}

type action struct {
	info ActionInfo
	run  moejs.Value
}

type card struct{ match, render moejs.Value }

// VM is the plugin's runtime.
func (p *Plugin) VM() *VM { return p.vm }

// Close stops the plugin's runtime.
func (p *Plugin) Close() { p.vm.Close() }

func (p *Plugin) Tools() []ToolInfo {
	out := []ToolInfo{}
	for _, t := range p.tools {
		out = append(out, t.info)
	}
	return out
}

func (p *Plugin) Actions() []ActionInfo {
	out := []ActionInfo{}
	for _, a := range p.actions {
		out = append(out, a.info)
	}
	return out
}

func (p *Plugin) HasCard() bool { return p.card != nil }

// Events lists the events it handles, sorted.
func (p *Plugin) Events() []string {
	out := []string{}
	for e := range p.events {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// Handles reports whether the plugin registered a handler for event.
func (p *Plugin) Handles(event string) bool { return isFunction(p.events[event]) }

// Emit runs the plugin's handler for event with args (plain JSON-like Go
// data) as one top-level call under inv, awaiting it if it is async.
func (p *Plugin) Emit(ctx context.Context, inv Invocation, event string, args ...any) error {
	_, err := p.emit(ctx, inv, event, true, args)
	return err
}

// EmitResult is Emit that returns the handler's (awaited) result as plain
// JSON data.
func (p *Plugin) EmitResult(ctx context.Context, inv Invocation, event string, args ...any) (any, error) {
	return p.emit(ctx, inv, event, false, args)
}

func (p *Plugin) emit(ctx context.Context, inv Invocation, event string, discard bool, args []any) (any, error) {
	if !p.Handles(event) {
		return nil, fmt.Errorf("plugin %s has no %s handler", p.Name, event)
	}
	return p.call(ctx, inv, p.events[event], discard, args...)
}

// call runs f(args...) (plain JSON-like Go data, passed as JSON) as one
// top-level call under inv, awaiting it if it is async; discard drops a
// non-promise result.
func (p *Plugin) call(ctx context.Context, inv Invocation, f moejs.Value, discard bool, args ...any) (any, error) {
	return p.vm.Call(ctx, inv, func(c *CallScope) (moejs.Value, error) {
		rt := c.Runtime()
		vals := make([]moejs.Value, len(args))
		for i, a := range args {
			var err error
			if vals[i], err = jsonValue(rt, a); err != nil {
				return moejs.Undefined(), err
			}
		}
		res, err := rt.CallFunction(f, moejs.Undefined(), vals...)
		if _, _, ok := moejs.PromiseResult(res); discard && !ok {
			res = moejs.Undefined()
		}
		return res, err
	})
}

// RunAction runs the registered action id on target under inv and returns its
// (awaited) result as plain JSON data.
func (p *Plugin) RunAction(ctx context.Context, inv Invocation, id string, target any) (any, error) {
	for _, a := range p.actions {
		if a.info.ID == id {
			return p.call(ctx, inv, a.run, false, target)
		}
	}
	return nil, fmt.Errorf("plugin %s has no action %s", p.Name, id)
}

// RunTool runs the registered tool name with args under inv and returns its
// (awaited) result as plain JSON data.
func (p *Plugin) RunTool(ctx context.Context, inv Invocation, name string, args any) (any, error) {
	for _, t := range p.tools {
		if t.info.Name == name {
			return p.call(ctx, inv, t.run, false, args)
		}
	}
	return nil, fmt.Errorf("plugin %s has no tool %s", p.Name, name)
}

// Card runs the card's match(m) and, if its result is truthy, render(m), both
// under inv, and returns render's result; matched is false when match was not
// truthy. ctx bounds both calls together.
func (p *Plugin) Card(ctx context.Context, inv Invocation, m MailMessage) (res any, matched bool, err error) {
	if p.card == nil {
		return nil, false, fmt.Errorf("plugin %s has no card", p.Name)
	}
	msg := pluginMail(m) // passed as JSON: match never changes what render gets
	ok, err := p.call(ctx, inv, p.card.match, false, msg)
	if err != nil || !truthy(ok) {
		return nil, false, err
	}
	res, err = p.call(ctx, inv, p.card.render, false, msg)
	return res, true, err
}

// truthy is JavaScript truthiness of exported JSON data.
func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case float64:
		return v != 0 // JSON has no NaN
	case string:
		return v != ""
	}
	return true
}

// pluginMail is m as a plugin gets it: bodies cut to MaxBodyBytes, no nil lists.
func pluginMail(m MailMessage) MailMessage {
	var t1, t2 bool
	m.Text, t1 = cut(m.Text, MaxBodyBytes)
	m.HTML, t2 = cut(m.HTML, MaxBodyBytes)
	m.Truncated = m.Truncated || t1 || t2
	m.To, m.Cc = nonNil(m.To), nonNil(m.Cc)
	if m.Attachments == nil {
		m.Attachments = []AttachmentMeta{}
	}
	return m
}

// Load builds the ctx object and runs the plugin's initialization in an init
// invocation (no writes, no egress, cfg as ctx.config), recording what it registers.
func Load(ctx context.Context, vm *VM, m Manifest, h Host, cfg *ConfigSnapshot) (*Plugin, error) {
	p := &Plugin{Name: m.Name, m: m, h: h, vm: vm, events: map[string]moejs.Value{}}
	if err := vm.Init(ctx, initInvocation(m, cfg), p.newCtx); err != nil {
		return nil, err
	}
	return p, p.checkRegistered()
}

func initInvocation(m Manifest, cfg *ConfigSnapshot) Invocation {
	egress, _ := EgressFor(KindInit, m, true, false, false) // none: init never fetches
	return Invocation{ID: "init", Kind: KindInit, EgressHosts: egress, Config: cfg}
}

// checkRegistered: initialization registered what the manifest declares.
func (p *Plugin) checkRegistered() error {
	if slices.Contains(p.m.Capabilities, "translate") && !isFunction(p.translator) {
		return fmt.Errorf("plugin %s declares translate but registered no translator", p.Name)
	}
	if slices.Contains(p.m.Capabilities, "models") && p.models == nil {
		return fmt.Errorf("plugin %s declares models but registered none", p.Name)
	}
	return nil
}

// need is a PermissionError unless the manifest, and for writes the current
// invocation, allow perm.
func (p *Plugin) need(c *CallScope, perm string) error {
	var ok bool
	switch perm {
	case permMailRead:
		ok = slices.Contains(p.m.Permissions.Mail, "read")
	case permMailWrite:
		ok = slices.Contains(p.m.Permissions.Mail, "write") && c.inv.Write
	case permLLM:
		ok = p.m.Permissions.LLM
	}
	if !ok {
		return &PermissionError{Perm: perm}
	}
	return nil
}

func duringInit(c *CallScope) error {
	if c.inv.Kind != KindInit {
		return errors.New(errOnlyDuringRun)
	}
	return nil
}

func isFunction(v moejs.Value) bool { return v.IsObject() && v.AsObject().IsCallable() }

// jsonData turns a host result into plain JSON data (times as RFC 3339).
func jsonData(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	return out, json.Unmarshal(b, &out)
}

// cut keeps at most n bytes of s, at a rune boundary.
func cut(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// egressHost is the URL's host as manifests write it: lowercased, with the
// port only when it is not 443.
func egressHost(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", errors.New("invalid URL")
	}
	h := strings.ToLower(u.Hostname())
	if p := u.Port(); p != "" && p != "443" {
		h = net.JoinHostPort(h, p)
	}
	return h, nil
}

// fetchDenied audits a fetch refused before the host saw it and returns err,
// joined with the audit's failure if the row could not be written.
func (p *Plugin) fetchDenied(ctx context.Context, inv Invocation, raw, reason string, err error) error {
	u, _ := url.Parse(raw) // egressHost parsed it
	if aerr := p.h.AuditEgress(ctx, inv, HostPort(u), "deny", reason); aerr != nil {
		return errors.Join(err, aerr)
	}
	return err
}

// fetchURLAllowed refuses (audited) a ctx.net.fetch of a URL whose host the
// manifest does not declare or inv was not given; it runs before init is read.
func (p *Plugin) fetchURLAllowed(ctx context.Context, inv Invocation, raw string) error {
	host, err := egressHost(raw)
	if err != nil {
		return err
	}
	if declared := slices.Contains(p.m.Permissions.Net, host); !declared || !slices.Contains(inv.EgressHosts, host) {
		reason := "host_not_allowed"
		if declared && inv.Unapproved {
			reason = "approval_required"
		}
		return p.fetchDenied(ctx, inv, raw, reason, &PermissionError{Perm: "net:" + host})
	}
	return nil
}

// fetchInit is ctx.net.fetch's init, its own type: no field of it can replace
// the URL checked first.
type fetchInit struct {
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// fetchInvocation refuses (audited) an oversized body and is what the host
// fetches req under: redirects only to hosts both declared and granted.
func (p *Plugin) fetchInvocation(ctx context.Context, inv Invocation, req FetchRequest) (Invocation, error) {
	if len(req.Body) > MaxFetchBody {
		return inv, p.fetchDenied(ctx, inv, req.URL, "too_large", ErrTooLarge)
	}
	inv.EgressHosts = slices.DeleteFunc(slices.Clone(inv.EgressHosts), func(h string) bool { return !slices.Contains(p.m.Permissions.Net, h) })
	return inv, nil
}

// hostFn is a ctx function's Go side (VM.HostFunc).
type hostFn = func(c *CallScope, args []moejs.Value) (moejs.Value, error)

var undef = moejs.Undefined()

func (p *Plugin) newCtx(c *CallScope) (moejs.Value, error) {
	v, rt := c.vm, c.Runtime()
	r := rt.Realm()
	fn := v.HostFunc
	object := func(kv ...any) moejs.Value {
		o := r.NewObject()
		for i := 0; i < len(kv); i += 2 {
			_ = o.SetProp(r, engine.InternKey(kv[i].(string)), kv[i+1].(moejs.Value))
		}
		return engine.ObjectValue(o)
	}

	mail := object(
		"get", fn("mail.get", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
			var id int64
			if err := cmp.Or(p.need(c, permMailRead), c.arg("mail.get", moejs.Arg(args, 0), &id, "id must be a message id")); err != nil {
				return undef, err
			}
			return c.AsyncJSON(func(ctx context.Context) (any, error) {
				m, err := p.h.MailGet(ctx, id)
				if err != nil {
					return nil, err
				}
				return pluginMail(m), nil
			}), nil
		}),
		"search", fn("mail.search", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
			const name = "mail.search"
			var q string
			if err := p.need(c, permMailRead); err != nil {
				return undef, err
			}
			if err := c.arg(name, moejs.Arg(args, 0), &q, "query must be a string"); err != nil {
				return undef, err
			}
			limit := defaultSearch
			if l := moejs.Arg(args, 1); !l.IsNullish() {
				if err := c.arg(name, l, &limit, "limit must be an integer"); err != nil {
					return undef, err
				}
			}
			if limit <= 0 {
				limit = defaultSearch
			}
			limit = min(limit, maxSearch)
			return c.AsyncJSON(func(ctx context.Context) (any, error) {
				r, err := p.h.MailSearch(ctx, q, limit)
				if r == nil {
					r = []MailSummary{}
				}
				return r, err
			}), nil
		}),
		"setFlags", fn("mail.setFlags", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
			const name = "mail.setFlags"
			var ids []int64
			var add, remove []string
			if err := p.need(c, permMailWrite); err != nil {
				return undef, err
			}
			if err := cmp.Or(c.arg(name, moejs.Arg(args, 0), &ids, "ids must be message ids"),
				c.arg(name, moejs.Arg(args, 1), &add, "add must be a list of flags"),
				c.arg(name, moejs.Arg(args, 2), &remove, "remove must be a list of flags")); err != nil {
				return undef, err
			}
			return c.AsyncJSON(func(ctx context.Context) (any, error) { return nil, p.h.MailSetFlags(ctx, ids, add, remove) }), nil
		}),
		"move", fn("mail.move", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
			const name = "mail.move"
			var ids []int64
			var folder int64
			if err := p.need(c, permMailWrite); err != nil {
				return undef, err
			}
			if err := cmp.Or(c.arg(name, moejs.Arg(args, 0), &ids, "ids must be message ids"),
				c.arg(name, moejs.Arg(args, 1), &folder, "folderId must be a folder id")); err != nil {
				return undef, err
			}
			return c.AsyncJSON(func(ctx context.Context) (any, error) { return nil, p.h.MailMove(ctx, ids, folder) }), nil
		}),
		"createDraft", fn("mail.createDraft", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
			var d DraftInput
			if err := p.need(c, permMailWrite); err != nil {
				return undef, err
			}
			if err := c.arg("mail.createDraft", moejs.Arg(args, 0), &d, "draft must be {accountId, to, cc?, subject, html}"); err != nil {
				return undef, err
			}
			return c.AsyncJSON(func(ctx context.Context) (any, error) { return p.h.MailCreateDraft(ctx, d) }), nil
		}),
		"updateDraft", fn("mail.updateDraft", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
			var u DraftUpdate
			if err := p.need(c, permMailWrite); err != nil {
				return undef, err
			}
			if err := c.arg("mail.updateDraft", moejs.Arg(args, 0), &u, "update must be {draftId, revision, html}"); err != nil {
				return undef, err
			}
			return c.AsyncJSON(func(ctx context.Context) (any, error) { return p.h.MailUpdateDraft(ctx, u) }), nil
		}),
	)

	llm := object(
		"complete", fn("llm.complete", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
			var prompt string
			if err := p.need(c, permLLM); err != nil {
				return undef, err
			}
			if err := c.arg("llm.complete", moejs.Arg(args, 0), &prompt, "prompt must be a string"); err != nil {
				return undef, err
			}
			if len(prompt) > MaxLLMBytes {
				return undef, ErrTooLarge
			}
			return c.AsyncJSON(func(ctx context.Context) (any, error) {
				out, err := p.h.LLMComplete(ctx, prompt)
				out, _ = cut(out, MaxLLMBytes)
				return out, err
			}), nil
		}),
		"decide", fn("llm.decide", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
			const name = "llm.decide"
			var input string
			var qs []Question
			if err := p.need(c, permLLM); err != nil {
				return undef, err
			}
			if err := cmp.Or(c.arg(name, moejs.Arg(args, 0), &input, "input must be a string"),
				c.arg(name, moejs.Arg(args, 1), &qs, "questions must be a list of {name, type, instructions, choices?, levels?}")); err != nil {
				return undef, err
			}
			if len(qs) > MaxQuestions {
				return undef, typeError(name + ": at most 128 questions")
			}
			if len(input)+QuestionBytes(qs) > MaxLLMBytes {
				return undef, ErrTooLarge
			}
			return c.AsyncJSON(func(ctx context.Context) (any, error) { return p.h.LLMDecide(ctx, input, qs) }), nil
		}),
	)

	netObj := object("fetch", fn("net.fetch", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
		var req FetchRequest
		if err := c.arg("net.fetch", moejs.Arg(args, 0), &req.URL, "url must be a string"); err != nil {
			return undef, err
		}
		if err := p.fetchURLAllowed(c.ctx, c.inv, req.URL); err != nil {
			return undef, err
		}
		if init := moejs.Arg(args, 1); !init.IsNullish() {
			var in fetchInit
			if err := c.arg("net.fetch", init, &in, "init must be {method?, headers?, body?}"); errors.Is(err, ErrTooLarge) {
				return undef, p.fetchDenied(c.ctx, c.inv, req.URL, "too_large", ErrTooLarge) // an oversized body
			} else if err != nil {
				return undef, err
			}
			req.Method, req.Headers, req.Body = in.Method, in.Headers, in.Body
		}
		inv, err := p.fetchInvocation(c.ctx, c.inv, req)
		if err != nil {
			return undef, err
		}
		return c.Async(func(ctx context.Context) (func(*moejs.Runtime) (moejs.Value, error), error) {
			res, err := p.h.Fetch(ctx, inv, req)
			if err != nil {
				return nil, err
			}
			if res.Headers == nil {
				res.Headers = map[string]string{}
			}
			return func(rt *moejs.Runtime) (moejs.Value, error) {
				o, err := jsonValue(rt, map[string]any{"status": res.Status, "headers": res.Headers})
				if err != nil {
					return undef, err
				}
				text := rt.Function("text", 0, func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
					pr, resolve, _ := rt.NewPromise()
					_ = resolve(moejs.String(res.Body))
					return pr, nil
				})
				return o, o.AsObject().SetProp(rt.Realm(), engine.InternKey("text"), text)
			}, nil
		}), nil
	}))

	storage := object(
		"get", fn("storage.get", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
			var k string
			if err := c.arg("storage.get", moejs.Arg(args, 0), &k, "key must be a string"); err != nil {
				return undef, err
			}
			return c.AsyncJSON(func(ctx context.Context) (any, error) {
				v, ok, err := p.h.StorageGet(ctx, k)
				if err != nil || !ok {
					return nil, err
				}
				return v, nil
			}), nil
		}),
		"set", fn("storage.set", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
			const name = "storage.set"
			var k, val string
			if err := cmp.Or(c.arg(name, moejs.Arg(args, 0), &k, "key must be a string"),
				c.arg(name, moejs.Arg(args, 1), &val, "value must be a string")); err != nil {
				return undef, err
			}
			if len(val) > MaxStorageValue {
				return undef, ErrTooLarge
			}
			return c.AsyncJSON(func(ctx context.Context) (any, error) { return nil, p.h.StorageSet(ctx, k, val) }), nil
		}),
		"delete", fn("storage.delete", func(c *CallScope, args []moejs.Value) (moejs.Value, error) {
			var k string
			if err := c.arg("storage.delete", moejs.Arg(args, 0), &k, "key must be a string"); err != nil {
				return undef, err
			}
			return c.AsyncJSON(func(ctx context.Context) (any, error) { return nil, p.h.StorageDelete(ctx, k) }), nil
		}),
	)

	o := object(
		"on", fn("on", p.on),
		"actions", object("register", fn("actions.register", p.registerAction)),
		"cards", object("register", fn("cards.register", p.registerCard)),
		"tools", object("register", fn("tools.register", p.registerTool)),
		"translators", object("register", fn("translators.register", p.registerTranslator)),
		"models", object("register", fn("models.register", p.registerModels)),
		"mail", mail, "llm", llm, "net", netObj, "storage", storage,
	)
	var config engine.PropertyDescriptor
	config.SetGet(fn("config", func(c *CallScope, _ []moejs.Value) (moejs.Value, error) {
		if p.cfgCall != c { // the invocation's snapshot, copied: what the plugin does to it stays in its call
			vals, err := configData(c.inv.Config)
			if err != nil {
				return undef, err
			}
			if p.cfgVal, err = jsonValue(rt, vals); err != nil {
				return undef, err
			}
			p.cfgCall = c
		}
		return p.cfgVal, nil
	}))
	config.SetEnumerable(true)
	config.SetConfigurable(false)
	return o, o.AsObject().DefinePropertyOrThrow(r, engine.InternKey("config"), config)
}

// configData is ctx.config: cfg's values as fresh JSON data.
func configData(cfg *ConfigSnapshot) (any, error) {
	if cfg == nil || cfg.Err != nil {
		return nil, errors.New("the plugin's configuration could not be read")
	}
	vals, err := jsonData(cfg.Values)
	if vals == nil && err == nil {
		vals = map[string]any{}
	}
	return vals, err
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func (p *Plugin) on(c *CallScope, args []moejs.Value) (moejs.Value, error) {
	var event string
	if err := cmp.Or(duringInit(c), p.need(c, permMailRead), // handlers get mail data
		c.arg("on", moejs.Arg(args, 0), &event, "event must be a string")); err != nil {
		return undef, err
	}
	if event != EventReceived && event != EventBeforeSend {
		return undef, fmt.Errorf("unknown event %q", event)
	}
	h := moejs.Arg(args, 1)
	if !isFunction(h) {
		return undef, typeError("on: handler must be a function")
	}
	if p.Handles(event) {
		return undef, fmt.Errorf("a %s handler is already registered", event)
	}
	p.events[event] = h
	return undef, nil
}

// field reads property key of the registration object o into out.
func (c *CallScope) field(fn string, o moejs.Value, key string, out any, what string) error {
	v, err := c.vm.rt.Get(o, key)
	if err != nil {
		return err
	}
	return c.arg(fn, v, out, what)
}

// function reads property key of o, which must be a function.
func (c *CallScope) function(fn string, o moejs.Value, key string) (moejs.Value, error) {
	f, err := c.vm.rt.Get(o, key)
	if err == nil && !isFunction(f) {
		err = typeError(fn + ": " + key + " must be a function")
	}
	return f, err
}

func (p *Plugin) registerTool(c *CallScope, args []moejs.Value) (moejs.Value, error) {
	const fn = "tools.register"
	o := moejs.Arg(args, 0)
	t := tool{info: ToolInfo{Write: true}}
	if err := cmp.Or(duringInit(c), c.field(fn, o, "name", &t.info.Name, "name must be a string")); err != nil {
		return undef, err
	}
	if !toolNameRE.MatchString(t.info.Name) {
		return undef, typeError(fn + ": name must match ^[a-z][a-z0-9_]{0,40}$")
	}
	if err := cmp.Or(c.field(fn, o, "description", &t.info.Description, "description must be a string"),
		c.field(fn, o, "schema", &t.info.Schema, "schema must be a JSON Schema object")); err != nil {
		return undef, err
	}
	w, err := c.vm.rt.Get(o, "write")
	if err == nil && !w.IsNullish() {
		err = c.arg(fn, w, &t.info.Write, "write must be a boolean")
	}
	if err != nil {
		return undef, err
	}
	if t.run, err = c.function(fn, o, "run"); err != nil {
		return undef, err
	}
	if slices.ContainsFunc(p.tools, func(x tool) bool { return x.info.Name == t.info.Name }) {
		return undef, fmt.Errorf("tool %s is already registered", t.info.Name)
	}
	p.tools = append(p.tools, t)
	return undef, nil
}

func (p *Plugin) registerAction(c *CallScope, args []moejs.Value) (moejs.Value, error) {
	const fn = "actions.register"
	o := moejs.Arg(args, 0)
	var a action
	if err := cmp.Or(duringInit(c), c.field(fn, o, "id", &a.info.ID, "id must be a string")); err != nil {
		return undef, err
	}
	if !actionIDRE.MatchString(a.info.ID) {
		return undef, typeError(fn + ": id must match ^[a-z][a-z0-9_-]{0,40}$")
	}
	if err := cmp.Or(c.field(fn, o, "label", &a.info.Label, "label must be a string or {default, ...}"),
		c.field(fn, o, "on", &a.info.On, "on must be a list")); err != nil {
		return undef, err
	}
	if len(a.info.On) == 0 || slices.ContainsFunc(a.info.On, func(k string) bool { return !slices.Contains(actionOn, k) }) {
		return undef, typeError(fn + ": on must list message, thread, selection or draft")
	}
	var err error
	if a.run, err = c.function(fn, o, "run"); err != nil {
		return undef, err
	}
	if slices.ContainsFunc(p.actions, func(x action) bool { return x.info.ID == a.info.ID }) {
		return undef, fmt.Errorf("action %s is already registered", a.info.ID)
	}
	p.actions = append(p.actions, a)
	return undef, nil
}

func (p *Plugin) registerTranslator(c *CallScope, args []moejs.Value) (moejs.Value, error) {
	const fn = "translators.register"
	if err := duringInit(c); err != nil {
		return undef, err
	}
	if !slices.Contains(p.m.Capabilities, "translate") {
		return undef, typeError(fn + ": plugin.json does not declare the translate capability")
	}
	f, err := c.function(fn, moejs.Arg(args, 0), "translate")
	if err != nil {
		return undef, err
	}
	if isFunction(p.translator) {
		return undef, errors.New("a translator is already registered")
	}
	p.translator = f
	return undef, nil
}

// Translate runs the registered translator on req under inv and returns its
// (awaited) result as plain JSON data.
func (p *Plugin) Translate(ctx context.Context, inv Invocation, req any) (any, error) {
	if !isFunction(p.translator) {
		return nil, fmt.Errorf("plugin %s has no translator", p.Name)
	}
	return p.call(ctx, inv, p.translator, false, req)
}

func (p *Plugin) registerCard(c *CallScope, args []moejs.Value) (moejs.Value, error) {
	const fn = "cards.register"
	if err := cmp.Or(duringInit(c), p.need(c, permMailRead)); err != nil {
		return undef, err
	}
	o := moejs.Arg(args, 0)
	match, err := c.function(fn, o, "match")
	if err != nil {
		return undef, err
	}
	render, err := c.function(fn, o, "render")
	if err != nil {
		return undef, err
	}
	if p.card != nil {
		return undef, errors.New("a card is already registered")
	}
	p.card = &card{match: match, render: render}
	return undef, nil
}

// QuestionBytes is the size of the questions' text: instructions, values and descriptions.
func QuestionBytes(qs []Question) int {
	n := 0
	for _, q := range qs {
		n += len(q.Instructions)
		for _, o := range q.Choices {
			n += len(o.Value) + len(o.Description)
		}
		for _, l := range q.Levels {
			n += len(l.Label) + len(l.Description)
		}
	}
	return n
}
