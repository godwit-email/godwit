package app

import (
	"context"
	"slices"
	"testing"

	"github.com/godwit-email/godwit/core/auth"
	"github.com/godwit-email/godwit/core/coretest"
)

func experimentalGraph(e *coretest.Env) {
	ctx := context.Background()
	st, err := e.Svc.GetSettings(ctx)
	if err == nil {
		st.ExperimentalGraph = true
		_, err = e.Svc.SetSettings(ctx, st)
	}
	if err != nil {
		panic(err)
	}
}

// With ExperimentalGraph off, Outlook goes straight to browser OAuth over IMAP
// without showing a Microsoft Graph protocol choice.
func TestOnboardingNoGraphByDefault(t *testing.T) {
	h, _ := withAccount(t)
	fo := &fakeOAuth{flows: map[string]chan flowResult{}}
	h.app.oauth = fo
	h.app.openAddAccount()
	h.click("Outlook / Hotmail")
	h.see("已在浏览器中打开登录页")
	if h.tt.HasText("Microsoft Graph（实验性）") {
		t.Fatal("Microsoft Graph offered while ExperimentalGraph is off")
	}
}

// With ExperimentalGraph on, Labs shows the switch, only newly added Outlook
// accounts offer Microsoft Graph (fixing both slots to auth.Graph), and other
// providers do not offer it.
func TestOnboardingGraph(t *testing.T) {
	h := newHarness(t, experimentalGraph)
	fo := &fakeOAuth{flows: map[string]chan flowResult{}}
	h.app.oauth = fo
	o := h.firstRun()

	// Non-Outlook providers never offer Microsoft Graph.
	h.click("其他 IMAP 邮箱")
	if _, open := h.tt.Find("IMAP 服务器"); !open {
		h.click("手动设置服务器")
	}
	if h.tt.HasText("Microsoft Graph（实验性）") {
		t.Fatal("Microsoft Graph offered on manual server form")
	}
	h.click("← 换一个邮箱")

	// Outlook offers IMAP vs Microsoft Graph before opening the browser.
	h.click("Outlook / Hotmail")
	h.see("Microsoft Graph（实验性）")
	if in := o.input(); in.Receiving.Protocol != "" {
		t.Fatalf("default Outlook protocol = %q, want IMAP (\"\")", in.Receiving.Protocol)
	}

	h.click("Microsoft Graph（实验性）")
	in := o.input()
	if in.Receiving.Protocol != auth.Graph || in.Sending.Protocol != auth.Graph || in.Receiving.Host != "graph.microsoft.com" {
		t.Fatalf("Graph Outlook input = %+v", in)
	}
	if steps := o.checkSteps(); len(steps) != 2 || steps[1].Name != auth.Graph {
		t.Fatalf("Graph checkSteps = %+v, want [settings graph]", steps)
	}

	h.click("在浏览器中登录")
	h.see("已在浏览器中打开登录页")
	h.until("OpenURL of the sign-in page", func() bool {
		return slices.Contains(h.plat.Calls(), "OpenURL https://login.example/authorize?n=1")
	})
}

// Labs page toggles ExperimentalGraph in Settings.
func TestSettingsLabsExperimentalGraph(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("labs")
	h.see("Microsoft Graph（Outlook）账户")
	h.click("Microsoft Graph（Outlook）账户")
	h.until("ExperimentalGraph saved", func() bool {
		st, err := h.env.Svc.GetSettings(context.Background())
		return err == nil && st.ExperimentalGraph
	})
}
