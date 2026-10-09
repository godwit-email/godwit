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
	kept := map[string]bool{}
	warned := map[string]bool{}
	warn := func(key, msg string) {
		if !warned[key] {
			warned[key] = true
			warnings = append(warnings, msg)
		}
	}
	var walk func(v any, visiting map[string]bool) any
	walkKeywords := func(m map[string]any, visiting map[string]bool) map[string]any {
		for k, v := range m {
			switch {
			case oneSchema[k]:
				m[k] = walk(v, visiting)
			case schemaList[k]:
				if l, ok := v.([]any); ok {
					for i := range l {
						l[i] = walk(l[i], visiting)
					}
				} else if k == "items" {
					m[k] = walk(v, visiting)
				}
			case schemaMap[k]:
				if sm, ok := v.(map[string]any); ok {
					for n := range sm {
						sm[n] = walk(sm[n], visiting)
					}
				}
			}
		}
		return m
	}
	walk = func(v any, visiting map[string]bool) any {
		m, ok := v.(map[string]any)
		if !ok {
			return v
		}
		ref, isRef := m["$ref"].(string)
		if !isRef {
			return walkKeywords(m, visiting)
		}
		delete(m, "$ref")
		sibs := walkKeywords(m, visiting)
		def, found := defs[ref]
		switch {
		case !found:
			warn(ref, "unresolved reference "+ref)
		case visiting[ref]:
			kept[ref] = true
			warn(ref, "recursive definition "+ref+" kept as a reference")
		default:
			visiting[ref] = true
			res := walk(deepCopy(def), visiting)
			delete(visiting, ref)
			if b, isBool := res.(bool); isBool {
				if !b {
					return false
				}
				return sibs // true accepts anything: only the siblings remain
			}
			rm, _ := res.(map[string]any)
			if rm == nil {
				break
			}
			asserts := map[string]any{}
			for k, x := range sibs {
				if annotation[k] {
					rm[k] = x
				} else {
					asserts[k] = x
				}
			}
			if len(asserts) == 0 {
				return rm
			}
			// Both the definition's and the sibling constraints must hold.
			outer := map[string]any{"allOf": []any{rm, asserts}}
			for k := range annotation {
				if x, ok := rm[k]; ok {
					outer[k] = x
					delete(rm, k)
				}
			}
			return outer
		}
		sibs["$ref"] = ref
		return sibs
	}
	switch r := walk(out, map[string]bool{}).(type) {
	case map[string]any:
		out = r
	case bool: // a root $ref to a boolean schema keeps its meaning
		out = map[string]any{}
		if !r {
			out["not"] = map[string]any{}
		}
	}
	// The root may itself have been a $ref: inspect the expanded root.
	if d, ok := out["description"].(string); ok {
		descr = d
		delete(out, "description")
	}
	for _, k := range []string{"anyOf", "oneOf", "allOf"} {
		if _, ok := out[k]; ok {
			warnings = append(warnings, fmt.Sprintf("root %s is not supported by every provider", k))
		}
	}

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
			container[unescapePtr(parts[2])] = walk(deepCopy(defs[ref]), map[string]bool{ref: true})
		}
	}
	return out, descr, warnings
}

// Keywords whose values are schemas; anything else (const, enum, default,
// examples...) is data and never rewritten.
var (
	oneSchema = set("additionalProperties", "additionalItems", "contains", "not", "if", "then", "else",
		"propertyNames", "unevaluatedItems", "unevaluatedProperties")
	schemaList = set("allOf", "anyOf", "oneOf", "prefixItems", "items")
	schemaMap  = set("properties", "patternProperties", "dependentSchemas", "$defs", "definitions")
	annotation = set("description", "title", "default", "examples", "example", "deprecated", "readOnly", "writeOnly", "$comment")
)

func set(k ...string) map[string]bool {
	m := map[string]bool{}
	for _, s := range k {
		m[s] = true
	}
	return m
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

// strictSchema is a copy of in with additionalProperties:false on every object
// reached through properties and items: what Genkit's compat_oai does for a
// strict tool, so both OpenAI protocols send the same schema.
func strictSchema(in map[string]any) map[string]any {
	out := copyMap(in)
	var walk func(s map[string]any)
	walk = func(s map[string]any) {
		switch s["type"] {
		case "object":
			s["additionalProperties"] = false
			props, _ := s["properties"].(map[string]any)
			for _, p := range props {
				if p, ok := p.(map[string]any); ok {
					walk(p)
				}
			}
		case "array":
			if it, ok := s["items"].(map[string]any); ok {
				walk(it)
			}
		}
	}
	if out != nil {
		walk(out)
	}
	return out
}
