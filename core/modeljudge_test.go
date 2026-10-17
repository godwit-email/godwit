package core

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/stretchr/testify/require"
)

func testPluginJudge(t *testing.T, body string) (*Service, ai.Judge) {
	t.Helper()
	e := newApprovalEnv(t)
	startTestPlugin(t, e.s, "localjudge", `{},"capabilities":["models"]`, `export default function(ctx){ctx.models.register({listModels:()=>({models:[{id:"j",name:"Judge",transport:"plugin",operation:"judge"}]}),judge(call){`+body+`}})}`)
	_, err := e.s.ListModels(bg)
	require.NoError(t, err)
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.JudgeModel = "localjudge/j"
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	return e.s, pluginJudge{s: e.s, name: "localjudge"}
}
func TestPluginJudgeOneQuestion(t *testing.T) {
	_, j := testPluginJudge(t, `if(call.questions.length!==1) throw new Error("batch");return {answers:[{name:call.questions[0].name,type:"predicate",probability:0.8}]}`)
	got, err := j.Ask(bg, "mail", []ai.Question{{Name: "a", Type: ai.Predicate, Instructions: "is relevant?"}, {Name: "b", Type: ai.Predicate, Instructions: "is relevant?"}, {Name: "c", Type: ai.Predicate, Instructions: "is relevant?"}})
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, "a", got[0].Name)
	require.Equal(t, "c", got[2].Name)
}
func TestPluginJudgeBatchAtomic(t *testing.T) {
	_, j := testPluginJudge(t, `if(call.questions[0].name==="b")throw {code:"model_unavailable",message:"wait",retryable:true};return {answers:[{name:"a",type:"predicate",probability:0.8}]}`)
	got, err := j.Ask(bg, "mail", []ai.Question{{Name: "a", Type: ai.Predicate, Instructions: "is relevant?"}, {Name: "b", Type: ai.Predicate, Instructions: "is relevant?"}})
	require.Nil(t, got)
	require.ErrorIs(t, err, ai.ErrJudgeUnavailable)
	var me *ModelError
	require.ErrorAs(t, err, &me)
}
func TestPluginJudgeBadAnswer(t *testing.T) {
	_, j := testPluginJudge(t, `return {answers:[{name:call.questions[0].name,type:"predicate",probability:2}]}`)
	got, err := j.Ask(bg, "mail", []ai.Question{{Name: "a", Type: ai.Predicate, Instructions: "is relevant?"}})
	require.Nil(t, got)
	require.ErrorIs(t, err, ai.ErrJudgePlugin)
	require.False(t, errors.Is(err, ai.ErrJudgeAnswer))
	ctx, cancel := context.WithCancel(bg)
	cancel()
	_, err = j.Ask(ctx, "mail", []ai.Question{{Name: "a", Type: ai.Predicate, Instructions: "is relevant?"}})
	require.ErrorIs(t, err, context.Canceled)
}

func TestPluginJudgeCanceledWaiter(t *testing.T) {
	s, record := nativeAssetService(t, map[string]any{"modelsSleepMs": 200})
	s.assets = &fakeModelAssets{}
	_, err := s.PrepareModel(bg, "assetjudge/a")
	require.NoError(t, err)
	j := pluginJudge{s: s, name: "assetjudge"}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() {
		_, err := j.Ask(ctx, "public", []ai.Question{{Name: "q", Type: ai.Predicate, Instructions: "test?"}})
		done <- err
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(record + ".models.judge"); return err == nil }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Len(t, s.running["assetjudge"].modelJudge.gate, 1)
	require.Error(t, s.RemoveModelFiles(bg, "assetjudge/a"))
	require.Eventually(t, func() bool { return len(s.running["assetjudge"].modelJudge.gate) == 0 }, time.Second, time.Millisecond)
}

func TestPluginJudgeJSCancellation(t *testing.T) {
	s, j := testPluginJudge(t, `return ctx.storage.get("barrier").then(()=>new Promise(()=>{}))`)
	entered := make(chan struct{}, 1)
	beforePluginStorageGet = func(string) { entered <- struct{}{} }
	t.Cleanup(func() { beforePluginStorageGet = nil })
	for range 3 {
		ctx, cancel := context.WithCancel(bg)
		done := make(chan error, 1)
		go func() {
			_, err := j.Ask(ctx, "public", []ai.Question{{Name: "q", Type: ai.Predicate, Instructions: "test?"}})
			done <- err
		}()
		<-entered
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
		require.Eventually(t, func() bool { return len(s.running["localjudge"].modelJudge.gate) == 0 }, time.Second, time.Millisecond)
	}
	rows, err := s.db.Query(bg, "SELECT failures,enabled FROM plugins WHERE name='localjudge'")
	require.NoError(t, err)
	require.EqualValues(t, 0, rows.Values[0][0])
	require.EqualValues(t, 1, rows.Values[0][1])
}
