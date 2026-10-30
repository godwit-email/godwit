package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/ai/mask"
	"github.com/godwit-email/godwit/core/ai/mask/guard"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeModel is a fake 识别模型: each value, wherever a text has it, under
// its label, scored 0.95; read counts the texts it was given.
type fakeModel struct {
	values map[string]string
	read   atomic.Int32
}

func newFakeModel(values map[string]string) *fakeModel { return &fakeModel{values: values} }

func (f *fakeModel) Recognize(text string) ([]mask.Recognized, error) {
	f.read.Add(1)
	var out []mask.Recognized
	for v, label := range f.values {
		for at := 0; ; {
			i := strings.Index(text[at:], v)
			if i < 0 {
				break
			}
			out = append(out, mask.Recognized{Start: at + i, End: at + i + len(v), Label: label, Score: 0.95})
			at += i + len(v)
		}
	}
	return out, nil
}

// installMaskModel puts the model's files where the Service looks for
// them, as a download would, and has it read with md.
func installMaskModel(t *testing.T, s *Service, md mask.Recognizer) {
	t.Helper()
	dir := s.maskModelDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	for _, f := range guard.Files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), nil, 0o600))
	}
	s.opts.Recognizer = md
}

// setMaskModel turns 识别模型 on or off.
func setMaskModel(t *testing.T, s *Service, on bool) Settings {
	t.Helper()
	st, err := s.GetSettings(t.Context())
	require.NoError(t, err)
	st.MaskModel = on
	st, err = s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	return st
}

// With 识别模型 on, Wit sends the names it finds as placeholders; the
// reply and the tool's arguments have the names.
func TestWitMasksTheNamesTheModelFinds(t *testing.T) {
	e := newApprovalEnv(t)
	installMaskModel(t, e.s, newFakeModel(map[string]string{"陈晓雯": "private_person"}))
	setMaskModel(t, e.s, true)
	m, err := e.s.masker()
	require.NoError(t, err)
	p, err := m.Text("陈晓雯")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(p, "{{NAME_"), p)

	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"写给" + p}, Calls: []testllm.Call{{Name: "fake_draft", Args: `{"subject":"给` + p + `"}`}}},
		testllm.Turn{Text: []string{"Finished."}})
	evs := e.start(t, conv, "给陈晓雯回信，说周五见")
	body := string(e.srv.Requests()[0].Body)
	assert.NotContains(t, body, "陈晓雯")
	assert.Contains(t, body, "给"+p+"回信")

	var text strings.Builder
	for _, ev := range evs {
		if ev.Kind == "text" {
			text.WriteString(ev.Text)
		}
	}
	assert.Equal(t, "写给陈晓雯", text.String())
	last := evs[len(evs)-1]
	require.Equal(t, "interrupted", last.Kind, "%+v", last.Err)
	assert.JSONEq(t, `{"account_id":1,"draft_id":0,"Subject":"给陈晓雯"}`, string(last.Approvals[0].Args))
}

// 识别模型 is off by default, and turns on only once its files are here.
// On, it turns names on; off, it leaves them as they are.
func TestTheMaskModelSwitch(t *testing.T) {
	e := newApprovalEnv(t)
	st, err := e.s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.False(t, st.MaskModel)
	assert.False(t, st.MaskCategories.Names.On)
	assert.False(t, e.s.MaskModelInstalled())
	st.MaskModel = true
	_, err = e.s.SetSettings(t.Context(), st)
	require.ErrorIs(t, err, ErrMaskModelMissing)

	installMaskModel(t, e.s, newFakeModel(nil))
	assert.True(t, e.s.MaskModelInstalled())
	st = setMaskModel(t, e.s, true)
	assert.True(t, st.MaskCategories.Names.On)
	st, err = e.s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.True(t, st.MaskModel)
	assert.True(t, st.MaskCategories.Names.On)

	st.MaskCategories.Names.On = false
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	st, err = e.s.GetSettings(t.Context())
	require.NoError(t, err)
	assert.False(t, st.MaskCategories.Names.On, "names off while it is on stay off")
	st.MaskCategories.Names.On = true
	_, err = e.s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	st = setMaskModel(t, e.s, false)
	assert.True(t, st.MaskCategories.Names.On, "off, it leaves names on")

	e.restart(t)
	assert.True(t, e.s.MaskModelInstalled(), "its files are no orphans of the cache")
}

// Off, the model reads nothing and Wit sends what it did without it; on,
// it reads from the next request, and off again stops it.
func TestTheMaskModelOffChangesNothing(t *testing.T) {
	e := newApprovalEnv(t)
	md := newFakeModel(map[string]string{"陈晓雯": "private_person"})
	installMaskModel(t, e.s, md)
	conv := e.newConv(t, "ask", 0)
	ask := func(q string) string {
		e.srv.Script(testllm.Turn{Text: []string{"ok"}})
		e.start(t, conv, q)
		reqs := e.srv.Requests()
		return string(reqs[len(reqs)-1].Body)
	}
	assert.Contains(t, ask("陈晓雯来吗？"), "陈晓雯来吗？")
	assert.Zero(t, md.read.Load())
	setMaskModel(t, e.s, true)
	assert.NotContains(t, ask("陈晓雯来吗?"), "陈晓雯")
	assert.NotZero(t, md.read.Load())
	setMaskModel(t, e.s, false)
	read := md.read.Load()
	assert.Contains(t, ask("陈晓雯走了吗？"), "陈晓雯走了吗？")
	assert.Equal(t, read, md.read.Load())
}

// Where the model and a rule both find an address, the rule's bounds hold:
// its user name goes as a placeholder, its domain as it is.
func TestTheMaskModelLeavesAnAddressItsDomain(t *testing.T) {
	e := newApprovalEnv(t)
	installMaskModel(t, e.s, newFakeModel(map[string]string{"ann.lee@example.com": "email", "<ann.lee@example.com": "email"}))
	setMaskModel(t, e.s, true)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"ok"}})
	e.start(t, conv, "Write to Ann <ann.lee@example.com>")
	body := string(e.srv.Requests()[0].Body)
	assert.NotContains(t, body, "ann.lee")
	assert.Regexp(t, `Write to Ann <\{\{EMAIL_[a-z2-7]{12}\}\}@example.com>`, body)
}

// A summary, run in the background too, goes through the model as Wit does.
func TestASummaryGoesThroughTheMaskModel(t *testing.T) {
	e := newSkillEnv(t)
	installMaskModel(t, e.s, newFakeModel(map[string]string{"Sarah Thompson": "private_person"}))
	setMaskModel(t, e.s, true)
	id := addMessage(t, e.s, "Visit", "Ann <ann@x>", "Sarah Thompson arrives on Friday.", "", 1_700_000_200)
	e.srv.Script(testllm.Turn{Text: []string{"- Fri"}})
	evs := drain(mustRunSkill(t, e, "summarize", Target{Kind: "message", ID: id}))
	require.Equal(t, "done", evs[len(evs)-1].Kind, "%+v", evs)
	body := string(e.srv.Requests()[0].Body)
	assert.NotContains(t, body, "Sarah Thompson")
	assert.Contains(t, body, "{{NAME_")
}

func mustRunSkill(t *testing.T, e skillEnv, name string, tg Target) <-chan ai.Event {
	t.Helper()
	ch, err := e.s.RunSkill(t.Context(), name, tg)
	require.NoError(t, err)
	return ch
}

// A plugin's judge model, which labels mail in the background, is asked
// with what the model found as placeholders.
func TestAPluginJudgeGoesThroughTheMaskModel(t *testing.T) {
	s := newMailContextService(t)
	installMaskModel(t, s, newFakeModel(map[string]string{"陈晓雯": "private_person"}))
	setMaskModel(t, s, true)
	record := usePluginJudge(t, s, nil, nil)
	j, _, _, err := s.resolveJudge(t.Context())
	require.NoError(t, err)
	_, err = j.Ask(t.Context(), "陈晓雯的账单", []ai.Question{{Name: "bill", Type: ai.Predicate, Instructions: "Is it a bill?"}})
	require.NoError(t, err)
	calls := recorded(t, record, "models.judge")
	require.Len(t, calls, 1)
	assert.NotContains(t, calls[0], "陈晓雯")
	assert.Contains(t, calls[0], "{{NAME_")
}

// A translation plugin gets what the model found as placeholders, and the
// reader the names.
func TestAPluginTranslatorGoesThroughTheMaskModel(t *testing.T) {
	e := newAssistEnv(t)
	installMaskModel(t, e.s, newFakeModel(map[string]string{"Sarah Thompson": "private_person"}))
	setMaskModel(t, e.s, true)
	record := useNativeTranslator(t, e.s, nil)
	setBody(t, e, "Sarah Thompson arrives", "")
	assert.Equal(t, []string{"[zh-CN] Sarah Thompson arrives"}, texts(e.run(t, "translate", Target{Kind: "message", ID: e.msg})))
	req := translateRequests(t, record)
	require.NotEmpty(t, req)
	got := fmt.Sprint(req[len(req)-1]["texts"])
	assert.NotContains(t, got, "Sarah Thompson")
	assert.Contains(t, got, "{{NAME_")
}

// The preview masks with the model while the settings it is given turn it on.
func TestTheMaskingPreviewUsesTheMaskModel(t *testing.T) {
	e := newApprovalEnv(t)
	installMaskModel(t, e.s, newFakeModel(map[string]string{"陈晓雯": "private_person"}))
	st := defaultSettings()
	out, err := e.s.PreviewMasking(t.Context(), st, "陈晓雯下午到")
	require.NoError(t, err)
	assert.Equal(t, "陈晓雯下午到", out)
	st.MaskModel, st.MaskCategories.Names.On = true, true
	out, err = e.s.PreviewMasking(t.Context(), st, "陈晓雯下午到")
	require.NoError(t, err)
	assert.Regexp(t, `^\{\{NAME_[a-z2-7]{12}\}\}下午到$`, out)
}

// The masking log says what found each value: the model the name it found,
// a rule the card.
func TestTheMaskingLogSaysWhatFoundEachValue(t *testing.T) {
	e := newApprovalEnv(t)
	installMaskModel(t, e.s, newFakeModel(map[string]string{"陈晓雯": "private_person"}))
	setMaskModel(t, e.s, true)
	conv := e.newConv(t, "ask", 0)
	e.srv.Script(testllm.Turn{Text: []string{"ok"}})
	e.start(t, conv, "陈晓雯的卡是 4111 1111 1111 1111 吗？")
	log, err := e.s.MaskingLog(t.Context())
	require.NoError(t, err)
	source := map[string]string{}
	for _, r := range log {
		source[r.Kind] = r.Source
	}
	assert.Equal(t, map[string]string{"NAME": MaskSourceModel, "BANK_CARD": MaskSourceRule}, source)
}

// AstrLink Guard itself, from the files in GODWIT_GUARD_DIR as
// core/ai/mask/guard's tests read them, finds a name and a phone.
func TestTheMaskModelReadsWithAstrLinkGuard(t *testing.T) {
	src := os.Getenv("GODWIT_GUARD_DIR")
	if src == "" {
		t.Skip("GODWIT_GUARD_DIR is not set")
	}
	e := newApprovalEnv(t)
	require.NoError(t, os.MkdirAll(e.s.maskModelDir(), 0o700))
	for _, f := range guard.Files {
		require.NoError(t, os.Symlink(filepath.Join(src, f), filepath.Join(e.s.maskModelDir(), f)))
	}
	st := setMaskModel(t, e.s, true)
	for range 2 { // the second from memory
		out, err := e.s.PreviewMasking(t.Context(), st, "Hi, I'm Sarah Thompson, call me at (415) 555-0132.")
		require.NoError(t, err)
		assert.Regexp(t, `^Hi, I'm \{\{NAME_[a-z2-7]{12}\}\}, call me at \(?\{\{PHONE_[a-z2-7]{12}\}\}\.$`, out)
	}
}
