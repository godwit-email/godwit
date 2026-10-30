package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/internal/testclock"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// brokenModel is a fake 识别模型 that fails while broken is set.
type brokenModel struct {
	*fakeModel
	broken atomic.Bool
}

var errBrokenModel = errors.New("the model's session was closed")

func (b *brokenModel) Recognize(text string) ([]mask.Recognized, error) {
	if b.broken.Load() {
		return nil, errBrokenModel
	}
	return b.fakeModel.Recognize(text)
}

// A 识别模型 that fails leaves Wit to the rules: the question goes out,
// what a rule finds masked, and the settings say why; once it reads
// again, it masks again and the reason is gone.
func TestAFailingRecognitionModelLeavesTheRules(t *testing.T) {
	e := newApprovalEnv(t)
	md := &brokenModel{fakeModel: newFakeModel(map[string]string{"陈晓雯": "private_person"})}
	md.broken.Store(true)
	installRecognitionModel(t, e.s, md)
	setRecognitionModel(t, e.s, true)
	conv := e.newConv(t, "ask", 0)
	ask := func(q string) string {
		e.srv.Script(testllm.Turn{Text: []string{"ok"}})
		evs := e.start(t, conv, q)
		require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs[len(evs)-1].Err)
		reqs := e.srv.Requests()
		return string(reqs[len(reqs)-1].Body)
	}

	body := ask("陈晓雯的卡是 4111 1111 1111 1111 吗？")
	assert.Contains(t, body, "陈晓雯的卡是")
	assert.NotContains(t, body, "4111")
	assert.ErrorIs(t, e.s.RecognitionModelError(), errBrokenModel)

	md.broken.Store(false)
	assert.NotContains(t, ask("陈晓雯来吗？"), "陈晓雯")
	assert.NoError(t, e.s.RecognitionModelError())

	md.broken.Store(true)
	ask("还有谁？")
	require.Error(t, e.s.RecognitionModelError())
	setRecognitionModel(t, e.s, false)
	assert.NoError(t, e.s.RecognitionModelError(), "off, it has nothing to say")
}

// A summary, run in the background, goes out on the rules too.
func TestASummaryGoesOutWhenTheRecognitionModelFails(t *testing.T) {
	e := newSkillEnv(t)
	md := &brokenModel{fakeModel: newFakeModel(nil)}
	md.broken.Store(true)
	installRecognitionModel(t, e.s, md)
	setRecognitionModel(t, e.s, true)
	id := addMessage(t, e.s, "Visit", "Ann <ann@x>", "Sarah Thompson pays with 4111 1111 1111 1111.", "", 1_700_000_200)
	e.srv.Script(testllm.Turn{Text: []string{"- Fri"}})
	evs := drain(mustRunSkill(t, e, "summarize", Target{Kind: "message", ID: id}))
	require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs)
	body := string(e.srv.Requests()[0].Body)
	assert.Contains(t, body, "Sarah Thompson")
	assert.NotContains(t, body, "4111")
	assert.ErrorIs(t, e.s.RecognitionModelError(), errBrokenModel)
}

// A rule that fails still fails the request, 识别模型 on or not.
func TestAFailingRuleStillFailsTheRequest(t *testing.T) {
	e := newApprovalEnv(t)
	md := &brokenModel{fakeModel: newFakeModel(nil)}
	md.broken.Store(true)
	installRecognitionModel(t, e.s, md)
	st := setRecognitionModel(t, e.s, true)
	st.MaskRules = []mask.Rule{{Kind: "BAD", Regex: "(x"}} // stored by hand: SetSettings refuses it
	b, err := json.Marshal(st)
	require.NoError(t, err)
	_, err = e.s.db.Execute(t.Context(), "UPDATE settings SET value = ? WHERE key = 'app'", string(b))
	require.NoError(t, err)
	_, err = e.s.startRun(t.Context(), e.newConv(t, "ask", 0), "陈晓雯来吗？", nil)
	require.ErrorContains(t, err, "masking rule 1 (BAD)")
	assert.Empty(t, e.srv.Requests())
}

// onFiles reads with md while s's 识别模型 files are here and fails
// without them, as AstrLink Guard does.
type onFiles struct {
	s  *Service
	md mask.Recognizer
}

func (o onFiles) Recognize(text string) ([]mask.Recognized, error) {
	if !o.s.RecognitionModelInstalled() {
		return nil, fmt.Errorf("guard: %w", os.ErrNotExist)
	}
	return o.md.Recognize(text)
}

// With 识别模型's files gone while it is on, the next request goes out on
// the rules and downloads them again in the background, once however many
// texts it reads; then the model masks again.
func TestLostRecognitionModelFilesAreDownloadedAgain(t *testing.T) {
	e := newApprovalEnv(t)
	files := recognitionModelContents()
	src := serveRecognitionModel(t, files)
	useRecognitionModelSources(t, e.s, files, src.URL)
	require.NoError(t, e.s.DownloadRecognitionModel(t.Context(), nil))
	e.s.opts.Recognizer = onFiles{e.s, newFakeModel(map[string]string{"陈晓雯": "private_person"})}
	setRecognitionModel(t, e.s, true)
	conv := e.newConv(t, "ask", 0)
	ask := func(q string) string {
		e.srv.Script(testllm.Turn{Text: []string{"ok"}})
		evs := e.start(t, conv, q)
		require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs[len(evs)-1].Err)
		reqs := e.srv.Requests()
		return string(reqs[len(reqs)-1].Body)
	}

	require.NoError(t, os.RemoveAll(e.s.recognitionModelDir()))
	asked := len(src.asked())
	hold := make(chan struct{})
	src.setHandle(func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		select {
		case <-hold:
		case <-r.Context().Done():
		}
		return false
	})
	assert.Contains(t, ask("陈晓雯走了吗？"), "陈晓雯走了吗？")
	assert.Contains(t, ask("陈晓雯呢？"), "陈晓雯呢？")
	assert.ErrorIs(t, e.s.RecognitionModelError(), ErrRecognitionModelMissing)
	require.Eventually(t, func() bool { return len(src.asked()) > asked }, 5*time.Second, 10*time.Millisecond)
	assert.Len(t, src.asked(), asked+1, "one download, however many texts were read")
	close(hold)

	require.Eventually(t, e.s.RecognitionModelInstalled, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return e.s.RecognitionModelError() == nil }, 5*time.Second, 10*time.Millisecond)
	assert.NotContains(t, ask("陈晓雯回来了吗？"), "陈晓雯")
}

// asker asks Wit q in one conversation of e: the body the model service
// got.
func asker(t *testing.T, e *approvalEnv) func(q string) string {
	conv := e.newConv(t, "ask", 0)
	return func(q string) string {
		t.Helper()
		e.srv.Script(testllm.Turn{Text: []string{"ok"}})
		evs := e.start(t, conv, q)
		require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs[len(evs)-1].Err)
		reqs := e.srv.Requests()
		return string(reqs[len(reqs)-1].Body)
	}
}

// A loaded 识别模型 reads on from memory when its files go, but they are
// missed all the same: the rules alone mask, and they come down again.
func TestLostFilesOfALoadedRecognitionModelAreDownloadedAgain(t *testing.T) {
	e := newApprovalEnv(t)
	files := recognitionModelContents()
	src := serveRecognitionModel(t, files)
	useRecognitionModelSources(t, e.s, files, src.URL)
	require.NoError(t, e.s.DownloadRecognitionModel(t.Context(), nil))
	e.s.opts.Recognizer = newFakeModel(map[string]string{"陈晓雯": "private_person", "李雷": "private_person"})
	setRecognitionModel(t, e.s, true)
	ask := asker(t, e)
	assert.NotContains(t, ask("陈晓雯在吗？"), "陈晓雯")

	require.NoError(t, os.RemoveAll(e.s.recognitionModelDir()))
	asked := len(src.asked())
	hold := make(chan struct{})
	src.setHandle(func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		select {
		case <-hold:
		case <-r.Context().Done():
		}
		return false
	})
	assert.Contains(t, ask("李雷走了吗？"), "李雷走了吗？")
	assert.ErrorIs(t, e.s.RecognitionModelError(), ErrRecognitionModelMissing)
	require.Eventually(t, func() bool { return len(src.asked()) > asked }, 5*time.Second, 10*time.Millisecond)
	close(hold)

	require.Eventually(t, e.s.RecognitionModelInstalled, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return e.s.RecognitionModelError() == nil }, 5*time.Second, 10*time.Millisecond)
	assert.NotContains(t, ask("李雷回来了吗？"), "李雷")
}

// A re-download of lost files that fails says why in the settings, and
// is not tried again by every request after it: offline, that would be a
// download a request. It is tried again after redownloadBackoff, or once
// the settings change.
func TestAFailedRedownloadIsNotRetriedByEveryRequest(t *testing.T) {
	e := newApprovalEnv(t)
	clock := testclock.New(time.Now())
	e.s.SetClockForTest(clock)
	files := recognitionModelContents()
	src := serveRecognitionModel(t, files)
	useRecognitionModelSources(t, e.s, files, src.URL)
	require.NoError(t, e.s.DownloadRecognitionModel(t.Context(), nil))
	e.s.opts.Recognizer = onFiles{e.s, newFakeModel(map[string]string{"陈晓雯": "private_person"})}
	setRecognitionModel(t, e.s, true)
	ask := asker(t, e)

	require.NoError(t, os.RemoveAll(e.s.recognitionModelDir()))
	src.setHandle(func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		http.Error(w, "offline", http.StatusServiceUnavailable)
		return true
	})
	failed := func() bool {
		err := e.s.RecognitionModelError()
		return err != nil && !errors.Is(err, ErrRecognitionModelMissing)
	}
	asked := len(src.asked())
	ask("陈晓雯走了吗？")
	require.Eventually(t, failed, 5*time.Second, 10*time.Millisecond)
	tried := len(src.asked())
	require.Greater(t, tried, asked)

	ask("陈晓雯呢？")
	assert.ErrorContains(t, e.s.RecognitionModelError(), "503", "the download's reason stands")
	assert.Never(t, func() bool { return len(src.asked()) > tried }, 200*time.Millisecond, 10*time.Millisecond)

	clock.Advance(redownloadBackoff)
	ask("陈晓雯呢？")
	require.Eventually(t, func() bool { return len(src.asked()) > tried }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, failed, 5*time.Second, 10*time.Millisecond)
	tried = len(src.asked())

	ask("陈晓雯呢？")
	assert.Never(t, func() bool { return len(src.asked()) > tried }, 200*time.Millisecond, 10*time.Millisecond)
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	st.MaskWordsCase = !st.MaskWordsCase
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	src.setHandle(nil)
	ask("陈晓雯呢？")
	require.Eventually(t, e.s.RecognitionModelInstalled, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return e.s.RecognitionModelError() == nil }, 5*time.Second, 10*time.Millisecond)
}
