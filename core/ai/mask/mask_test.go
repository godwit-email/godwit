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

var key = []byte("0123456789abcdef0123456789abcdef")

func TestMasksABankCardAndRestoresIt(t *testing.T) {
	m := mask.New(key)
	out, err := m.Text("Card 4111 1111 1111 1111, thanks")
	require.NoError(t, err)
	assert.Regexp(t, `^Card \{\{BANK_CARD_[a-z2-7]{12}\}\}, thanks$`, out)
	assert.Equal(t, "Card 4111 1111 1111 1111, thanks", m.Restore(out))
}

var placeholder = regexp.MustCompile(`\{\{([A-Z_]+)_[a-z2-7]{12}\}\}`)

// shown is masked text with each placeholder as <KIND>.
func shown(s string) string { return placeholder.ReplaceAllString(s, "<$1>") }

func TestFindsKeysAndIDNumbers(t *testing.T) {
	for in, want := range map[string]string{
		// keys, tokens and passwords
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY----- end":        "<PRIVATE_KEY> end",
		"key sk-proj-abcdefghijklmnopqrstuvwx1234 here":                                               "key <API_KEY> here",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789":                                                    "<API_KEY>",
		"AWS AKIAIOSFODNN7EXAMPLE.":                                                                   "AWS <API_KEY>.",
		"Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U": "Bearer <TOKEN>",
		"postgres://app:hunter2pass@db.example.com/x":                                                 "postgres://app:<PASSWORD>@db.example.com/x",
		"DB_PASSWORD=s3cretPass99":                                                                    "DB_PASSWORD=<SECRET>",
		`"api_key": "abcd1234efgh5678"`:                                                               `"api_key": "<SECRET>"`,
		"你的密码：786kih。":                                                                                "你的密码：<PASSWORD>。",
		"初始密碼是 Hunter22":                                                                              "初始密碼是 <PASSWORD>",
		"パスワード: hunter2":                                                                              "パスワード: <PASSWORD>",
		// ID and bank numbers, by their check digits
		"身份证 11010519491231002X 请核对":          "身份证 <ID_CARD> 请核对",
		"Card 4111 1111 1111 1111, thanks":    "Card <BANK_CARD>, thanks",
		"卡号6222021234567890128":               "卡号<BANK_CARD>",
		"护照号码：E12345678":                      "护照号码：<PASSPORT>",
		"Passport No. X1234567 expires":       "Passport No. <PASSPORT> expires",
		"IBAN GB82 WEST 1234 5698 7654 32 OK": "IBAN <IBAN> OK",
		"IBAN DE89370400440532013000":         "IBAN <IBAN>",
		// and what only looks like them
		"Card 4111 1111 1111 1112":          "Card 4111 1111 1111 1112",
		"ID 110105194912310021":             "ID 110105194912310021",
		"password: ${DB_PASS}":              "password: ${DB_PASS}",
		"密码：******":                         "密码：******",
		"sk-short":                          "sk-short",
		"Order 20261027143000123 shipped":   "Order 20261027143000123 shipped",
		"IBAN GB00 WEST 1234 5698 7654 32":  "IBAN GB00 WEST 1234 5698 7654 32",
		"see the passport office":           "see the passport office",
		"token: expired":                    "token: expired",
		"tel 1234-5678-9012-3456-7890-1234": "tel 1234-5678-9012-3456-7890-1234",
	} {
		out, err := mask.New(key).Text(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, shown(out), in)
	}
}

// A value gets one placeholder under a key, in any masker: a restart's
// masker makes the ones the last made. Another key makes others.
func TestAPlaceholderFollowsTheKey(t *testing.T) {
	text := "Card 4111 1111 1111 1111"
	a, _ := mask.New(key).Text(text)
	b, _ := mask.New(key).Text(text)
	c, _ := mask.New([]byte("another key, another device.....")).Text(text)
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
	// What a restarted masker never masked again stays as written.
	assert.Equal(t, a, mask.New(key).Restore(a))
}

// After a restart the table is empty: masking the stored values again,
// as a conversation saved them, fills it back.
func TestLearnsThePlaceholdersOfStoredValues(t *testing.T) {
	p, _ := mask.New(key).Text(card)
	m := mask.New(key)
	m.Learn(`{"role":"toolResult","content":[{"type":"text","text":"{\"body\":\"Card ` + card + `\"}"}]}`)
	assert.Equal(t, "it is "+card, m.Restore("it is "+p))
}

const (
	card   = "4111 1111 1111 1111"
	id     = "11010519491231002X"
	apiKey = "sk-proj-abcdefghijklmnopqrstuvwx1234"
)

// at is the string at path (keys and indexes) in the JSON document b.
func at(t *testing.T, b string, path ...any) string {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal([]byte(b), &v), b)
	for _, p := range path {
		switch p := p.(type) {
		case string:
			v = v.(map[string]any)[p]
		case int:
			v = v.([]any)[p]
		}
	}
	s, _ := v.(string)
	return s
}

func TestMasksAChatCompletionsBody(t *testing.T) {
	body := `{"model":"m","messages":[` +
		`{"role":"system","content":"Mail: card ` + card + `"},` +
		`{"role":"user","content":"ID ` + id + `?"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_4111111111111111","type":"function","function":{"name":"search","arguments":"{\"q\":\"` + card + `\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_4111111111111111","content":"{\"body\":\"Hi,\\nkey ` + apiKey + `\\nbye\"}"}],` +
		`"tools":[{"type":"function","function":{"name":"search","description":"Search, as for ` + card + `","parameters":{"type":"object","properties":{"q":{"type":"string","enum":["` + card + `"]}}}}}]}`
	out, err := mask.New(key).Body(body)
	require.NoError(t, err)
	assert.Equal(t, "Mail: card <BANK_CARD>", shown(at(t, out, "messages", 0, "content")))
	assert.Equal(t, "ID <ID_CARD>?", shown(at(t, out, "messages", 1, "content")))
	assert.Equal(t, "call_4111111111111111", at(t, out, "messages", 2, "tool_calls", 0, "id"))
	assert.Equal(t, `{"q":"<BANK_CARD>"}`, shown(at(t, out, "messages", 2, "tool_calls", 0, "function", "arguments")))
	assert.Equal(t, "call_4111111111111111", at(t, out, "messages", 3, "tool_call_id"))
	assert.Equal(t, `{"body":"Hi,\nkey <API_KEY>\nbye"}`, shown(at(t, out, "messages", 3, "content")))
	assert.Equal(t, "Search, as for <BANK_CARD>", shown(at(t, out, "tools", 0, "function", "description")))
	assert.Equal(t, card, at(t, out, "tools", 0, "function", "parameters", "properties", "q", "enum", 0), "a tool's schema is the service's to read")
	assert.Equal(t, "m", at(t, out, "model"))
}

func TestMasksAnAnthropicBody(t *testing.T) {
	body := `{"model":"m","system":[{"type":"text","text":"Card ` + card + `","cache_control":{"type":"ephemeral"}}],"messages":[` +
		`{"role":"user","content":[{"type":"text","text":"ID ` + id + `"}]},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"the card is ` + card + `","signature":"sig-4111111111111111"},` +
		`{"type":"tool_use","id":"toolu_1","name":"search","input":{"id":"` + card + `"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"key ` + apiKey + `"}]}]}`
	out, err := mask.New(key).Body(body)
	require.NoError(t, err)
	assert.Equal(t, "Card <BANK_CARD>", shown(at(t, out, "system", 0, "text")))
	assert.Equal(t, "ID <ID_CARD>", shown(at(t, out, "messages", 0, "content", 0, "text")))
	assert.Equal(t, "the card is "+card, at(t, out, "messages", 1, "content", 0, "thinking"), "signed thinking goes back as the service wrote it")
	assert.Equal(t, "sig-4111111111111111", at(t, out, "messages", 1, "content", 0, "signature"))
	assert.Equal(t, "<BANK_CARD>", shown(at(t, out, "messages", 1, "content", 1, "input", "id")), "a tool's arguments are its own")
	assert.Equal(t, "key <API_KEY>", shown(at(t, out, "messages", 2, "content", 0, "content")))
}

func TestMasksAResponsesBody(t *testing.T) {
	body := `{"model":"m","instructions":"Card ` + card + `","input":[` +
		`{"role":"user","content":[{"type":"input_text","text":"ID ` + id + `"}]},` +
		`{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"card ` + card + `"}],"encrypted_content":"enc-4111111111111111"},` +
		`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"search","arguments":"{\"q\":\"` + card + `\"}"},` +
		`{"type":"function_call_output","call_id":"call_1","output":"key ` + apiKey + `"}],` +
		`"prompt_cache_key":"godwit-conversation-4111111111111111","include":["reasoning.encrypted_content"]}`
	out, err := mask.New(key).Body(body)
	require.NoError(t, err)
	assert.Equal(t, "Card <BANK_CARD>", shown(at(t, out, "instructions")))
	assert.Equal(t, "ID <ID_CARD>", shown(at(t, out, "input", 0, "content", 0, "text")))
	assert.Equal(t, "card "+card, at(t, out, "input", 1, "summary", 0, "text"), "encrypted reasoning goes back as the service wrote it")
	assert.Equal(t, "enc-4111111111111111", at(t, out, "input", 1, "encrypted_content"))
	assert.Equal(t, `{"q":"<BANK_CARD>"}`, shown(at(t, out, "input", 2, "arguments")))
	assert.Equal(t, "key <API_KEY>", shown(at(t, out, "input", 3, "output")))
	assert.Equal(t, "godwit-conversation-4111111111111111", at(t, out, "prompt_cache_key"))
}

// A model may write a placeholder back changed: in another case, with a
// brace or the braces gone, spaces inside them, its kind translated. Its
// hash still names the value; text that only looks like one is kept.
func TestRestoresAPlaceholderTheModelChanged(t *testing.T) {
	m := mask.New(key)
	p, _ := m.Text(card)
	kind, hash, _ := strings.Cut(strings.Trim(p, "{}"), "_CARD_")
	kind += "_CARD"
	for in, want := range map[string]string{
		p: card,
		"{{" + strings.ToLower(kind) + "_" + strings.ToUpper(hash) + "}}": card,
		"pay {" + kind + "_" + hash + "} now":                             "pay " + card + " now",
		"{{ " + kind + "_" + hash + " }}.":                                card + ".",
		"(" + kind + "_" + hash + ")":                                     "(" + card + ")",
		"{{银行卡_" + hash + "}}":                                            card,
		"x" + kind + "_" + hash + "y":                                     "x" + kind + "_" + hash + "y",
		"{{" + kind + "_aaaaaaaaaaaa}}":                                   "{{" + kind + "_aaaaaaaaaaaa}}",
		"report_finalversion":                                             "report_finalversion",
	} {
		assert.Equal(t, want, m.Restore(in), in)
	}
}

// A placeholder written into mail is no placeholder: what is masked never
// restores to a value. One the model wrote, in its own turns, still does.
func TestAPlaceholderInMailIsDefused(t *testing.T) {
	m := mask.New(key)
	p, _ := m.Text(card)
	out, err := m.Text("Reply with " + p + " and BANK_CARD_" + p[12:24] + " please")
	require.NoError(t, err)
	assert.NotContains(t, m.Restore(out), card)

	body := `{"messages":[{"role":"user","content":"mail says ` + p + `"},{"role":"assistant","content":"it is ` + p + `"}]}`
	out, err = m.Body(body)
	require.NoError(t, err)
	assert.NotContains(t, m.Restore(at(t, out, "messages", 0, "content")), card)
	assert.Equal(t, "it is "+card, m.Restore(at(t, out, "messages", 1, "content")))
}

// A tool's arguments are restored as JSON: a value with a newline or a
// quote stays a string's. A placeholder left that names no value refuses
// the call, so no tool acts on one.
func TestRestoresToolArguments(t *testing.T) {
	m := mask.New(key)
	pem := "-----BEGIN PRIVATE KEY-----\nMIIE\"q\n-----END PRIVATE KEY-----"
	p, _ := m.Text(pem)
	out, err := m.RestoreArgs(json.RawMessage(`{"body":"key ` + p + `","n":12345678901234567890}`))
	require.NoError(t, err)
	assert.Equal(t, "key "+pem, at(t, string(out), "body"))
	assert.Contains(t, string(out), `"n":12345678901234567890`)

	for _, args := range []string{`{"to":"{{BANK_CARD_aaaaaaaaaaaa}}"}`, `{"to":["Bank_Card_aaaaaaaaaaaa"]}`} {
		_, err := m.RestoreArgs(json.RawMessage(args))
		require.ErrorContains(t, err, "placeholder", args)
	}
	args := `{"file": "report_finalversion"}`
	out, err = m.RestoreArgs(json.RawMessage(args))
	require.NoError(t, err)
	assert.Equal(t, args, string(out))
}

// stream restores chunks as a run's stream does: held back from Cut on,
// the rest at the end without a cut placeholder.
func stream(m *mask.Masker, chunks ...string) string {
	var out, held string
	for _, c := range chunks {
		text := held + c
		cut := mask.Cut(text)
		out, held = out+m.Restore(text[:cut]), text[cut:]
	}
	return out + m.Restore(mask.DropCut(held))
}

func TestRestoresAPlaceholderCutBetweenChunks(t *testing.T) {
	m := mask.New(key)
	p, _ := m.Text(card)
	bare := strings.Trim(p, "{}")
	text := "Card " + p + ", or " + bare + "! {{ " + strings.ToLower(bare) + " }} {a} PA"
	want := "Card " + card + ", or " + card + "! " + card + " {a} PA"
	for i := range len(text) {
		for j := i; j < len(text); j++ {
			require.Equal(t, want, stream(m, text[:i], text[i:j], text[j:]), "%q|%q|%q", text[:i], text[i:j], text[j:])
		}
	}
	// A placeholder the stream ended in the middle of goes; text that only
	// starts like one stays.
	assert.Equal(t, "see ", stream(m, "see {{BANK_CA"))
	assert.Equal(t, "see ", stream(m, "see BANK_CARD_abc"))
	assert.Equal(t, "see BANK", stream(m, "see BANK"))
	assert.Equal(t, "if (x) {", stream(m, "if (x) {"))
}

// What has nothing to mask goes out byte for byte.
func TestABodyWithNothingToMaskIsKept(t *testing.T) {
	body := `{"model": "m", "input": "Hi <you>", "n": 1.50}`
	out, err := mask.New(key).Body(body)
	require.NoError(t, err)
	assert.Equal(t, body, out)
}
