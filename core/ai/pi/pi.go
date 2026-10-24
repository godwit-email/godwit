// Package pi runs pi-ai, the model layer of the pi agent harness, on moejs, a
// JavaScript runtime in pure Go (docs/adr/0024-pi-moejs-agent-runtime.md).
// The bundle (engine.js, built from js/ by `mise run ai:bundle`) speaks the
// model protocols; every HTTP request it makes is sent here, through the
// caller's client. It must not import package ai.
package pi

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Calcium-Ion/moejs"
	"github.com/Calcium-Ion/moejs/eventloop"
)

//go:embed engine.js
var bundle string

var compiled = sync.OnceValues(func() (*moejs.Module, error) { return moejs.Compile("engine.js", bundle) })

// Request is one reply to a one-off prompt from an OpenAI-compatible service
// (chat completions). Effort is "", "low", "medium" or "high".
type Request struct {
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	APIKey  string `json:"apiKey"`
	System  string `json:"system"`
	Prompt  string `json:"prompt"`
	Effort  string `json:"effort"`
}

// Result is how a reply ended. StopReason is pi-ai's: "stop" is a whole reply,
// "length" one cut off at the output limit, "error" a failure (Error says what
// pi-ai saw). Status is the last HTTP status a request got, 0 if none
// answered; Transport the error a request failed with before any answer.
type Result struct {
	StopReason    string `json:"stopReason"`
	Error         string `json:"error"`
	Input, Output int
	Status        int   `json:"-"`
	Transport     error `json:"-"`
}

// Generate streams one reply: emit gets each text and reasoning delta on the
// calling goroutine, in order. It returns when the reply ended or ctx did
// (ctx's error then).
func Generate(ctx context.Context, client *http.Client, req Request, emit func(kind, text string)) (Result, error) {
	mod, err := compiled()
	if err != nil {
		return Result{}, fmt.Errorf("pi: compile: %w", err)
	}
	rt := moejs.NewRuntime(moejs.Options{})
	loop, err := eventloop.New(rt, eventloop.Options{})
	if err != nil {
		return Result{}, err
	}
	// Every request of the run ends with it.
	hctx, cancel := context.WithCancel(ctx)
	defer cancel()
	h := &host{rt: rt, loop: loop, ctx: hctx, client: client, bodies: map[int64]*body{}, emit: emit}
	defer h.closeAll()
	if err := h.install(); err != nil {
		return Result{}, err
	}
	if err := rt.Load(mod); err != nil {
		return Result{}, fmt.Errorf("pi: load: %w", err)
	}
	hook, err := mod.Hook("generate")
	if err != nil {
		return Result{}, err
	}
	stop := context.AfterFunc(ctx, loop.Terminate)
	defer stop()
	var promise moejs.Value
	err = loop.Run(func(rt *moejs.Runtime) error {
		b, err := json.Marshal(req)
		if err != nil {
			return err
		}
		arg, err := rt.ParseJSONString(string(b))
		if err != nil {
			return err
		}
		promise, err = rt.Call(hook, arg)
		return err
	})
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if err != nil {
		return Result{}, fmt.Errorf("pi: %w", err)
	}
	state, v, ok := moejs.PromiseResult(promise)
	if !ok || state != moejs.PromiseFulfilled {
		msg := "the reply never settled"
		if ok {
			msg = v.String()
		}
		return Result{}, fmt.Errorf("pi: %s", msg)
	}
	var res Result
	if err := rt.Unmarshal(v, &res); err != nil {
		return Result{}, fmt.Errorf("pi: result: %w", err)
	}
	h.mu.Lock()
	res.Status, res.Transport = h.status, h.transport
	h.mu.Unlock()
	return res, nil
}

// host is one run's side of the bundle's host functions (js/src/web.ts).
type host struct {
	rt     *moejs.Runtime
	loop   *eventloop.Loop
	ctx    context.Context
	client *http.Client
	emit   func(kind, text string)
	start  time.Time

	mu        sync.Mutex
	next      int64
	bodies    map[int64]*body
	status    int
	transport error
}

// body is a response being read: pending is a UTF-8 sequence a chunk cut.
type body struct {
	rc      io.ReadCloser
	cancel  context.CancelFunc
	pending []byte
}

// maxRequestBody caps what the bundle may send in one request.
const maxRequestBody = 32 << 20

func (h *host) install() error {
	h.start = time.Now()
	str := func(v moejs.Value) string {
		s, _ := h.rt.ToGo(v)
		t, _ := s.(string)
		return t
	}
	num := func(v moejs.Value) int64 {
		n, _ := h.rt.ToGo(v)
		switch n := n.(type) {
		case int64:
			return n
		case float64:
			return int64(n)
		}
		return 0
	}
	fns := map[string]moejs.NativeFunc{
		"hostNow": func(*moejs.Realm, moejs.Value, []moejs.Value) (moejs.Value, error) {
			return h.rt.FromGo(float64(time.Since(h.start).Microseconds()) / 1000)
		},
		"hostRandomBytes": func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			n := num(moejs.Arg(args, 0))
			if n < 0 || n > 65536 {
				return moejs.Undefined(), fmt.Errorf("getRandomValues: %d bytes", n)
			}
			b := make([]byte, n)
			rand.Read(b)
			out := make([]int, n)
			for i, x := range b {
				out[i] = int(x)
			}
			return h.rt.FromGo(out)
		},
		"hostEmit": func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			if kind := str(moejs.Arg(args, 0)); kind == "text" || kind == "reasoning" {
				if t := str(moejs.Arg(args, 1)); t != "" {
					h.emit(kind, t)
				}
			}
			return moejs.Undefined(), nil
		},
		"hostFetch": func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			url, method, hdrs, payload := str(moejs.Arg(args, 0)), str(moejs.Arg(args, 1)), str(moejs.Arg(args, 2)), str(moejs.Arg(args, 3))
			if len(payload) > maxRequestBody {
				return moejs.Undefined(), errors.New("fetch: request body too large")
			}
			p, settle := h.loop.NewPromise()
			go h.fetch(url, method, hdrs, payload, settle)
			return p, nil
		},
		"hostRead": func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			id := num(moejs.Arg(args, 0))
			p, settle := h.loop.NewPromise()
			go h.read(id, settle)
			return p, nil
		},
		"hostAbort": func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			h.close(num(moejs.Arg(args, 0)))
			return moejs.Undefined(), nil
		},
	}
	for name, f := range fns {
		if err := h.rt.SetGlobal(name, f); err != nil {
			return err
		}
	}
	return nil
}

type settleFunc = func(func(*moejs.Runtime) (moejs.Value, error))

func (h *host) fetch(url, method, hdrs, payload string, settle settleFunc) {
	fail := func(err error) {
		settle(func(*moejs.Runtime) (moejs.Value, error) { return moejs.Undefined(), err })
	}
	ctx, cancel := context.WithCancel(h.ctx)
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(payload))
	if err != nil {
		cancel()
		fail(err)
		return
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(hdrs), &headers); err != nil {
		cancel()
		fail(err)
		return
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		cancel()
		h.mu.Lock()
		h.transport = err
		h.mu.Unlock()
		fail(errors.New("fetch failed")) // the URL and its error stay on the Go side
		return
	}
	h.mu.Lock()
	h.next++
	id := h.next
	h.bodies[id] = &body{rc: resp.Body, cancel: cancel}
	h.status, h.transport = resp.StatusCode, nil
	h.mu.Unlock()
	out := map[string]string{}
	for k := range resp.Header {
		out[strings.ToLower(k)] = resp.Header.Get(k)
	}
	hj, _ := json.Marshal(out)
	settle(func(rt *moejs.Runtime) (moejs.Value, error) {
		return rt.FromGo(map[string]any{"id": id, "status": resp.StatusCode, "headers": string(hj)})
	})
}

// read settles with the body's next chunk, whole UTF-8 sequences only (the
// bundle encodes strings back to bytes), or null at its end.
func (h *host) read(id int64, settle settleFunc) {
	h.mu.Lock()
	b := h.bodies[id]
	h.mu.Unlock()
	if b == nil {
		settle(func(*moejs.Runtime) (moejs.Value, error) { return moejs.Null(), nil })
		return
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := b.rc.Read(buf)
		chunk := append(b.pending, buf[:n]...)
		cut := len(chunk)
		if err == nil {
			cut = completeUTF8(chunk)
		}
		b.pending = append([]byte(nil), chunk[cut:]...)
		if cut > 0 {
			s := string(chunk[:cut])
			settle(func(*moejs.Runtime) (moejs.Value, error) { return moejs.String(s), nil })
			return
		}
		if err != nil {
			h.close(id)
			if err == io.EOF {
				settle(func(*moejs.Runtime) (moejs.Value, error) { return moejs.Null(), nil })
			} else {
				settle(func(*moejs.Runtime) (moejs.Value, error) {
					return moejs.Undefined(), errors.New("reading the response failed")
				})
			}
			return
		}
	}
}

// completeUTF8 is the length of b's prefix that ends on a whole UTF-8 sequence.
func completeUTF8(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if utf8.FullRune(b[i:]) {
				return len(b)
			}
			return i
		}
	}
	return len(b)
}

func (h *host) close(id int64) {
	h.mu.Lock()
	b := h.bodies[id]
	delete(h.bodies, id)
	h.mu.Unlock()
	if b != nil {
		b.cancel()
		b.rc.Close()
	}
}

func (h *host) closeAll() {
	h.mu.Lock()
	ids := make([]int64, 0, len(h.bodies))
	for id := range h.bodies {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.close(id)
	}
}
