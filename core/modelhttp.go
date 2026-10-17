package core

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/plugins"
	"golang.org/x/net/http/httpguts"
)

func (s *Service) prepareModelHTTP(ctx context.Context, snap modelSnapshot, meta ai.ModelSession, req *http.Request) (*http.Request, error) {
	if req.GetBody == nil {
		return nil, errors.New("model request body cannot be replayed")
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, plugins.MaxResultBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > plugins.MaxResultBytes {
		return nil, plugins.ErrTooLarge
	}
	var original map[string]any
	if json.Unmarshal(raw, &original) != nil || original == nil {
		return nil, errors.New("invalid model request")
	}
	id, _ := original["model"].(string)
	var model ProviderModel
	for _, m := range snap.models {
		if m.ID == id && m.Transport == "http" {
			model = m
			break
		}
	}
	if model.ID == "" {
		return nil, modelUnavailable()
	}
	headers := map[string]string{}
	for k, v := range req.Header {
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "X-Api-Key") {
			continue
		}
		headers[k] = strings.Join(v, ", ")
	}
	call := plugins.PrepareModelRequest{Model: model, SessionID: meta.ID, SessionCreatedAt: meta.CreatedAt, RequestID: rand.Text(), Request: plugins.ModelHTTPRequest{URL: strings.TrimRight(model.BaseURL, "/") + plugins.ProtocolPath(model.Protocol), Headers: headers, Body: original}}
	raw, err = json.Marshal(call)
	if err != nil {
		return nil, errors.New("invalid model request")
	}
	if len(raw) > plugins.MaxResultBytes {
		return nil, plugins.ErrTooLarge
	}
	// Compare against an independent copy; the adapter gets JSON and may mutate it.
	var before plugins.PrepareModelRequest
	if err = json.Unmarshal(raw, &before); err != nil {
		return nil, err
	}
	adapted := call.Request
	if snap.rp.native == nil || s.nativeModelMethod(snap, "models.prepare") {
		err = s.callPluginModel(ctx, snap, "models.prepare", call, &adapted)
	} else if !s.modelSnapshotCurrent(ctx, snap) {
		err = modelUnavailable()
	}
	if err != nil {
		return nil, err
	}
	invalid := func() (*http.Request, error) {
		_ = s.pluginFailed(context.WithoutCancel(ctx), snap.name, snap.rp.gen)
		return nil, errors.New("plugin returned an invalid model request")
	}
	raw, err = json.Marshal(adapted)
	if err != nil || len(raw) > plugins.MaxResultBytes || !validModelBody(model.Protocol, before.Request.Body, adapted.Body) {
		return invalid()
	}
	u, err := url.Parse(adapted.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return invalid()
	}
	allowed := s.modelEgress(snap, u)
	if !modelHostAllowed(u, allowed) {
		return invalid()
	}
	data, err := json.Marshal(adapted.Body)
	if err != nil {
		return invalid()
	}
	out, err := http.NewRequestWithContext(ctx, http.MethodPost, adapted.URL, bytes.NewReader(data))
	if err != nil {
		return invalid()
	}
	connection := []string{}
	for k, v := range adapted.Headers {
		if !httpguts.ValidHeaderFieldName(k) || !httpguts.ValidHeaderFieldValue(v) {
			return invalid()
		}
		if strings.EqualFold(k, "Connection") {
			for _, part := range strings.Split(v, ",") {
				connection = append(connection, strings.ToLower(strings.TrimSpace(part)))
			}
		}
	}
	for k, v := range adapted.Headers {
		key := strings.ToLower(k)
		if modelTransportHeader(key) || slices.Contains(connection, key) {
			continue
		}
		out.Header.Set(k, v)
	}
	out.Header.Set("Content-Type", "application/json")
	out = out.WithContext(context.WithValue(out.Context(), modelProtocolKey{}, model.Protocol))
	if !s.modelSnapshotCurrent(ctx, snap) {
		out.Body.Close()
		return nil, modelUnavailable()
	}
	if err = s.pluginSucceeded(ctx, snap.name, snap.rp.gen); err != nil {
		out.Body.Close()
		return nil, err
	}
	return out, nil
}
func modelTransportHeader(k string) bool {
	return strings.HasPrefix(k, "proxy-") || slices.Contains([]string{"host", "content-length", "connection", "keep-alive", "transfer-encoding", "trailer", "te", "upgrade", "cookie"}, k)
}
func validModelBody(protocol string, original, adapted map[string]any) bool {
	if adapted == nil || !reflect.DeepEqual(original["model"], adapted["model"]) {
		return false
	}
	generate := protocol == "openai-completions" || protocol == "openai-responses" || protocol == "anthropic-message"
	if generate && adapted["stream"] != true {
		return false
	}
	required := []string{"messages", "input", "questions", "max_tokens"}
	for _, k := range required {
		if v, ok := original[k]; ok {
			got, present := adapted[k]
			if !present || reflect.TypeOf(v) != reflect.TypeOf(got) {
				return false
			}
		}
	}
	for _, key := range []string{"tool_choice", "parallel_tool_calls"} {
		if v, ok := original[key]; ok && !reflect.DeepEqual(v, adapted[key]) {
			return false
		}
	}
	originalTools, ok := modelTools(protocol, original["tools"])
	if !ok {
		return false
	}
	adaptedTools, ok := modelTools(protocol, adapted["tools"])
	if !ok {
		return false
	}
	for name, tool := range originalTools {
		if !reflect.DeepEqual(tool, adaptedTools[name]) {
			return false
		}
	}
	return true
}
func modelTools(protocol string, value any) (map[string]any, bool) {
	result := map[string]any{}
	if value == nil {
		return result, true
	}
	tools, ok := value.([]any)
	if !ok {
		return nil, false
	}
	for _, tool := range tools {
		desc, ok := tool.(map[string]any)
		if !ok {
			return nil, false
		}
		name, _ := desc["name"].(string)
		if fn, ok := desc["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		}
		if name == "" {
			return nil, false
		}
		if _, exists := result[name]; exists {
			return nil, false
		}
		result[name] = tool
	}
	return result, true
}
func (s *Service) modelEgress(snap modelSnapshot, u *url.URL) []string {
	if snap.rp.native != nil {
		return []string{plugins.HostPort(u)}
	}
	return snap.rp.row.m.Permissions.Net
}
func modelHostAllowed(u *url.URL, allowed []string) bool {
	for _, host := range allowed {
		if !strings.Contains(host, ":") {
			host += ":443"
		}
		if strings.EqualFold(host, plugins.HostPort(u)) {
			return true
		}
	}
	return false
}

type modelHTTPTransport struct {
	s    *Service
	snap modelSnapshot
}

func (t modelHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.s.modelSnapshotCurrent(req.Context(), t.snap) {
		return nil, modelUnavailable()
	}
	resp, hops, err := t.snap.rp.modelNet.DoModel(req.Context(), t.s.modelEgress(t.snap, req.URL), req)
	h := pluginHost{s: t.s, name: t.snap.name}
	inv := plugins.Invocation{ID: "models.http:" + rand.Text(), Kind: plugins.KindModelsPrepare}
	for _, hop := range hops {
		if auditErr := h.audit(req.Context(), inv, hop.Dest, hop.Decision, hop.Reason); auditErr != nil {
			if resp != nil {
				resp.Body.Close()
			}
			return nil, auditErr
		}
	}
	if err != nil {
		return nil, errors.New("model connection failed")
	}
	if protocol, _ := req.Context().Value(modelProtocolKey{}).(string); protocol == "openai-completions" && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		resp.Body = &modelSSEBody{ReadCloser: resp.Body}
	}
	return resp, nil
}
func (s *Service) modelHTTPClient(snap modelSnapshot) *http.Client {
	return ai.PreparingClient(&http.Client{Transport: modelHTTPTransport{s: s, snap: snap}}, func(ctx context.Context, req *http.Request) (*http.Request, error) {
		return s.prepareModelHTTP(ctx, snap, ai.ModelSessionFromContext(ctx), req)
	})
}

type modelProtocolKey struct{}

// The chat SDK parses empty heartbeat events as JSON and accepts EOF without [DONE].
// Only data fields matter for Chat Completions; retain them without buffering events.
type modelSSEBody struct {
	io.ReadCloser
	reader                                   *bufio.Reader
	pending                                  []byte
	readErr                                  error
	continuation, forward, hasData, terminal bool
}

func (b *modelSSEBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.reader == nil {
		b.reader = bufio.NewReader(b.ReadCloser)
	}
	for len(b.pending) == 0 {
		if b.readErr != nil {
			if b.readErr == io.EOF && !b.terminal {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, b.readErr
		}
		line, err := b.reader.ReadSlice('\n')
		if !b.continuation {
			b.forward = bytes.HasPrefix(line, []byte("data:")) || bytes.Equal(line, []byte("data\n")) || bytes.Equal(line, []byte("data\r\n"))
			if b.forward {
				b.hasData = true
			}
			if bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n")) {
				b.forward, b.hasData = b.hasData, false
			}
			if err == nil && bytes.HasPrefix(line, []byte("data:")) {
				value := bytes.TrimSuffix(bytes.TrimSuffix(line[5:], []byte("\n")), []byte("\r"))
				if bytes.Equal(bytes.TrimPrefix(value, []byte(" ")), []byte("[DONE]")) {
					b.terminal = true
				}
			}
		}
		b.continuation = err == bufio.ErrBufferFull
		if err != bufio.ErrBufferFull {
			b.readErr = err
		}
		if b.forward {
			b.pending = line
		}
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}
