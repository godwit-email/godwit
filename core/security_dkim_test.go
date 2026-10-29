package core

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/internal/testmail"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/require"
)

func TestSecurityDKIMFetchesAndChecksOriginalMessage(t *testing.T) {
	fixture := testmail.NewDKIMFixture(t)
	for _, tc := range []struct {
		name, from, domain string
		tamper             bool
		want               gomail.SecurityStatus
	}{
		{"valid signature", "Alice <alice@sender.example>", "sender.example", false, gomail.SecurityVerifiedDomain},
		{"tampered raw body", "Alice <alice@sender.example>", "sender.example", true, gomail.SecurityNeutral},
		{"forged brand signed by attacker", "PayPal <service@paypal.com>", "attacker.example", false, gomail.SecurityNeutral},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSyncEnv(t)
			enableSecurity(t, e.s)
			raw := fixture.Sign(t, tc.from, tc.domain)
			if tc.tamper {
				raw = bytes.Replace(raw, []byte("Original body."), []byte("Attack body."), 1)
			}
			raw = append([]byte("Received: from attacker.invalid by mx.receiver.test; Thu, 29 Oct 2026 00:00:00 +0000\r\nAuthentication-Results: mx.receiver.test; dmarc=pass header.from=paypal.com; bimi=pass\r\n"), raw...)
			e.s.securityLookupTXT = fixture.LookupTXT
			getter := e.s.securityRaw
			var downloads atomic.Int64
			e.s.securityRaw = func(ctx context.Context, id int64) ([]byte, error) { downloads.Add(1); return getter(ctx, id) }
			e.put(t, "INBOX", string(raw))
			e.sync(t)
			rows := e.rows(t, "SELECT id,security_verify_dkim FROM messages")
			require.Len(t, rows, 1)
			require.Equal(t, int64(1), rows[0][1])
			id := rows[0][0].(int64)
			// A changed cached preview must never substitute for signed original bytes.
			_, err := e.s.db.Execute(t.Context(), "UPDATE messages SET body_text='unrelated cached preview' WHERE id=?", id)
			require.NoError(t, err)
			_, err = e.s.scanSecurityBatch(t.Context())
			require.NoError(t, err)
			m, err := e.s.GetMessage(t.Context(), id, false)
			require.NoError(t, err)
			require.Equal(t, tc.want, m.Security.Status)
			require.GreaterOrEqual(t, downloads.Load(), int64(1))
			_, err = e.s.db.Execute(t.Context(), "UPDATE messages SET security_scanned_at=0 WHERE id=?", id)
			require.NoError(t, err)
			_, err = e.s.scanSecurityBatch(t.Context())
			require.NoError(t, err)
			require.GreaterOrEqual(t, downloads.Load(), int64(2), "an explicit rescan must verify the original again")
		})
	}
}

func TestSecurityDKIMDownloadFailureRemainsQueued(t *testing.T) {
	s := newLabelService(t, "", "de")
	enableSecurity(t, s)
	id := addMessage(t, s, "probe", "Alice <alice@sender.example>", "hi", "", time.Now().Unix())
	s.securityRaw = func(context.Context, int64) ([]byte, error) { return nil, context.DeadlineExceeded }
	_, err := s.db.Execute(t.Context(), "UPDATE messages SET security_verify_dkim=1,security_scanned_at=0 WHERE id=?", id)
	require.NoError(t, err)
	_, err = s.scanSecurityBatch(t.Context())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	n, err := s.PendingSecurity(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestSecurityDKIMStoppedScanCannotWriteVerdict(t *testing.T) {
	s := newLabelService(t, "", "de")
	enableSecurity(t, s)
	f := testmail.NewDKIMFixture(t)
	raw := f.Sign(t, "Alice <alice@sender.example>", "sender.example")
	id := addMessage(t, s, "probe", "Alice <alice@sender.example>", "hi", "", time.Now().Unix())
	s.securityLookupTXT = f.LookupTXT
	s.securityRaw = func(context.Context, int64) ([]byte, error) {
		require.NoError(t, s.StopSecurityRescan(t.Context()))
		return raw, nil
	}
	_, err := s.db.Execute(t.Context(), "UPDATE messages SET security_verify_dkim=1,security_scanned_at=0 WHERE id=?", id)
	require.NoError(t, err)
	_, err = s.scanSecurityBatch(t.Context())
	require.NoError(t, err)
	m, err := s.GetMessage(t.Context(), id, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityNeutral, m.Security.Status)
}

func TestSecurityDKIMTooLargeDoesNotRetryForever(t *testing.T) {
	s := newLabelService(t, "", "de")
	enableSecurity(t, s)
	id := addMessage(t, s, "probe", "alice@sender.example", "hi", "", time.Now().Unix())
	s.securityRaw = func(context.Context, int64) ([]byte, error) { return nil, ErrTooLarge }
	_, err := s.db.Execute(t.Context(), "UPDATE messages SET security_verify_dkim=1,security_scanned_at=0 WHERE id=?", id)
	require.NoError(t, err)
	_, err = s.scanSecurityBatch(t.Context())
	require.NoError(t, err)
	n, err := s.PendingSecurity(t.Context())
	require.NoError(t, err)
	require.Zero(t, n)
	m, err := s.GetMessage(t.Context(), id, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityNeutral, m.Security.Status)
}

func TestSecurityDKIMNeverDowngradesNativeWarning(t *testing.T) {
	s := newLabelService(t, "", "de")
	enableSecurity(t, s)
	id := addMessage(t, s, "probe", "Alice <alice@sender.example>", "hi", "", time.Now().Unix())
	s.securityRaw = func(context.Context, int64) ([]byte, error) {
		t.Fatal("native warning must not trigger a replacement DKIM verdict")
		return nil, nil
	}
	_, err := s.db.Execute(t.Context(), "UPDATE messages SET security_verify_dkim=1,security_scanned_at=0,auth_results='receiver.test; dmarc=fail header.from=sender.example' WHERE id=?", id)
	require.NoError(t, err)
	_, err = s.scanSecurityBatch(t.Context())
	require.NoError(t, err)
	m, err := s.GetMessage(t.Context(), id, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityWarning, m.Security.Status)
	require.Equal(t, gomail.WarnAuthFailed, m.Security.WarningKind)
}
