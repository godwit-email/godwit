package core_test

import (
	"context"
	"testing"

	"github.com/godwit-email/godwit/core"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

var ctx = context.Background()

func newTestService(t *testing.T) *core.Service {
	t.Helper()
	keyring.MockInit()
	s, err := core.New(ctx, core.Options{DataDir: t.TempDir(), AutoSync: false})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}
