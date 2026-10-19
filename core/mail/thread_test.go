package mail_test

import (
	"testing"

	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
)

func TestThreadKeys(t *testing.T) {
	ids, key := mail.ThreadKeys(&mail.Parsed{
		MessageID: "c@x", InReplyTo: "b@x", References: []string{"a@x", "b@x", ""},
		Subject: "  Re: 回复：FWD:  Quarterly\t Report ",
	})
	assert.Equal(t, []string{"c@x", "b@x", "a@x"}, ids)
	assert.Equal(t, "quarterly report", key)

	for _, s := range []string{"Re: 同一主题", "回复: 同一主题", "转发：同一主题", "回覆: 同一主题", "轉寄：同一主题", "AW: WG: 同一主题", "TR: RV: 同一主题", "답장: 전달: 同一主题", "Fw: Re[2]: 同一主题", "同一主题"} {
		_, k := mail.ThreadKeys(&mail.Parsed{Subject: s})
		assert.Equal(t, "同一主题", k, s)
	}
	ids, key = mail.ThreadKeys(&mail.Parsed{Subject: "Re:"})
	assert.Empty(t, ids)
	assert.Empty(t, key)
	_, key = mail.ThreadKeys(&mail.Parsed{Subject: "Report: Q3"})
	assert.Equal(t, "report: q3", key)
}
