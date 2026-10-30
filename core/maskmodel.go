package core

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/ai/mask/guard"
	"github.com/godwit-email/godwit/core/plugins"
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

// ErrMaskModelMismatch: a downloaded file of 识别模型 was not the one its
// revision has, and was discarded.
var ErrMaskModelMismatch = errors.New("a downloaded file of the recognition model does not match its checksum; it was discarded")

// maskModelFiles are guard.Files as Hugging Face has them at
// guard.Revision, by path under a source (maskModelSources). Tests replace them.
var maskModelFiles = []plugins.AssetFile{
	{Name: "model_int8.onnx", URL: "int8/model_int8.onnx", Size: 330781, SHA256: "0a070d3113615e33ba3b84d6bd486f663a377c04c4b056e7381732179f51832a"},
	{Name: "model_int8.onnx_data", URL: "int8/model_int8.onnx_data", Size: 37124608, SHA256: "0ba34216ceead01e0ae2dfd8e46d52b67e7aee80b04f722d9b758613a91c0067"},
	{Name: "tokenizer.json", URL: "int8/tokenizer.json", Size: 1502881, SHA256: "0cf3b235ef125658015a6c8026a5fe6f28088bf825101a990ec18a10200feef4"},
}

// maskModelSources are where maskModelFiles are downloaded from, in order:
// Hugging Face, then its mirror for where it can't be reached.
var maskModelSources = []string{
	"https://huggingface.co/QuantumNous/astrlink-guard/resolve/" + guard.Revision + "/",
	"https://hf-mirror.com/QuantumNous/astrlink-guard/resolve/" + guard.Revision + "/",
}

// DownloadMaskModel downloads 识别模型's files into maskModelDir, each
// checked against its SHA-256 (ErrMaskModelMismatch), from the first of
// its sources that serves it (Options.MaskModelSources): a source that
// fails is not asked for the rest. What an earlier download left is
// resumed; a file already here is checked, not downloaded again. progress
// (nil: none) gets the bytes so far of all of them. Turning 识别模型 on is
// the caller's, once this succeeds.
func (s *Service) DownloadMaskModel(ctx context.Context, progress func(got, total int64)) error {
	if progress == nil {
		progress = func(int64, int64) {}
	}
	s.maskModelMu.Lock() // one download writes the files at a time
	defer s.maskModelMu.Unlock()
	sources := s.opts.MaskModelSources
	if sources == nil {
		sources = maskModelSources
	}
	var total, done int64
	for _, f := range maskModelFiles {
		total += f.Size
	}
	src := 0
	for _, f := range maskModelFiles {
		dst := filepath.Join(s.maskModelDir(), f.Name)
		if present(dst, f.Size) {
			sum, err := fileSHA256(ctx, dst)
			if err != nil {
				return err
			}
			if sum == f.SHA256 {
				done += f.Size
				progress(done, total)
				continue
			}
		}
		for {
			from := f
			from.URL = sources[src] + f.URL
			err := downloadFile(ctx, s.opts.MaskModelClient, from, dst+".partial", dst, func(got int64) { progress(done+got, total) })
			if errors.Is(err, errAssetMismatch) {
				err = ErrMaskModelMismatch
			}
			if err == nil {
				break
			}
			if ctx.Err() != nil || src == len(sources)-1 {
				return err
			}
			src++
		}
		done += f.Size
	}
	return nil
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
