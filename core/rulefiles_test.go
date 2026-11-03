package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFillNaming(t *testing.T) {
	vals := map[string]string{"date": "2026-11-03", "subject": "Invoice #1234", "from": "Stripe", "domain": "stripe.com",
		"year": "2026", "month": "11", "account": "you@example.com"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range vals {
			m[a] = b
		}
		m[k] = v
		return m
	}
	long := strings.Repeat("长", 300)
	for _, tc := range []struct {
		name, tpl string
		vals      map[string]string
		want      []string // nil: refused
	}{
		{"date subject", "{date} {subject}", vals, []string{"2026-11-03 Invoice #1234"}},
		{"levels from the template", "{year}/{month}/{date} {subject}", vals, []string{"2026", "11", "2026-11-03 Invoice #1234"}},
		{"sender level", "{from}/{date} {subject}", vals, []string{"Stripe", "2026-11-03 Invoice #1234"}},
		{"separators in a value stay one level", "{subject}", with("subject", "a/b\\c:d"), []string{"a-b-c-d"}},
		{"dot-dot in a value", "{subject}", with("subject", ".."), []string{"(no subject)"}},
		{"dot-dot path in a value", "{subject}", with("subject", "../../etc"), []string{"-..-etc"}},
		{"control characters", "{subject}", with("subject", "a\x00b\nc\u202ed"), []string{"a-b-c-d"}},
		{"leading and trailing dots and spaces", "{subject}", with("subject", " .hidden. "), []string{"hidden"}},
		{"empty subject", "{date} {subject}", with("subject", ""), []string{"2026-11-03 (no subject)"}},
		{"empty sender", "{from}/{subject}", with("from", ""), []string{"-", "Invoice #1234"}},
		{"value length capped", "{subject}", with("subject", strings.Repeat("a", 300)), []string{strings.Repeat("a", 80)}},
		{"name capped in bytes", "{subject}", with("subject", long), []string{strings.Repeat("长", 66)}}, // 200 bytes: room for " (2).pdf" under 255
		{"unknown token is text", "{nope} {date}", vals, []string{"{nope} 2026-11-03"}},
		{"literal text only", "invoices", vals, nil},
		{"empty", "", vals, nil},
		{"dot-dot segment", "../{date}", vals, nil},
		{"dot segment", "./{date}", vals, nil},
		{"empty segment", "{year}//{date}", vals, nil},
		{"leading slash", "/{date}", vals, nil},
		{"backslash in literal text", `a\{date}`, vals, []string{"a_2026-11-03"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FillNaming(tc.tpl, tc.vals)
			if tc.want == nil {
				assert.True(t, errors.Is(err, ErrInvalidRule), "err = %v, got %q", err, got)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNumberedName(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		want string
	}{
		{"invoice.pdf", 1, "invoice.pdf"},
		{"invoice.pdf", 2, "invoice (2).pdf"},
		{"2026-11-03 Invoice", 3, "2026-11-03 Invoice (3)"},
		{"archive.tar.gz", 2, "archive.tar (2).gz"},
		{".env", 2, ".env (2)"},
	} {
		assert.Equal(t, tc.want, numberedName(tc.name, tc.n), "%s #%d", tc.name, tc.n)
	}
}
