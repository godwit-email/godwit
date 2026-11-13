package app

import (
	"context"
	"testing"

	"github.com/godwit-email/godwit/core"
)

// The masking preview's block (a category set to 拦截) reads the same over
// the ui socket as in process: the interface words it by its categories.
func TestMaskPreviewBlockedOverSocket(t *testing.T) {
	h, _ := withAccount(t)
	ctx := context.Background()
	st, err := h.env.Svc.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.Masking = true
	st.MaskCategories.IDs.Action = core.MaskBlock
	const text = "my card is 4111 1111 1111 1111"
	_, inProc := h.env.Svc.PreviewMasking(ctx, st, text)
	_, overSocket := h.ui.PreviewMasking(ctx, st, text)
	if core.MaskBlocked(inProc) == nil {
		t.Fatalf("in process the sample was not blocked: %v", inProc)
	}
	if got, want := humanize(overSocket), humanize(inProc); got != want {
		t.Errorf("over the socket the preview says %q, in process %q", got, want)
	}
}
