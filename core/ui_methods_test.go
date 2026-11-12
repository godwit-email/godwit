package core_test

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/ai/mask/guard"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeUISysTranslator struct{ langs []string }

func (f *fakeUISysTranslator) Translate(_ context.Context, texts []string, _, _ string) ([]string, string, error) {
	return texts, "en", nil
}
func (f *fakeUISysTranslator) Version() string              { return "1" }
func (f *fakeUISysTranslator) InstalledLanguages() []string { return f.langs }

func makeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))))
	return b.Bytes()
}

func TestUIImageCacheHelpers(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	pic := makeTestPNG(t, 6, 4)
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(pic)
	}))
	defer srv.Close()

	src := srv.URL + "/pic.png"
	assert.Equal(t, core.ImageDims{}, e.Svc.CachedImageDims(src))
	_, err := e.Svc.CachedImage(t.Context(), src)
	require.Error(t, err)

	// Remembered failure holds until ForgetImageFailure clears it.
	fail.Store(false)
	_, err = e.Svc.CachedImage(t.Context(), src)
	require.Error(t, err)

	e.Svc.ForgetImageFailure([]string{src})
	got, err := e.Svc.CachedImage(t.Context(), src)
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(got.Path))
	assert.Equal(t, "png", got.Format)
	assert.Equal(t, 6, got.Width)
	assert.Equal(t, 4, got.Height)
	data, err := os.ReadFile(got.Path)
	require.NoError(t, err)
	assert.Equal(t, pic, data)
	assert.Equal(t, core.ImageDims{Width: 6, Height: 4, OK: true}, e.Svc.CachedImageDims(src))
}

func TestUISystemTranslatorLanguages(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		e := coretest.New(t, coretest.WithoutAutoSync())
		assert.Nil(t, e.Svc.SystemTranslatorLanguages())
	})
	t.Run("empty", func(t *testing.T) {
		e := coretest.New(t, coretest.WithoutAutoSync(), coretest.WithSystemTranslator(&fakeUISysTranslator{}))
		empty := e.Svc.SystemTranslatorLanguages()
		assert.NotNil(t, empty)
		assert.Empty(t, empty)
	})
	t.Run("installed", func(t *testing.T) {
		e := coretest.New(t, coretest.WithoutAutoSync(), coretest.WithSystemTranslator(&fakeUISysTranslator{langs: []string{"de", "en"}}))
		assert.Equal(t, []string{"de", "en"}, e.Svc.SystemTranslatorLanguages())
	})
}

func TestUIInlineImagesAndFetchAttachmentsStreams(t *testing.T) {
	e := coretest.New(t, coretest.WithoutAutoSync())
	tctx := t.Context()
	acc := e.AddAccount(t, "alice@example.com")
	pic := makeTestPNG(t, 2, 2)
	e.Deliver(t, acc.ID, "INBOX", coretest.Msg("bob@example.com", "alice@example.com", "Assets", "body",
		coretest.HTML(`<p><img src="cid:logo@test"></p>`),
		coretest.Inline("logo@test", "image/png", pic),
		coretest.Attachment("a.txt", "text/plain", []byte("alpha")),
		coretest.Attachment("b.txt", "text/plain", []byte("beta"))))
	page, err := e.Svc.ListMessages(tctx, core.View{Unified: "inbox"}, "", 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	msgID := page.Items[0].ID

	_, err = e.Svc.InlineImagesStream(tctx, 999999)
	require.Error(t, err)

	imgCh, err := e.Svc.InlineImagesStream(tctx, msgID)
	require.NoError(t, err)
	var imgs []core.InlineImage
	for it := range imgCh {
		require.NoError(t, it.StreamError())
		imgs = append(imgs, it)
	}
	require.Len(t, imgs, 1)
	assert.Equal(t, "logo@test", imgs[0].CID)
	assert.Equal(t, "image/png", imgs[0].ContentType)
	raw, err := os.ReadFile(imgs[0].Path)
	require.NoError(t, err)
	assert.Equal(t, pic, raw)

	txt, err := e.Svc.GetMessageText(tctx, msgID)
	require.NoError(t, err)
	require.Len(t, txt.Attachments, 3)

	attCh, err := e.Svc.FetchAttachmentsStream(tctx, []int64{txt.Attachments[1].ID, txt.Attachments[2].ID})
	require.NoError(t, err)
	var got []string
	for a := range attCh {
		require.NoError(t, a.StreamError())
		b, err := os.ReadFile(a.Path)
		require.NoError(t, err)
		got = append(got, a.Info.Filename+":"+string(b))
	}
	assert.Equal(t, []string{"a.txt:alpha", "b.txt:beta"}, got)

	badCh, err := e.Svc.FetchAttachmentsStream(tctx, []int64{txt.Attachments[1].ID, 999999})
	require.NoError(t, err)
	var count int
	var streamErr error
	for a := range badCh {
		if a.StreamError() != nil {
			streamErr = a.StreamError()
			continue
		}
		count++
	}
	assert.Equal(t, 1, count)
	require.Error(t, streamErr)
}

func TestUIInstallPluginFromRegistryStream(t *testing.T) {
	noReg := regService(t, nil, t.TempDir())
	_, err := noReg.InstallPluginFromRegistryStream(t.Context(), "hello")
	require.ErrorIs(t, err, core.ErrRegistryOff)

	rs := coretest.NewRegistryServer(t)
	code := strings.Repeat("// plugin\n", 100)
	rel := rs.ScriptRelease("hello", "0.1.0", `{}`, code)
	badRel := rs.ScriptRelease("broken", "0.1.0", `{}`, code)
	badRel["artifacts"].(map[string]any)["any"].(map[string]any)["sha256"] = strings.Repeat("0", 64)
	rs.Publish(t, coretest.Registry(t0, plugin("hello", rel), plugin("broken", badRel)))
	s := regService(t, rs, t.TempDir())
	require.NoError(t, s.RefreshPluginRegistry(t.Context()))

	ch, err := s.InstallPluginFromRegistryStream(t.Context(), "hello")
	require.NoError(t, err)
	var stages []string
	var installed *core.PluginInfo
	for ev := range ch {
		require.NoError(t, ev.StreamError())
		if ev.Plugin != nil {
			installed = ev.Plugin
		} else {
			stages = append(stages, ev.Progress.Stage)
		}
	}
	require.NotNil(t, installed)
	assert.Equal(t, "hello", installed.Name)
	assert.Contains(t, stages, "download")
	assert.Contains(t, stages, "verify")
	assert.Contains(t, stages, "install")

	bch, err := s.InstallPluginFromRegistryStream(t.Context(), "broken")
	require.NoError(t, err)
	var mismatch error
	for ev := range bch {
		if ev.StreamError() != nil {
			mismatch = ev.StreamError()
		}
	}
	require.ErrorIs(t, mismatch, core.ErrRegistryMismatch)
}

func TestUIPrepareModelStream(t *testing.T) {
	e := hostedJudgeEnv(t)
	_, err := e.s.PrepareModelStream(t.Context(), "plugin/dlx/nope")
	require.ErrorIs(t, err, core.ErrModelUnavailable)

	ch, err := e.s.PrepareModelStream(t.Context(), "plugin/dlx/decision-4b-q4")
	require.NoError(t, err)
	var states []core.ModelState
	var final *core.ModelStatus
	for ev := range ch {
		require.NoError(t, ev.StreamError())
		if ev.Done {
			st := ev.Status
			final = &st
		} else if len(states) == 0 || states[len(states)-1] != ev.Status.State {
			states = append(states, ev.Status.State)
		}
	}
	require.NotNil(t, final)
	assert.Equal(t, core.ModelDownloaded, final.State)
	assert.Equal(t, []core.ModelState{core.ModelDownloading, core.ModelVerifying}, states)
}

func TestUIDownloadRecognitionModelStream(t *testing.T) {
	want := map[string][]byte{}
	for i, name := range guard.Files {
		want[name] = bytes.Repeat([]byte{byte('x' + i)}, 512+i)
	}
	core.SetRecognitionModelFilesForTest(t, want)

	var corrupt atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := want[strings.TrimPrefix(r.URL.Path, "/int8/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if corrupt.Load() {
			body = bytes.Repeat([]byte{'!'}, len(body))
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
	}))
	defer srv.Close()

	e := coretest.New(t, coretest.WithoutAutoSync(), coretest.WithRecognitionModelSources(srv.URL+"/"))
	corrupt.Store(true)
	bch, err := e.Svc.DownloadRecognitionModelStream(t.Context())
	require.NoError(t, err)
	var merr error
	for ev := range bch {
		if ev.StreamError() != nil {
			merr = ev.StreamError()
		}
	}
	require.ErrorIs(t, merr, core.ErrRecognitionModelMismatch)
	assert.False(t, e.Svc.RecognitionModelInstalled())

	corrupt.Store(false)
	ch, err := e.Svc.DownloadRecognitionModelStream(t.Context())
	require.NoError(t, err)
	var last core.RecognitionModelProgress
	for ev := range ch {
		require.NoError(t, ev.StreamError())
		last = ev
	}
	assert.Positive(t, last.Total)
	assert.Equal(t, last.Total, last.Got)
	assert.True(t, e.Svc.RecognitionModelInstalled())
}
