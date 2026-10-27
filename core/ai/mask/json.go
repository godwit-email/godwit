package mask

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Body masks a model service's JSON request body, in any of the protocols
// pi speaks and the judge formats: only string values, and of those not the
// ids, names and kinds the service matches or reads as structure, nor what
// it sealed (signed thinking, encrypted reasoning: it goes back as the
// service wrote it). Of the tools, only their descriptions are masked. A
// string holding JSON of its own (a tool's arguments or result) is masked
// as that JSON, so values are found in its text as the model reads it.
// What has nothing to mask comes back byte for byte; a body that isn't
// JSON is masked as text.
func (m *Masker) Body(body string) (string, error) {
	out, ok, err := m.json(body, scope{top: true})
	if !ok {
		return m.Text(body)
	}
	return out, err
}

// json masks the JSON document s at sc; ok is false if it is not one.
func (m *Masker) json(s string, sc scope) (out string, ok bool, err error) {
	out, ok = walk(s, sc, func(s string, sc scope) string {
		if t := strings.TrimLeft(s, " \t\r\n"); t != "" && (t[0] == '{' || t[0] == '[') {
			if out, ok, e := m.json(s, scope{tool: true, model: sc.model}); ok {
				err = cmpErr(err, e)
				return out
			}
		}
		out, e := m.text(s, !sc.model)
		err = cmpErr(err, e)
		return out
	})
	return out, ok, err
}

func cmpErr(err, e error) error {
	if err != nil {
		return err
	}
	return e
}

// errUnknown refuses a tool call whose arguments hold a placeholder that
// names no value: the tool would act on the placeholder.
var errUnknown = errors.New("a {{…}} placeholder in the arguments could not be turned back into its value: copy each one exactly as it was given, or leave it out")

// RestoreArgs is a tool call's JSON arguments with the placeholders in its
// strings restored. err, when one is left that names no value, refuses the
// call; out is still all that could be restored, for showing it.
func (m *Masker) RestoreArgs(args json.RawMessage) (out json.RawMessage, err error) {
	s, ok := walk(string(args), scope{tool: true}, func(s string, _ scope) string {
		s = m.Restore(s)
		loose(s, func(p, _ string, braced bool) string {
			if braced || isKind(p) {
				err = errUnknown
			}
			return p
		})
		return s
	})
	if !ok {
		return args, nil // no tool takes it: the call fails as it would have
	}
	return json.RawMessage(s), err
}

// isKind says the bare placeholder p starts with a kind the rules mask:
// BANK_CARD_…, not report_finalversion.
func isKind(p string) bool {
	u := strings.ToUpper(p)
	for _, r := range rules {
		if strings.HasPrefix(u, r.kind+"_") && len(u) == len(r.kind)+1+hashLen {
			return true
		}
	}
	return false
}

// scope is where in a body a value is.
type scope struct {
	key  string // the key it is under
	top  bool   // it is the body itself
	defs bool   // in what the request defines for the service: only descriptions are masked
	tool bool   // in a tool's own JSON: every string is masked
	// model: the model wrote it (an assistant message, a call), so its
	// placeholders are ones the masker gave it, not planted
	model bool
}

// kept are the keys whose strings go as they are: ids, names and kinds the
// service matches or checks, what it sealed, data it decodes.
var kept = map[string]bool{
	"model": true, "id": true, "type": true, "role": true, "name": true, "object": true, "status": true, "event": true,
	"signature": true, "encrypted_content": true, "data": true, "media_type": true, "mime_type": true, "url": true, "image_url": true,
	"prompt_cache_key": true, "effort": true, "reasoning_effort": true, "service_tier": true, "stop_reason": true, "finish_reason": true,
	"namespace": true, "server_label": true, "server_name": true,
}

// isID says a key holds an id the service matches against another: a tool
// result's tool_call_id against its call's id.
func isID(key string) bool {
	return strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "_ids") || strings.HasSuffix(key, "Id")
}

// defKeys are a request's own keys whose values define (tools, the output's
// shape, reasoning and streaming settings) rather than say.
var defKeys = map[string]bool{
	"tools": true, "tool_choice": true, "functions": true, "function_call": true, "text": true, "reasoning": true, "include": true,
	"stream_options": true, "response_format": true, "thinking": true, "metadata": true, "output_config": true, "output_format": true,
}

// skipped says the string s at sc goes as it is.
func skipped(s string, sc scope) bool {
	switch {
	case strings.HasPrefix(s, "data:"):
		return true
	case sc.tool:
		return false
	case sc.defs:
		return sc.key != "description"
	}
	return kept[sc.key] || isID(sc.key)
}

// walk calls fn with each string of the JSON document s that skipped
// leaves, and puts what it returns in its place; ok is false if s is not
// JSON. The rest stays byte for byte.
func walk(s string, sc scope, fn func(s string, sc scope) string) (out string, ok bool) {
	w := walker{in: s, fn: fn}
	end, ok := w.value(w.ws(0), sc)
	if !ok || w.ws(end) != len(s) {
		return s, false
	}
	if w.last == 0 {
		return s, true
	}
	w.out.WriteString(s[w.last:])
	return w.out.String(), true
}

// walker rewrites a JSON document's strings in place: out is in up to
// last, each string fn changed replaced.
type walker struct {
	in   string
	fn   func(s string, sc scope) string
	out  strings.Builder
	last int
}

func (w *walker) ws(i int) int {
	for i < len(w.in) && strings.IndexByte(" \t\r\n", w.in[i]) >= 0 {
		i++
	}
	return i
}

// member is an object's key and its value at in[start:end].
type member struct {
	key        string
	start, end int
}

// value rewrites the value at in[i:] and returns its end.
func (w *walker) value(i int, sc scope) (int, bool) {
	if i >= len(w.in) {
		return i, false
	}
	switch w.in[i] {
	case '{':
		ms, end, ok := w.members(i)
		if !ok {
			return i, false
		}
		typ := w.str(ms, "type")
		if !sc.tool && (typ == "thinking" || typ == "redacted_thinking" || typ == "reasoning") &&
			(w.str(ms, "signature") != "" || w.str(ms, "encrypted_content") != "") {
			return end, true // sealed by the service
		}
		model := sc.model || !sc.tool && (w.str(ms, "role") == "assistant" || typ == "function_call")
		for _, mb := range ms {
			c := scope{key: mb.key, defs: sc.defs, tool: sc.tool, model: model}
			switch {
			case sc.tool:
			case sc.top && defKeys[mb.key]:
				c.defs = mb.key != "text" || w.in[mb.start] == '{' // Responses' text is settings, a completion's is a prompt
			case mb.key == "input" && (typ == "tool_use" || typ == "server_tool_use"):
				c.tool = true
			}
			if _, ok := w.value(mb.start, c); !ok {
				return i, false
			}
		}
		return end, true
	case '[':
		i = w.ws(i + 1)
		if i < len(w.in) && w.in[i] == ']' {
			return i + 1, true
		}
		for {
			end, ok := w.value(i, scope{key: sc.key, defs: sc.defs, tool: sc.tool, model: sc.model}) // an array's strings are under its key
			if !ok {
				return i, false
			}
			switch i = w.ws(end); {
			case i < len(w.in) && w.in[i] == ',':
				i = w.ws(i + 1)
			case i < len(w.in) && w.in[i] == ']':
				return i + 1, true
			default:
				return i, false
			}
		}
	case '"':
		end, ok := w.skipString(i)
		if !ok {
			return i, false
		}
		var s string
		if json.Unmarshal([]byte(w.in[i:end]), &s) != nil {
			return i, false
		}
		if skipped(s, sc) {
			return end, true
		}
		if t := w.fn(s, sc); t != s {
			w.out.WriteString(w.in[w.last:i])
			w.out.WriteByte('"')
			w.out.WriteString(jsonEscape(t))
			w.out.WriteByte('"')
			w.last = end
		}
		return end, true
	}
	return w.skip(i)
}

// members are the object's at in[i:], read but not rewritten, and its end.
func (w *walker) members(i int) ([]member, int, bool) {
	var ms []member
	i = w.ws(i + 1)
	if i < len(w.in) && w.in[i] == '}' {
		return nil, i + 1, true
	}
	for {
		if i >= len(w.in) || w.in[i] != '"' {
			return nil, i, false
		}
		kend, ok := w.skipString(i)
		var k string
		if !ok || json.Unmarshal([]byte(w.in[i:kend]), &k) != nil {
			return nil, i, false
		}
		if i = w.ws(kend); i >= len(w.in) || w.in[i] != ':' {
			return nil, i, false
		}
		start := w.ws(i + 1)
		end, ok := w.skip(start)
		if !ok {
			return nil, i, false
		}
		ms = append(ms, member{k, start, end})
		switch i = w.ws(end); {
		case i < len(w.in) && w.in[i] == ',':
			i = w.ws(i + 1)
		case i < len(w.in) && w.in[i] == '}':
			return ms, i + 1, true
		default:
			return nil, i, false
		}
	}
}

// str is the string value of member key of ms, "" if it has none.
func (w *walker) str(ms []member, key string) string {
	for _, mb := range ms {
		if mb.key == key {
			var s string
			json.Unmarshal([]byte(w.in[mb.start:mb.end]), &s)
			return s
		}
	}
	return ""
}

// skip is the end of the value at in[i:].
func (w *walker) skip(i int) (int, bool) {
	if i >= len(w.in) {
		return i, false
	}
	switch w.in[i] {
	case '"':
		return w.skipString(i)
	case '{', '[':
		depth := 0
		for j := i; j < len(w.in); j++ {
			switch w.in[j] {
			case '"':
				end, ok := w.skipString(j)
				if !ok {
					return j, false
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return j + 1, true
				}
			}
		}
		return i, false
	}
	j := i
	for j < len(w.in) && strings.IndexByte(",]} \t\r\n", w.in[j]) < 0 {
		j++
	}
	return j, j > i
}

// skipString is the end of the string literal at in[i:].
func (w *walker) skipString(i int) (int, bool) {
	for j := i + 1; j < len(w.in); j++ {
		switch w.in[j] {
		case '\\':
			j++
		case '"':
			return j + 1, true
		}
	}
	return i, false
}

// jsonEscape is s as it goes between a JSON string's quotes, < > & as they are.
func jsonEscape(s string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.Encode(s)
	return string(b.Bytes()[1 : b.Len()-2]) // the quotes and the newline
}
