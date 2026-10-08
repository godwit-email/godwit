package mail_test

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/godwitmail/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParse(t *testing.T, name string) *mail.Parsed {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	p, err := mail.Parse(raw)
	require.NoError(t, err)
	return p
}

func TestParseCharsets(t *testing.T) {
	assert.Equal(t, "报销单已通过", mustParse(t, "gbk.eml").Subject)
	assert.Contains(t, mustParse(t, "gbk.eml").Text, "请查收发票")
	assert.Equal(t, "報銷單已通過", mustParse(t, "big5.eml").Subject)
	assert.Contains(t, mustParse(t, "big5.eml").Text, "請查收發票")
	assert.Equal(t, "経費精算が承認されました", mustParse(t, "iso2022jp.eml").Subject)
	assert.Contains(t, mustParse(t, "iso2022jp.eml").Text, "請求書をご確認ください")
}

func TestParseAlternative(t *testing.T) {
	p := mustParse(t, "alternative.eml")
	assert.Equal(t, "Hello", p.Subject)
	assert.Contains(t, p.Text, "plain body")
	assert.Contains(t, p.HTML, "<b>body</b>")
	assert.Equal(t, "alt@example.com", p.MessageID)
	assert.Equal(t, time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC), p.Date.UTC())
	assert.Empty(t, p.Attachments)
}

func TestParseAttachment(t *testing.T) {
	p := mustParse(t, "attachment.eml")
	require.Len(t, p.Attachments, 1)
	a := p.Attachments[0]
	assert.False(t, a.Inline)
	assert.Equal(t, "invoice.pdf", a.Filename)
	assert.Equal(t, "2", a.Part)
	assert.Equal(t, "base64", a.Encoding)
	assert.Equal(t, "application/pdf", a.ContentType)
	assert.True(t, strings.HasPrefix(string(a.Data), "%PDF-1.4"))
	assert.Equal(t, len(a.Data), a.Size)

	enc := base64.StdEncoding.EncodeToString(a.Data)
	got, err := mail.DecodePart([]byte(enc), "base64", "")
	require.NoError(t, err)
	assert.Equal(t, a.Data, got)
}

func TestParseLimits(t *testing.T) {
	big := strings.Repeat("a", 3<<20)
	p, err := mail.Parse([]byte("Subject: x\r\nContent-Type: text/plain\r\n\r\n" + big))
	require.NoError(t, err)
	assert.LessOrEqual(t, len(p.Text), mail.MaxBodyBytes)
	assert.True(t, p.Truncated)

	var sb strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&sb, "Content-Type: multipart/mixed; boundary=\"b%d\"\r\n\r\n--b%d\r\n", i, i)
	}
	sb.WriteString("Content-Type: text/plain\r\n\r\ndeep\r\n")
	_, err = mail.Parse([]byte("Subject: x\r\n" + sb.String()))
	assert.NoError(t, err)
}

func TestParseStructure(t *testing.T) {
	bs := &imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{
		&imap.BodyStructureMultiPart{Subtype: "alternative", Children: []imap.BodyStructure{
			&imap.BodyStructureSinglePart{Type: "text", Subtype: "plain", Encoding: "quoted-printable", Params: map[string]string{"charset": "gbk"}},
			&imap.BodyStructureSinglePart{Type: "text", Subtype: "html", Encoding: "base64"},
		}},
		&imap.BodyStructureSinglePart{Type: "application", Subtype: "pdf", Encoding: "base64", Size: 10,
			Extended: &imap.BodyStructureSinglePartExt{Disposition: &imap.BodyStructureDisposition{Value: "attachment", Params: map[string]string{"filename": "a.pdf"}}}},
	}}
	text, atts := mail.ParseStructure(bs)
	require.Len(t, text, 2)
	assert.Equal(t, "1.1", text[0].Part)
	assert.Equal(t, "gbk", text[0].Charset)
	assert.Equal(t, "1.2", text[1].Part)
	require.Len(t, atts, 1)
	assert.Equal(t, "2", atts[0].Part)
	assert.Equal(t, "a.pdf", atts[0].Filename)

	// single-part message body is part "1"
	text, _ = mail.ParseStructure(&imap.BodyStructureSinglePart{Type: "text", Subtype: "plain"})
	require.Len(t, text, 1)
	assert.Equal(t, "1", text[0].Part)

	// depth cap: no panic, keeps what was parsed
	var deep imap.BodyStructure = &imap.BodyStructureSinglePart{Type: "text", Subtype: "plain"}
	for i := 0; i < 30; i++ {
		deep = &imap.BodyStructureMultiPart{Subtype: "mixed", Children: []imap.BodyStructure{deep}}
	}
	text, _ = mail.ParseStructure(deep)
	assert.Empty(t, text)
}

func TestSnippet(t *testing.T) {
	assert.Equal(t, "a b c", mail.Snippet("  a \n\n b\t c "))
	assert.Len(t, []rune(mail.Snippet(strings.Repeat("好", 300))), 200)
}
