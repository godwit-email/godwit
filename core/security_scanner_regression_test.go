package core

import (
	"context"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/stretchr/testify/require"
)

func queuedSecurityMessage(t *testing.T) (*Service, int64) {
	t.Helper()
	s := newLabelService(t, "", "de")
	st, err := s.loadSettings(t.Context())
	require.NoError(t, err)
	st.SecurityAI = true
	_, err = s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	id := addMessage(t, s, "Probe", "Alice <alice@yahoo.com>", "hi", "", time.Now().Unix())
	_, err = s.db.Execute(t.Context(), "UPDATE messages SET security_scanned_at=0 WHERE id=?", id)
	require.NoError(t, err)
	return s, id
}
func TestSecurityModelResolutionFailureRetries(t *testing.T) {
	s, _ := queuedSecurityMessage(t)
	s.securityJudge = func(context.Context) (ai.Judge, error) { return nil, context.DeadlineExceeded }
	_, err := s.scanSecurityBatch(t.Context())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	pending, err := s.PendingSecurity(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, pending)
}
func TestSecurityRefusalRemainsUnscanned(t *testing.T) {
	s, _ := queuedSecurityMessage(t)
	s.securityJudge = func(context.Context) (ai.Judge, error) {
		return &fakeJudge{refused: map[string]bool{"phishing_fraud": true}}, nil
	}
	_, err := s.scanSecurityBatch(t.Context())
	require.ErrorIs(t, err, ai.ErrJudgeAnswer)
	pending, err := s.PendingSecurity(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, pending)
}
func TestSecurityStopProtectsInflightVerdict(t *testing.T) {
	s, id := queuedSecurityMessage(t)
	j := &fakeJudge{p: map[string]float64{"phishing_fraud": 1}, during: func() { require.NoError(t, s.StopSecurityRescan(t.Context())) }}
	s.securityJudge = func(context.Context) (ai.Judge, error) { return j, nil }
	_, err := s.scanSecurityBatch(t.Context())
	require.NoError(t, err)
	m, err := s.GetMessage(t.Context(), id, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityNeutral, m.Security.Status)
}

func TestSecuritySettingsChangeProtectsInflightVerdict(t *testing.T) {
	s, id := queuedSecurityMessage(t)
	j := &fakeJudge{p: map[string]float64{"phishing_fraud": 1}, during: func() {
		st, err := s.loadSettings(t.Context())
		require.NoError(t, err)
		st.SecurityAI = false
		_, err = s.SetSettings(t.Context(), st)
		require.NoError(t, err)
	}}
	s.securityJudge = func(context.Context) (ai.Judge, error) { return j, nil }
	_, err := s.scanSecurityBatch(t.Context())
	require.NoError(t, err)
	m, err := s.GetMessage(t.Context(), id, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityNeutral, m.Security.Status)
	n, err := s.PendingSecurity(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, n)
}
func TestSecurityOwnFormattedAddressSkipsOnlyAI(t *testing.T) {
	s, id := queuedSecurityMessage(t)
	_, err := s.db.Execute(t.Context(), "UPDATE messages SET from_addr='My Name <me@x>' WHERE id=?", id)
	require.NoError(t, err)
	j := &fakeJudge{p: map[string]float64{"phishing_fraud": 1}}
	s.securityJudge = func(context.Context) (ai.Judge, error) { return j, nil }
	_, err = s.scanSecurityBatch(t.Context())
	require.NoError(t, err)
	require.Zero(t, j.count())
	m, err := s.GetMessage(t.Context(), id, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityNeutral, m.Security.Status)
}

// A quoted receiver DKIM result must prevent a false-positive content verdict,
// including when an older ai_fraud verdict is explicitly rescanned.
func TestSecurityQuotedDomainSkipsAI(t *testing.T) {
	for _, header := range []string{
		`mx.receiver.test; dkim=pass header.d="\"malt.com\""`,
		`strato.com; =none smtp.remote-ip=192.0.2.1; dkim=pass header.d="\"malt.com\""`,
		`strato.com; dkim=pass header.d="\"malt.com\""; =pass smtp.remote-ip=192.0.2.1`,
	} {
		t.Run(header, func(t *testing.T) {
			s, id := queuedSecurityMessage(t)
			_, err := s.db.Execute(t.Context(), `UPDATE messages SET subject=?, from_addr=?, auth_results=?, security_status='warning', security_reason='ai_fraud' WHERE id=?`,
				"Onboarding: Welcome to Malt", "Malt <hello@malt.com>", header, id)
			require.NoError(t, err)
			j := &fakeJudge{p: map[string]float64{"phishing_fraud": 1}}
			s.securityJudge = func(context.Context) (ai.Judge, error) { return j, nil }
			_, err = s.scanSecurityBatch(t.Context())
			require.NoError(t, err)
			m, err := s.GetMessage(t.Context(), id, false)
			require.NoError(t, err)
			require.Equal(t, gomail.SecurityVerifiedDomain, m.Security.Status)
		})
	}
}
