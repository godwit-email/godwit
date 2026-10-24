package mail_test

import (
	"testing"

	"github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMailto(t *testing.T) {
	for link, want := range map[string]mail.Mailto{
		"mailto:a@x.com":                                                      {To: []string{"a@x.com"}},
		"MAILTO:a@x.com,%20b@x.com":                                           {To: []string{"a@x.com", "b@x.com"}},
		"mailto:a+tag@x.com?to=b@x.com":                                       {To: []string{"a+tag@x.com", "b@x.com"}},
		"mailto:?to=a@x.com&cc=c@x.com,d@x.com&bcc=e@x.com":                   {To: []string{"a@x.com"}, Cc: []string{"c@x.com", "d@x.com"}, Bcc: []string{"e@x.com"}},
		"mailto:a@x.com?subject=Hi+there%21&body=line%201%0D%0Aline%202":      {To: []string{"a@x.com"}, Subject: "Hi+there!", Body: "line 1\nline 2"},
		"mailto:a@x.com?Subject=%E9%80%80%E8%AE%A2&in-reply-to=%3Cm@x%3E&x=1": {To: []string{"a@x.com"}, Subject: "退订"},
		"mailto:a@x.com?subject=a%0D%0Ab":                                     {To: []string{"a@x.com"}, Subject: "a b"},
	} {
		got, err := mail.ParseMailto(link)
		require.NoError(t, err, link)
		assert.Equal(t, want, got, link)
	}
	for _, bad := range []string{"https://x.com", "mailto:a@x.com?subject=%zz", "a@x.com"} {
		_, err := mail.ParseMailto(bad)
		assert.Error(t, err, bad)
	}
}

func TestParseListHeaders(t *testing.T) {
	p, err := mail.Parse([]byte("List-Unsubscribe: <mailto:u@x.com>,\r\n <https://x.com/u>\r\nList-Unsubscribe-Post:  List-Unsubscribe=One-Click \r\n" +
		"List-Id: News <news.x.com>\r\n\r\nbody\r\n"))
	require.NoError(t, err)
	assert.Equal(t, "<mailto:u@x.com>, <https://x.com/u>", p.ListUnsubscribe)
	assert.Equal(t, "List-Unsubscribe=One-Click", p.ListUnsubscribePost)
	assert.Equal(t, "News <news.x.com>", p.ListID)
}
