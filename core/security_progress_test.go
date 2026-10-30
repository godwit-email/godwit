package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/stretchr/testify/require"
)

func TestSecurityProgressFixedScope(t *testing.T) {
	s := newLabelService(t, "", "de")
	enableSecurity(t, s)
	now := time.Now()
	old := addMessage(t, s, "Old", "alice@yahoo.com", "hi", "", now.AddDate(0, 0, -60).Unix())
	first := addMessage(t, s, "Recent 1", "alice@yahoo.com", "hi", "", now.Unix())
	addMessage(t, s, "Recent 2", "alice@yahoo.com", "hi", "", now.Unix())
	total, err := s.RescanSecurity(t.Context(), 30, true)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	// Simulate an unrelated legacy queue and mail arriving after the run starts.
	_, err = s.db.Execute(t.Context(), "UPDATE messages SET security_scanned_at=0 WHERE id=?", old)
	require.NoError(t, err)
	newer := addMessage(t, s, "New arrival", "alice@yahoo.com", "hi", "", now.Unix())
	_, err = s.db.Execute(t.Context(), "UPDATE messages SET security_scanned_at=0 WHERE id=?", newer)
	require.NoError(t, err)
	global, err := s.PendingSecurity(t.Context())
	require.NoError(t, err)
	require.Equal(t, 4, global)
	progress, err := s.SecurityProgress(t.Context())
	require.NoError(t, err)
	require.Equal(t, 2, progress.Total)
	require.Equal(t, 2, progress.Pending)
	require.True(t, progress.Busy)
	_, err = s.db.Execute(t.Context(), "UPDATE messages SET security_scanned_at=? WHERE id=?", now.Unix(), first)
	require.NoError(t, err)
	progress, err = s.SecurityProgress(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, progress.Total-progress.Pending)
	require.NoError(t, s.StopSecurityRescan(t.Context()))
	progress, err = s.SecurityProgress(t.Context())
	require.NoError(t, err)
	require.Zero(t, progress.Pending)
	require.False(t, progress.Busy)
}

func TestSecurityRescanAdmission(t *testing.T) {
	s := newLabelService(t, "", "de")
	enableSecurity(t, s)
	addMessage(t, s, "Message", "alice@yahoo.com", "hi", "", time.Now().Unix())
	s.securityMu.Lock()
	s.securitySyncs = 1
	s.securityMu.Unlock()
	_, err := s.RescanSecurity(t.Context(), 30, true)
	require.ErrorIs(t, err, ErrSecurityScanBusy)
	progress, err := s.SecurityProgress(t.Context())
	require.NoError(t, err)
	require.True(t, progress.Importing)
	s.securityMu.Lock()
	s.securitySyncs = 0
	s.securityMu.Unlock()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := s.RescanSecurity(t.Context(), 30, true); results <- err })
	}
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else {
			require.ErrorIs(t, err, ErrSecurityScanBusy)
			rejected++
		}
	}
	require.Equal(t, 1, accepted)
	require.Equal(t, 1, rejected)
}

func TestSecuritySingleWorkerAndIncrementalProgress(t *testing.T) {
	s, _ := queuedSecurityMessage(t)
	addMessage(t, s, "Second", "alice@yahoo.com", "hi", "", time.Now().Unix())
	_, err := s.db.Execute(t.Context(), "UPDATE messages SET security_scanned_at=0")
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	j := &fakeJudge{during: func() { close(entered); <-release }}
	s.securityJudge = func(context.Context) (ai.Judge, error) { return j, nil }
	done := make(chan error, 1)
	go func() { _, err := s.scanSecurityBatch(t.Context()); done <- err }()
	<-entered
	_, err = s.scanSecurityBatch(t.Context())
	require.ErrorIs(t, err, ErrSecurityScanBusy)
	_, err = s.RescanSecurity(t.Context(), 30, true)
	require.ErrorIs(t, err, ErrSecurityScanBusy)
	require.NoError(t, s.StopSecurityRescan(t.Context()))
	// Stop cannot admit a new run while the old model invocation is in flight.
	_, err = s.RescanSecurity(t.Context(), 30, true)
	require.ErrorIs(t, err, ErrSecurityScanBusy)
	unblock()
	require.NoError(t, <-done)
	require.Equal(t, 1, j.count(), "stopped batch must not scan its remaining messages")
	_, err = s.RescanSecurity(t.Context(), 30, true)
	require.NoError(t, err)
}

// The first result must be visible while the second model call is still blocked.
func TestSecurityProgressPublishedPerMessage(t *testing.T) {
	s, _ := queuedSecurityMessage(t)
	addMessage(t, s, "Second", "alice@yahoo.com", "hi", "", time.Now().Unix())
	require.NoError(t, s.StopSecurityRescan(t.Context()))
	_, err := s.RescanSecurity(t.Context(), 30, true)
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	j := &securityProgressJudge{entered: entered, release: release}
	s.securityJudge = func(context.Context) (ai.Judge, error) { return j, nil }
	done := make(chan error, 1)
	go func() { _, err := s.scanSecurityBatch(t.Context()); done <- err }()
	<-entered
	progress, err := s.SecurityProgress(t.Context())
	require.NoError(t, err)
	require.Equal(t, 2, progress.Total)
	require.Equal(t, 1, progress.Pending)
	unblock()
	require.NoError(t, <-done)
	progress, err = s.SecurityProgress(t.Context())
	require.NoError(t, err)
	require.Zero(t, progress.Pending)
	require.False(t, progress.Busy)
}

type securityProgressJudge struct {
	entered, release chan struct{}
	calls            int
}

func (j *securityProgressJudge) Ask(ctx context.Context, _ string, qs []ai.Question) ([]ai.Answer, error) {
	j.calls++
	if j.calls == 2 {
		close(j.entered)
		select {
		case <-j.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p := 0.0
	return []ai.Answer{{Name: qs[0].Name, Type: ai.Predicate, Probability: &p}}, nil
}
