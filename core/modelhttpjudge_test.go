package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/plugins"
	"github.com/stretchr/testify/require"
)

func testHTTPJudge(t *testing.T, prepare string, handler http.HandlerFunc) (*Service, ai.Judge) {
	t.Helper()
	e := newApprovalEnv(t)
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	e.s.pluginNet = plugins.NetOptions{Resolver: modelIntegrationDNS{}, AllowLoopback: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	startTestPlugin(t, e.s, "httpjudge", `{"net":["example.com"]},"capabilities":["models"]`, fmt.Sprintf(`export default function(ctx){ctx.models.register({listModels:()=>({models:[{id:"j",name:"J",transport:"http",protocol:"typesafe-systemone",baseURL:"https://example.com/v1"}]}),prepareRequest(call){%s}})}`, prepare))
	e.s.running["httpjudge"].modelNet.Transport().TLSClientConfig.InsecureSkipVerify = true // pinned to loopback
	rows, err := e.s.ListModels(bg)
	require.NoError(t, err)
	rows = slices.DeleteFunc(rows, func(m ListedModel) bool { return m.Ref != "httpjudge/j" })
	require.Len(t, rows, 1)
	st, err := e.s.GetSettings(bg)
	require.NoError(t, err)
	st.JudgeModel = "httpjudge/j"
	_, err = e.s.SetSettings(bg, st)
	require.NoError(t, err)
	j, _, _, err := e.s.resolveJudge(bg)
	require.NoError(t, err)
	return e.s, j
}

func TestPluginModelsHTTPJudgeBusinessError(t *testing.T) {
	_, j := testHTTPJudge(t, `throw {code:"model_not_ready",message:"Prepare model",retryable:true}`, func(w http.ResponseWriter, r *http.Request) { t.Error("business error must stop before HTTP") })
	answers, err := j.Ask(bg, "public", []ai.Question{{Name: "q", Type: ai.Predicate, Instructions: "test?"}})
	require.Nil(t, answers)
	require.ErrorIs(t, err, ai.ErrJudgeUnavailable)
	var me *ModelError
	require.ErrorAs(t, err, &me)
	require.Equal(t, "model_not_ready", me.Code)
	require.True(t, me.Retryable)
}

func TestPluginModelsHTTPJudgeInvalidated(t *testing.T) {
	for _, change := range []string{"disable", "config", "caller-cancel"} {
		t.Run(change, func(t *testing.T) {
			entered, released := make(chan struct{}), make(chan struct{})
			s, j := testHTTPJudge(t, `return call.request`, func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-released:
				case <-r.Context().Done():
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"answers":{"q":{"type":"noul","noul":0.8}}}`))
			})
			defer close(released)
			done := make(chan error, 1)
			ctx, cancel := context.WithCancel(bg)
			defer cancel()
			go func() {
				answers, err := j.Ask(ctx, "public", []ai.Question{{Name: "q", Type: ai.Predicate, Instructions: "test?"}})
				if len(answers) > 0 {
					err = errors.New("invalidated request returned answers")
				}
				done <- err
			}()
			<-entered
			if change == "disable" {
				require.NoError(t, s.SetPluginEnabled(bg, "httpjudge", false))
			} else if change == "config" {
				require.NoError(t, s.SetPluginConfig(bg, "httpjudge", nil))
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if change == "caller-cancel" {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.ErrorIs(t, err, ai.ErrJudgeUnavailable)
				}
			case <-time.After(time.Second):
				t.Fatal("in-flight judge did not stop")
			}
		})
	}
}
