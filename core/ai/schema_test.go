package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godwit-email/godwit/core/internal/testllm"
)

func obj(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

func TestNormalizeInlinesDefs(t *testing.T) {
	in := obj(map[string]any{"a": map[string]any{"$ref": "#/$defs/Addr"}, "b": map[string]any{"$ref": "#/$defs/we~1ird~0"}})
	in["$defs"] = map[string]any{
		"Addr":    map[string]any{"type": "object", "properties": map[string]any{"c": map[string]any{"$ref": "#/$defs/Str"}}},
		"Str":     map[string]any{"type": "string"},
		"we/ird~": map[string]any{"type": "integer"},
	}
	out, _, warns := NormalizeSchema(in)
	assert.Empty(t, warns)
	assert.NotContains(t, out, "$defs")
	assert.Equal(t, obj(map[string]any{
		"a": obj(map[string]any{"c": map[string]any{"type": "string"}}),
		"b": map[string]any{"type": "integer"},
	}), out)
}

func TestNormalizeDefinitions(t *testing.T) {
	in := obj(map[string]any{"a": map[string]any{"$ref": "#/definitions/X"}})
	in["definitions"] = map[string]any{"X": map[string]any{"type": "string"}, "Unused": map[string]any{"type": "number"}}
	out, _, warns := NormalizeSchema(in)
	assert.Empty(t, warns)
	assert.Equal(t, obj(map[string]any{"a": map[string]any{"type": "string"}}), out)
}

func TestNormalizeRootDescription(t *testing.T) {
	in := obj(map[string]any{})
	in["description"] = "Does things."
	out, descr, _ := NormalizeSchema(in)
	assert.Equal(t, "Does things.", descr)
	assert.NotContains(t, out, "description")
}

func TestNormalizeWarnsOnRootOneOf(t *testing.T) {
	for _, k := range []string{"anyOf", "oneOf", "allOf"} {
		_, _, warns := NormalizeSchema(map[string]any{k: []any{map[string]any{"type": "object"}}})
		require.Len(t, warns, 1, k)
		assert.Contains(t, warns[0], k)
	}
}

func TestNormalizeRecursionKeepsDefs(t *testing.T) {
	in := obj(map[string]any{"tree": map[string]any{"$ref": "#/$defs/Node"}})
	in["$defs"] = map[string]any{
		"Node": obj(map[string]any{"kids": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/$defs/Node"}}, "x": map[string]any{"$ref": "#/$defs/Leaf"}}),
		"Leaf": map[string]any{"type": "string"},
		"A":    obj(map[string]any{"b": map[string]any{"$ref": "#/$defs/B"}}),
		"B":    obj(map[string]any{"a": map[string]any{"$ref": "#/$defs/A"}}),
	}
	in["properties"].(map[string]any)["m"] = map[string]any{"$ref": "#/$defs/A"}
	out, _, warns := NormalizeSchema(in)
	assert.NotEmpty(t, warns)
	defs, _ := out["$defs"].(map[string]any)
	var check func(v any)
	n := 0
	check = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if r, ok := v["$ref"].(string); ok {
				n++
				_, found := defs[strings.TrimPrefix(r, "#/$defs/")]
				assert.True(t, found, "dangling %s", r)
			}
			for _, c := range v {
				check(c)
			}
		case []any:
			for _, c := range v {
				check(c)
			}
		}
	}
	check(out)
	assert.Positive(t, n)
	assert.NotContains(t, defs, "Leaf", "non-recursive defs are inlined away")
}

func TestNormalizeDoesNotMutate(t *testing.T) {
	in := obj(map[string]any{"a": map[string]any{"$ref": "#/$defs/S"}})
	in["$defs"] = map[string]any{"S": map[string]any{"type": "string"}}
	in["description"] = "d"
	before, _ := json.Marshal(in)
	out, _, _ := NormalizeSchema(in)
	out["properties"].(map[string]any)["zzz"] = 1
	after, _ := json.Marshal(in)
	assert.JSONEq(t, string(before), string(after))
}

func TestAnthropicReceivesNormalizedSchema(t *testing.T) {
	srv := testllm.Start(t, "anthropic")
	srv.Script(testllm.Turn{Text: []string{"ok"}})
	schema := obj(map[string]any{"a": map[string]any{"$ref": "#/$defs/S"}})
	schema["$defs"] = map[string]any{"S": map[string]any{"type": "string"}}
	schema["description"] = "Root words."
	tools := []ToolSpec{{Name: "t", Description: "Tool.", Schema: schema,
		Run: func(context.Context, json.RawMessage) (any, error) { return nil, nil }}}
	_, res := run(newEngine(t), Request{Model: modelFor(srv, "anthropic", "k"), Prompt: "hi", Tools: tools})
	require.Nil(t, res.Err)
	reqs := srv.Requests()
	require.NotEmpty(t, reqs)
	var got []struct {
		Description string         `json:"description"`
		InputSchema map[string]any `json:"input_schema"`
	}
	require.NoError(t, json.Unmarshal(reqs[0].Tools, &got))
	require.Len(t, got, 1)
	assert.Equal(t, "Tool.\n\nRoot words.", got[0].Description)
	assert.Equal(t, map[string]any{"a": map[string]any{"type": "string"}}, got[0].InputSchema["properties"])
	assert.NotContains(t, got[0].InputSchema, "$defs")
}
