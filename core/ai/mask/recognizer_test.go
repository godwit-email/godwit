package mask_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godwit-email/godwit/core/ai/mask"
)

// model is a fake 识别模型: each value, wherever a text has it, under its
// label and with its score.
type model []struct {
	v, label string
	score    float64
}

func (md model) Recognize(text string) ([]mask.Recognized, error) {
	var out []mask.Recognized
	for _, x := range md {
		for at := 0; ; {
			i := strings.Index(text[at:], x.v)
			if i < 0 {
				break
			}
			out = append(out, mask.Recognized{Start: at + i, End: at + i + len(x.v), Label: x.label, Score: x.score})
			at += i + len(x.v)
		}
	}
	return out, nil
}

// modelMasker masks with md, names on.
func modelMasker(t *testing.T, md model) *mask.Masker {
	m := mask.New(key)
	require.NoError(t, m.Configure(mask.Config{Recognizer: md}))
	return m
}

func TestMasksWhatTheModelFinds(t *testing.T) {
	m := modelMasker(t, model{{"陈晓雯", "private_person", 0.97}})
	out, err := m.Text("陈晓雯下午到。")
	require.NoError(t, err)
	assert.Equal(t, "<NAME>下午到。", shown(out))
	assert.Equal(t, "陈晓雯下午到。", m.Restore(out))
}

// Each label goes as the kind its category has, if masking takes it; a
// date, a link, an address or an account (until their categories), or a
// score under 0.8, goes as it is.
func TestTheModelsLabelsAndScore(t *testing.T) {
	for _, c := range []struct {
		v, label string
		score    float64
		want     string
	}{
		{"Sarah Thompson", "private_person", 0.8, "<NAME>"},
		{"(415) 555-0132", "phone", 0.9, "(<PHONE>"}, // its parenthesis is trimmed, as a separator
		{"Tq7#vLm2!pZx", "common_secret", 0.9, "<SECRET>"},
		{"5500 0000 0000 0004", "payment_card", 0.9, "<BANK_CARD>"},
		{"Sarah Thompson", "private_person", 0.79, "Sarah Thompson"},
		{"10 月 3 日", "private_date", 0.99, "10 月 3 日"},
		{"example.org/x", "url", 0.99, "example.org/x"},
		{"10.0.0.8", "ip_address", 0.99, "10.0.0.8"},
		{"上海市徐汇区漕溪北路 88 号", "private_address", 0.99, "<ADDRESS>"},
		{"octocat", "account", 0.99, "<ACCOUNT>"},
	} {
		m := modelMasker(t, model{{c.v, c.label, c.score}})
		out, err := m.Text("> " + c.v + " <")
		require.NoError(t, err)
		assert.Equal(t, "> "+c.want+" <", shown(out), c.label)
	}
}

// A secret the model finds is one of 8 characters or more, not one
// character over and over, nor 3, 6 or 8 hex digits: a colour, a short hash.
func TestTheModelsSecretsThatAreNot(t *testing.T) {
	for v, want := range map[string]string{
		"Tq7#vLm2":    "<SECRET>",
		"Tq7#vLm":     "Tq7#vLm",
		"********":    "********",
		"--------":    "--------", // trimmed away, as separators
		"3fa9c1d2":    "3fa9c1d2",
		"DEADBEEF":    "DEADBEEF",
		"3fa9c1d2e":   "<SECRET>",
		"#fff":        "#fff",
		"#a1b2c3":     "#a1b2c3",
		"\"zR8!kq2Lp": "\"<SECRET>",
	} {
		m := modelMasker(t, model{{v, "common_secret", 0.95}})
		out, err := m.Text("new: " + v + " ok")
		require.NoError(t, err)
		assert.Equal(t, "new: "+want+" ok", shown(out), v)
	}
}

// Where what the model found overlaps what a rule, the user's or a
// built-in one, found, the rule's kind and bounds hold: an address keeps
// its domain, and the model's value is not even another category's to
// block.
func TestARuleWinsOverTheModel(t *testing.T) {
	const card = "4111 1111 1111 1111"
	m := mask.New(key)
	require.NoError(t, m.Configure(mask.Config{Words: []string{"Falcon"}, Actions: map[string]string{mask.Phones: mask.Block},
		Recognizer: model{{"到 ann.lee@example.com", "email", 0.99}, {"1111 1111 转 8", "phone", 0.99}, {"Falcon Lee", "private_person", 0.99}}}))
	out, err := m.Text("写信到 ann.lee@example.com，卡号 " + card + " 转 8，Falcon Lee 收")
	require.NoError(t, err)
	assert.Equal(t, "写信到 <EMAIL>@example.com，卡号 <BANK_CARD> 转 8，<TERM> Lee 收", shown(out))
}

// The model reads what the rules read but a request's tools, their
// descriptions too; a category off and an allowed value go as they are.
func TestTheModelReadsWhatTheRulesDo(t *testing.T) {
	m := mask.New(key)
	require.NoError(t, m.Configure(mask.Config{Off: []string{mask.Phones}, Allow: []string{"bob stone"},
		Recognizer: model{{"Ann Lee", "private_person", 0.9}, {"Bob Stone", "private_person", 0.9}, {"415 555 0132", "phone", 0.9}}}))
	out, err := m.Body(`{"model":"m","messages":[{"role":"user","content":"Ann Lee, Bob Stone: 415 555 0132"},` +
		`{"role":"tool","tool_call_id":"c1","content":"{\"from\":\"Ann Lee\"}"}],` +
		`"tools":[{"type":"function","function":{"name":"mail","description":"Mail as Ann Lee","parameters":{}}}]}`)
	require.NoError(t, err)
	assert.Equal(t, `{"model":"m","messages":[{"role":"user","content":"<NAME>, Bob Stone: 415 555 0132"},`+
		`{"role":"tool","tool_call_id":"c1","content":"{\"from\":\"<NAME>\"}"}],`+
		`"tools":[{"type":"function","function":{"name":"mail","description":"Mail as Ann Lee","parameters":{}}}]}`, shown(out))
}

// What the model found is known from then on, while it is on, as names
// are (ADR 0028): masked where the model does not find it, as written and
// not inside a longer word, unless a rule found something there. An
// account, or a value as short as a name too short to know (Ann), is
// masked only where it is found. Off, they go as they are, and restore.
func TestTheModelsValuesAreKnownWhileItIsOn(t *testing.T) {
	m := modelMasker(t, model{{"陈晓雯", "private_person", 0.9}, {"Sarah Lee", "private_person", 0.9}, {"Ann", "private_person", 0.9},
		{"octocat", "account", 0.9}, {"ann.lee", "private_person", 0.9}})
	first, err := m.Text("陈晓雯, Sarah Lee, Ann, octocat, ann.lee")
	require.NoError(t, err)
	assert.Equal(t, "<NAME>, <NAME>, <NAME>, <ACCOUNT>, <NAME>", shown(first))

	require.NoError(t, m.Configure(mask.Config{Recognizer: model{}}))
	out, err := m.Text("陈晓雯好, Sarah Leeds, Sarah Lee: Ann, octocat, ann.lee@example.com")
	require.NoError(t, err)
	assert.Equal(t, "<NAME>好, Sarah Leeds, <NAME>: Ann, octocat, <EMAIL>@example.com", shown(out))
	assert.Equal(t, strings.SplitN(first, ",", 2)[0], strings.SplitN(out, "好", 2)[0], "the same placeholder")

	require.NoError(t, m.Configure(mask.Config{Recognizer: model{}, Allow: []string{"sarah lee"}}))
	out, err = m.Text("Sarah Lee")
	require.NoError(t, err)
	assert.Equal(t, "Sarah Lee", out)

	require.NoError(t, m.Configure(mask.Config{}))
	out, err = m.Text("陈晓雯下午到。")
	require.NoError(t, err)
	assert.Equal(t, "陈晓雯下午到。", out)
	assert.Equal(t, "陈晓雯, Sarah Lee, Ann, octocat, ann.lee", m.Restore(first))
}

// Learned again, as after a restart, what the model found is known again.
func TestTheModelsValuesAreLearned(t *testing.T) {
	m := modelMasker(t, model{{"Sarah Lee", "private_person", 0.9}})
	m.Learn(`{"messages":[{"role":"user","content":"Sarah Lee"}]}`)
	require.NoError(t, m.Configure(mask.Config{Recognizer: model{}}))
	out, err := m.Text("Hi Sarah Lee")
	require.NoError(t, err)
	assert.Equal(t, "Hi <NAME>", shown(out))
}

// A model that fails fails the text.
func TestAFailingModelFailsTheText(t *testing.T) {
	m := mask.New(key)
	require.NoError(t, m.Configure(mask.Config{Recognizer: failing{}}))
	_, err := m.Text("陈晓雯下午到。")
	assert.ErrorContains(t, err, "out of memory")
}

type failing struct{}

func (failing) Recognize(string) ([]mask.Recognized, error) { return nil, errors.New("out of memory") }

// The spaces and separators the model takes in at a value's ends stay
// out of its placeholder; a value of nothing else is no value.
func TestTheModelsValuesAreTrimmed(t *testing.T) {
	m := modelMasker(t, model{{" 何志明律师，", "private_person", 0.9}, {"「」", "private_person", 0.9}})
	out, err := m.Text("致 何志明律师，「」见信好")
	require.NoError(t, err)
	assert.Equal(t, "致 <NAME>，「」见信好", shown(out))
	assert.Equal(t, "致 何志明律师，「」见信好", m.Restore(out))
}
