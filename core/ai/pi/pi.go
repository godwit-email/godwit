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

// Request is one reply to a one-off prompt. API is the pi-ai API that speaks
// the service's protocol: openai-completions, anthropic-messages,
// openai-responses or openai-codex-responses. APIKey "" sends no credential.
// Effort is "", "low", "medium" or "high". MaxTokens caps the reply (0: none
// where the API allows it); ThinkingBudget is anthropic-messages' thinking
// budget for Effort.
type Request struct {
	API            string `json:"api"`
	BaseURL        string `json:"baseUrl"`
	Model          string `json:"model"`
	APIKey         string `json:"apiKey"`
	System         string `json:"system"`
	Prompt         string `json:"prompt"`
	Effort         string `json:"effort"`
	MaxTokens      int64  `json:"maxTokens"`
	ThinkingBudget int64  `json:"thinkingBudget"`
}

// Result is how a reply ended. StopReason is pi-ai's: "stop" is a whole reply,
// "length" one cut off at the output limit, "error" a failure (Error says what
// pi-ai saw); or "blocked", a reply the service withheld, and "incomplete",
// one it ended early for another reason. Code is the error code the service
// last answered with (an HTTP error body's or a failed response's), Status
// the last HTTP status a request got, 0 if none answered; Transport the error
// a request failed with before any answer.
type Result struct {
	StopReason    string `json:"stopReason"`
	Error         string `json:"error"`
	Code          string `json:"code"`
	Input, Output int
	Status        int   `json:"-"`
	Transport     error `json:"-"`
}

// Generate streams one reply: emit gets each text and reasoning delta on the
// calling goroutine, in order. It returns when the reply ended or ctx did
// (ctx's error then).
func Generate(ctx context.Context, client *http.Client, req Request, emit func(kind, text string)) (Result, error) {
	var res Result
	h, err := run(ctx, client, "generate", req, &res, Hooks{Emit: emit}, 0)
	if err != nil {
		return Result{}, err
	}
	h.mu.Lock()
	res.Status, res.Transport = h.status, h.transport
	h.mu.Unlock()
	return res, nil
}

// JudgeRequest is one judge API call: Body, the API's JSON request, is
// posted to URL with the key as a bearer token (none when APIKey is "").
type JudgeRequest struct {
	URL    string          `json:"url"`
	APIKey string          `json:"apiKey"`
	Body   json.RawMessage `json:"body"`
}

// JudgeResult is a judge call's outcome: Body the reply's JSON when the
// service answered 200, else Status its last HTTP status (0 if none
// answered: Transport is then what the request failed with, nil when the
// reply was unreadable).
type JudgeResult struct {
	Body      json.RawMessage
	Status    int
	Transport error
}

// Judge posts one judge request through pi-ai's classifier transport, which
// retries as the SDKs do. A reply over maxBody bytes fails to read.
func Judge(ctx context.Context, client *http.Client, req JudgeRequest, maxBody int64) (JudgeResult, error) {
	var out struct {
		Body   string `json:"body"`
		Status int    `json:"status"`
	}
	h, err := run(ctx, client, "judge", req, &out, Hooks{}, maxBody)
	if err != nil {
		return JudgeResult{}, err
	}
	res := JudgeResult{Status: out.Status}
	if out.Body != "" {
		res.Body = json.RawMessage(out.Body)
	}
	if res.Body == nil && res.Status == 0 {
		h.mu.Lock()
		res.Transport = h.transport
		h.mu.Unlock()
	}
	return res, nil
}

// AgentRequest is one turn of a conversation with the service Request names
// (its Prompt unused): Messages are pi messages (pi-ai's Message, as JSON),
// ending with the question. Tools are offered to the model; past MaxTurns
// rounds of calls it answers from what it found, its system prompt ending
// with LimitNote, and the last round's calls are refused. Answers, if set,
// resume a held round (AgentResult.Held) instead: Messages end with it, and
// Answers are the results of its calls, by ID, that held or ran; the others
// run first.
type AgentRequest struct {
	Request
	Messages  []json.RawMessage `json:"messages"`
	Answers   map[string]Answer `json:"answers,omitempty"`
	Tools     []Tool            `json:"tools"`
	MaxTurns  int               `json:"maxTurns"`
	LimitNote string            `json:"limitNote"`
}

// Answer is a call's result as the model reads it: Text, an error if IsError.
type Answer struct {
	Text    string `json:"text"`
	IsError bool   `json:"isError"`
}

// Tool is a tool offered to the model; Parameters is its JSON schema, which
// Strict asks the service to hold calls to (Responses).
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

// Hooks are what a run calls back on its goroutine, Tool on one of its own
// per call. Emit gets each text and reasoning delta. Args rewrites a call's
// arguments before they are validated (an error refuses the call); Gate,
// given each valid call of a round before any of them runs, holds one for
// the user (true): the round then stops. Tool runs a call, its output JSON
// or an error message, or holds it as it runs (interrupt); Refused tells of
// a call that never reached Tool.
type Hooks struct {
	Emit    func(kind, text string)
	Args    func(args json.RawMessage) (json.RawMessage, error)
	Gate    func(ctx context.Context, id, name string, args json.RawMessage) (hold bool)
	Tool    func(ctx context.Context, id, name string, args json.RawMessage) (output json.RawMessage, errMsg string, interrupt bool)
	Refused func(id, name string, args json.RawMessage, errMsg string)
}

// AgentResult is how a turn ended, its last reply as Result tells it.
// Messages are the pi messages it added, the question first. Limited: the
// model reached MaxTurns and the last message is its answer after it; Calls:
// that message still calls tools. Input and Output sum every reply the turn
// was billed for; Last is the last request's input. Held: the turn ended at
// a round whose calls with these IDs wait for the user; none of its calls
// ran but those that were running, and Deferred were held back with them.
// Messages then end with the round and every call's result, held ones too.
type AgentResult struct {
	Result
	Messages       []json.RawMessage `json:"-"`
	Raw            string            `json:"messages"`
	Limited, Calls bool
	Last           int
	Held, Deferred []string
}

// Agent runs one turn; it returns when the turn ended, or when ctx did
// (ctx's error then) and the tools it ran have returned.
func Agent(ctx context.Context, client *http.Client, req AgentRequest, hooks Hooks) (AgentResult, error) {
	var res AgentResult
	h, err := run(ctx, client, "agent", req, &res, hooks, 0)
	if err != nil {
		return AgentResult{}, err
	}
	if err := json.Unmarshal([]byte(res.Raw), &res.Messages); err != nil {
		return AgentResult{}, fmt.Errorf("pi: messages: %w", err)
	}
	res.Raw = ""
	h.mu.Lock()
	res.Status, res.Transport = h.status, h.transport
	h.mu.Unlock()
	return res, nil
}

// run calls the bundle's hook with arg, as JSON, on a new runtime and
// decodes what it settles with into out. maxBody caps each response read, 0
// not at all.
func run(ctx context.Context, client *http.Client, hook string, arg, out any, hooks Hooks, maxBody int64) (*host, error) {
	mod, err := compiled()
	if err != nil {
		return nil, fmt.Errorf("pi: compile: %w", err)
	}
	rt := moejs.NewRuntime(moejs.Options{})
	loop, err := eventloop.New(rt, eventloop.Options{})
	if err != nil {
		return nil, err
	}
	// Every request and tool of the run ends with it.
	hctx, cancel := context.WithCancel(ctx)
	h := &host{rt: rt, loop: loop, ctx: hctx, client: client, bodies: map[int64]*body{}, hooks: hooks, maxBody: maxBody}
	defer h.tools.Wait()
	defer cancel()
	defer h.closeAll()
	if err := h.install(); err != nil {
		return nil, err
	}
	if err := rt.Load(mod); err != nil {
		return nil, fmt.Errorf("pi: load: %w", err)
	}
	fn, err := mod.Hook(hook)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, loop.Terminate)
	defer stop()
	var promise moejs.Value
	err = loop.Run(func(rt *moejs.Runtime) error {
		b, err := json.Marshal(arg)
		if err != nil {
			return err
		}
		v, err := rt.ParseJSONString(string(b))
		if err != nil {
			return err
		}
		promise, err = rt.Call(fn, v)
		return err
	})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("pi: %w", err)
	}
	state, v, ok := moejs.PromiseResult(promise)
	if !ok || state != moejs.PromiseFulfilled {
		msg := "the reply never settled"
		if ok {
			msg = v.String()
		}
		return nil, fmt.Errorf("pi: %s", msg)
	}
	if err := rt.Unmarshal(v, out); err != nil {
		return nil, fmt.Errorf("pi: result: %w", err)
	}
	return h, nil
}

// host is one run's side of the bundle's host functions (js/src/web.ts).
type host struct {
	rt     *moejs.Runtime
	loop   *eventloop.Loop
	ctx    context.Context
	client *http.Client
	hooks  Hooks
	start  time.Time
	tools  sync.WaitGroup // calls hostTool is running
	// maxBody caps what is read of one response, 0 not at all.
	maxBody int64

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
	read    int64
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
			switch kind, t := str(moejs.Arg(args, 0)), str(moejs.Arg(args, 1)); {
			case (kind == "text" || kind == "reasoning") && t != "" && h.hooks.Emit != nil:
				h.hooks.Emit(kind, t)
			case kind == "refused" && h.hooks.Refused != nil:
				var c struct {
					ID, Name, Error string
					Args            json.RawMessage
				}
				if json.Unmarshal([]byte(t), &c) == nil {
					h.hooks.Refused(c.ID, c.Name, c.Args, c.Error)
				}
			}
			return moejs.Undefined(), nil
		},
		"hostArgs": func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			in := str(moejs.Arg(args, 0))
			if h.hooks.Args == nil {
				return moejs.String(in), nil
			}
			out, err := h.hooks.Args(json.RawMessage(in))
			if err != nil {
				return moejs.Undefined(), err // the loop refuses the call with its message
			}
			return moejs.String(string(out)), nil
		},
		"hostGate": func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			hold := h.hooks.Gate != nil && h.hooks.Gate(h.ctx, str(moejs.Arg(args, 0)), str(moejs.Arg(args, 1)), json.RawMessage(str(moejs.Arg(args, 2))))
			return h.rt.FromGo(hold)
		},
		"hostTool": func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			id, name, in := str(moejs.Arg(args, 0)), str(moejs.Arg(args, 1)), str(moejs.Arg(args, 2))
			p, settle := h.loop.NewPromise()
			h.tools.Add(1)
			go func() {
				defer h.tools.Done()
				var out json.RawMessage
				msg, interrupt := "this tool cannot run here", false
				if h.hooks.Tool != nil {
					out, msg, interrupt = h.hooks.Tool(h.ctx, id, name, json.RawMessage(in))
				}
				settle(func(rt *moejs.Runtime) (moejs.Value, error) {
					return rt.FromGo(map[string]any{"output": string(out), "error": msg, "interrupt": interrupt})
				})
			}()
			return p, nil
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
		if b.read += int64(n); h.maxBody > 0 && b.read > h.maxBody {
			n, err = 0, errors.New("response too large")
		}
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
