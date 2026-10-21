package plugins

import (
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

	"github.com/dop251/goja"
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
// initialization. Registrations never change after Load.
type Plugin struct {
	Name string
	m    Manifest
	h    Host
	vm   *VM

	events  map[string]goja.Callable
	tools   []tool
	actions []action
	card    *card

	// ctx.config is read once per call (owner goroutine only).
	cfgCall    *CallScope
	cfgVal     goja.Value
	translator goja.Callable  // ctx.translators.register's translate
	models     *modelProvider // ctx.models.register's (models.go)
}

type tool struct {
	info ToolInfo
	run  goja.Callable
}

type action struct {
	info ActionInfo
	run  goja.Callable
}

type card struct{ match, render goja.Callable }

func (p *Plugin) VM() *VM { return p.vm }

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
func (p *Plugin) Handles(event string) bool { return p.events[event] != nil }

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
	h := p.events[event]
	if h == nil {
		return nil, fmt.Errorf("plugin %s has no %s handler", p.Name, event)
	}
	return p.call(ctx, inv, h, discard, args...)
}

// call runs f(args...) (plain JSON-like Go data) as one top-level call under
// inv, awaiting it if it is async; discard drops a non-promise result.
func (p *Plugin) call(ctx context.Context, inv Invocation, f goja.Callable, discard bool, args ...any) (any, error) {
	return p.vm.Call(ctx, inv, func(c *CallScope) (any, error) {
		vals := make([]goja.Value, len(args))
		for i, a := range args {
			vals[i] = c.Runtime().ToValue(a)
		}
		res, err := f(goja.Undefined(), vals...)
		if err != nil {
			return nil, err
		}
		if discard {
			return p.vm.discard(res), nil
		}
		return res, nil
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
	msg, err := jsonData(pluginMail(m))
	if err != nil {
		return nil, false, err
	}
	ok, err := p.call(ctx, inv, p.card.match, false, msg)
	if err != nil || !truthy(ok) {
		return nil, false, err
	}
	if msg, err = jsonData(pluginMail(m)); err != nil { // a fresh copy: match never changes what render gets
		return nil, true, err
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
	p := &Plugin{Name: m.Name, m: m, h: h, vm: vm, events: map[string]goja.Callable{}}
	egress, _ := EgressFor(KindInit, m, true, false, false) // none: init never fetches
	if err := vm.Init(ctx, Invocation{ID: "init", Kind: KindInit, EgressHosts: egress, Config: cfg}, p.newCtx); err != nil {
		return nil, err
	}
	if slices.Contains(m.Capabilities, "translate") && p.translator == nil {
		return nil, fmt.Errorf("plugin %s declares translate but registered no translator", m.Name)
	}
	if slices.Contains(m.Capabilities, "models") && p.models == nil {
		return nil, fmt.Errorf("plugin %s declares models but registered none", m.Name)
	}
	return p, nil
}

// need panics with a PermissionError unless the manifest, and for writes the
// current invocation, allow perm.
func (p *Plugin) need(c *CallScope, perm string) {
	var ok bool
	switch perm {
	case permMailRead:
		ok = slices.Contains(p.m.Permissions.Mail, "read")
	case permMailWrite:
		ok = slices.Contains(p.m.Permissions.Mail, "write") && c.Invocation().Write
	case permLLM:
		ok = p.m.Permissions.LLM
	}
	if !ok {
		panic(&PermissionError{Perm: perm})
	}
}

func duringInit(c *CallScope) {
	if c.Invocation().Kind != KindInit {
		panic(errors.New(errOnlyDuringRun))
	}
}

// arg decodes JS value val into out through JSON; anything else is a TypeError.
func arg(c *CallScope, fn string, val goja.Value, out any, what string) {
	bad := func() { panic(c.Runtime().NewTypeError(fn + ": " + what)) }
	if optional(val) { // null too: it would decode as the zero value
		bad()
	}
	s, err := c.vm.stringify(goja.Undefined(), val)
	if err != nil || goja.IsUndefined(s) {
		bad()
	}
	if len(s.String()) > MaxResultBytes {
		panic(ErrTooLarge)
	}
	if json.Unmarshal([]byte(s.String()), out) != nil {
		bad()
	}
}

func optional(v goja.Value) bool { return v == nil || goja.IsUndefined(v) || goja.IsNull(v) }

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

// denyFetch audits a fetch refused before the host saw it, then throws err,
// joined with the audit's failure if the row could not be written.
func (p *Plugin) denyFetch(c *CallScope, raw, reason string, err error) {
	u, _ := url.Parse(raw) // egressHost parsed it
	if aerr := p.h.AuditEgress(c.ctx, c.Invocation(), HostPort(u), "deny", reason); aerr != nil {
		panic(errors.Join(err, aerr))
	}
	panic(err)
}

func (p *Plugin) newCtx(c *CallScope) goja.Value {
	rt, v := c.Runtime(), c.VM()
	fn := v.HostFunc
	object := func(kv ...any) *goja.Object {
		o := rt.NewObject()
		for i := 0; i < len(kv); i += 2 {
			_ = o.Set(kv[i].(string), kv[i+1])
		}
		return o
	}
	async := func(c *CallScope, f func(ctx context.Context) (any, error)) goja.Value {
		return c.Async(func(ctx context.Context) (any, error) {
			r, err := f(ctx)
			if err != nil || r == nil {
				return nil, err
			}
			return jsonData(r)
		})
	}

	mail := object(
		"get", fn("mail.get", func(c *CallScope, call goja.FunctionCall) goja.Value {
			p.need(c, permMailRead)
			var id int64
			arg(c, "mail.get", call.Argument(0), &id, "id must be a message id")
			return async(c, func(ctx context.Context) (any, error) {
				m, err := p.h.MailGet(ctx, id)
				if err != nil {
					return nil, err
				}
				return pluginMail(m), nil
			})
		}),
		"search", fn("mail.search", func(c *CallScope, call goja.FunctionCall) goja.Value {
			p.need(c, permMailRead)
			var q string
			arg(c, "mail.search", call.Argument(0), &q, "query must be a string")
			limit := defaultSearch
			if l := call.Argument(1); !optional(l) {
				arg(c, "mail.search", l, &limit, "limit must be an integer")
			}
			if limit <= 0 {
				limit = defaultSearch
			}
			limit = min(limit, maxSearch)
			return async(c, func(ctx context.Context) (any, error) {
				r, err := p.h.MailSearch(ctx, q, limit)
				if r == nil {
					r = []MailSummary{}
				}
				return r, err
			})
		}),
		"setFlags", fn("mail.setFlags", func(c *CallScope, call goja.FunctionCall) goja.Value {
			p.need(c, permMailWrite)
			var ids []int64
			var add, remove []string
			arg(c, "mail.setFlags", call.Argument(0), &ids, "ids must be message ids")
			arg(c, "mail.setFlags", call.Argument(1), &add, "add must be a list of flags")
			arg(c, "mail.setFlags", call.Argument(2), &remove, "remove must be a list of flags")
			return async(c, func(ctx context.Context) (any, error) { return nil, p.h.MailSetFlags(ctx, ids, add, remove) })
		}),
		"move", fn("mail.move", func(c *CallScope, call goja.FunctionCall) goja.Value {
			p.need(c, permMailWrite)
			var ids []int64
			var folder int64
			arg(c, "mail.move", call.Argument(0), &ids, "ids must be message ids")
			arg(c, "mail.move", call.Argument(1), &folder, "folderId must be a folder id")
			return async(c, func(ctx context.Context) (any, error) { return nil, p.h.MailMove(ctx, ids, folder) })
		}),
		"createDraft", fn("mail.createDraft", func(c *CallScope, call goja.FunctionCall) goja.Value {
			p.need(c, permMailWrite)
			var d DraftInput
			arg(c, "mail.createDraft", call.Argument(0), &d, "draft must be {accountId, to, cc?, subject, html}")
			return async(c, func(ctx context.Context) (any, error) { return p.h.MailCreateDraft(ctx, d) })
		}),
		"updateDraft", fn("mail.updateDraft", func(c *CallScope, call goja.FunctionCall) goja.Value {
			p.need(c, permMailWrite)
			var u DraftUpdate
			arg(c, "mail.updateDraft", call.Argument(0), &u, "update must be {draftId, revision, html}")
			return async(c, func(ctx context.Context) (any, error) { return p.h.MailUpdateDraft(ctx, u) })
		}),
	)

	llm := object("complete", fn("llm.complete", func(c *CallScope, call goja.FunctionCall) goja.Value {
		p.need(c, permLLM)
		var prompt string
		arg(c, "llm.complete", call.Argument(0), &prompt, "prompt must be a string")
		if len(prompt) > MaxLLMBytes {
			panic(ErrTooLarge)
		}
		return async(c, func(ctx context.Context) (any, error) {
			out, err := p.h.LLMComplete(ctx, prompt)
			out, _ = cut(out, MaxLLMBytes)
			return out, err
		})
	}), "decide", fn("llm.decide", func(c *CallScope, call goja.FunctionCall) goja.Value {
		p.need(c, permLLM)
		var input string
		var qs []Question
		arg(c, "llm.decide", call.Argument(0), &input, "input must be a string")
		arg(c, "llm.decide", call.Argument(1), &qs, "questions must be a list of {name, type, instructions, choices?, levels?}")
		if len(qs) > MaxQuestions {
			panic(c.Runtime().NewTypeError("llm.decide: at most 128 questions"))
		}
		if len(input)+QuestionBytes(qs) > MaxLLMBytes {
			panic(ErrTooLarge)
		}
		return async(c, func(ctx context.Context) (any, error) { return p.h.LLMDecide(ctx, input, qs) })
	}))

	netObj := object("fetch", fn("net.fetch", func(c *CallScope, call goja.FunctionCall) goja.Value {
		var req FetchRequest
		arg(c, "net.fetch", call.Argument(0), &req.URL, "url must be a string")
		host, err := egressHost(req.URL)
		if err != nil {
			panic(err)
		}
		if declared := slices.Contains(p.m.Permissions.Net, host); !declared || !slices.Contains(c.Invocation().EgressHosts, host) {
			reason := "host_not_allowed"
			if declared && c.Invocation().Unapproved {
				reason = "approval_required"
			}
			p.denyFetch(c, req.URL, reason, &PermissionError{Perm: "net:" + host})
		}
		if init := call.Argument(1); !optional(init) {
			// Its own type: no field of init can replace the URL checked above.
			var in struct {
				Method  string            `json:"method"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			}
			func() {
				defer func() { // an init over the argument limit is an oversized body: audited
					if r := recover(); r == ErrTooLarge {
						p.denyFetch(c, req.URL, "too_large", ErrTooLarge)
					} else if r != nil {
						panic(r)
					}
				}()
				arg(c, "net.fetch", init, &in, "init must be {method?, headers?, body?}")
			}()
			req.Method, req.Headers, req.Body = in.Method, in.Headers, in.Body
		}
		if len(req.Body) > MaxFetchBody {
			p.denyFetch(c, req.URL, "too_large", ErrTooLarge)
		}
		inv, rt := c.Invocation(), c.Runtime()
		// The host allows redirects only to hosts both declared and granted.
		inv.EgressHosts = slices.DeleteFunc(slices.Clone(inv.EgressHosts), func(h string) bool { return !slices.Contains(p.m.Permissions.Net, h) })
		return c.Async(func(ctx context.Context) (any, error) {
			r, err := p.h.Fetch(ctx, inv, req)
			if err != nil {
				return nil, err
			}
			if r.Headers == nil {
				r.Headers = map[string]string{}
			}
			return map[string]any{"status": r.Status, "headers": r.Headers,
				"text": func(goja.FunctionCall) goja.Value { // runs on the owner, when JS calls it
					pr, resolve, _ := rt.NewPromise()
					_ = resolve(rt.ToValue(r.Body))
					return rt.ToValue(pr)
				}}, nil
		})
	}))

	storage := object(
		"get", fn("storage.get", func(c *CallScope, call goja.FunctionCall) goja.Value {
			var k string
			arg(c, "storage.get", call.Argument(0), &k, "key must be a string")
			return c.Async(func(ctx context.Context) (any, error) {
				v, ok, err := p.h.StorageGet(ctx, k)
				if err != nil || !ok {
					return nil, err
				}
				return v, nil
			})
		}),
		"set", fn("storage.set", func(c *CallScope, call goja.FunctionCall) goja.Value {
			var k, val string
			arg(c, "storage.set", call.Argument(0), &k, "key must be a string")
			arg(c, "storage.set", call.Argument(1), &val, "value must be a string")
			if len(val) > MaxStorageValue {
				panic(ErrTooLarge)
			}
			return c.Async(func(ctx context.Context) (any, error) { return nil, p.h.StorageSet(ctx, k, val) })
		}),
		"delete", fn("storage.delete", func(c *CallScope, call goja.FunctionCall) goja.Value {
			var k string
			arg(c, "storage.delete", call.Argument(0), &k, "key must be a string")
			return c.Async(func(ctx context.Context) (any, error) { return nil, p.h.StorageDelete(ctx, k) })
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
	_ = o.DefineAccessorProperty("config", fn("config", func(c *CallScope, _ goja.FunctionCall) goja.Value {
		if p.cfgCall != c { // the invocation's snapshot, copied: what the plugin does to it stays in its call
			cfg := c.Invocation().Config
			if cfg == nil || cfg.Err != nil {
				panic(errors.New("the plugin's configuration could not be read"))
			}
			vals, err := jsonData(cfg.Values)
			if err != nil {
				panic(err)
			}
			if vals == nil {
				vals = map[string]any{}
			}
			p.cfgCall, p.cfgVal = c, c.Runtime().ToValue(vals)
		}
		return p.cfgVal
	}), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
	return o
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func (p *Plugin) on(c *CallScope, call goja.FunctionCall) goja.Value {
	duringInit(c)
	p.need(c, permMailRead) // handlers get mail data
	var event string
	arg(c, "on", call.Argument(0), &event, "event must be a string")
	if event != EventReceived && event != EventBeforeSend {
		panic(fmt.Errorf("unknown event %q", event))
	}
	h, ok := goja.AssertFunction(call.Argument(1))
	if !ok {
		panic(c.Runtime().NewTypeError("on: handler must be a function"))
	}
	if p.events[event] != nil {
		panic(fmt.Errorf("a %s handler is already registered", event))
	}
	p.events[event] = h
	return goja.Undefined()
}

// field reads a property of the registration object o.
func field(c *CallScope, fn string, o *goja.Object, key string, out any, what string) {
	arg(c, fn, o.Get(key), out, what)
}

func function(c *CallScope, fn string, o *goja.Object, key string) goja.Callable {
	f, ok := goja.AssertFunction(o.Get(key))
	if !ok {
		panic(c.Runtime().NewTypeError(fn + ": " + key + " must be a function"))
	}
	return f
}

func (p *Plugin) registerTool(c *CallScope, call goja.FunctionCall) goja.Value {
	const fn = "tools.register"
	duringInit(c)
	o := call.Argument(0).ToObject(c.Runtime())
	t := tool{info: ToolInfo{Write: true}}
	field(c, fn, o, "name", &t.info.Name, "name must be a string")
	if !toolNameRE.MatchString(t.info.Name) {
		panic(c.Runtime().NewTypeError(fn + ": name must match ^[a-z][a-z0-9_]{0,40}$"))
	}
	field(c, fn, o, "description", &t.info.Description, "description must be a string")
	field(c, fn, o, "schema", &t.info.Schema, "schema must be a JSON Schema object")
	if w := o.Get("write"); !optional(w) {
		field(c, fn, o, "write", &t.info.Write, "write must be a boolean")
	}
	t.run = function(c, fn, o, "run")
	if slices.ContainsFunc(p.tools, func(x tool) bool { return x.info.Name == t.info.Name }) {
		panic(fmt.Errorf("tool %s is already registered", t.info.Name))
	}
	p.tools = append(p.tools, t)
	return goja.Undefined()
}

func (p *Plugin) registerAction(c *CallScope, call goja.FunctionCall) goja.Value {
	const fn = "actions.register"
	duringInit(c)
	o := call.Argument(0).ToObject(c.Runtime())
	var a action
	field(c, fn, o, "id", &a.info.ID, "id must be a string")
	if !actionIDRE.MatchString(a.info.ID) {
		panic(c.Runtime().NewTypeError(fn + ": id must match ^[a-z][a-z0-9_-]{0,40}$"))
	}
	field(c, fn, o, "label", &a.info.Label, "label must be a string or {default, ...}")
	field(c, fn, o, "on", &a.info.On, "on must be a list")
	if len(a.info.On) == 0 || slices.ContainsFunc(a.info.On, func(k string) bool { return !slices.Contains(actionOn, k) }) {
		panic(c.Runtime().NewTypeError(fn + ": on must list message, thread, selection or draft"))
	}
	a.run = function(c, fn, o, "run")
	if slices.ContainsFunc(p.actions, func(x action) bool { return x.info.ID == a.info.ID }) {
		panic(fmt.Errorf("action %s is already registered", a.info.ID))
	}
	p.actions = append(p.actions, a)
	return goja.Undefined()
}

func (p *Plugin) registerTranslator(c *CallScope, call goja.FunctionCall) goja.Value {
	const fn = "translators.register"
	duringInit(c)
	if !slices.Contains(p.m.Capabilities, "translate") {
		panic(c.Runtime().NewTypeError(fn + ": plugin.json does not declare the translate capability"))
	}
	o := call.Argument(0).ToObject(c.Runtime())
	f := function(c, fn, o, "translate")
	if p.translator != nil {
		panic(errors.New("a translator is already registered"))
	}
	p.translator = f
	return goja.Undefined()
}

// Translate runs the registered translator on req under inv and returns its
// (awaited) result as plain JSON data.
func (p *Plugin) Translate(ctx context.Context, inv Invocation, req any) (any, error) {
	if p.translator == nil {
		return nil, fmt.Errorf("plugin %s has no translator", p.Name)
	}
	return p.call(ctx, inv, p.translator, false, req)
}

func (p *Plugin) registerCard(c *CallScope, call goja.FunctionCall) goja.Value {
	const fn = "cards.register"
	duringInit(c)
	p.need(c, permMailRead)
	o := call.Argument(0).ToObject(c.Runtime())
	cd := &card{match: function(c, fn, o, "match"), render: function(c, fn, o, "render")}
	if p.card != nil {
		panic(errors.New("a card is already registered"))
	}
	p.card = cd
	return goja.Undefined()
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
