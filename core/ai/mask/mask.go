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
	"html"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// Masker masks text and restores it, with one table of the placeholders it
// made, kept in memory only: after a restart, masking the stored values
// again (Learn) fills it back. Safe for concurrent use; a nil Masker
// restores nothing.
type Masker struct {
	key     []byte
	off     atomic.Bool
	namesOn atomic.Bool
	cfg     atomic.Pointer[compiled]

	// Record, if set, is told what each Request found, with the feature
	// its context is Tagged with. Set it before the first Request.
	Record func(feature string, hits []Hit)

	mu      sync.RWMutex
	values  map[string]entry // by hash
	learned map[[sha256.Size]byte]bool
	names   []string // the values of NAME placeholders, longest first
}

type entry struct{ kind, value string }

// New is a masker with key, the device's secret, on.
func New(key []byte) *Masker {
	m := &Masker{key: slices.Clone(key), values: map[string]entry{}, learned: map[[sha256.Size]byte]bool{}}
	m.cfg.Store(&compiled{})
	return m
}

// On says whether what is sent is to be masked (the switch, 总开关): the
// callers that send ask it. Restoring goes on either way.
func (m *Masker) On() bool { return !m.off.Load() }

// SetOn turns masking on or off.
func (m *Masker) SetOn(on bool) { m.off.Store(!on) }

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// hashLen is a placeholder's hash in base32 characters: 60 bits.
const hashLen = 12

// errCollision: two values share a hash, and one would restore as the other.
var errCollision = errors.New("mask: two values share a placeholder")

// hash is the hash part of the placeholder of v, of kind.
func (m *Masker) hash(kind, v string) string {
	h := hmac.New(sha256.New, m.key)
	h.Write([]byte(kind + "\x00" + v))
	return strings.ToLower(b32.EncodeToString(h.Sum(nil))[:hashLen])
}

// placeholder is v's placeholder, remembered for Restore.
// shortcut: the table only grows, by each value masked; evict when it is seen to matter.
func (m *Masker) placeholder(kind, v string) (string, error) {
	hash := m.hash(kind, v)
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.values[hash]; ok && e != (entry{kind, v}) {
		return "", errCollision
	}
	if _, ok := m.values[hash]; !ok && kind == nameKind {
		m.addName(v)
	}
	m.values[hash] = entry{kind, v}
	return "{{" + kind + "_" + hash + "}}", nil
}

// Text masks the values found in s, text Godwit wrote: a placeholder in it
// (planted in mail) is defused first. A category set to Block fails it
// (*BlockedError); one set to Warn goes as it is.
func (m *Masker) Text(s string) (string, error) { return m.textTo(s, nil) }

// textTo is Text, what it finds added to f.
func (m *Masker) textTo(s string, f *Found) (string, error) {
	if err := m.learnNames(s); err != nil {
		return "", err
	}
	out, err := m.text(s, true, f)
	if err != nil {
		return "", err
	}
	return out, nil
}

// Learn masks text, a stored request's or message's (JSON or not), for the
// placeholders alone: the values it holds restore again after a restart.
// A text learned before is not read again.
// shortcut: learned keeps a hash per text for good; a bounded cache if it is seen to matter.
func (m *Masker) Learn(text string) {
	sum := sha256.Sum256([]byte(text))
	m.mu.Lock()
	seen := m.learned[sum]
	m.learned[sum] = true
	m.mu.Unlock()
	if !seen {
		m.Body(text)
	}
}

// text masks s; defuse says it is not the model's own. A rule that fails
// fails it: what it would have found must not go out. Each value found is
// added to f; one of a category set to Block fails it (*BlockedError), but
// only once all of s is masked, so a blocked request still teaches the
// masker its placeholders (Learn).
func (m *Masker) text(s string, defuse bool, f *Found) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = "", fmt.Errorf("mask: %v", r)
		}
	}()
	if defuse {
		s = m.defuse(s)
	}
	var b strings.Builder
	var block *BlockedError
	last := 0
	cc := m.cfg.Load()
	for _, sp := range cc.find(s, m.nameSpans(s)) {
		v, act := s[sp.start:sp.end], cc.action(sp.kind)
		f.add(m.hit(sp.kind, v, act))
		switch act {
		case Warn:
			continue
		case Block:
			block = block.with(categories[sp.kind])
			continue
		}
		p, err := m.placeholder(sp.kind, v)
		if err != nil {
			return "", err
		}
		b.WriteString(s[last:sp.start])
		b.WriteString(p)
		last = sp.end
	}
	b.WriteString(s[last:])
	if block != nil {
		return b.String(), block
	}
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
func (m *Masker) Restore(s string) string { return m.restore(s, nil) }

// RestoreHTML is Restore for HTML, a draft's body: each value is escaped,
// and a placeholder whose braces are percent-encoded, as sanitizing leaves
// one in an href, restores too.
func (m *Masker) RestoreHTML(s string) string { return m.restore(unencode(s), html.EscapeString) }

// restore is Restore, each value through esc if set.
func (m *Masker) restore(s string, esc func(string) string) string {
	if m == nil {
		return s
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return loose(s, func(p, hash string, _ bool) string {
		e, ok := m.values[hash]
		switch {
		case !ok:
			return p
		case esc != nil:
			return esc(e.value)
		}
		return e.value
	})
}

// encodedRe is a placeholder with its braces percent-encoded.
var encodedRe = regexp.MustCompile(`(?i)(?:%7B){1,2}[A-Za-z0-9_]*_[A-Za-z2-7]{12}(?:%7D){1,2}`)

var braces = strings.NewReplacer("%7B", "{", "%7b", "{", "%7D", "}", "%7d", "}")

// unencode is s with the braces of each placeholder in it decoded.
func unencode(s string) string {
	return encodedRe.ReplaceAllStringFunc(s, braces.Replace)
}

// Left says s, about to be mailed, still holds a placeholder (its braces
// percent-encoded too): one in braces, or a bare one of a kind a rule masks.
// Sending it would mail the placeholder, not the value.
func Left(s string) bool {
	left := false
	loose(unencode(s), func(p, _ string, braced bool) string {
		left = left || braced || isKind(p)
		return p
	})
	return left
}

// defuse breaks the placeholders in s, as core's defuseRefs does link
// references planted in mail: one written in braces, or one the masker
// made, gets a "-" for the "_" before its hash, which no placeholder has.
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
