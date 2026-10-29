package core

import (
	"encoding/json"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A thread's summary reads as its steps with the emails they cite, a cite
// not of the thread dropped, and the user's to-do; none without a TODO line.
func TestParseThreadDigest(t *testing.T) {
	d, ok := ParseThreadDigest("PROGRESS\n- Lena proposed a launch <mail id=\"7\"/>, Bob agreed <mail id=\"9\"/><mail id=\"7\"/>.\n"+
		"- Someone said more <mail id=\"99\"/>\n- No source here\n- Sent <mail id=\"7\"/> and resent\n- <mail id=\"7\"/>\nTODO: Reply by Friday <mail id=\"9\"/>\n", []int64{7, 9})
	require.True(t, ok)
	assert.Equal(t, ThreadDigest{Progress: []DigestLine{
		{{Text: "Lena proposed a launch"}, {Message: 7}, {Text: ", Bob agreed"}, {Message: 9}, {Message: 7}, {Text: "."}},
		{{Text: "Someone said more"}},
		{{Text: "No source here"}},
		{{Text: "Sent"}, {Message: 7}, {Text: " and resent"}},
	}, Todo: DigestLine{{Text: "Reply by Friday"}, {Message: 9}}}, d)

	d, ok = ParseThreadDigest("PROGRESS\n- Done <mail id=\"7\"/>\n", []int64{7})
	require.True(t, ok)
	assert.Nil(t, d.Todo)

	_, ok = ParseThreadDigest("Some prose the model wrote instead.", []int64{7})
	assert.False(t, ok)
}

// summarize asks a thread for the digest's form; a message keeps its bullets.
func TestSummarizeThreadAsksDigest(t *testing.T) {
	e := newSkillEnv(t)
	e.srv.Script(testllm.Turn{Text: []string{"PROGRESS\n- x <mail id=\"1\"/>"}}, testllm.Turn{Text: []string{"- a point"}})
	e.run(t, "summarize", Target{Kind: "thread", ID: 1})
	e.run(t, "summarize", Target{Kind: "message", ID: e.msg})
	require.Len(t, e.srv.Requests(), 2)
	var thread, message string
	require.NoError(t, json.Unmarshal(e.srv.Requests()[0].System, &thread))
	require.NoError(t, json.Unmarshal(e.srv.Requests()[1].System, &message))
	assert.Contains(t, thread, "PROGRESS")
	assert.NotContains(t, message, "PROGRESS")
}
