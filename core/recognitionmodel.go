package core

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/ai/mask/guard"
)

// ErrRecognitionModelMissing refuses to turn 识别模型 on: its files are not in
// the cache (recognitionModelDir).
var ErrRecognitionModelMissing = errors.New("the recognition model's files are not on this device")

// recognitionModelDir is where 识别模型's files are, by revision: under cache/,
// for they are downloaded again when lost and stay behind when the data
// directory moves (docs/adr/0028-recognition-model-for-masking.md).
func (s *Service) recognitionModelDir() string {
	return filepath.Join(s.dataDir, "cache", "guard", guard.Revision)
}

// RecognitionModelInstalled says 识别模型's files are all on this device: only
// then can it be turned on.
func (s *Service) RecognitionModelInstalled() bool {
	for _, f := range guard.Files {
		if _, err := os.Stat(filepath.Join(s.recognitionModelDir(), f)); err != nil {
			return false
		}
	}
	return true
}

// ErrRecognitionModelMismatch: a downloaded file of 识别模型 was not the one its
// revision has, and was discarded.
var ErrRecognitionModelMismatch = errors.New("a downloaded file of the recognition model does not match its checksum; it was discarded")

// recognitionModelFiles are guard.Files as Hugging Face has them at
// guard.Revision, by path under a source (recognitionModelSources), each
// named as its path ends. Tests replace them.
var recognitionModelFiles = []remoteFile{
	{URL: "int8/model_int8.onnx", Size: 330781, SHA256: "0a070d3113615e33ba3b84d6bd486f663a377c04c4b056e7381732179f51832a"},
	{URL: "int8/model_int8.onnx_data", Size: 37124608, SHA256: "0ba34216ceead01e0ae2dfd8e46d52b67e7aee80b04f722d9b758613a91c0067"},
	{URL: "int8/tokenizer.json", Size: 1502881, SHA256: "0cf3b235ef125658015a6c8026a5fe6f28088bf825101a990ec18a10200feef4"},
}

// recognitionModelSources are where recognitionModelFiles are downloaded from, in order:
// Hugging Face, then its mirror for where it can't be reached.
var recognitionModelSources = []string{
	"https://huggingface.co/QuantumNous/astrlink-guard/resolve/" + guard.Revision + "/",
	"https://hf-mirror.com/QuantumNous/astrlink-guard/resolve/" + guard.Revision + "/",
}

// DownloadRecognitionModel downloads 识别模型's files into recognitionModelDir, each
// checked against its SHA-256 (ErrRecognitionModelMismatch), from the first of
// its sources that serves it (Options.RecognitionModelSources): a source that
// fails is not asked for the rest. What an earlier download left is
// resumed; a file already here is checked, not downloaded again. progress
// (nil: none) gets the bytes so far of all of them. Turning 识别模型 on is
// the caller's, once this succeeds.
func (s *Service) DownloadRecognitionModel(ctx context.Context, progress func(got, total int64)) error {
	if progress == nil {
		progress = func(int64, int64) {}
	}
	s.recognitionModelMu.Lock() // one download writes the files at a time
	defer s.recognitionModelMu.Unlock()
	sources := s.opts.RecognitionModelSources
	if sources == nil {
		sources = recognitionModelSources
	}
	var total, done int64
	for _, f := range recognitionModelFiles {
		total += f.Size
	}
	src := 0
	for _, f := range recognitionModelFiles {
		dst := filepath.Join(s.recognitionModelDir(), path.Base(f.URL))
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
			err := downloadFile(ctx, s.opts.RecognitionModelClient, from, dst+".partial", dst, func(got int64) { progress(done+got, total) })
			if errors.Is(err, errDownloadMismatch) {
				err = ErrRecognitionModelMismatch
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

// recognizes says 识别模型 reads what goes out: it and masking are on.
func (st Settings) recognizes() bool { return st.Masking && st.RecognitionModel }

// recognizer is 识别模型 as st sets it, nil unless st.recognizes():
// Options.Recognizer, else AstrLink Guard, kept until it is turned off
// (maskingChanged), either read through orRules. Called with maskMu held.
func (s *Service) recognizer(st Settings) mask.Recognizer {
	switch {
	case !st.recognizes():
		return nil
	case s.opts.Recognizer != nil:
		return orRules{s, s.opts.Recognizer}
	}
	if s.guard == nil {
		s.guard = newGuardModel(s.recognitionModelDir())
	}
	return orRules{s, s.guard}
}

// orRules is 识别模型 as masking reads it: where it fails, it finds
// nothing, so the rules alone mask that text and it goes out, the failure
// kept for the settings (RecognitionModelError). A rule that fails still fails
// it: this is ADR 0025's one exception (docs/adr/0028-recognition-model-for-masking.md).
type orRules struct {
	s *Service
	mask.Recognizer
}

func (r orRules) Recognize(text string) ([]mask.Recognized, error) {
	if !r.s.RecognitionModelInstalled() { // a loaded model reads on from memory, but its files are gone all the same
		r.s.redownloadRecognitionModel()
		return nil, nil
	}
	found, err := r.Recognizer.Recognize(text)
	r.s.recognitionModelRead(err)
	if err != nil {
		return nil, nil
	}
	return found, nil
}

// recognitionState is how 识别模型 last read, under mu: why it failed
// (RecognitionModelError), whether its lost files are coming down again
// (redownloadRecognitionModel), and until when, that having failed, they
// are not tried again.
type recognitionState struct {
	mu       sync.Mutex
	err      error
	fetching bool
	retryAt  time.Time
}

// recognitionModelRead keeps how 识别模型 last read, err nil for well.
// Failed for want of its files, it downloads them again
// (redownloadRecognitionModel).
func (s *Service) recognitionModelRead(err error) {
	if err != nil && !s.RecognitionModelInstalled() {
		s.redownloadRecognitionModel()
		return
	}
	s.recognition.mu.Lock()
	s.recognition.err = err
	s.recognition.mu.Unlock()
}

// RecognitionModelError is why 识别模型 last failed to read, nil once it reads
// again or is turned off: meanwhile the rules alone mask. Files it lost
// are ErrRecognitionModelMissing until they are downloaded again, or why that
// download failed.
func (s *Service) RecognitionModelError() error {
	s.recognition.mu.Lock()
	defer s.recognition.mu.Unlock()
	return s.recognition.err
}

// redownloadBackoff is how long after a failed re-download of 识别模型's
// lost files no request tries again, unless the settings change
// (maskingChanged): offline, each request would download again.
const redownloadBackoff = 5 * time.Minute

// redownloadRecognitionModel downloads 识别模型's lost files in the
// background, ErrRecognitionModelMissing meanwhile, unless that is under
// way or failed less than redownloadBackoff ago: that failure stays the
// reason. Once they are here, the model is loaded again from them.
func (s *Service) redownloadRecognitionModel() {
	r := &s.recognition
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fetching || s.clk().Now().Before(r.retryAt) {
		return
	}
	ctx, end, err := s.begin(context.Background(), 0) // Close stops it
	if err != nil {
		return
	}
	r.err, r.fetching = ErrRecognitionModelMissing, true
	go func() {
		defer end()
		err := s.DownloadRecognitionModel(ctx, nil)
		r.mu.Lock()
		r.err, r.fetching = err, false
		if err != nil {
			r.retryAt = s.clk().Now().Add(redownloadBackoff)
		}
		r.mu.Unlock()
		if err != nil {
			return
		}
		s.maskMu.Lock()
		s.guard = nil // loaded without the files, it failed for good, or reads from what was deleted
		s.maskMu.Unlock()
		if st, err := s.loadSettings(ctx); err == nil {
			s.maskingChanged(st)
		}
	}()
}

// RecognitionModelBackend is what reads 识别模型: CoreML on Device, or with
// Device "" the CPU, NotCoreML saying why not CoreML on a Mac.
type RecognitionModelBackend struct {
	Device    string
	NotCoreML string
}

// RecognitionModelBackend is what reads 识别模型 now, loading it if need be: nil
// while it is off, or for an Options.Recognizer that doesn't say.
func (s *Service) RecognitionModelBackend(ctx context.Context) (*RecognitionModelBackend, error) {
	st, err := s.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	s.maskMu.Lock()
	r, _ := s.recognizer(st).(orRules)
	s.maskMu.Unlock()
	b, ok := r.Recognizer.(interface {
		Backend() (RecognitionModelBackend, error)
	})
	if !ok {
		return nil, nil
	}
	bk, err := b.Backend()
	if err != nil {
		s.recognitionModelRead(err) // the settings say why, as for a request
		return nil, err
	}
	return &bk, nil
}

func (g *guardModel) Backend() (RecognitionModelBackend, error) {
	m, err := g.load()
	if err != nil {
		return RecognitionModelBackend{}, err
	}
	dev, why := m.CoreML()
	b := RecognitionModelBackend{Device: dev}
	if why != nil && runtime.GOOS == "darwin" { // elsewhere there is no CoreML to miss
		b.NotCoreML = why.Error()
	}
	return b, nil
}

// guardModel is AstrLink Guard (core/ai/mask/guard) from its files in a
// directory, loaded on first use. What it found in a text is kept in
// memory by the text's hash, the model's revision and what reads it
// (CoreML or Go).
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
	key := sha256.Sum256([]byte(guard.Revision + "\x00" + m.Device() + "\x00" + text))
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
		clear(g.seen) // shortcut: forgets everything at once past guardMemoBytes, which one history fits; evict the oldest when one no longer does
		g.size = len(text)
	}
	g.seen[key] = found
	return found, nil
}
