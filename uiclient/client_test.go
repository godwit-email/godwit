package uiclient_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/ai/mask/guard"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/imagecache"
	"github.com/godwit-email/godwit/ownerlock"
	"github.com/godwit-email/godwit/rpc"
	"github.com/godwit-email/godwit/rpcbind"
	"github.com/godwit-email/godwit/uiclient"
	"github.com/godwit-email/godwit/webui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	coretest.ServeNativeIfChild()
	coretest.ServeMCPIfChild()
	os.Exit(m.Run())
}

var (
	_ app.Service   = (*uiclient.Client)(nil)
	_ webui.Service = (*uiclient.Client)(nil)
)

func startUIServer(t *testing.T, svc *core.Service) (*rpc.Server, *uiclient.Client, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "gw")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })

	release, err := ownerlock.Acquire(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })

	srv, err := rpcbind.ListenWith(dir, svc, rpcbind.ListenOptions{Version: "test", External: false})
	require.NoError(t, err)
	go srv.Serve(context.Background())
	t.Cleanup(func() { _ = srv.Close() })

	cl, err := uiclient.Dial(dir, "uiclient-test")
	require.NoError(t, err)
	t.Cleanup(func() { _ = cl.Close() })
	return srv, cl, dir
}

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, image.NewNRGBA(image.Rect(0, 0, 1, 1)).NRGBAAt(0, 0))
			img.Pix[(y*w+x)*4] = byte((x*31 + y*17) & 0xff)
			img.Pix[(y*w+x)*4+1] = byte((x*13 + y*53) & 0xff)
			img.Pix[(y*w+x)*4+2] = byte((x*7 + y*29) & 0xff)
			img.Pix[(y*w+x)*4+3] = 255
		}
	}
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return b.Bytes()
}

func TestGeneratedClientFresh(t *testing.T) {
	got, err := uiclient.Generate("../app/service.go", "../webui/service.go")
	require.NoError(t, err)
	require.NotEmpty(t, got)

	onDisk, err := os.ReadFile("client_gen.go")
	require.NoError(t, err)
	onDisk = bytes.ReplaceAll(onDisk, []byte("\r\n"), []byte("\n")) // a Windows checkout has CRLF
	assert.True(t, bytes.Equal(onDisk, got), "uiclient/client_gen.go is stale; run go generate ./uiclient")

	// Drift check: changing either interface produces different generated output.
	appSrc, err := os.ReadFile("../app/service.go")
	require.NoError(t, err)
	drifted := filepath.Join(t.TempDir(), "service.go")
	mutated := strings.Replace(string(appSrc), "SyncNow(ctx context.Context, accountID int64) error",
		"SyncNow(ctx context.Context, accountID int64) error\n\tExtraMethodForDriftCheck(ctx context.Context) error", 1)
	require.NotEqual(t, string(appSrc), mutated)
	require.NoError(t, os.WriteFile(drifted, []byte(mutated), 0o600))

	changed, err := uiclient.Generate(drifted, "../webui/service.go")
	require.NoError(t, err)
	assert.False(t, bytes.Equal(onDisk, changed), "generator must reflect interface changes")
}

func readMethodList(t *testing.T, path string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	out := map[string]bool{}
	for line := range strings.Lines(string(b)) {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out[line] = true
		}
	}
	return out
}

func TestMethodsInUISurface(t *testing.T) {
	full := readMethodList(t, "../rpcbind/testdata/full_methods.txt")
	uiOnly := readMethodList(t, "../rpcbind/testdata/ui_methods.txt")

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	rpcHelpers := map[string]int{
		"Call":          1,
		"Stream":        1,
		"call":          2,
		"call2":         2,
		"callErr":       2,
		"callVal":       2,
		"callVoid":      2,
		"stream":        2,
		"streamErr":     2,
		"streamStop":    1,
		"consumeStream": 2,
	}

	fset := token.NewFileSet()
	wireMethods := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "gen.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		ast.Inspect(f, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fnName := calleeName(ce.Fun)
			idx, ok := rpcHelpers[fnName]
			if !ok || idx >= len(ce.Args) {
				return true
			}
			lit, ok := ce.Args[idx].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			m, err := strconv.Unquote(lit.Value)
			require.NoError(t, err)
			if m != "hello" {
				wireMethods[m] = true
			}
			return true
		})
	}

	require.NotEmpty(t, wireMethods)
	var list []string
	for m := range wireMethods {
		list = append(list, m)
	}
	slices.Sort(list)

	for _, m := range list {
		assert.True(t, rpcbind.ExposeUI(m), "uiclient calls %q which is not in rpcbind.ExposeUI", m)
		assert.True(t, full[m] || uiOnly[m], "uiclient calls %q which is in neither full_methods.txt nor ui_methods.txt", m)
	}

	for _, forbidden := range []string{
		"Close", "Redact", "AuditExternal", "Proxy", "FollowConversation",
		"InlineImages", "FetchAttachments",
		"InstallPluginFromRegistryWithProgress", "PrepareModelWithProgress", "DownloadRecognitionModel",
	} {
		assert.False(t, wireMethods[forbidden], "hidden method %q must never cross the wire", forbidden)
	}
}

func calleeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	case *ast.IndexExpr:
		return calleeName(e.X)
	case *ast.IndexListExpr:
		return calleeName(e.X)
	}
	return ""
}

func TestErrorCodecPreservesKeys(t *testing.T) {
	// 1. Every sentinel row in rpcbind.All() preserves rpcbind.Key(err) across codec encode/decode.
	codec := rpcbind.NewCodec()
	for _, row := range rpcbind.All() {
		require.NotEmpty(t, row.Code, "every row must have a wire code for RoleUI")
		we := codec.Encode(fmt.Errorf("wrapped: %w", row.Err), nil)
		decoded := codec.Decode(we)
		assert.ErrorIs(t, decoded, row.Err, row.Code)
		assert.Equal(t, rpcbind.Key(row.Err), rpcbind.Key(decoded), row.Code)
	}

	// Structured *ai.Error survives encode/decode with Code and Detail intact.
	aiWe := codec.Encode(&ai.Error{Code: ai.CodeUnauthorized, Msg: "bad key", Detail: "invalid_api_key"}, nil)
	var decodedAI *ai.Error
	require.ErrorAs(t, codec.Decode(aiWe), &decodedAI)
	assert.Equal(t, ai.CodeUnauthorized, decodedAI.Code)
	assert.Equal(t, "invalid_api_key", decodedAI.Detail)

	// 2. End-to-end sentinel and structured errors through *uiclient.Client over unix socket.
	env := coretest.New(t, coretest.WithoutAutoSync())
	_, cl, _ := startUIServer(t, env.Svc)
	ctx := t.Context()

	_, err := cl.InstallPluginFromRegistryWithProgress(ctx, "any", nil)
	require.ErrorIs(t, err, core.ErrRegistryOff)
	assert.Equal(t, rpcbind.Key(core.ErrRegistryOff), rpcbind.Key(err))

	acc := env.AddAccount(t, "alice@example.com")
	d, err := cl.NewDraft(ctx, acc.ID, "new", 0)
	require.NoError(t, err)
	d.Subject = "v1"
	d1, err := cl.SaveDraft(ctx, d)
	require.NoError(t, err)
	d.Subject = "stale"
	_, err = cl.SaveDraft(ctx, d)
	require.ErrorIs(t, err, core.ErrStaleDraft)
	assert.Equal(t, rpcbind.Key(core.ErrStaleDraft), rpcbind.Key(err))

	env.InstallPlugin(t, "send-warn")
	d1.To = []string{"bob@example.com"}
	d1.HTML = "<p>hello</p>"
	d2, err := cl.SaveDraft(ctx, d1)
	require.NoError(t, err)
	err = cl.SendRevision(ctx, d2.ID, d2.Revision)
	var warnErr *core.SendWarningsError
	require.ErrorAs(t, err, &warnErr)
	assert.NotEmpty(t, warnErr.Check.Digest)

	ih, ip, sh, sp, user, _ := env.StartServers(t)
	_, err = cl.AddAccount(ctx, core.AccountInput{
		Email:     "wrong@example.com",
		Username:  user,
		Password:  "bad-password-value",
		Receiving: core.Server{Host: ih, Port: ip, Security: core.SecurityNone},
		Sending:   core.Server{Host: sh, Port: sp, Security: core.SecurityNone},
	})
	var checkErr *core.CheckError
	require.ErrorAs(t, err, &checkErr)
	assert.ErrorIs(t, err, backend.ErrAuthFailed)
	assert.Equal(t, rpcbind.Key(backend.ErrAuthFailed), rpcbind.Key(err))
}

// A UI that falls behind on events loses the oldest, as the in-process bus
// does, but its subscription lives on: the App's event pump has no restart.
func TestSubscribeSurvivesSlowReader(t *testing.T) {
	env := coretest.New(t, coretest.WithoutAutoSync())
	_, cl, _ := startUIServer(t, env.Svc)
	sub, stop := cl.Subscribe()
	defer stop()
	for i := range 3000 {
		env.Svc.Publish(events.Event{Type: events.MessagesChanged, AccountID: int64(i + 1)})
		if i%100 == 0 {
			time.Sleep(time.Millisecond) // let the bus's forwarder keep up, as a burst of syncs would
		}
	}
	time.Sleep(200 * time.Millisecond)
	env.Svc.Publish(events.Event{Type: events.OpenRequested})
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-sub:
			require.True(t, ok, "the subscription ended: %v", cl.StreamErr(sub))
			if ev.Type == events.OpenRequested {
				return
			}
		case <-timeout:
			t.Fatal("the event after the burst never came")
		}
	}
}

func TestSubscriptionsAndDisconnectCause(t *testing.T) {
	env := coretest.New(t, coretest.WithoutAutoSync())
	srv, cl, _ := startUIServer(t, env.Svc)
	ctx := t.Context()

	acc := env.AddAccount(t, "alice@example.com")
	require.NoError(t, env.Svc.SyncNow(ctx, acc.ID))

	// Normal Subscribe + stop reports nil StreamErr.
	sub1, stop1 := cl.Subscribe()
	env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Hi", "Body"))
	var gotEvent events.Event
	timeout := time.After(10 * time.Second)
	for gotEvent.Type != events.MessageReceived {
		select {
		case ev, ok := <-sub1:
			require.True(t, ok)
			gotEvent = ev
		case <-timeout:
			t.Fatal("timed out waiting for message.received")
		}
	}
	stop1()
	for range sub1 {
	}
	assert.NoError(t, cl.StreamErr(sub1))

	// Active Subscribe and SubscribeConversation report rpc.ErrClosed when server disconnects.
	conv, err := cl.NewConversation(ctx, "ask", 0)
	require.NoError(t, err)
	sub2, stop2 := cl.Subscribe()
	defer stop2()
	convSub, stopConv := cl.SubscribeConversation(conv.ID)
	defer stopConv()

	l := env.LLM(t)
	defer l.Release()
	l.Script(coretest.Turn{Text: []string{"Hello ", "world"}})
	askCh, err := cl.AskWith(ctx, conv.ID, "greet", nil)
	require.NoError(t, err)
	for range askCh {
	}
	assert.NoError(t, cl.StreamErr(askCh))

	// Read at least one Wit event from convSub before dropping the server.
	select {
	case _, ok := <-convSub:
		require.True(t, ok)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for conversation event")
	}

	require.NoError(t, srv.Close())
	<-cl.Done()

	for range sub2 {
	}
	for range convSub {
	}
	assert.ErrorIs(t, cl.StreamErr(sub2), rpc.ErrClosed)
	assert.ErrorIs(t, cl.StreamErr(convSub), rpc.ErrClosed)

	// Subscribing on a closed client immediately yields a closed channel with rpc.ErrClosed.
	deadSub, deadStop := cl.Subscribe()
	deadStop()
	_, ok := <-deadSub
	assert.False(t, ok)
	assert.ErrorIs(t, cl.StreamErr(deadSub), rpc.ErrClosed)
}

func TestStreamErrorSurvivesCancel(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close(); _ = serverConn.Close() })
	queued := make(chan func() int, 1)
	go func() {
		r := bufio.NewReader(serverConn)
		next := func() json.RawMessage {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return nil
			}
			var m struct{ ID json.RawMessage }
			_ = json.Unmarshal(line, &m)
			return m.ID
		}
		send := func(v any) { b, _ := json.Marshal(v); _, _ = serverConn.Write(append(b, '\n')) }
		send(map[string]any{"jsonrpc": "2.0", "id": next(), "result": map[string]any{"stream": 7}})
		event := func() {
			send(map[string]any{"jsonrpc": "2.0", "method": "$/stream", "params": []any{7, map[string]any{"Type": "message.received"}}})
		}
		for range rpc.StreamBuffer {
			event()
		}
		for n := <-queued; n() < rpc.StreamBuffer-1; {
			time.Sleep(time.Millisecond)
		}
		for range 45 {
			event()
		}
		send(map[string]any{"jsonrpc": "2.0", "method": "$/stream.end", "params": []any{7, map[string]any{"code": -32000, "message": "socket lost"}}})
		send(map[string]any{"jsonrpc": "2.0", "id": next(), "result": nil})
		for next() != nil {
		}
	}()

	cl := uiclient.New(rpc.NewClient(clientConn))
	ch, stop := cl.Subscribe()
	ev := <-ch
	queued <- func() int { return len(ch) }
	require.Equal(t, "message.received", ev.Type)
	require.NoError(t, cl.RPC().Call(t.Context(), "ping", nil))
	stop()
	err := cl.StreamErr(ch)
	require.Error(t, err)
	assert.ErrorContains(t, err, "socket lost")
}

// A Wit stream's reader far behind loses the oldest text, as in process,
// but still gets the run's end: the stream never overflows.
func TestWitStreamDropsTextKeepsEnd(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close(); _ = serverConn.Close() })
	sent := make(chan struct{})
	go func() {
		r := bufio.NewReader(serverConn)
		line, _ := r.ReadBytes('\n')
		var m struct{ ID json.RawMessage }
		_ = json.Unmarshal(line, &m)
		send := func(v any) { b, _ := json.Marshal(v); _, _ = serverConn.Write(append(b, '\n')) }
		send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"stream": 7}})
		for i := range 3 * rpc.StreamBuffer {
			send(map[string]any{"jsonrpc": "2.0", "method": "$/stream", "params": []any{7, core.WitEvent{RunID: 1, Kind: "text", Text: strconv.Itoa(i)}}})
		}
		send(map[string]any{"jsonrpc": "2.0", "method": "$/stream", "params": []any{7, core.WitEvent{RunID: 1, Kind: "done", Ends: true}}})
		send(map[string]any{"jsonrpc": "2.0", "method": "$/stream.end", "params": []any{7}})
		close(sent)
		_, _ = io.Copy(io.Discard, r)
	}()

	cl := uiclient.New(rpc.NewClient(clientConn))
	ch, err := cl.AskWith(t.Context(), 1, "hi", nil)
	require.NoError(t, err)
	<-sent
	var got []core.WitEvent
	for ev := range ch {
		got = append(got, ev)
	}
	require.NotEmpty(t, got)
	assert.Less(t, len(got), 3*rpc.StreamBuffer, "none was dropped")
	assert.Equal(t, "done", got[len(got)-1].Kind)
	assert.Equal(t, strconv.Itoa(3*rpc.StreamBuffer-1), got[len(got)-2].Text, "the newest text is kept")
	assert.NoError(t, cl.StreamErr(ch))
}

func TestCanceledWitStreamDiscardsPendingEvents(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { _ = clientConn.Close(); _ = serverConn.Close() })
	sent := make(chan struct{})
	go func() {
		r := bufio.NewReader(serverConn)
		line, _ := r.ReadBytes('\n')
		var m struct{ ID json.RawMessage }
		_ = json.Unmarshal(line, &m)
		send := func(v any) { b, _ := json.Marshal(v); _, _ = serverConn.Write(append(b, '\n')) }
		send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"stream": 7}})
		for range 64 {
			send(map[string]any{"jsonrpc": "2.0", "method": "$/stream", "params": []any{7, core.WitEvent{Kind: "text", Text: "queued"}}})
		}
		close(sent)
		_, _ = io.Copy(io.Discard, r)
	}()
	cl := uiclient.New(rpc.NewClient(clientConn))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ch, err := cl.AskWith(ctx, 1, "hi", nil)
	require.NoError(t, err)
	<-sent
	<-ch // the forwarder has started, with more events waiting
	cancel()
	got := 0
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				assert.LessOrEqual(t, got, 1, "only an event already being delivered may survive cancellation")
				return
			}
			got++
		case <-deadline:
			t.Fatal("canceled stream did not close")
		}
	}
}

type countingConn struct {
	net.Conn
	readBytes atomic.Int64
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.readBytes.Add(int64(n))
	return n, err
}

func TestCallbacksAndDiskPathsNeverCrossWire(t *testing.T) {
	recFiles := map[string][]byte{}
	for i, name := range guard.Files {
		recFiles[name] = bytes.Repeat([]byte{byte('a' + i)}, 300+i)
	}
	core.SetRecognitionModelFilesForTest(t, recFiles)

	rs := coretest.NewRegistryServer(t)
	scriptRel := rs.ScriptRelease("hello", "0.1.0", `{}`, strings.Repeat("// script\n", 32))
	badRel := rs.ScriptRelease("badsum", "0.1.0", `{}`, strings.Repeat("// script\n", 32))
	badRel["artifacts"].(map[string]any)["any"].(map[string]any)["sha256"] = strings.Repeat("0", 64)

	modelBytes := bytes.Repeat([]byte("M"), 512)
	modelSum := sha256.Sum256(modelBytes)
	rs.Serve("/assets/model.gguf", modelBytes)
	nativeRel := rs.NativeModelsRelease(t, "dlx", "0.1.0", filepath.Join(t.TempDir(), "rec"), []any{
		map[string]any{"id": "m-q4", "name": "M", "transport": "plugin", "operation": "judge", "assets": map[string]string{"model": "q4"}},
	})
	nativeRel["assets"] = []any{
		map[string]any{
			"name": "model",
			"variants": []any{
				map[string]any{
					"id": "q4",
					"files": []any{
						map[string]any{
							"name":   "model.gguf",
							"url":    rs.URL("/assets/model.gguf"),
							"sha256": hex.EncodeToString(modelSum[:]),
							"size":   len(modelBytes),
						},
					},
				},
			},
		},
	}
	rs.Publish(t, coretest.Registry(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
		map[string]any{"name": "hello", "title": "Hello", "summary": "S", "releases": []any{scriptRel}},
		map[string]any{"name": "badsum", "title": "Bad", "summary": "B", "releases": []any{badRel}},
		map[string]any{"name": "dlx", "title": "DLX", "summary": "D", "releases": []any{nativeRel}},
	))

	recSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := recFiles[strings.TrimPrefix(r.URL.Path, "/int8/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
	}))
	defer recSrv.Close()

	env := coretest.New(t,
		coretest.WithoutAutoSync(),
		coretest.WithRegistry(rs.Src),
		coretest.WithRecognitionModelSources(recSrv.URL+"/"),
	)
	_, _, dir := startUIServer(t, env.Svc)

	// Dial manually with countingConn to measure exact JSON-RPC bytes over the socket.
	tok, err := os.ReadFile(filepath.Join(dir, rpc.UITokenName))
	require.NoError(t, err)
	rawConn, err := net.Dial("unix", filepath.Join(dir, rpc.SocketName))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawConn.Close() })
	cc := &countingConn{Conn: rawConn}
	rc := rpc.NewClient(cc)
	rc.DecodeError = rpcbind.NewCodec().Decode
	t.Cleanup(func() { _ = rc.Close() })
	require.NoError(t, rc.Call(t.Context(), "hello", nil, string(tok), rpc.RoleUI, "wire-meter"))
	cl := uiclient.New(rc)

	ctx := t.Context()
	acc := env.AddAccount(t, "alice@example.com")
	largePNG := makePNG(t, 180, 180)
	largeBlob := bytes.Repeat([]byte("attachment-payload-"), 4000) // ~76 KB
	require.Greater(t, len(largePNG)+len(largeBlob), 64<<10)

	env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Files", "body",
		coretest.HTML(`<p><img src="cid:hero@test"></p>`),
		coretest.Inline("hero@test", "image/png", largePNG),
		coretest.Attachment("big.bin", "application/octet-stream", largeBlob),
		coretest.Attachment("note.txt", "text/plain", []byte("second"))))

	msgs, err := cl.ListMessages(ctx, core.View{Unified: "inbox"}, "", 0)
	require.NoError(t, err)
	require.Len(t, msgs.Items, 1)
	msgID := msgs.Items[0].ID
	txt, err := cl.GetMessageText(ctx, msgID)
	require.NoError(t, err)
	require.Len(t, txt.Attachments, 3)

	cc.readBytes.Store(0)

	// 1. InlineImages reads local file path; wire carries only path metadata, never largePNG.
	var gotInline map[string]mail.Attachment
	err = cl.InlineImages(ctx, msgID, func(cid string, a mail.Attachment) {
		if gotInline == nil {
			gotInline = map[string]mail.Attachment{}
		}
		gotInline[cid] = a
	})
	require.NoError(t, err)
	require.Contains(t, gotInline, "hero@test")
	assert.Equal(t, largePNG, gotInline["hero@test"].Data)
	assert.Equal(t, "image/png", gotInline["hero@test"].ContentType)

	// 2. FetchAttachments reads local file paths; wire carries only path + AttachmentInfo.
	var fetched []string
	err = cl.FetchAttachments(ctx, []int64{txt.Attachments[1].ID, txt.Attachments[2].ID}, func(p string, info core.AttachmentInfo) error {
		assert.True(t, filepath.IsAbs(p))
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		fetched = append(fetched, fmt.Sprintf("%s:%d", info.Filename, len(b)))
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{fmt.Sprintf("big.bin:%d", len(largeBlob)), "note.txt:6"}, fetched)
	assert.Less(t, cc.readBytes.Load(), int64(4096), "attachments and inline images must cross as paths, not wire bytes")

	// FetchAttachments stops early when callback returns an error.
	sentinelStop := errors.New("stop early")
	calls := 0
	err = cl.FetchAttachments(ctx, []int64{txt.Attachments[1].ID, txt.Attachments[2].ID}, func(string, core.AttachmentInfo) error {
		calls++
		return sentinelStop
	})
	assert.ErrorIs(t, err, sentinelStop)
	assert.Equal(t, 1, calls)

	// FetchAttachments propagates server mid-stream error.
	err = cl.FetchAttachments(ctx, []int64{txt.Attachments[1].ID, 999999}, func(string, core.AttachmentInfo) error { return nil })
	require.Error(t, err)

	// 3. InstallPluginFromRegistryWithProgress
	require.NoError(t, cl.RefreshPluginRegistry(ctx))
	var stages []string
	info, err := cl.InstallPluginFromRegistryWithProgress(ctx, "hello", func(p core.PluginInstallProgress) {
		stages = append(stages, p.Stage)
	})
	require.NoError(t, err)
	assert.Equal(t, "hello", info.Name)
	assert.Contains(t, stages, "download")
	assert.Contains(t, stages, "install")

	_, err = cl.InstallPluginFromRegistryWithProgress(ctx, "badsum", nil)
	require.ErrorIs(t, err, core.ErrRegistryMismatch)

	// 4. PrepareModelWithProgress
	_, err = cl.InstallPluginFromRegistryWithProgress(ctx, "dlx", nil)
	require.NoError(t, err)
	var prepProgress []core.ModelStatus
	finalStatus, err := cl.PrepareModelWithProgress(ctx, "plugin/dlx/m-q4", func(st core.ModelStatus) {
		prepProgress = append(prepProgress, st)
	})
	require.NoError(t, err)
	assert.NotEmpty(t, prepProgress)
	assert.Equal(t, core.ModelState("downloaded"), finalStatus.State)

	// 5. DownloadRecognitionModel
	var gotProgress [][2]int64
	err = cl.DownloadRecognitionModel(ctx, func(got, total int64) {
		gotProgress = append(gotProgress, [2]int64{got, total})
	})
	require.NoError(t, err)
	require.NotEmpty(t, gotProgress)
	last := gotProgress[len(gotProgress)-1]
	assert.Equal(t, last[1], last[0])
	assert.True(t, cl.RecognitionModelInstalled())
}

func TestRecentLogRedactedOverSocket(t *testing.T) {
	env := coretest.New(t, coretest.WithoutAutoSync())
	_, cl, _ := startUIServer(t, env.Svc)
	ctx := t.Context()

	const secretKey = "sk-super-secret-model-key-998877"
	_, err := cl.AddModelService(ctx, core.ModelServiceInput{
		Name: "OpenAI", Kind: "openai", BaseURL: "https://example.com/v1", APIKey: secretKey, Models: []string{"gpt-test"},
	})
	require.NoError(t, err)

	_, err = fmt.Fprintf(core.RecentLog, "model request failed with key %s on example.com\n", secretKey)
	require.NoError(t, err)

	got := cl.RecentLog()
	assert.Contains(t, got, "model request failed with key [redacted] on example.com")
	assert.NotContains(t, got, secretKey)
}

func TestTimeZonePropagatesOverSocket(t *testing.T) {
	env := coretest.New(t, coretest.WithoutAutoSync())
	_, cl, _ := startUIServer(t, env.Svc)
	ctx := t.Context()

	env.AddAccount(t, "alice@example.com")
	l := env.LLM(t)
	defer l.Release()

	askInZone := func(zone string) string {
		conv, err := cl.NewConversation(ctx, "ask", 0)
		require.NoError(t, err)
		l.Script(coretest.Turn{Text: []string{"ok"}})
		zctx := core.WithTimeZone(ctx, zone)
		ch, err := cl.AskWith(zctx, conv.ID, "what time is it?", nil)
		require.NoError(t, err)
		for range ch {
		}
		reqs := l.RequestLog()
		raw, err := json.Marshal(reqs[len(reqs)-1])
		require.NoError(t, err)
		return string(raw)
	}

	tokyo := askInZone("Asia/Tokyo")
	assert.Contains(t, tokyo, "+09:00")
	assert.Contains(t, tokyo, "JST")

	ny := askInZone("America/New_York")
	assert.True(t, strings.Contains(ny, "EDT") || strings.Contains(ny, "EST"), ny)
	assert.NotContains(t, ny, "JST")

	// AssistWith also propagates timezone over the socket.
	acc, err := cl.ListAccounts(ctx)
	require.NoError(t, err)
	draft, err := cl.NewDraft(ctx, acc[0].ID, "new", 0)
	require.NoError(t, err)
	l.Script(coretest.Turn{Text: []string{"sure"}})
	ach, err := cl.AssistWith(core.WithTimeZone(ctx, "Asia/Tokyo"), draft.ID, "help", nil)
	require.NoError(t, err)
	for range ach {
	}
	reqs := l.RequestLog()
	raw, err := json.Marshal(reqs[len(reqs)-1])
	require.NoError(t, err)
	assert.Contains(t, string(raw), "JST")
}

type readerPageKey struct{}

func TestReaderImagesOverSocket(t *testing.T) {
	pic := makePNG(t, 12, 8)
	var failRemote atomic.Bool
	failRemote.Store(true)
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failRemote.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write(pic)
	}))
	defer imgSrv.Close()

	env := coretest.New(t, coretest.WithoutAutoSync())
	_, cl, _ := startUIServer(t, env.Svc)
	ctx := t.Context()

	acc := env.AddAccount(t, "alice@example.com")
	inlinePic := makePNG(t, 6, 6)
	remoteURL := imgSrv.URL + "/remote.png"
	env.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Images", "body",
		coretest.HTML(fmt.Sprintf(`<p><img src="cid:emb@test"><img src=%q></p>`, remoteURL)),
		coretest.Inline("emb@test", "image/png", inlinePic)))

	msgs, err := cl.ListMessages(ctx, core.View{Unified: "inbox"}, "", 0)
	require.NoError(t, err)
	msgID := msgs.Items[0].ID

	pages := webui.NewPages(func(ctx context.Context) any { return ctx.Value(readerPageKey{}) })
	built := make(chan webui.ReaderBody, 4)
	r := webui.NewReader(pages, webui.ReaderDeps{
		Emit:    func(_ any, b webui.ReaderBody) { built <- b },
		OnBuilt: func(_ any, _ webui.ReaderBody) {},
	})
	r.SetService(cl)
	rp := webui.NewReaderPage(r)

	pKey := "page-1"
	pages.Register(pKey, "reader")
	r.Bind(pKey, msgID)
	pCtx := context.WithValue(ctx, readerPageKey{}, pKey)

	body, err := rp.Load(pCtx)
	require.NoError(t, err)

	handler := r.ImageHandler()
	inlineTok := imagecache.Key("cid:emb@test")
	remoteTok := imagecache.Key(remoteURL)
	assert.Contains(t, body.HTML, inlineTok)
	assert.Contains(t, body.HTML, remoteTok)

	// Inline image served from cached disk file via cl.InlineImages -> InlineImagesStream.
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://m%d/%s", msgID, inlineTok), nil))
	require.Equal(t, http.StatusOK, w1.Code)
	assert.Equal(t, inlinePic, w1.Body.Bytes())

	// Allow remote images: first attempt fails (502), then Allow() calls cl.ForgetImageFailure and retries cleanly.
	r.Allow(msgID)
	<-built
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://m%d/%s", msgID, remoteTok), nil))
	require.Equal(t, http.StatusBadGateway, w2.Code)

	failRemote.Store(false)
	r.Allow(msgID)
	<-built
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://m%d/%s", msgID, remoteTok), nil))
	require.Equal(t, http.StatusOK, w3.Code)
	assert.Equal(t, pic, w3.Body.Bytes())
}

// An original near MaxMessageBytes reaches the interface as a file, not one
// base64 response: that would overflow the send queue and drop the
// connection, which the interface takes for the backend's loss.
func TestRawMessageFileLargeOriginal(t *testing.T) {
	env := coretest.New(t, coretest.WithoutAutoSync())
	_, cl, _ := startUIServer(t, env.Svc)
	ctx := t.Context()
	acc, err := env.Svc.AddLocalArchive(ctx, "old")
	require.NoError(t, err)
	folders, err := env.Svc.ListFolders(ctx, acc.ID)
	require.NoError(t, err)
	var inbox int64
	for _, f := range folders {
		if f.Role == "inbox" {
			inbox = f.ID
		}
	}
	// Written out, not coretest.Msg: its body encoding would push this over MaxMessageBytes.
	raw := []byte("From: bob@example.com\r\nTo: me@example.com\r\nSubject: Big\r\nMessage-ID: <big@example.com>\r\n" +
		"Date: Thu, 12 Nov 2026 10:00:00 +0000\r\nContent-Type: text/plain\r\n\r\n" +
		strings.Repeat(strings.Repeat("x", 76)+"\r\n", (24<<20)/78))
	p := filepath.Join(t.TempDir(), "big.eml")
	require.NoError(t, os.WriteFile(p, raw, 0o600))
	sum, err := env.Svc.ImportEML(ctx, acc.ID, inbox, []string{p})
	require.NoError(t, err)
	require.Equal(t, 1, sum.Imported, "%+v", sum)
	page, err := env.Svc.ListMessages(ctx, core.View{FolderID: inbox}, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)

	path, err := cl.RawMessageFile(ctx, page.Items[0].ID)
	require.NoError(t, err)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(raw, got), "the file holds the original")
	fi, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	_, err = cl.ListAccounts(ctx)
	require.NoError(t, err, "the connection survived")
}

// An export too big for one Local API response (signatures repeat per account)
// reaches the interface as a file, and the connection survives.
func TestExportAccountsFileLarge(t *testing.T) {
	env := coretest.New(t, coretest.WithoutAutoSync())
	_, cl, _ := startUIServer(t, env.Svc)
	ctx := t.Context()
	acc := env.AddAccount(t, "alice@example.com")
	sig := strings.Repeat("Best regards from a very long signature. ", (24<<20)/41)
	_, err := env.Svc.UpdateAccount(ctx, acc.ID, core.AccountPatch{Signature: &sig})
	require.NoError(t, err)

	path, err := cl.ExportAccountsFile(ctx)
	require.NoError(t, err)
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Greater(t, len(data), 24<<20)
	var exp core.AccountExport
	require.NoError(t, json.Unmarshal(data, &exp))
	require.Len(t, exp.Accounts, 1)
	_, err = cl.GetSettings(ctx) // ListAccounts carries the signature: too large for one result
	require.NoError(t, err, "the connection survived")
}
