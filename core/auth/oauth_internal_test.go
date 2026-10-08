package auth

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmailFromIDToken(t *testing.T) {
	tok := func(claims string) string {
		return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + "."
	}
	assert.Equal(t, "alice@example.com", emailFromIDToken(tok(`{"email":"Alice@Example.com","preferred_username":"x@y"}`)))
	assert.Equal(t, "bob@outlook.com", emailFromIDToken(tok(`{"preferred_username":"Bob@Outlook.com"}`)))
	assert.Equal(t, "bob@outlook.com", emailFromIDToken(tok(`{"email":"","preferred_username":"bob@outlook.com"}`)))
	assert.Equal(t, "padded@example.com", emailFromIDToken("h."+base64.URLEncoding.EncodeToString([]byte(`{"email":"padded@example.com"}`))+".s"))
	assert.Empty(t, emailFromIDToken(tok(`{}`)))
	assert.Empty(t, emailFromIDToken("not-a-jwt"))
	assert.Empty(t, emailFromIDToken("a.!!!.b"))
}
