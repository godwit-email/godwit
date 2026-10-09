package plugins

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Every row of the egress rule (plan 4 规格解释): which hosts a call may fetch
// from and whether it needs an egress approval first.
func TestEgressFor(t *testing.T) {
	hosts := []string{"api.example.com", "cdn.example.com:8443"}
	var (
		readNet  = Manifest{Permissions: Permissions{Mail: []string{"read"}, Net: hosts}}
		rwNet    = Manifest{Permissions: Permissions{Mail: []string{"read", "write"}, Net: hosts}}
		writeNet = Manifest{Permissions: Permissions{Mail: []string{"write"}, Net: hosts}}
		netOnly  = Manifest{Permissions: Permissions{Net: hosts}}
		readOnly = Manifest{Permissions: Permissions{Mail: []string{"read"}}}
	)
	for _, tc := range []struct {
		name     string
		kind     InvocationKind
		m        Manifest
		granted  bool
		readMail bool
		hosts    []string
		approval bool
	}{
		// init, card and beforeSend never fetch, whatever the plugin holds.
		{"init", KindInit, readNet, true, true, nil, false},
		{"init net only", KindInit, netOnly, true, false, nil, false},
		{"card", KindCard, readNet, true, true, nil, false},
		{"card net only", KindCard, netOnly, true, false, nil, false},
		{"beforeSend", KindBeforeSend, rwNet, true, true, nil, false},
		// hook: every declared host, no per-call approval.
		{"hook", KindHook, readNet, true, false, hosts, false},
		{"hook read mail scope", KindHook, readNet, true, true, hosts, false},
		{"hook net only", KindHook, netOnly, true, false, hosts, false},
		{"hook no net", KindHook, readOnly, true, false, nil, false},
		// action: net and (mail:read or a target carrying mail, a selection) needs approval.
		{"action read+net", KindAction, readNet, true, false, hosts, true},
		{"action read+write+net", KindAction, rwNet, true, false, hosts, true},
		{"action net only", KindAction, netOnly, true, false, hosts, false},
		{"action net only, selection target", KindAction, netOnly, true, true, hosts, true},
		{"action write+net", KindAction, writeNet, true, false, hosts, false},
		{"action no net", KindAction, readOnly, true, false, nil, false},
		// tool: net and (mail:read or a scope that read mail) needs approval.
		{"tool read+net", KindTool, readNet, true, false, hosts, true},
		{"tool read+net, scope read mail", KindTool, readNet, true, true, hosts, true},
		{"tool net only, scope clean", KindTool, netOnly, true, false, hosts, false},
		{"tool net only, scope read mail", KindTool, netOnly, true, true, hosts, true},
		{"tool no net, scope read mail", KindTool, readOnly, true, true, nil, false},
		// Ungranted permissions grant nothing.
		{"hook ungranted", KindHook, readNet, false, false, nil, false},
		{"action ungranted", KindAction, netOnly, false, false, nil, false},
		{"tool ungranted", KindTool, netOnly, false, true, nil, false},
		// An unknown kind fails closed.
		{"unknown kind", InvocationKind("other"), netOnly, true, true, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, approval := EgressFor(tc.kind, tc.m, tc.granted, false, tc.readMail)
			assert.Equal(t, tc.hosts, got)
			assert.Equal(t, tc.approval, approval)
		})
	}
}

// The returned hosts are a copy: an Invocation cannot change the manifest.
func TestEgressForCopies(t *testing.T) {
	m := Manifest{Permissions: Permissions{Net: []string{"api.example.com"}}}
	got, _ := EgressFor(KindHook, m, true, false, false)
	got[0] = "evil.example.com"
	assert.Equal(t, []string{"api.example.com"}, m.Permissions.Net)
}

// A plugin holding mail (its state was handed mail content) needs approval
// for action and tool egress like one with mail:read; other kinds are unchanged.
func TestEgressForHoldsMail(t *testing.T) {
	netOnly := Manifest{Permissions: Permissions{Net: []string{"api.example.com"}}}
	for kind, want := range map[InvocationKind]bool{KindAction: true, KindTool: true, KindHook: false} {
		_, approval := EgressFor(kind, netOnly, true, true, false)
		assert.Equal(t, want, approval, kind)
	}
	got, approval := EgressFor(KindCard, netOnly, true, true, false)
	assert.Nil(t, got)
	assert.False(t, approval)
}
