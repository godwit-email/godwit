package core

// Signing in to a native plugin's own service (capability "login", ADR 0033):
// the plugin runs the sign-in and keeps the credentials. Godwit only starts it
// when the user asks, opens the page it names, and shows the outcome.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/plugins"
)

// PluginLogin is whether the user is signed in to a plugin's service. Notice
// is what they must read before signing in.
type PluginLogin struct {
	SignedIn bool         `json:"signedIn"`
	Account  string       `json:"account"`
	Notice   plugins.Text `json:"notice"`
}

// PluginLoginState is where a sign-in stands: State is "preparing", "open"
// (URL is the page to open), "done" (Account signed in) or "failed" (Message says why).
type PluginLoginState struct {
	State   string `json:"state"`
	URL     string `json:"url,omitempty"`
	Account string `json:"account,omitempty"`
	Message string `json:"message,omitempty"`
}

// loginCall runs one login.* call on the running plugin name, counted as any
// call into a plugin is, and decodes its answer into out.
func (s *Service) loginCall(ctx context.Context, name, method string, params, out any) (*runningPlugin, error) {
	s.pluginsMu.RLock()
	rp := s.running[name]
	s.pluginsMu.RUnlock()
	if rp == nil || rp.native == nil || !slices.Contains(rp.row.m.Capabilities, "login") {
		return nil, ErrPluginNotFound
	}
	err := s.callModelPlugin(ctx, name, rp, func(ctx context.Context) error {
		v, cerr := rp.native.Call(ctx, method, params)
		return decodeAnswer[json.RawMessage](v, cerr, out, method)
	})
	return rp, err
}

// PluginLoginStatus asks the plugin whether the user is signed in; it starts
// the plugin's process if it went idle.
func (s *Service) PluginLoginStatus(ctx context.Context, name string) (PluginLogin, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return PluginLogin{}, err
	}
	defer end()
	var st PluginLogin
	_, err = s.loginCall(ctx, name, "login.status", struct{}{}, &st)
	return st, err
}

// BeginPluginLogin starts signing in to the plugin's service and returns the
// flow to poll. The plugin may download and reach its hosts from here on,
// which is why the Local API audits this call.
func (s *Service) BeginPluginLogin(ctx context.Context, name string) (string, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return "", err
	}
	defer end()
	var r struct {
		Flow string `json:"flow"`
	}
	if _, err := s.loginCall(ctx, name, "login.start", struct{}{}, &r); err != nil {
		return "", err
	}
	if r.Flow == "" {
		return "", fmt.Errorf("%w: login.start", errPluginAnswer)
	}
	return r.Flow, nil
}

// PollPluginLogin says where a flow stands. The page of an "open" state is
// https and audited as the user's own step, by host alone, once per flow.
func (s *Service) PollPluginLogin(ctx context.Context, name, flow string) (PluginLoginState, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return PluginLoginState{}, err
	}
	defer end()
	var st PluginLoginState
	rp, err := s.loginCall(ctx, name, "login.poll", map[string]string{"flow": flow}, &st)
	if err != nil {
		return PluginLoginState{}, err
	}
	switch st.State {
	case "preparing", "failed":
	case "open":
		u, err := url.Parse(st.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return PluginLoginState{}, fmt.Errorf("%w: login.poll must name an https page", errPluginAnswer)
		}
		if _, seen := rp.loginOpened.LoadOrStore(flow, true); !seen {
			if err := s.gateway().Record(ctx, gateway.Entry{Source: gateway.SourceUser, Extension: name, Tool: "plugin:" + name + ":login",
				Decision: "allow", Destinations: []string{u.Host}}); err != nil {
				return PluginLoginState{}, err
			}
		}
	case "done":
		s.pluginLoginChanged(name, rp)
	default:
		return PluginLoginState{}, fmt.Errorf("%w: login.poll state %q", errPluginAnswer, st.State)
	}
	return st, nil
}

// CancelPluginLogin ends a flow the user gave up on.
func (s *Service) CancelPluginLogin(ctx context.Context, name, flow string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	_, err = s.loginCall(ctx, name, "login.cancel", map[string]string{"flow": flow}, &struct{}{})
	return err
}

// PluginLogout signs out of the plugin's service.
func (s *Service) PluginLogout(ctx context.Context, name string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	rp, err := s.loginCall(ctx, name, "login.logout", struct{}{}, &struct{}{})
	if err == nil {
		s.pluginLoginChanged(name, rp)
	}
	return err
}

// pluginLoginChanged: the plugin's model list and running models were for the
// account before.
func (s *Service) pluginLoginChanged(name string, rp *runningPlugin) {
	rp.models.Store(nil)
	s.forgetPluginModels(name, rp)
	s.bus.Publish(events.Event{Type: events.PluginsChanged})
}
