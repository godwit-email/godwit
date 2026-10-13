package avatar

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// GravatarURL hashes the complete normalized address, retaining dots and tags.
func GravatarURL(email string) string {
	hash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "https://gravatar.com/avatar/" + hex.EncodeToString(hash[:]) + "?s=64&d=404&r=g"
}
