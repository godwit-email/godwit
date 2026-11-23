package mask_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/godwit-email/godwit/core/ai/mask"
)

// 仅警告 sends a category's values as they are; 拦截 sends nothing and says
// which categories held it back; the rest are masked.
func TestWarnSendsAsItIsAndBlockSendsNothing(t *testing.T) {
	m := configured(t, mask.Config{Actions: map[string]string{mask.IDs: mask.Warn}})
	assert.Equal(t, "card "+card+", key <API_KEY>", masked(t, m, "card "+card+", key "+apiKey))

	m = configured(t, mask.Config{Actions: map[string]string{mask.IDs: mask.Block, mask.Phones: mask.Block, mask.Emails: mask.Warn}})
	out, err := m.Text("card " + card + ", call +86 138 1234 5678, key " + apiKey)
	var blocked *mask.BlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Equal(t, []string{mask.IDs, mask.Phones}, blocked.Categories)
	assert.Empty(t, out)
	_, err = m.Body(`{"messages":[{"role":"user","content":"card ` + card + `"}]}`)
	assert.True(t, errors.As(err, &blocked), "a body too")
}

// A request records what it found, each value once, by its first character
// and length, never the value; and adds it to what its context gathers.
func TestARequestRecordsWhatItFound(t *testing.T) {
	m := configured(t, mask.Config{Actions: map[string]string{mask.Emails: mask.Warn, mask.Phones: mask.Block}})
	var feature string
	var got []mask.Hit
	m.Record = func(f string, hits []mask.Hit) { feature, got = f, hits }
	turn := new(mask.Found)
	ctx := mask.Tag(context.Background(), "wit", turn)

	out, err := m.Request(ctx, `{"messages":[{"role":"user","content":"card `+card+` and `+card+`, ann@example.com"}]}`)
	require.NoError(t, err)
	assert.Contains(t, out, "ann@example.com")
	assert.Equal(t, "wit", feature)
	assert.Equal(t, []mask.Hit{
		{Kind: "BANK_CARD", Category: mask.IDs, First: "4", Len: 19, Action: mask.Mask},
		{Kind: "EMAIL", Category: mask.Emails, First: "a", Len: 3, Action: mask.Warn},
	}, plain(got))

	_, err = m.Request(ctx, "call +86 138 1234 5678 about "+card)
	var blocked *mask.BlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Equal(t, []mask.Hit{
		{Kind: "PHONE", Category: mask.Phones, First: "+", Len: 17, Action: mask.Block},
		{Kind: "BANK_CARD", Category: mask.IDs, First: "4", Len: 19, Action: mask.Mask},
	}, plain(got), "a blocked request is recorded too")
	assert.Equal(t, 1, turn.Count(mask.Mask), "the card, once")
	assert.Equal(t, 1, turn.Count(mask.Warn))
	assert.Equal(t, 1, turn.Count(mask.Block))

	got = nil
	_, err = m.Request(context.Background(), "nothing here")
	require.NoError(t, err)
	assert.Nil(t, got, "nothing found, nothing recorded")
}

// plain is hits as a test can write them.
func plain(hits []mask.Hit) []mask.Hit {
	out := make([]mask.Hit, len(hits))
	for i, h := range hits {
		out[i] = mask.Hit{Kind: h.Kind, Category: h.Category, First: h.First, Len: h.Len, Action: h.Action}
	}
	return out
}

// A value another one overlaps still blocks: a custom word inside an address
// set to 拦截 does not let the address go out.
func TestAnOverlappedValueStillBlocks(t *testing.T) {
	m := configured(t, mask.Config{Words: []string{"alice"}, Actions: map[string]string{mask.Emails: mask.Block}})
	_, err := m.Text("mail alice@example.com")
	var blocked *mask.BlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Equal(t, []string{mask.Emails}, blocked.Categories)
}

// Learning a stored text rebuilds its placeholders whatever its category's
// action is now: one masked before, then set to 拦截, still restores.
func TestLearnRestoresACategoryNowBlocked(t *testing.T) {
	p, err := configured(t, mask.Config{}).Text("card " + card)
	require.NoError(t, err)
	m := configured(t, mask.Config{Actions: map[string]string{mask.IDs: mask.Block}})
	m.Learn(`{"messages":[{"role":"user","content":"card ` + card + `"}]}`)
	assert.Equal(t, "card "+card, m.Restore(p))
}

// whole is texts read as one, after a subject's line, and where each stands.
func whole(texts ...string) (string, [][2]int) {
	var b strings.Builder
	b.WriteString("Subject: Plans\n")
	var nodes [][2]int
	for _, t := range texts {
		nodes = append(nodes, [2]int{b.Len(), b.Len() + len(t)})
		b.WriteString(t)
	}
	return b.String(), nodes
}

// A code markup cuts in two is found by its label in another piece, and
// each piece of it is masked wherever it stands, restoring as it was: but
// a piece is not a value from then on, and the code is recorded once.
func TestRequestWholeMasksTheCutsOfAValue(t *testing.T) {
	m := configured(t, mask.Config{})
	var got []mask.Hit
	m.Record = func(_ string, hits []mask.Hit) { got = hits }
	texts := []string{"验证码 ", "482", "913", "\n", "Call 913 later"}
	w, nodes := whole(texts...)
	out, err := m.RequestWhole(mask.Tag(context.Background(), "translate", nil), w, nodes, texts)
	require.NoError(t, err)
	assert.Equal(t, "验证码 ", out[0])
	assert.True(t, strings.HasPrefix(out[1], "{{CODE_") && strings.HasPrefix(out[2], "{{CODE_") && out[1] != out[2], out)
	assert.Equal(t, "Call "+out[2]+" later", out[4])
	for i := range texts {
		assert.Equal(t, texts[i], m.Restore(out[i]))
	}
	assert.Equal(t, []mask.Hit{{Kind: "CODE", Category: mask.Codes, First: "4", Len: 6, Action: mask.Mask}}, plain(got))
	assert.Equal(t, "913 apples", masked(t, m, "913 apples"), "a piece is not learned")
}

// 仅警告 sends the pieces as they are and records the code whole; 拦截
// sends nothing.
func TestRequestWholeWarnsAndBlocksAValueAsAWhole(t *testing.T) {
	texts := []string{"验证码", "482913"}
	w, nodes := whole(texts[0], "\n", texts[1])
	nodes = [][2]int{nodes[0], nodes[2]} // the line break is not sent

	m := configured(t, mask.Config{Actions: map[string]string{mask.Codes: mask.Warn}})
	var got []mask.Hit
	m.Record = func(_ string, hits []mask.Hit) { got = hits }
	out, err := m.RequestWhole(mask.Tag(context.Background(), "translate", nil), w, nodes, texts)
	require.NoError(t, err)
	assert.Equal(t, texts, out)
	assert.Equal(t, []mask.Hit{{Kind: "CODE", Category: mask.Codes, First: "4", Len: 6, Action: mask.Warn}}, plain(got))

	m = configured(t, mask.Config{Actions: map[string]string{mask.Codes: mask.Block}})
	_, err = m.RequestWhole(context.Background(), w, nodes, texts)
	var b *mask.BlockedError
	require.ErrorAs(t, err, &b)
	assert.Equal(t, []string{mask.Codes}, b.Categories)
}

// What each text holds on its own is masked as Text masks it, whatever
// reading them whole finds: a word inside another, and two codes that,
// read together, are no one code (#873).
func TestRequestWholeMasksEachTextToo(t *testing.T) {
	m := configured(t, mask.Config{Words: []string{"cat"}})
	texts := []string{"concatenate"}
	w, nodes := whole(texts...)
	out, err := m.RequestWhole(context.Background(), w, nodes, texts)
	require.NoError(t, err)
	assert.NotContains(t, out[0], "cat")

	m = configured(t, mask.Config{Actions: map[string]string{mask.Codes: mask.Block}})
	texts = []string{"Your verification code is 482913.", "Your verification code is 731559."}
	w, nodes = whole(texts[0], "\n", texts[1])
	_, err = m.RequestWhole(context.Background(), w, [][2]int{nodes[0], nodes[2]}, texts)
	var b *mask.BlockedError
	require.ErrorAs(t, err, &b)
}
