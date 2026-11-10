package kit_test

import (
	"testing"

	"github.com/godwit-email/godwit/internal/kit"
	"github.com/stretchr/testify/require"
)

func TestLabelInitial(t *testing.T) {
	for name, want := range map[string]string{
		"Acme":                      "A",
		"  acme srl":                "A",
		"Client · Acme":             "A", // the part after a " · " is the name
		"Client · Acme / Sede Nord": "A",
		"Équipe":                    "É",
		"123 Studio":                "1",
		"":                          "?",
		" · ":                       "?",
	} {
		require.Equal(t, want, kit.LabelInitial(name), name)
	}
}
