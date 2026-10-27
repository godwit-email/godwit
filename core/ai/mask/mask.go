// Package mask is 脱敏 (docs/adr/0025-mask-at-cloud-egress.md): the keys,
// ID and card numbers in what goes to a model service off this device are
// replaced by placeholders, {{KIND_<12 base32>}}, and the placeholders in
// what comes back are restored to their values. A placeholder is an HMAC of
// its kind and value under the device's key, so a value always gets the
// same one: across turns, across restarts, and the prompt cache holds.
package mask

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Masker masks text and restores it, with one table of the placeholders it
// made, kept in memory only: after a restart, masking the stored values
// again (Learn) fills it back. Safe for concurrent use.
type Masker struct {
	key []byte

	mu     sync.RWMutex
	values map[string]entry // by hash
}

type entry struct{ kind, value string }

// New is a masker with key, the device's secret.
func New(key []byte) *Masker {
	return &Masker{key: slices.Clone(key), values: map[string]entry{}}
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// hashLen is a placeholder's hash in base32 characters: 60 bits.
const hashLen = 12

// errCollision: two values share a hash, and one would restore as the other.
var errCollision = errors.New("mask: two values share a placeholder")

// placeholder is v's placeholder, remembered for Restore.
// shortcut: the table only grows, by each value masked; evict when it is seen to matter.
func (m *Masker) placeholder(kind, v string) (string, error) {
	h := hmac.New(sha256.New, m.key)
	h.Write([]byte(kind + "\x00" + v))
	hash := strings.ToLower(b32.EncodeToString(h.Sum(nil))[:hashLen])
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.values[hash]; ok && e != (entry{kind, v}) {
		return "", errCollision
	}
	m.values[hash] = entry{kind, v}
	return "{{" + kind + "_" + hash + "}}", nil
}

// Text masks the values found in s, text Godwit wrote: a placeholder in it
// (planted in mail) is defused first.
func (m *Masker) Text(s string) (string, error) { return m.text(s, true) }

// Learn masks text, a stored request's or message's (JSON or not), for the
// placeholders alone: the values it holds restore again after a restart.
func (m *Masker) Learn(text string) { m.Body(text) }

// text masks s; defuse says it is not the model's own. A rule that fails
// fails it: what it would have found must not go out.
func (m *Masker) text(s string, defuse bool) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = "", fmt.Errorf("mask: %v", r)
		}
	}()
	if defuse {
		s = m.defuse(s)
	}
	var b strings.Builder
	last := 0
	for _, sp := range find(s) {
		p, err := m.placeholder(sp.kind, s[sp.start:sp.end])
		if err != nil {
			return "", err
		}
		b.WriteString(s[last:sp.start])
		b.WriteString(p)
		last = sp.end
	}
	b.WriteString(s[last:])
	return b.String(), nil
}

// looseRe is a placeholder as a model may write one back: in any case, a
// brace or both dropped, spaces inside them, its kind translated; or with
// no braces, KIND_hash as a word of its own. The hash is group 1 or 2.
var looseRe = regexp.MustCompile(`\{\{?[ \t]*[^{}\s]*_([A-Za-z2-7]{12})[ \t]*\}\}?|\b[A-Za-z][A-Za-z0-9_]*_([A-Za-z2-7]{12})\b`)

// loose calls f with each placeholder looseRe finds in s, its hash and
// whether it is braced, and puts what f returns in its place.
func loose(s string, f func(p, hash string, braced bool) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range looseRe.FindAllStringSubmatchIndex(s, -1) {
		g := 2 // the group that matched
		if loc[g] < 0 {
			g = 4
		}
		braced, hash := g == 2, s[loc[g]:loc[g+1]]
		if r := f(s[loc[0]:loc[1]], strings.ToLower(hash), braced); r != s[loc[0]:loc[1]] {
			b.WriteString(s[last:loc[0]])
			b.WriteString(r)
			last = loc[1]
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// Restore puts back the value of each placeholder in s this masker made,
// however the model wrote it; others are left as they are.
func (m *Masker) Restore(s string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return loose(s, func(p, hash string, _ bool) string {
		if e, ok := m.values[hash]; ok {
			return e.value
		}
		return p
	})
}

// defuse breaks the placeholders in s, as linkRefs' defuseRefs does the
// link references in mail: one written in braces, or one the masker made,
// gets a "-" before its hash, which no placeholder has.
func (m *Masker) defuse(s string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return loose(s, func(p, hash string, braced bool) string {
		if _, ok := m.values[hash]; !ok && !braced {
			return p // a word that only looks like one: report_finalversion
		}
		i := strings.LastIndex(strings.ToLower(p), hash)
		return p[:i-1] + "-" + p[i:]
	})
}
