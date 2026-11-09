package conn

import "github.com/emersion/go-sasl"

type xoauth2 struct{ user, token string }

// NewXOAuth2Client returns a SASL XOAUTH2 client (Google and Microsoft IMAP/SMTP).
func NewXOAuth2Client(username, token string) sasl.Client { return xoauth2{username, token} }

func (a xoauth2) Start() (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + a.user + "\x01auth=Bearer " + a.token + "\x01\x01"), nil
}

// Next answers the only challenge XOAUTH2 has, the server's JSON error, with an
// empty response; the server then fails the exchange. Non-nil, so the SMTP client
// sends it rather than stopping.
func (xoauth2) Next([]byte) ([]byte, error) { return []byte{}, nil }
