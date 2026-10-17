package main

import (
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

func signInCodeMaterial(w *mygo.Window) {
	w.SetVibrancy(mygo.VibrancyPopover)
	mygo.RunOnMain(func() {
		view := objc.ID(w.NativeHandle()).Send(objc.RegisterName("contentView"))
		view.Send(objc.RegisterName("setWantsLayer:"), true)
		layer := view.Send(objc.RegisterName("layer"))
		objc.Send[objc.ID](layer, objc.RegisterName("setCornerRadius:"), float64(16))
		layer.Send(objc.RegisterName("setMasksToBounds:"), true)
	})
}

// snapFeedback taps the Force Touch trackpad with the alignment pattern, as
// macOS does when something snaps into line.
func snapFeedback() {
	mygo.RunOnMain(func() {
		performer := objc.ID(objc.GetClass("NSHapticFeedbackManager")).Send(objc.RegisterName("defaultPerformer"))
		// NSHapticFeedbackPatternAlignment, NSHapticFeedbackPerformanceTimeNow
		performer.Send(objc.RegisterName("performFeedbackPattern:performanceTime:"), 1, 1)
	})
}
