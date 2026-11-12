package carddav

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVCardEdgeCases(t *testing.T) {
	raw := "BEGIN:VCARD\r\n" +
		"VERSION:3.0\r\n" +
		"FN;CHARSET=ISO-8859-1;ENCODING=QUOTED-PRINTABLE:Ren=E9=\r\n" +
		"e M=FCller\r\n" +
		"ORG:Acme\\; Global;R&D\r\n" +
		"TITLE:VP\\, Product\r\n" +
		"item1.EMAIL;type=INTERNET;type=WORK;type=pref:Renee@Example.com\r\n" +
		"item2.EMAIL:not-an-email\r\n" +
		"PHOTO;VALUE=URI:http://insecure.example.com/p.png\r\n" +
		"PHOTO;VALUE=URI:https\\://cdn.example.com/avatar.png\r\n" +
		"END:VCARD\r\n"

	c := ParseVCard(raw)
	assert.Equal(t, "Renée Müller", c.FN)
	assert.Equal(t, "Acme; Global R&D", c.Org)
	assert.Equal(t, "VP, Product", c.Title)
	require.Len(t, c.Emails, 1)
	assert.Equal(t, "renee@example.com", c.Emails[0].AddrKey)
	assert.Equal(t, "Renee@Example.com", c.Emails[0].Address)
	assert.True(t, c.Emails[0].Pref)
	require.Len(t, c.Photos, 1, "http:// PHOTO must be dropped")
	assert.Equal(t, "https://cdn.example.com/avatar.png", c.Photos[0].URL)
}
