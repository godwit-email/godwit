package plugins

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Calcium-Ion/moejs"
	"github.com/Calcium-Ion/moejs/engine"
	"github.com/dop251/goja"
)

// RunsOnMoejs reports whether script plugin m runs on moejs when the Labs
// switch (Settings.PiEngine) is on (#237): a translator that holds neither
// mail nor llm, so the translate capability's ctx (translators, config, net,
// storage) is all it can use. Everything else stays on goja until #238.
func RunsOnMoejs(m Manifest) bool {
	return !m.IsNative() && slices.Contains(m.Capabilities, "translate") && !slices.Contains(m.Capabilities, "models") &&
		len(m.Permissions.Mail) == 0 && !m.Permissions.LLM
}

// ErrNeedsGoja is LoadMoe's error for a plugin whose initialization reached for
// a ctx API that has not moved to moejs (a tool or an action, say): the host
// loads it on goja instead, where it works as before.
var ErrNeedsGoja = errors.New("plugin: needs the goja runtime")

// moePlugin is a Plugin's moejs side: what its initialization registered.
type moePlugin struct {
	vm         *MoeVM
	translator moejs.Value // ctx.translators.register's translate, or undefined
	needsGoja  bool

	cfgCall *MoeScope // ctx.config is read once per call
	cfgVal  moejs.Value
}

// gojaOnly are the ctx APIs a plugin on moejs does not have yet. During
// initialization any of them sends the plugin back to goja; after it, they
// fail as goja fails them for a plugin RunsOnMoejs picks: registrations only
// during initialization, mail and llm without the permission.
var gojaOnly = map[string][]string{
	"":        {"on"},
	"actions": {"register"}, "cards": {"register"}, "tools": {"register"}, "models": {"register"},
	"mail": {"get", "search", "setFlags", "move", "createDraft", "updateDraft"},
	"llm":  {"complete", "decide"},
}

var gojaOnlyPerm = map[string]string{"mail": permMailRead, "llm": permLLM}

// LoadMoe is Load on moejs: it builds the ctx object and runs the plugin's
// initialization in an init invocation, recording what it registers. It
// returns ErrNeedsGoja when the plugin needs goja.
func LoadMoe(ctx context.Context, vm *MoeVM, m Manifest, h Host, cfg *ConfigSnapshot) (*Plugin, error) {
	mp := &moePlugin{vm: vm, translator: moejs.Undefined()}
	p := &Plugin{Name: m.Name, m: m, h: h, moe: mp, events: map[string]goja.Callable{}}
	err := vm.Init(ctx, initInvocation(m, cfg), p.newMoeCtx)
	if mp.needsGoja { // even if the plugin caught the refusal
		return nil, ErrNeedsGoja
	}
	if err != nil {
		return nil, err
	}
	return p, p.checkRegistered()
}

func (mp *moePlugin) translate(ctx context.Context, inv Invocation, req any) (any, error) {
	if !isFunction(mp.translator) {
		return nil, fmt.Errorf("plugin %s has no translator", mp.vm.name)
	}
	return mp.vm.CallFunction(ctx, inv, mp.translator, req)
}

func isFunction(v moejs.Value) bool { return v.IsObject() && v.AsObject().IsCallable() }

func (p *Plugin) newMoeCtx(c *MoeScope) (moejs.Value, error) {
	mp, v, rt := p.moe, c.vm, c.Runtime()
	r := rt.Realm()
	fn := v.HostFunc
	object := func(kv ...any) moejs.Value {
		o := r.NewObject()
		for i := 0; i < len(kv); i += 2 {
			_ = o.SetProp(r, engine.InternKey(kv[i].(string)), kv[i+1].(moejs.Value))
		}
		return engine.ObjectValue(o)
	}
	undef := moejs.Undefined()

	netObj := object("fetch", fn("net.fetch", func(c *MoeScope, args []moejs.Value) (moejs.Value, error) {
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
		"get", fn("storage.get", func(c *MoeScope, args []moejs.Value) (moejs.Value, error) {
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
		"set", fn("storage.set", func(c *MoeScope, args []moejs.Value) (moejs.Value, error) {
			var k, val string
			if err := c.arg("storage.set", moejs.Arg(args, 0), &k, "key must be a string"); err != nil {
				return undef, err
			}
			if err := c.arg("storage.set", moejs.Arg(args, 1), &val, "value must be a string"); err != nil {
				return undef, err
			}
			if len(val) > MaxStorageValue {
				return undef, ErrTooLarge
			}
			return c.AsyncJSON(func(ctx context.Context) (any, error) { return nil, p.h.StorageSet(ctx, k, val) }), nil
		}),
		"delete", fn("storage.delete", func(c *MoeScope, args []moejs.Value) (moejs.Value, error) {
			var k string
			if err := c.arg("storage.delete", moejs.Arg(args, 0), &k, "key must be a string"); err != nil {
				return undef, err
			}
			return c.AsyncJSON(func(ctx context.Context) (any, error) { return nil, p.h.StorageDelete(ctx, k) }), nil
		}),
	)

	translators := object("register", fn("translators.register", func(c *MoeScope, args []moejs.Value) (moejs.Value, error) {
		const name = "translators.register"
		if c.inv.Kind != KindInit {
			return undef, errors.New(errOnlyDuringRun)
		}
		if !slices.Contains(p.m.Capabilities, "translate") {
			return undef, typeError(name + ": plugin.json does not declare the translate capability")
		}
		f, err := rt.Get(moejs.Arg(args, 0), "translate")
		if err != nil {
			return undef, err
		}
		if !isFunction(f) {
			return undef, typeError(name + ": translate must be a function")
		}
		if !mp.translator.IsUndefined() {
			return undef, errors.New("a translator is already registered")
		}
		mp.translator = f
		return undef, nil
	}))

	kv := []any{"translators", translators, "net", netObj, "storage", storage}
	for ns, names := range gojaOnly {
		var members []any
		for _, n := range names {
			full := n
			if ns != "" {
				full = ns + "." + n
			}
			f := fn(full, func(c *MoeScope, _ []moejs.Value) (moejs.Value, error) {
				if c.inv.Kind == KindInit {
					mp.needsGoja = true
					return undef, ErrNeedsGoja
				}
				if perm := gojaOnlyPerm[ns]; perm != "" {
					if ns == "mail" && n != "get" && n != "search" {
						perm = permMailWrite
					}
					return undef, &PermissionError{Perm: perm}
				}
				return undef, errors.New(errOnlyDuringRun)
			})
			if ns == "" {
				kv = append(kv, n, f)
			} else {
				members = append(members, n, f)
			}
		}
		if ns != "" {
			kv = append(kv, ns, object(members...))
		}
	}
	o := object(kv...)

	var config engine.PropertyDescriptor
	config.SetGet(fn("config", func(c *MoeScope, _ []moejs.Value) (moejs.Value, error) {
		if mp.cfgCall != c { // the invocation's snapshot, copied: what the plugin does to it stays in its call
			vals, err := configData(c.inv.Config)
			if err != nil {
				return undef, err
			}
			if mp.cfgVal, err = jsonValue(rt, vals); err != nil {
				return undef, err
			}
			mp.cfgCall = c
		}
		return mp.cfgVal, nil
	}))
	config.SetEnumerable(true)
	config.SetConfigurable(false)
	return o, o.AsObject().DefinePropertyOrThrow(r, engine.InternKey("config"), config)
}
