package ai

import (
	"fmt"
	"strings"
)

// NormalizeSchema flattens a tool input schema for providers that keep only the
// root type/properties/required (Genkit's Anthropic plugin, spec 6.3): local
// $defs/definitions references are inlined, the root description is returned as
// descr, and constructs that cannot be flattened (recursive definitions, root
// anyOf/oneOf/allOf, unresolved refs) are reported in warnings. Recursive
// definitions are kept so every remaining $ref resolves. in is not modified.
func NormalizeSchema(in map[string]any) (out map[string]any, descr string, warnings []string) {
	out = copyMap(in)
	defs := map[string]any{} // "#/$defs/Name" -> definition
	for _, key := range []string{"$defs", "definitions"} {
		if m, ok := out[key].(map[string]any); ok {
			for name, d := range m {
				defs["#/"+key+"/"+escapePtr(name)] = d
			}
		}
		delete(out, key)
	}
	if d, ok := out["description"].(string); ok {
		descr = d
		delete(out, "description")
	}
	for _, k := range []string{"anyOf", "oneOf", "allOf"} {
		if _, ok := out[k]; ok {
			warnings = append(warnings, fmt.Sprintf("root %s is not supported by every provider", k))
		}
	}

	kept := map[string]bool{}
	warned := map[string]bool{}
	var inline func(v any, visiting map[string]bool) any
	inline = func(v any, visiting map[string]bool) any {
		switch v := v.(type) {
		case []any:
			for i := range v {
				v[i] = inline(v[i], visiting)
			}
			return v
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				def, found := defs[ref]
				switch {
				case !found:
					if !warned[ref] {
						warned[ref] = true
						warnings = append(warnings, "unresolved reference "+ref)
					}
				case visiting[ref]:
					kept[ref] = true
					if !warned[ref] {
						warned[ref] = true
						warnings = append(warnings, "recursive definition "+ref+" kept as a reference")
					}
				default:
					visiting[ref] = true
					res, _ := inline(deepCopy(def), visiting).(map[string]any)
					delete(visiting, ref)
					if res == nil { // boolean schema etc.
						return v
					}
					for k, s := range v { // siblings of $ref override
						if k != "$ref" {
							res[k] = inline(s, visiting)
						}
					}
					return res
				}
				return v
			}
			for k := range v {
				v[k] = inline(v[k], visiting)
			}
		}
		return v
	}
	out = inline(out, map[string]bool{}).(map[string]any)

	// Kept definitions may reference further recursive ones: process to a fixpoint.
	done := map[string]bool{}
	for more := true; more; {
		more = false
		for ref := range kept {
			if done[ref] {
				continue
			}
			done[ref], more = true, true
			parts := strings.SplitN(ref, "/", 3) // "#", container, name
			container, _ := out[parts[1]].(map[string]any)
			if container == nil {
				container = map[string]any{}
				out[parts[1]] = container
			}
			container[unescapePtr(parts[2])] = inline(deepCopy(defs[ref]), map[string]bool{ref: true})
		}
	}
	return out, descr, warnings
}

func escapePtr(s string) string { return strings.NewReplacer("~", "~0", "/", "~1").Replace(s) }

func unescapePtr(s string) string { return strings.NewReplacer("~1", "/", "~0", "~").Replace(s) }

func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return deepCopy(m).(map[string]any)
}

func deepCopy(v any) any {
	switch v := v.(type) {
	case map[string]any:
		c := make(map[string]any, len(v))
		for k, x := range v {
			c[k] = deepCopy(x)
		}
		return c
	case []any:
		c := make([]any, len(v))
		for i, x := range v {
			c[i] = deepCopy(x)
		}
		return c
	}
	return v
}
