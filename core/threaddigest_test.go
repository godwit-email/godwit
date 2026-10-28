package core

import (
	"encoding/json"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A thread's summary reads as its steps with their emails, a source not of
// the thread dropped, and the user's to-do; none without a TODO line.
func TestParseThreadDigest(t *testing.T) {
	d, ok := ParseThreadDigest("PROGRESS\n- Lena proposed a launch [message:7]\n- Bob agreed [message: 9]\n"+
		"- Someone said more [message:99]\n- No source here\nTODO: Reply by Friday\n", []int64{7, 9})
	require.True(t, ok)
	assert.Equal(t, ThreadDigest{Progress: []DigestStep{
		{"Lena proposed a launch", 7}, {"Bob agreed", 9}, {"Someone said more", 0}, {"No source here", 0},
	}, Todo: "Reply by Friday"}, d)

	d, ok = ParseThreadDigest("PROGRESS\n- Done [message:7]\n", []int64{7})
	require.True(t, ok)
	assert.Empty(t, d.Todo)

	_, ok = ParseThreadDigest("Some prose the model wrote instead.", []int64{7})
	assert.False(t, ok)
}

// summarize asks a thread for the digest's form; a message keeps its bullets.
func TestSummarizeThreadAsksDigest(t *testing.T) {
	e := newSkillEnv(t)
	e.srv.Script(testllm.Turn{Text: []string{"PROGRESS\n- x [message:1]"}}, testllm.Turn{Text: []string{"- a point"}})
	e.run(t, "summarize", Target{Kind: "thread", ID: 1})
	e.run(t, "summarize", Target{Kind: "message", ID: e.msg})
	require.Len(t, e.srv.Requests(), 2)
	var thread, message string
	require.NoError(t, json.Unmarshal(e.srv.Requests()[0].System, &thread))
	require.NoError(t, json.Unmarshal(e.srv.Requests()[1].System, &message))
	assert.Contains(t, thread, "PROGRESS")
	assert.NotContains(t, message, "PROGRESS")
}
