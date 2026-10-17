package contract_test

import (
	"github.com/godwit-email/godwit/contract"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPluginModelsContract(t *testing.T) {
	runContract(t, func(t *testing.T, api contract.API, env *coretest.Env) any {
		ctx := t.Context()
		_, err := env.Svc.AddModelService(ctx, core.ModelServiceInput{Name: "service", Kind: "openai", BaseURL: "https://example.com/v1", Models: []string{"m"}, JudgeFormat: "systemone", JudgeModel: "j"})
		require.NoError(t, err)
		catalog, err := api.ListModels(ctx)
		require.NoError(t, err)
		require.Len(t, catalog, 2)
		refreshed, err := api.RefreshModels(ctx)
		require.NoError(t, err)
		require.Equal(t, catalog, refreshed)
		status, err := api.PrepareModel(ctx, "service/j")
		require.NoError(t, err)
		require.Equal(t, core.ModelReady, status.State)
		return catalog
	})
}
