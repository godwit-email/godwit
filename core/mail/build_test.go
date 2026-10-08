package mail_test

import (
	"testing"
	"time"

	"github.com/godwitmail/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildRoundTrip(t *testing.T) {
	o := mail.Outgoing{
		From: "我 <me@x.com>", To: []string{"张三 <a@y.com>", "b@y.com"}, Cc: []string{"c@y.com"},
		Bcc:     []string{"secret@z.com"},
		Subject: "报销单", HTML: `<p>你好 <b>世界</b></p><script>x()</script>`,
		InReplyTo: "orig@y.com", References: []string{"r1@y.com", "orig@y.com"},
		Attachments: []mail.Attachment{{Filename: "a.txt", ContentType: "text/plain", Data: []byte("hello")}},
	}
	raw, id, err := mail.Build(o, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "secret@z.com")
	assert.NotContains(t, string(raw), "Bcc")

	p, err := mail.Parse(raw)
	require.NoError(t, err)
	assert.False(t, p.Damaged)
	assert.Equal(t, id, p.MessageID)
	assert.Regexp(t, `^[0-9a-f]{32}@x\.com$`, id)
	assert.Equal(t, "报销单", p.Subject)
	assert.Equal(t, o.To, p.To)
	assert.Equal(t, o.Cc, p.Cc)
	assert.Equal(t, "orig@y.com", p.InReplyTo)
	assert.Equal(t, o.References, p.References)
	assert.Contains(t, p.HTML, "<b>世界</b>")
	assert.NotContains(t, p.HTML, "script")
	assert.Contains(t, p.Text, "你好 世界")
	assert.NotContains(t, p.Text, "<")
	require.Len(t, p.Attachments, 1)
	assert.Equal(t, "a.txt", p.Attachments[0].Filename)
	assert.Equal(t, []byte("hello"), p.Attachments[0].Data)
}

func TestBuildNoAttachment(t *testing.T) {
	raw, _, err := mail.Build(mail.Outgoing{From: "me@x.com", To: []string{"a@y.com"}, HTML: "<p>hi</p>"}, time.Now())
	require.NoError(t, err)
	p, err := mail.Parse(raw)
	require.NoError(t, err)
	assert.Contains(t, p.HTML, "hi")
	assert.Contains(t, p.Text, "hi")
	assert.Empty(t, p.Attachments)
}

func TestHTMLToText(t *testing.T) {
	got := mail.HTMLToText(`<p>a &amp; b</p><ul><li>x</li><li>y</li></ul><a href="http://e.com">link</a><br>end`)
	assert.Equal(t, "a & b\n- x\n- y\nlink (http://e.com)\nend", got)
	assert.Equal(t, "hi (http://d.com)", mail.HTMLToText(`<a data-href="http://d.com">hi</a>`))
}
