package rpc_test

import (
	"github.com/godwit-email/godwit/rpc"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestModelMethodsRequireFull(t *testing.T) {
	for _, method := range []string{"ListModels", "RefreshModels", "PrepareModel", "RemoveModelFiles"} {
		require.False(t, rpc.MCPAllowed(method, nil), method)
	}
}
