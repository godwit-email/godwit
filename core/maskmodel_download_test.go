package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai/mask/guard"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// maskModelSource is a stand-in for Hugging Face: it serves files at
// /int8/<name>, with ranges, unless handle answers the request itself;
// ranges are the Range headers it was asked with.
type maskModelSource struct {
	*httptest.Server
	mu     sync.Mutex
	ranges []string
	handle func(w http.ResponseWriter, r *http.Request, body []byte) bool
}

func serveMaskModel(t *testing.T, files map[string][]byte) *maskModelSource {
	t.Helper()
	src := &maskModelSource{}
	src.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/int8/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		src.mu.Lock()
		src.ranges = append(src.ranges, r.Header.Get("Range"))
		handle := src.handle
		src.mu.Unlock()
		if handle != nil && handle(w, r, body) {
			return
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
	}))
	t.Cleanup(src.Close)
	return src
}

func (src *maskModelSource) setHandle(h func(w http.ResponseWriter, r *http.Request, body []byte) bool) {
	src.mu.Lock()
	defer src.mu.Unlock()
	src.handle = h
}

func (src *maskModelSource) asked() []string {
	src.mu.Lock()
	defer src.mu.Unlock()
	return append([]string(nil), src.ranges...)
}

// maskModelContents are made-up files for guard.Files, big enough to be
// downloaded in more than one read.
func maskModelContents() map[string][]byte {
	out := map[string][]byte{}
	for i, name := range guard.Files {
		out[name] = bytes.Repeat([]byte{byte('a' + i)}, 600<<10+i)
	}
	return out
}

// useMaskModelSources has s download want's files from sources, in order.
func useMaskModelSources(t *testing.T, s *Service, want map[string][]byte, sources ...string) {
	t.Helper()
	old := maskModelFiles
	t.Cleanup(func() { maskModelFiles = old })
	maskModelFiles = nil
	for _, name := range guard.Files {
		sum := sha256.Sum256(want[name])
		maskModelFiles = append(maskModelFiles, plugins.AssetFile{Name: name, URL: "int8/" + name, Size: int64(len(want[name])), SHA256: hex.EncodeToString(sum[:])})
	}
	s.opts.MaskModelSources = nil
	for _, u := range sources {
		s.opts.MaskModelSources = append(s.opts.MaskModelSources, u+"/")
	}
	s.opts.MaskModelClient = http.DefaultClient
}

// turnMaskModelOn tries to turn 识别模型 on.
func turnMaskModelOn(t *testing.T, s *Service) error {
	t.Helper()
	st, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	st.MaskModel = true
	_, err = s.SetSettings(t.Context(), st)
	return err
}

// The files come down, each checked, and progress counts up to all of
// them; then 识别模型 can be turned on.
func TestDownloadingTheMaskModel(t *testing.T) {
	e := newApprovalEnv(t)
	files := maskModelContents()
	src := serveMaskModel(t, files)
	useMaskModelSources(t, e.s, files, src.URL)

	var total, last int64
	require.NoError(t, e.s.DownloadMaskModel(t.Context(), func(got, all int64) { last, total = got, all }))
	var want int64
	for _, b := range files {
		want += int64(len(b))
	}
	assert.Equal(t, want, total)
	assert.Equal(t, want, last)
	for name, b := range files {
		got, err := os.ReadFile(filepath.Join(e.s.maskModelDir(), name))
		require.NoError(t, err)
		assert.True(t, bytes.Equal(b, got), name)
	}
	require.NoError(t, turnMaskModelOn(t, e.s))

	// Already here: nothing is asked again.
	n := len(src.asked())
	require.NoError(t, e.s.DownloadMaskModel(t.Context(), nil))
	assert.Len(t, src.asked(), n)
}

// A file that is not the one expected is discarded, from the mirror too,
// and 识别模型 stays off.
func TestADownloadedMaskModelFileThatDoesNotMatchIsDiscarded(t *testing.T) {
	e := newApprovalEnv(t)
	files := maskModelContents()
	tampered := maskModelContents()
	tampered[guard.Files[1]][7] = 'x'
	hf, mirror := serveMaskModel(t, tampered), serveMaskModel(t, tampered)
	useMaskModelSources(t, e.s, files, hf.URL, mirror.URL)

	err := e.s.DownloadMaskModel(t.Context(), nil)
	require.ErrorIs(t, err, ErrMaskModelMismatch)
	assert.False(t, e.s.MaskModelInstalled())
	_, err = os.Stat(filepath.Join(e.s.maskModelDir(), guard.Files[1]))
	assert.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorIs(t, turnMaskModelOn(t, e.s), ErrMaskModelMissing)
}

// When Hugging Face can't be reached, the mirror serves the files.
func TestTheMaskModelComesFromTheMirrorWhenHuggingFaceFails(t *testing.T) {
	e := newApprovalEnv(t)
	files := maskModelContents()
	hf, mirror := serveMaskModel(t, files), serveMaskModel(t, files)
	hf.setHandle(func(w http.ResponseWriter, _ *http.Request, _ []byte) bool {
		http.Error(w, "blocked", http.StatusBadGateway)
		return true
	})
	useMaskModelSources(t, e.s, files, hf.URL, mirror.URL)

	require.NoError(t, e.s.DownloadMaskModel(t.Context(), nil))
	assert.True(t, e.s.MaskModelInstalled())
	assert.Len(t, hf.asked(), 1, "the mirror serves the rest")
	assert.Len(t, mirror.asked(), len(guard.Files))
}

// A download cut off, or canceled, goes on from where it stopped; until it
// is done, 识别模型 stays off.
func TestAnInterruptedMaskModelDownloadResumes(t *testing.T) {
	e := newApprovalEnv(t)
	files := maskModelContents()
	src := serveMaskModel(t, files)
	useMaskModelSources(t, e.s, files, src.URL)
	first := guard.Files[0]
	half := len(files[first]) / 2
	src.setHandle(func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		w.Header().Set("Content-Length", "1000000000") // more than comes: the connection drops mid-file
		w.WriteHeader(http.StatusOK)
		w.Write(body[:half])
		return true
	})

	err := e.s.DownloadMaskModel(t.Context(), nil)
	require.Error(t, err)
	require.ErrorIs(t, turnMaskModelOn(t, e.s), ErrMaskModelMissing)

	src.setHandle(nil)
	require.NoError(t, e.s.DownloadMaskModel(t.Context(), nil))
	assert.Equal(t, "bytes="+strconv.Itoa(half)+"-", src.asked()[1])
	got, err := os.ReadFile(filepath.Join(e.s.maskModelDir(), first))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(files[first], got))
	require.NoError(t, turnMaskModelOn(t, e.s))
}

// Canceled, the download stops, 识别模型 stays off, and the next one picks up
// what came.
func TestACanceledMaskModelDownloadStaysOffAndResumes(t *testing.T) {
	e := newApprovalEnv(t)
	files := maskModelContents()
	src := serveMaskModel(t, files)
	useMaskModelSources(t, e.s, files, src.URL)
	first := guard.Files[0]
	hold := make(chan struct{})
	defer close(hold)
	src.setHandle(func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		w.Write(body[:1000])
		w.(http.Flusher).Flush()
		select {
		case <-hold:
		case <-r.Context().Done():
		}
		return true
	})

	ctx, cancel := context.WithCancel(t.Context())
	err := e.s.DownloadMaskModel(ctx, func(got, _ int64) {
		if got >= 1000 {
			cancel()
		}
	})
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, turnMaskModelOn(t, e.s), ErrMaskModelMissing)

	src.setHandle(nil)
	require.NoError(t, e.s.DownloadMaskModel(t.Context(), nil))
	assert.Equal(t, "bytes=1000-", src.asked()[1])
	got, err := os.ReadFile(filepath.Join(e.s.maskModelDir(), first))
	require.NoError(t, err)
	assert.True(t, bytes.Equal(files[first], got))
}

// The files to download are AstrLink Guard's, at its revision.
func TestTheMaskModelFilesAreGuards(t *testing.T) {
	var names []string
	for _, f := range maskModelFiles {
		names = append(names, f.Name)
		assert.Equal(t, "int8/"+f.Name, f.URL)
	}
	assert.Equal(t, guard.Files, names)
	assert.Contains(t, maskModelSources[0], guard.Revision)
}
