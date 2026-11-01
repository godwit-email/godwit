package auth

import (
	"encoding/json"
	"errors"
)

var (
	// ErrNotClientFile: the file is not a Google OAuth client file.
	ErrNotClientFile = errors.New("oauth: not an OAuth client file")
	// ErrWebClient: the file is a Web application client's, which cannot take
	// the sign-in on a loopback address as a Desktop app client can.
	ErrWebClient = errors.New("oauth: a web application client, not a desktop app one")
)

// ParseClientFile reads the client_secret_….json Google Cloud offers for an
// OAuth client: a Desktop app client's ID and secret. Errors carry nothing
// from the file, whose secret is a credential.
func ParseClientFile(data []byte) (id, secret string, err error) {
	type client struct {
		ID     string `json:"client_id"`
		Secret string `json:"client_secret"`
	}
	var f struct{ Installed, Web *client }
	if json.Unmarshal(data, &f) != nil {
		return "", "", ErrNotClientFile
	}
	switch {
	case f.Installed != nil && f.Installed.ID != "" && f.Installed.Secret != "":
		return f.Installed.ID, f.Installed.Secret, nil
	case f.Web != nil:
		return "", "", ErrWebClient
	}
	return "", "", ErrNotClientFile
}
