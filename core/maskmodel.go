package core

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/ai/mask/guard"
)

// ErrMaskModelMissing refuses to turn 识别模型 on: its files are not in
// the cache (maskModelDir).
var ErrMaskModelMissing = errors.New("the recognition model's files are not on this device")

// maskModelDir is where 识别模型's files are, by revision: under cache/,
// for they are downloaded again when lost and stay behind when the data
// directory moves (docs/adr/0028-local-model-for-masking.md).
func (s *Service) maskModelDir() string {
	return filepath.Join(s.dataDir, "cache", "guard", guard.Revision)
}

// MaskModelInstalled says 识别模型's files are all on this device: only
// then can it be turned on.
func (s *Service) MaskModelInstalled() bool {
	for _, f := range guard.Files {
		if _, err := os.Stat(filepath.Join(s.maskModelDir(), f)); err != nil {
			return false
		}
	}
	return true
}

// recognizer is 识别模型 as st sets it, nil while it or masking is off:
// Options.Recognizer, else AstrLink Guard, kept until it is turned off
// (maskingChanged). Called with maskMu held.
func (s *Service) recognizer(st Settings) mask.Recognizer {
	switch {
	case !st.Masking || !st.MaskModel:
		return nil
	case s.opts.Recognizer != nil:
		return s.opts.Recognizer
	}
	if s.guard == nil {
		s.guard = newGuardModel(s.maskModelDir())
	}
	return s.guard
}

// guardModel is AstrLink Guard (core/ai/mask/guard) from its files in a
// directory, loaded on first use. What it found in a text is kept in
// memory by the text's hash and what reads it (CoreML or Go): its revision
// is the directory's.
type guardModel struct {
	load func() (*guard.Model, error)
	mu   sync.Mutex
	seen map[[sha256.Size]byte][]mask.Recognized
	size int // the bytes of text seen holds the spans of
}

func newGuardModel(dir string) *guardModel {
	return &guardModel{load: sync.OnceValues(func() (*guard.Model, error) { return guard.Load(dir) }), seen: map[[sha256.Size]byte][]mask.Recognized{}}
}

// guardMemoBytes bounds the text guardModel keeps what it found in.
const guardMemoBytes = 16 << 20

func (g *guardModel) Recognize(text string) ([]mask.Recognized, error) {
	m, err := g.load()
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte(m.Device() + "\x00" + text))
	g.mu.Lock()
	found, ok := g.seen[key]
	g.mu.Unlock()
	if ok {
		return found, nil
	}
	for _, sp := range m.Find(text) {
		found = append(found, mask.Recognized(sp))
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.size += len(text); g.size > guardMemoBytes {
		clear(g.seen) // shortcut: forgets everything at once, enough while one history fits
		g.size = len(text)
	}
	g.seen[key] = found
	return found, nil
}
