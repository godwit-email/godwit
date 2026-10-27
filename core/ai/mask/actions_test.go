package mask_test

import (
	"context"
	"errors"
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
		{Kind: "BANK_CARD", Category: mask.IDs, First: "4", Len: 19, Action: mask.Hide},
		{Kind: "EMAIL", Category: mask.Emails, First: "a", Len: 3, Action: mask.Warn},
	}, plain(got))

	_, err = m.Request(ctx, "call +86 138 1234 5678 about "+card)
	var blocked *mask.BlockedError
	require.ErrorAs(t, err, &blocked)
	assert.Equal(t, []mask.Hit{
		{Kind: "PHONE", Category: mask.Phones, First: "+", Len: 17, Action: mask.Block},
		{Kind: "BANK_CARD", Category: mask.IDs, First: "4", Len: 19, Action: mask.Hide},
	}, plain(got), "a blocked request is recorded too")
	assert.Equal(t, 1, turn.Count(mask.Hide), "the card, once")
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
