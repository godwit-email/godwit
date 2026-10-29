package core

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/stretchr/testify/require"
)

func enableSecurity(t *testing.T, s *Service) {
	t.Helper()
	st, err := s.loadSettings(t.Context())
	require.NoError(t, err)
	st.SecurityAI = true
	_, err = s.SetSettings(t.Context(), st)
	require.NoError(t, err)
	s.securityJudge = func(context.Context) (ai.Judge, error) {
		return &fakeJudge{p: map[string]float64{}}, nil
	}
}

func TestSecurityRescanAndBatch(t *testing.T) {
	ctx := t.Context()
	s := newLabelService(t, "", "de")

	st, err := s.loadSettings(ctx)
	require.NoError(t, err)
	st.SecurityAI = true
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)

	now := time.Now().Unix()
	// Insert 3 test messages:
	// 1. Spoofing PayPal
	// 2. Legit organization (sparkasse.de with DMARC pass)
	// 3. Freemail Yahoo (Nigerian scam content)
	id1 := addMessage(t, s, "Account Alert", "\"PayPal\" <phish@attacker.com>", "Please login to verify your account", "", now-100)
	id2 := addMessage(t, s, "Kontoauszug", "\"Sparkasse\" <service@sparkasse.de>", "Ihr Kontoauszug liegt vor.", "", now-200)
	id3 := addMessage(t, s, "Erbschaft", "\"Richard M. Kenny\" <richardalakpe277@yahoo.com>", "Ich bitte Sie um Ihre Unterstützung bei der Rückforderung von 18,7 Millionen US-Dollar.", "", now-300)

	// Set auth_results for Sparkasse (DMARC pass) and Yahoo (DKIM pass on freemail)
	_, err = s.db.Execute(ctx, "UPDATE messages SET auth_results = 'mx.example.com; dmarc=pass header.from=sparkasse.de' WHERE id = ?", id2)
	require.NoError(t, err)
	_, err = s.db.Execute(ctx, "UPDATE messages SET auth_results = 'mx.example.com; dkim=pass header.i=@yahoo.com' WHERE id = ?", id3)
	require.NoError(t, err)

	// Initially, messages are unscanned (security_scanned_at IS NULL)
	p, err := s.PendingSecurity(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, p)

	// Trigger Rescan for all messages
	queued, err := s.RescanSecurity(ctx, 0, true)
	require.NoError(t, err)
	require.Equal(t, 3, queued)

	p, err = s.PendingSecurity(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, p)

	// Configure mock AI judge that flags "phishing_fraud" for the Yahoo email
	prob := 0.95
	s.securityJudge = func(context.Context) (ai.Judge, error) {
		return &fakeJudge{p: map[string]float64{"phishing_fraud": prob}}, nil
	}

	// Run scan batch
	n, err := s.scanSecurityBatch(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, n)

	p, err = s.PendingSecurity(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, p)

	// Verify results in database:
	rows, err := s.db.Query(ctx, "SELECT id, security_status, security_reason FROM messages WHERE id IN (?, ?, ?) ORDER BY id", id1, id2, id3)
	require.NoError(t, err)
	require.Len(t, rows.Values, 3)

	// Message id1: PayPal spoofing -> warning (spoofing)
	require.Equal(t, id1, store.Int(rows.Values[0][0]))
	require.Equal(t, "warning", store.Str(rows.Values[0][1]))
	require.Equal(t, "spoofing", store.Str(rows.Values[0][2]))

	// Message id2: Sparkasse DMARC pass -> verified_domain (dmarc_pass)
	require.Equal(t, id2, store.Int(rows.Values[1][0]))
	require.Equal(t, "verified_domain", store.Str(rows.Values[1][1]))
	require.Equal(t, "dmarc_pass", store.Str(rows.Values[1][2]))

	// Message id3: Yahoo Nigerian scam -> Step 1 Freemail neutral, Step 2 AI flagged -> warning (ai_fraud)
	require.Equal(t, id3, store.Int(rows.Values[2][0]))
	require.Equal(t, "warning", store.Str(rows.Values[2][1]))
	require.Equal(t, "ai_fraud", store.Str(rows.Values[2][2]))

	// Now check MessageSummary & GetMessage:
	msg3, err := s.GetMessage(ctx, id3, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityWarning, msg3.Security.Status)
	require.Equal(t, gomail.WarnAIFraud, msg3.Security.WarningKind)

	msg2, err := s.GetMessage(ctx, id2, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityVerifiedDomain, msg2.Security.Status)

	msg1, err := s.GetMessage(ctx, id1, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityWarning, msg1.Security.Status)
	require.Equal(t, gomail.WarnDisplaySpoof, msg1.Security.WarningKind)
}

func TestSecurityRescanWithAIOff(t *testing.T) {
	ctx := t.Context()
	s := newLabelService(t, "", "de")

	// Set SecurityAI = false
	st, err := s.loadSettings(ctx)
	require.NoError(t, err)
	st.SecurityAI = false
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)

	now := time.Now().Unix()
	id := addMessage(t, s, "Erbschaft", "\"Richard M. Kenny\" <richardalakpe277@yahoo.com>", "Erbschaft 18.7M", "", now-100)
	_, err = s.db.Execute(ctx, "UPDATE messages SET auth_results = 'mx.example.com; dkim=pass header.i=@yahoo.com' WHERE id = ?", id)
	require.NoError(t, err)

	_, err = s.RescanSecurity(ctx, 0, true)
	require.ErrorIs(t, err, ErrSecurityScanDisabled)

	n, err := s.scanSecurityBatch(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, n)

	// Now turn SecurityAI ON:
	st.SecurityAI = true
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)
	s.securityJudge = func(context.Context) (ai.Judge, error) {
		return &fakeJudge{p: map[string]float64{}}, nil
	}

	queued, err := s.RescanSecurity(ctx, 0, true)
	require.NoError(t, err)
	require.Equal(t, 1, queued)

	n, err = s.scanSecurityBatch(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestSecurityAINeverDowngradesNativeWarning(t *testing.T) {
	ctx := t.Context()
	s := newLabelService(t, "", "de")

	st, err := s.loadSettings(ctx)
	require.NoError(t, err)
	st.SecurityAI = true
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)

	now := time.Now().Unix()
	// Native warning: Display spoofing + DMARC fail
	id := addMessage(t, s, "Urgent Notice", "\"Acme Support\" <support@evil.invalid>", "Please confirm password", "", now-100)
	_, err = s.db.Execute(ctx, "UPDATE messages SET auth_results = 'mx.example.com; dmarc=fail (p=reject) header.from=evil.invalid' WHERE id = ?", id)
	require.NoError(t, err)

	// Mock AI judge says: perfectly clean (0.0 fraud probability)
	s.securityJudge = func(context.Context) (ai.Judge, error) {
		return &fakeJudge{p: map[string]float64{"phishing_fraud": 0.0}}, nil
	}

	_, err = s.RescanSecurity(ctx, 0, true)
	require.NoError(t, err)

	n, err := s.scanSecurityBatch(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// The native warning MUST NOT have been overwritten by AI clean
	msg, err := s.GetMessage(ctx, id, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityWarning, msg.Security.Status)
	require.Equal(t, gomail.WarnAuthFailed, msg.Security.WarningKind)
}

func TestSecurityAIFailureLeavesInQueue(t *testing.T) {
	ctx := t.Context()
	s := newLabelService(t, "", "de")

	st, err := s.loadSettings(ctx)
	require.NoError(t, err)
	st.SecurityAI = true
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)

	now := time.Now().Unix()
	id := addMessage(t, s, "Neutral Message", "\"Alice\" <alice@example.com>", "Hello world", "", now-100)

	// Mock AI judge that fails (e.g. timeout / network error)
	s.securityJudge = func(context.Context) (ai.Judge, error) {
		return &errJudge{err: context.DeadlineExceeded}, nil
	}

	_, err = s.RescanSecurity(ctx, 0, true)
	require.NoError(t, err)

	p, err := s.PendingSecurity(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, p)

	// Batch scan should NOT mark this failed message as scanned
	n, err := s.scanSecurityBatch(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 0, n) // 0 statements executed because it continued without updating

	p, err = s.PendingSecurity(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, p) // Still queued for retry!

	row, err := s.db.Query(ctx, "SELECT security_scanned_at FROM messages WHERE id = ?", id)
	require.NoError(t, err)
	require.Equal(t, int64(0), store.Int(row.Values[0][0]))
}

type errJudge struct {
	err error
}

func (j *errJudge) Ask(ctx context.Context, state string, questions []ai.Question) ([]ai.Answer, error) {
	return nil, j.err
}

func TestSecurityRescanDoesNotHideExistingWarnings(t *testing.T) {
	ctx := t.Context()
	s := newLabelService(t, "", "de")
	enableSecurity(t, s)

	now := time.Now().Unix()
	id := addMessage(t, s, "PayPal Alert", "\"PayPal\" <phish@attacker.com>", "Action required", "", now-100)

	_, err := s.RescanSecurity(ctx, 0, true)
	require.NoError(t, err)

	n, err := s.scanSecurityBatch(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	msg, err := s.GetMessage(ctx, id, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityWarning, msg.Security.Status)

	// Trigger Rescan again: sets security_scanned_at = 0
	_, err = s.RescanSecurity(ctx, 0, true)
	require.NoError(t, err)

	// Warning must STILL be visible while rescan is pending!
	msg, err = s.GetMessage(ctx, id, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityWarning, msg.Security.Status)

	// Stop rescan: sets security_scanned_at = NULL
	err = s.StopSecurityRescan(ctx)
	require.NoError(t, err)

	// Warning must STILL be visible after stopping rescan!
	msg, err = s.GetMessage(ctx, id, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityWarning, msg.Security.Status)
}

func TestSecurityOwnAddressHandling(t *testing.T) {
	ctx := t.Context()
	s := newLabelService(t, "", "de")
	enableSecurity(t, s)

	// Update existing account to me@mycompany.com
	_, err := s.db.Execute(ctx, "UPDATE accounts SET email = 'me@mycompany.com' WHERE id = 1")
	require.NoError(t, err)

	now := time.Now().Unix()
	// An incoming message in INBOX spoofing the user's own address with auth failure
	idSpoof := addMessage(t, s, "Your account is locked", "\"Security Team\" <me@mycompany.com>", "Confirm password", "", now-100)
	_, err = s.db.Execute(ctx, "UPDATE messages SET auth_results = 'mx.mycompany.com; dmarc=fail (p=reject) header.from=mycompany.com' WHERE id = ?", idSpoof)
	require.NoError(t, err)

	_, err = s.RescanSecurity(ctx, 0, true)
	require.NoError(t, err)

	n, err := s.scanSecurityBatch(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// The incoming spoof of own email MUST trigger a warning!
	msg, err := s.GetMessage(ctx, idSpoof, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityWarning, msg.Security.Status)
	require.Equal(t, gomail.WarnAuthFailed, msg.Security.WarningKind)
}

type mockSecurityJudge struct {
	fn func(state string) float64
}

func (m *mockSecurityJudge) Ask(_ context.Context, state string, qs []ai.Question) ([]ai.Answer, error) {
	p := m.fn(state)
	return []ai.Answer{{Type: ai.Predicate, Name: qs[0].Name, Probability: &p}}, nil
}

func TestSecurityFiveStatesScanner(t *testing.T) {
	ctx := t.Context()
	s := newLabelService(t, "", "de")
	st, err := s.loadSettings(ctx)
	require.NoError(t, err)
	st.SecurityAI = true
	_, err = s.SetSettings(ctx, st)
	require.NoError(t, err)

	now := time.Now().Unix()
	// 1. AFFiNE login code on authenticated domain -> Grün (SecurityVerifiedDomain)
	idAffine := addMessage(t, s, "Sign in to AFFiNE • 709997", "AFFiNE Team <noreply@toeverything.info>", "Here is your 6-digit login code: 709997", "", now-10)
	_, err = s.db.Execute(ctx, "UPDATE messages SET auth_results = 'mx.example.com; dmarc=pass header.from=toeverything.info; dkim=pass header.d=toeverything.info' WHERE id = ?", idAffine)
	require.NoError(t, err)

	// 2. Confirmed domain with unconfirmed discrepancy (prob 0.60: 50% - 74%) -> Gelb (SecurityNeedsReview / ai_suspicious)
	idSuspicious := addMessage(t, s, "Important Project Update", "Partner <colleague@partner-company.com>", "Please review the updated agreement on the external server.", "", now-20)
	_, err = s.db.Execute(ctx, "UPDATE messages SET auth_results = 'mx.example.com; dmarc=pass header.from=partner-company.com; dkim=pass header.d=partner-company.com' WHERE id = ?", idSuspicious)
	require.NoError(t, err)

	// 3. Sparkasse imitation with signed attacker domain -> Rot (SecurityFraud / spoofing)
	idSparkasse := addMessage(t, s, "Konto gesperrt: Sofort handeln", "Sparkasse Team <service@attacker-control-center.de>", "Ihr Konto wurde gesperrt. Klicken Sie hier zur Entsperrung.", "", now-30)
	_, err = s.db.Execute(ctx, "UPDATE messages SET auth_results = 'mx.example.com; dmarc=pass header.from=attacker-control-center.de; dkim=pass header.d=attacker-control-center.de' WHERE id = ?", idSparkasse)
	require.NoError(t, err)

	// 4. Unauthenticated scam email (prob 0.85: >= 75%) -> Rot (SecurityFraud / ai_fraud)
	idScam := addMessage(t, s, "Erbschaftsmitteilung", "Richard Alakpe <richardalakpe277@yahoo.com>", "Ich kontaktiere Sie bezüglich 18,7 Millionen US-Dollar.", "", now-35)
	require.NoError(t, err)

	// 5. Private mail without auth headers (prob 0.10: < 50%) -> Grau (SecurityNeutral / ai_clean)
	idPrivate := addMessage(t, s, "Treffen am Wochenende", "Klaus Schmidt <klaus.schmidt@privat-mail.de>", "Hallo, wollen wir uns am Samstag treffen?", "", now-40)
	require.NoError(t, err)

	s.securityJudge = func(context.Context) (ai.Judge, error) {
		return &mockSecurityJudge{
			fn: func(state string) float64 {
				switch {
				case strings.Contains(state, "AFFiNE"):
					return 0.85 // >= 0.75, but routine auth on authenticated domain -> stays verified
				case strings.Contains(state, "Important Project Update"):
					return 0.60 // 50% - 74% -> Gelb (SecurityNeedsReview)
				case strings.Contains(state, "Sparkasse"):
					return 0.90 // >= 75% -> Rot (SecurityFraud)
				case strings.Contains(state, "Erbschaft"):
					return 0.85 // >= 75% -> Rot (SecurityFraud)
				default:
					return 0.10 // < 50% -> Grau (SecurityNeutral / ai_clean)
				}
			},
		}, nil
	}

	_, err = s.RescanSecurity(ctx, 0, true)
	require.NoError(t, err)

	n, err := s.scanSecurityBatch(ctx)
	require.NoError(t, err)
	require.Equal(t, 5, n)

	// 1. AFFiNE remains Grün (SecurityVerifiedDomain)
	msgAffine, err := s.GetMessage(ctx, idAffine, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityVerifiedDomain, msgAffine.Security.Status)

	// 2. Confirmed domain with discrepancy becomes Gelb (SecurityNeedsReview)
	msgSuspicious, err := s.GetMessage(ctx, idSuspicious, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityNeedsReview, msgSuspicious.Security.Status)
	require.Equal(t, gomail.WarnSuspicious, msgSuspicious.Security.WarningKind)

	// 3. Sparkasse imitation becomes Rot (SecurityFraud)
	msgSparkasse, err := s.GetMessage(ctx, idSparkasse, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityFraud, msgSparkasse.Security.Status)

	// 4. Unauthenticated scam email becomes Rot (SecurityFraud)
	msgScam, err := s.GetMessage(ctx, idScam, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityFraud, msgScam.Security.Status)
	require.Equal(t, gomail.WarnAIFraud, msgScam.Security.WarningKind)

	// 5. Private mail becomes Grau (SecurityNeutral)
	msgPrivate, err := s.GetMessage(ctx, idPrivate, false)
	require.NoError(t, err)
	require.Equal(t, gomail.SecurityNeutral, msgPrivate.Security.Status)
	require.Equal(t, gomail.WarnNone, msgPrivate.Security.WarningKind)

	// Verify exact reason strings in database
	row, err := s.db.Query(ctx, "SELECT security_reason FROM messages WHERE id = ?", idPrivate)
	require.NoError(t, err)
	require.Equal(t, "ai_clean", store.Str(row.Values[0][0]))

	row, err = s.db.Query(ctx, "SELECT security_reason FROM messages WHERE id = ?", idSuspicious)
	require.NoError(t, err)
	require.Equal(t, "ai_suspicious", store.Str(row.Values[0][0]))

	row, err = s.db.Query(ctx, "SELECT security_reason FROM messages WHERE id = ?", idScam)
	require.NoError(t, err)
	require.Equal(t, "ai_fraud", store.Str(row.Values[0][0]))
}
