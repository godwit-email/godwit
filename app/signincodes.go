package app

import (
	"context"
	"slices"
	"time"

	"github.com/godwit-email/godwit/core"
)

// refreshSignInCodes also polls the durable arrival log: UI events are only
// wakeups and can be dropped. All displayed codes share this one list.
func (a *App) refreshSignInCodes() {
	if !a.loaded {
		return
	}
	if a.codesLoading {
		a.codesAgain = true
		return
	}
	if a.codesStop != nil {
		a.codesStop()
		a.codesStop = nil
	}
	a.codesLoading = true
	now := a.clock.Now()
	var codes []core.SignInCode
	a.do(func(ctx context.Context) (err error) {
		codes, err = a.svc.RecentSignInCodes(ctx, now)
		return err
	}, func(err error) {
		a.codesLoading = false
		if err != nil {
			a.logf("sign-in codes: reading arrivals: %v", err)
		} else {
			if len(codes) > 0 && (a.codesReady || !codes[0].ReceivedAt.Before(a.codesStarted)) && !a.foreground && a.settings.NotifyNewMail && a.settings.NotifyPreview && a.plat.SignInCodeBannerSupported() &&
				!slices.ContainsFunc(a.codes, func(old core.SignInCode) bool { return old.MessageID == codes[0].MessageID }) {
				a.plat.ShowSignInCode(&codes[0])
			}
			a.codes, a.codesReady = codes, true
			a.plat.SetSignInCodes(codes)
			if !a.settings.NotifyNewMail || !a.settings.NotifyPreview || len(codes) == 0 {
				a.plat.ShowSignInCode(nil)
			}
		}
		if a.codesAgain {
			a.codesAgain = false
			a.refreshSignInCodes()
			return
		}
		wait := 30 * time.Second
		for _, code := range a.codes {
			wait = min(wait, max(time.Second, code.ReceivedAt.Add(15*time.Minute).Sub(a.clock.Now())))
		}
		a.codesStop = a.after(wait, a.refreshSignInCodes)
	})
}
