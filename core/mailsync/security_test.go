package mailsync

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/require"
)

func TestAutomaticReceiverAuthenticationImport(t *testing.T) {
	for _, tc := range []struct{ name, headers, want string }{
		{"arbitrary provider without setup", "Authentication-Results: newest-service.test; dkim=pass header.d=company.example\r\nReceived: from sender.invalid by mx.receiver.test; Thu, 29 Oct 2026 00:00:00 +0000\r\n", "verified_domain"},
		{"receiver below Received requires proof", "Received: from sender.invalid by mx.receiver.test; Thu, 29 Oct 2026 00:00:00 +0000\r\nAuthentication-Results: mx.receiver.test; dkim=pass header.d=company.example\r\n", "neutral"},
		{"sender cannot validate itself", "Received: from sender.invalid by mx.receiver.test; Thu, 29 Oct 2026 00:00:00 +0000\r\nAuthentication-Results: attacker.invalid; dmarc=pass header.from=company.example\r\n", "neutral"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "mail.db"))
			require.NoError(t, err)
			defer db.Close()
			_, err = db.Execute(t.Context(), "INSERT INTO accounts(id,email,imap_host) VALUES (1,'me@customer.test','imap.receiver.test')")
			require.NoError(t, err)
			_, err = db.Execute(t.Context(), "INSERT INTO folders(id,account_id,name,role,uidvalidity) VALUES (1,1,'INBOX','inbox',1)")
			require.NoError(t, err)
			a, err := loadAccount(t.Context(), db, 1)
			require.NoError(t, err)
			m := &imapclient.FetchMessageBuffer{UID: 1, Envelope: &imap.Envelope{MessageID: "probe@company.example", Subject: "hello", Date: time.Now(), From: []imap.Address{{Name: "Alice", Mailbox: "alice", Host: "company.example"}}}, BodySection: []imapclient.FetchBodySectionBuffer{{Section: headerSection(), Bytes: []byte(tc.headers + "From: Alice <alice@company.example>\r\nSubject: hello\r\n\r\n")}}}
			err = importMessage(t.Context(), db, a, &folder{id: 1, name: "INBOX", role: "inbox", uidValidity: 1}, m, nil, 1, true, &result{})
			require.NoError(t, err)
			rows, err := db.Query(t.Context(), "SELECT security_status,auth_results FROM messages")
			require.NoError(t, err)
			require.Len(t, rows.Values, 1)
			require.Equal(t, tc.want, store.Str(rows.Values[0][0]))
			require.NotNil(t, rows.Values[0][1])
		})
	}
}

func TestAutomaticReceiverHostDoesNotProveHeaderOrigin(t *testing.T) {
	p := toParsed(&imapclient.FetchMessageBuffer{BodySection: []imapclient.FetchBodySectionBuffer{{Section: headerSection(), Bytes: []byte("Received: from attacker.invalid by mx.independent-provider.test; Thu, 29 Oct 2026 00:00:00 +0000\r\nAuthentication-Results: mx.independent-provider.test; dkim=pass header.d=company.example\r\n\r\n")}}}, nil)
	require.Empty(t, mail.ReceiverAuthenticationResults(p.AuthenticationTrace, "mx.independent-provider.test"))
}
