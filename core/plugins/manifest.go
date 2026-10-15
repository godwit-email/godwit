package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/godwit-email/godwit/core/skills"
)

// Manifest is a plugin's plugin.json (spec 5.3–5.4). A script plugin has
// Main and Permissions; a native one (docs/superpowers/specs/2026-10-14-native-plugins-design.md)
// has Native instead, and its shared library is LibraryName() beside plugin.json.
type Manifest struct {
	Name         string        `json:"name"`
	Version      string        `json:"version"`
	Description  Text          `json:"description"` // one line, shown under the name in settings
	Main         string        `json:"main,omitempty"`
	Native       *NativeSpec   `json:"native,omitempty"`
	Capabilities []string      `json:"capabilities,omitempty"` // what it provides to Godwit's own features
	Permissions  Permissions   `json:"permissions"`
	Config       []ConfigField `json:"config"`
}

// NativeABI is the C ABI version this build of Godwit loads.
const NativeABI uint32 = 1

// NativeSpec marks a native plugin: the C ABI version its library exports.
type NativeSpec struct {
	ABI uint32 `json:"abi"`
}

// IsNative reports whether m is a native (shared library) plugin.
func (m Manifest) IsNative() bool { return m.Native != nil }

// capabilities a plugin may declare.
var knownCapabilities = []string{"translate"}

// Permissions: Mail holds "read" and/or "write" (there is no send); Net the
// hosts, each "host" or "host:port", lowercased and without ":443" after
// ParseManifest.
type Permissions struct {
	Mail []string `json:"mail"`
	Net  []string `json:"net"`
	LLM  bool     `json:"llm"`
}

// Text is a label: a plain string, or {"default": ..., "<lang>": ...}.
type Text struct {
	Default string
	ByLang  map[string]string
}

// For returns the text for lang, then for its base language ("de" for
// "de-AT"), then the default.
func (t Text) For(lang string) string {
	if s, ok := t.ByLang[lang]; ok {
		return s
	}
	if base, _, ok := strings.Cut(lang, "-"); ok {
		if s, ok := t.ByLang[base]; ok {
			return s
		}
	}
	return t.Default
}

func (t *Text) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if s, ok := v.(string); ok {
		*t = Text{Default: s}
		return nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return errors.New("text must be a string or an object of strings")
	}
	m := map[string]string{}
	for k, x := range obj {
		s, ok := x.(string)
		if !ok {
			return fmt.Errorf("text %q must be a string", k)
		}
		m[k] = s
	}
	def, ok := m["default"]
	if !ok {
		return errors.New(`text object needs "default"`)
	}
	delete(m, "default")
	if len(m) == 0 {
		m = nil
	}
	*t = Text{def, m}
	return nil
}

func (t Text) MarshalJSON() ([]byte, error) {
	if len(t.ByLang) == 0 {
		return json.Marshal(t.Default)
	}
	m := map[string]string{"default": t.Default}
	for k, v := range t.ByLang {
		m[k] = v
	}
	return json.Marshal(m)
}

type Option struct {
	Value string `json:"value"`
	Label Text   `json:"label"`
}

// When shows a field only if the earlier field Key equals (or does not equal)
// a value of that field's type.
type When struct {
	Key       string          `json:"key"`
	Equals    json.RawMessage `json:"equals,omitempty"`
	NotEquals json.RawMessage `json:"notEquals,omitempty"`
}

// Match reports whether the condition holds for v, the referenced field's
// JSON value (nil when unset). Values compare by type and value: "true" never
// equals true, 3.0 equals 3.
func (w *When) Match(v json.RawMessage) bool {
	got := scalar(v)
	if w.Equals != nil {
		return got != nil && got == scalar(w.Equals)
	}
	return got != scalar(w.NotEquals)
}

// scalar decodes a JSON string, number or boolean; anything else is nil.
func scalar(raw json.RawMessage) any {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	switch v.(type) {
	case string, float64, bool:
		return v
	}
	return nil
}

// ConfigField is one setting the user fills in. Type is text, secret, number,
// boolean or select. A secret has no default and is stored in the keychain.
type ConfigField struct {
	Key         string          `json:"key"`
	Type        string          `json:"type"`
	Label       Text            `json:"label"`
	Options     []Option        `json:"options,omitempty"`
	AllowCustom bool            `json:"allowCustom,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
	When        *When           `json:"when,omitempty"`
}

var (
	keyRE   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// ParseManifest parses and strictly validates plugin.json of the plugin
// installed in the directory dirName. A manifest that fails is not loaded.
func ParseManifest(data []byte, dirName string) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("plugin.json: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Manifest{}, errors.New("plugin.json: data after the manifest")
	}
	if !skills.ValidName(m.Name) || m.Name != dirName {
		return Manifest{}, fmt.Errorf("plugin name %q: must match its directory and use lowercase letters, digits and hyphens", m.Name)
	}
	if m.Version == "" {
		return Manifest{}, fmt.Errorf("plugin %s: version is required", m.Name)
	}
	for i, c := range m.Capabilities {
		if !slices.Contains(knownCapabilities, c) || slices.Contains(m.Capabilities[:i], c) {
			return Manifest{}, fmt.Errorf("plugin %s: capability %q is unknown or repeated", m.Name, c)
		}
	}
	if m.IsNative() {
		// Native code runs with all of Godwit's rights: permissions would read as limits it does not have.
		switch {
		case m.Main != "":
			return Manifest{}, fmt.Errorf("plugin %s: a native plugin has no main", m.Name)
		case len(m.Permissions.Mail) > 0 || len(m.Permissions.Net) > 0 || m.Permissions.LLM:
			return Manifest{}, fmt.Errorf("plugin %s: a native plugin declares no permissions", m.Name)
		case m.Native.ABI != NativeABI:
			return Manifest{}, fmt.Errorf("plugin %s: native ABI %d is not the supported %d", m.Name, m.Native.ABI, NativeABI)
		case len(m.Capabilities) == 0:
			return Manifest{}, fmt.Errorf("plugin %s: a native plugin needs capabilities", m.Name)
		}
	} else if !fs.ValidPath(m.Main) || m.Main == "." || strings.ContainsAny(m.Main, `:\`) || // no ":" or backslash: "C:/x.ts" and "C:x.ts" leave the plugin on Windows
		!(strings.HasSuffix(m.Main, ".ts") || strings.HasSuffix(m.Main, ".js")) {
		return Manifest{}, fmt.Errorf("plugin %s: main %q must be a relative .ts or .js path inside the plugin", m.Name, m.Main)
	}
	for _, p := range m.Permissions.Mail {
		if p != "read" && p != "write" {
			return Manifest{}, fmt.Errorf("plugin %s: mail permission %q is not read or write", m.Name, p)
		}
	}
	for i, h := range m.Permissions.Net {
		if !validHost(h) {
			return Manifest{}, fmt.Errorf("plugin %s: net %q must be a host name or host:port", m.Name, h)
		}
		m.Permissions.Net[i] = strings.TrimSuffix(strings.ToLower(h), ":443") // 443 is the default
	}
	if err := checkConfig(m.Config); err != nil {
		return Manifest{}, fmt.Errorf("plugin %s: config: %w", m.Name, err)
	}
	return m, nil
}

// validHost accepts a DNS name, optionally with a port: no IP literal (the last
// label must start with a letter), wildcard, scheme, path or userinfo.
func validHost(h string) bool {
	h = strings.ToLower(h)
	if host, port, ok := strings.Cut(h, ":"); ok {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || port != strconv.Itoa(n) {
			return false
		}
		h = host
	}
	labels := strings.Split(h, ".")
	last := labels[len(labels)-1]
	if len(h) > 253 || last == "" || last[0] < 'a' || last[0] > 'z' {
		return false
	}
	for _, l := range labels {
		if !labelRE.MatchString(l) {
			return false
		}
	}
	return true
}

func checkConfig(fields []ConfigField) error {
	seen := map[string]ConfigField{}
	for _, f := range fields {
		if !keyRE.MatchString(f.Key) {
			return fmt.Errorf("key %q: letters, digits and underscores, starting with a letter", f.Key)
		}
		if _, dup := seen[f.Key]; dup {
			return fmt.Errorf("duplicate key %q", f.Key)
		}
		switch f.Type {
		case "text", "secret", "number", "boolean", "select":
		default:
			return fmt.Errorf("%s: unknown type %q", f.Key, f.Type)
		}
		if f.Type != "select" && (len(f.Options) > 0 || f.AllowCustom) {
			return fmt.Errorf("%s: only a select has options", f.Key)
		}
		if f.Type == "select" {
			if len(f.Options) == 0 {
				return fmt.Errorf("%s: a select needs options", f.Key)
			}
			vals := map[string]bool{}
			for _, o := range f.Options {
				if o.Value == "" || vals[o.Value] {
					return fmt.Errorf("%s: option values must be non-empty and unique", f.Key)
				}
				vals[o.Value] = true
			}
		}
		if f.Default != nil {
			if f.Type == "secret" {
				return fmt.Errorf("%s: a secret has no default", f.Key)
			}
			if !fits(f, f.Default) {
				return fmt.Errorf("%s: default does not match the field", f.Key)
			}
		}
		if w := f.When; w != nil {
			ref, ok := seen[w.Key]
			if !ok {
				return fmt.Errorf("%s: when refers to %q, which is not an earlier field", f.Key, w.Key)
			}
			if ref.Type == "secret" {
				return fmt.Errorf("%s: when cannot refer to a secret", f.Key)
			}
			if (w.Equals == nil) == (w.NotEquals == nil) {
				return fmt.Errorf("%s: when needs exactly one of equals and notEquals", f.Key)
			}
			if !fits(ref, either(w.Equals, w.NotEquals)) {
				return fmt.Errorf("%s: when compares %q with a value of another type", f.Key, w.Key)
			}
		}
		seen[f.Key] = f
	}
	return nil
}

// Accepts reports whether v is a value f can hold (not for a secret).
func (f ConfigField) Accepts(v json.RawMessage) bool { return fits(f, v) }

// fits reports whether v is a value field f can hold.
func fits(f ConfigField, v json.RawMessage) bool {
	switch s := scalar(v).(type) {
	case string:
		return f.Type == "text" || f.Type == "select" && (f.AllowCustom || slices.ContainsFunc(f.Options, func(o Option) bool { return o.Value == s }))
	case float64:
		return f.Type == "number"
	case bool:
		return f.Type == "boolean"
	}
	return false
}

func either(a, b json.RawMessage) json.RawMessage {
	if a != nil {
		return a
	}
	return b
}

// PermissionHash identifies what the user granted: it changes when the mail,
// net or llm permissions change, and not with their order, case, a ":443" or the config.
func (m Manifest) PermissionHash() string {
	norm := func(xs []string) []string { return slices.Compact(slices.Sorted(slices.Values(xs))) }
	b, _ := json.Marshal(Permissions{norm(m.Permissions.Mail), norm(m.Permissions.Net), m.Permissions.LLM})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
