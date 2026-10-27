package mail_test

import (
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
)

func TestReplyRecipients(t *testing.T) {
	o := &mail.Parsed{From: "A <a@x.com>", To: []string{"Me <me@x.com>", "b@x.com"}, Cc: []string{"c@x.com", "ME@X.com", "b@x.com"}}
	to, cc := mail.ReplyRecipients(o, "me@x.com", false)
	assert.Equal(t, []string{"A <a@x.com>"}, to)
	assert.Empty(t, cc)
	to, cc = mail.ReplyRecipients(o, "me@x.com", true)
	assert.Equal(t, []string{"A <a@x.com>"}, to)
	assert.Equal(t, []string{"b@x.com", "c@x.com"}, cc)
	o.ReplyTo = "r@x.com"
	to, _ = mail.ReplyRecipients(o, "me@x.com", false)
	assert.Equal(t, []string{"r@x.com"}, to)
}

func TestSubjects(t *testing.T) {
	assert.Equal(t, "Re: Q4", mail.ReplySubject("Re: Q4"))
	assert.Equal(t, "Re: 报销", mail.ReplySubject("报销"))
	assert.Equal(t, "回复：报销", mail.ReplySubject("回复：报销"))
	assert.Equal(t, "回覆：報銷", mail.ReplySubject("回覆：報銷"))
	assert.Equal(t, "AW: Angebot", mail.ReplySubject("AW: Angebot"))
	assert.Equal(t, "답장: 보고서", mail.ReplySubject("답장: 보고서"))
	assert.Equal(t, "Fwd: Q4", mail.ForwardSubject("Q4"))
	assert.Equal(t, "Fwd: Q4", mail.ForwardSubject("Fwd: Q4"))
	assert.Equal(t, "转发：Q4", mail.ForwardSubject("转发：Q4"))
	assert.Equal(t, "轉寄：Q4", mail.ForwardSubject("轉寄：Q4"))
	assert.Equal(t, "WG: Angebot", mail.ForwardSubject("WG: Angebot"))
	assert.Equal(t, "TR: Facture", mail.ForwardSubject("TR: Facture"))
}

func TestQuoteHTML(t *testing.T) {
	d := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	q := mail.QuoteHTML(&mail.Parsed{From: "A <a@x.com>", Date: d, HTML: "<p>hi</p><script>x</script>"})
	assert.Contains(t, q, "2026/01/02 03:04")
	assert.Contains(t, q, "A &lt;a@x.com&gt;")
	assert.Contains(t, q, "<blockquote>")
	assert.Contains(t, q, "<p>hi</p>")
	assert.NotContains(t, q, "script")
	q = mail.QuoteHTML(&mail.Parsed{From: "a@x.com", Date: d, Text: "<b>\nline2"})
	assert.Contains(t, q, "&lt;b&gt;<br>")
	assert.Contains(t, mail.QuoteHTMLIn(&mail.Parsed{From: "a@x.com", Date: d, Text: "hi"}, "en"), "On 1/02/2026 03:04, a@x.com wrote:")
	assert.Contains(t, mail.QuoteHTMLIn(&mail.Parsed{From: "a@x.com", Date: d, Text: "hi"}, "en-GB"), "On 02/01/2026 03:04, a@x.com wrote:")
	assert.Contains(t, mail.QuoteHTMLIn(&mail.Parsed{From: "a@x.com", Date: d, Text: "hi"}, "zh-Hant"), "於 2026/01/02 03:04，a@x.com 寫道：")
	assert.Contains(t, mail.QuoteHTMLIn(&mail.Parsed{From: "a@x.com", Date: d, Text: "hi"}, "de"), "Am 02.01.2026 03:04 schrieb a@x.com:")
	assert.Contains(t, mail.QuoteHTMLIn(&mail.Parsed{From: "a@x.com", Date: d, Text: "hi"}, "ja"), "2026/01/02 03:04、a@x.com さんは書きました：")
}
