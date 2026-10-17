package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/require"
)

func TestNativeModelsMethods(t *testing.T) {
	for _, tc := range []struct {
		name    string
		methods any
		caps    []string
		bad     bool
	}{
		{"models missing", nil, []string{"models"}, true},
		{"models no list", []string{"models.judge"}, []string{"models"}, true},
		{"models duplicate", []string{"models.list", "models.list"}, []string{"models"}, true},
		{"models invalid type", true, []string{"models"}, true},
		{"models valid", []string{"models.list", "models.prepare"}, []string{"models"}, false},
		{"old translator", nil, []string{"translate"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rp := &runningPlugin{}
			cfg := map[string]any{"key": "value"}
			rp.cfg.Store(&cfg)
			r := pluginRow{m: plugins.Manifest{Name: "test", Version: "1.0.0", Capabilities: tc.caps}}
			configured := false
			call := func(ctx context.Context, method string, req any) (json.RawMessage, error) {
				if method == "describe" {
					return json.Marshal(map[string]any{"name": "test", "version": "1.0.0", "abi": 1, "capabilities": tc.caps, "methods": tc.methods})
				}
				require.Equal(t, "configure", method)
				b, err := json.Marshal(req)
				require.NoError(t, err)
				require.JSONEq(t, `{"config":{"key":"value"}}`, string(b))
				configured = true
				return json.RawMessage(`{}`), nil
			}
			err := setUpNative(context.Background(), "test", r, rp, call)
			if tc.bad {
				require.ErrorIs(t, err, plugins.ErrNativeABI)
				require.False(t, configured)
			} else {
				require.NoError(t, err)
				require.True(t, configured)
			}
		})
	}
}
