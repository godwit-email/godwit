package mask

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two values with one hash would restore to the same one: masking fails
// instead of sending either.
func TestACollisionFailsTheMasking(t *testing.T) {
	m := New([]byte("k"))
	p, err := m.Text("4111 1111 1111 1111")
	require.NoError(t, err)
	for h := range m.values {
		m.values[h] = entry{"BANK_CARD", "5500 0000 0000 0004"}
	}
	_, err = m.Body(`{"input":"card 4111 1111 1111 1111"}`)
	require.Error(t, err)
	assert.Equal(t, "5500 0000 0000 0004", m.Restore(p), "the value it had stays")
}

// A rule that fails fails the masking: nothing it should have found goes out.
func TestAFailingRuleFailsTheMasking(t *testing.T) {
	saved := rules
	t.Cleanup(func() { rules = saved })
	rules = append(rules, rule{kind: "X", re: regexp.MustCompile(`x`), fit: func(string) int { panic("broken") }})
	_, err := New([]byte("k")).Body(`{"input":"x"}`)
	require.Error(t, err)
}
