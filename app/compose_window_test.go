package app

import (
	"strconv"
	"strings"
	"testing"
)

func TestComposeInWindowOpensWindow(t *testing.T) {
	h, accs := mailHarness(t, nil, "me@example.com")
	h.app.ComposeInWindow()
	var call string
	h.until("OpenCompose", func() bool {
		for _, c := range h.plat.Calls() {
			if strings.HasPrefix(c, "OpenCompose ") {
				call = c
				return true
			}
		}
		return false
	})
	id, err := strconv.ParseInt(strings.TrimPrefix(call, "OpenCompose "), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if d := h.draft(id); d.AccountID != accs[0].ID {
		t.Fatalf("draft %+v", d)
	}
	if h.app.route.Page == PageCompose {
		t.Fatal("the old compose page opened too")
	}
}

func TestSettingsChangedReachesPlatform(t *testing.T) {
	h, _ := withAccount(t)
	h.openSettings("general")
	h.click("深色")
	h.called("SettingsChanged dark")
}
