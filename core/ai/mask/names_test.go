package mask_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godwit-email/godwit/core/ai/mask"
)

// A tool result: the mail as the model reads it, JSON in a JSON string.
func mailBody(t *testing.T, mail string) string {
	return marshal(t, map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "张三说了什么？"},
		map[string]any{"role": "tool", "tool_call_id": "c1", "content": marshal(t, map[string]any{"result": mail})},
	}})
}

// namesMasker keeps addresses as they are: these tests are about names.
func namesMasker() *mask.Masker {
	m := mask.New(key)
	_ = m.Configure(mask.Config{Off: []string{mask.Emails}})
	m.SetNames(false) // until a test turns them on
	return m
}

// marshal is v as JSON, < > & as they are.
func marshal(t *testing.T, v any) string {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	require.NoError(t, e.Encode(v))
	return strings.TrimSuffix(b.String(), "\n")
}

func TestMasksADisplayNameInTheHeaderAndTheText(t *testing.T) {
	m := namesMasker()
	m.SetNames(true)
	in := mailBody(t, "Subject: 周报\nFrom: 张三 <zhang@example.com>\nTo: \"Li, Na\" <li@example.com>, Robert Smith <bob@example.com>\n\n李娜你好，张三这周休假。Robert Smith 代班, Li, Na 知道。")
	out, err := m.Body(in)
	require.NoError(t, err)

	names := regexp.MustCompile(`\{\{NAME_[a-z2-7]{12}\}\}`).FindAllString(out, -1)
	require.Len(t, names, 7, out) // 张三 in the question, From and the text; Li, Na twice; Robert Smith twice
	assert.NotContains(t, out, "张三")
	assert.NotContains(t, out, "Robert Smith")
	assert.NotContains(t, out, "Li, Na")
	assert.Equal(t, names[0], names[1], "the question's 张三 is the header's")
	assert.Equal(t, names[1], names[4], "and the text's")
	assert.Contains(t, out, "李娜", "a name not in a header goes as it is")
	assert.Contains(t, out, "zhang@example.com")

	assert.Equal(t, in, m.Restore(out))
}

func TestNamesAreNotMaskedWhenOff(t *testing.T) {
	m := namesMasker()
	in := mailBody(t, "From: 张三 <zhang@example.com>\n\n张三这周休假。")
	out, err := m.Body(in)
	require.NoError(t, err)
	assert.Equal(t, in, out)

	m.SetNames(true)
	_, err = m.Body(in)
	require.NoError(t, err)
	m.SetNames(false)
	out, err = m.Text("张三这周休假。")
	require.NoError(t, err)
	assert.Equal(t, "张三这周休假。", out, "a name learned while on is not masked once off")
}

func TestAShortDisplayNameIsNotMasked(t *testing.T) {
	m := namesMasker()
	m.SetNames(true)
	for _, mail := range []string{
		"From: Al <al@example.com>\n\nAl said the deal is final.",
		"From: Ann <ann@example.com>\n\nAnn said the deal is final.",
		"From: 王 <wang@example.com>\n\n王先生说可以。",
	} {
		out, err := m.Text(mail)
		require.NoError(t, err)
		assert.Equal(t, mail, out)
	}
}

func TestANameIsMatchedAsAWholeWord(t *testing.T) {
	m := namesMasker()
	m.SetNames(true)
	out, err := m.Text("From: Mark Lee <mark@example.com>\n\nMark Leeds wrote to Mark Lee.")
	require.NoError(t, err)
	assert.Equal(t, "From: <NAME> <mark@example.com>\n\nMark Leeds wrote to <NAME>.", shown(out))
}

// The model wrote a name it was given into a draft's recipients: restored,
// the call goes; a made-up NAME_ placeholder refuses it.
func TestRestoresANameInToolArguments(t *testing.T) {
	m := namesMasker()
	m.SetNames(true)
	out, err := m.Text("From: 张三 <zhang@example.com>")
	require.NoError(t, err)
	p := regexp.MustCompile(`\{\{NAME_[a-z2-7]{12}\}\}`).FindString(out)
	args, err := m.RestoreArgs(json.RawMessage(`{"to":["` + p + ` <zhang@example.com>"]}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"to":["张三 <zhang@example.com>"]}`, string(args))
	_, err = m.RestoreArgs(json.RawMessage(`{"to":["NAME_aaaaaaaaaaaa"]}`))
	require.Error(t, err)
}

func TestSetKnownNamesMasksAndRetractsWithoutAffectingHeaderNames(t *testing.T) {
	m := namesMasker()
	m.SetKnownNames([]string{
		"  张伟  ",
		"王",                // too short CJK (< 2)
		"Bob",              // too short non-CJK (< 4)
		"user@example.com", // contains @
		"Robert Johnson",   // longer than header's "Robert"
		" Elena Rostova ",  // valid non-CJK
	})

	// Off when SetNames is false.
	out, err := m.Text("请给张伟和 Elena Rostova 发会议邀请。")
	require.NoError(t, err)
	assert.Equal(t, "请给张伟和 Elena Rostova 发会议邀请。", out)

	// Turn names on, plus learn a header name ("陈明") and shorter prefix ("Robert").
	m.SetNames(true)
	_, err = m.Text("From: 陈明 <chen@example.com>, Robert <rob@example.com>\n\nHi")
	require.NoError(t, err)

	// Known card names mask without appearing in headers; short/@ names stay unmasked;
	// longer external name "Robert Johnson" beats shorter header name "Robert".
	src := "王先生和 Bob 说请给张伟、Elena Rostova、Robert Johnson 和陈明发邮件到 user@example.com。"
	masked, err := m.Text(src)
	require.NoError(t, err)
	assert.NotContains(t, masked, "张伟")
	assert.NotContains(t, masked, "Elena Rostova")
	assert.NotContains(t, masked, "Robert Johnson")
	assert.NotContains(t, masked, "Johnson", "Robert Johnson must mask as one whole name, not just Robert")
	assert.NotContains(t, masked, "陈明")
	assert.Contains(t, masked, "王先生")
	assert.Contains(t, masked, "Bob")
	assert.Contains(t, masked, "user@example.com")
	assert.Equal(t, src, m.Restore(masked))

	// Retracting card names stops masking 张伟 and Elena Rostova on new texts,
	// while prior placeholders still restore and header-learned 陈明 stays masked!
	m.SetKnownNames(nil)
	assert.Equal(t, src, m.Restore(masked), "already-issued placeholders must still restore")

	after, err := m.Text("再问一下张伟、Elena Rostova 和陈明。")
	require.NoError(t, err)
	assert.Contains(t, after, "张伟", "removed known card name must not stay stuck in learned names")
	assert.Contains(t, after, "Elena Rostova", "removed known card name must not stay stuck in learned names")
	assert.NotContains(t, after, "陈明", "header-learned name must remain masked")
}

// A contact a tool returned, its name in no header: read back from a stored
// conversation by a masker that never saw it, the name is masked again.
func TestAContactResultNameIsLearnedAgain(t *testing.T) {
	m := namesMasker()
	m.SetNames(true)
	contact := "<untrusted_email source=\"contact:1\">\nName: Margaret Hale\nAddress: margaret@example.com\n</untrusted_email>"
	m.Learn(marshal(t, map[string]any{"role": "tool", "tool_call_id": "c1", "content": marshal(t, map[string]any{"results": []any{map[string]any{"contact": contact}}})}))
	out, err := m.Text("Write to Margaret Hale.")
	require.NoError(t, err)
	assert.NotContains(t, out, "Margaret Hale")
	assert.Contains(t, out, "{{NAME_")

	// Mail cannot pass its text off as a contact: ai.Untrusted escapes the tag inside it.
	m2 := namesMasker()
	m2.SetNames(true)
	m2.Learn("&lt;untrusted_email source=\"contact:1\">\nName: Plain Words\n")
	out, err = m2.Text("Plain Words")
	require.NoError(t, err)
	assert.Equal(t, "Plain Words", out)
}
