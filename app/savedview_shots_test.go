package app

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/theme"
)

// TestViewShots writes the view screens to $GODWIT_SHOTS, named after
// the design's artboards, to compare with them by eye.
func TestViewShots(t *testing.T) {
	dir := os.Getenv("GODWIT_SHOTS")
	if dir == "" {
		t.Skip("GODWIT_SHOTS not set")
	}
	h, accs := mailHarness(t, nil, "you@example.com")
	h.resize(1440, 900)
	acc := accs[0]
	shot := func(name string) {
		t.Helper()
		h.advance(debounce)
		h.tt.Advance(2 * theme.MotionBase)
		h.drain()
		h.tt.Frame()
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, h.tt.Image()); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	ago := func(d int) coretest.MsgOption { return coretest.Date(now.Add(-time.Duration(d) * 24 * time.Hour)) }
	h.put(acc, "INBOX", coretest.Msg("Anna Park <anna@acme.example>", acc.Email, "Order #4821 到货破损，要求退款", "这批订单里有两台设备外壳破损，希望先退款，再补发配件。", ago(0)))
	h.putSeen(acc, "Sent", coretest.Msg("Me <you@example.com>", "Tom Reed <tom@acme.example>", "2027 年续约报价", "2027 年的续约报价在附件里，请确认。", ago(4)))
	h.putSeen(acc, "INBOX", coretest.Msg("Kim Lee <kim@acme.example>", acc.Email, "下周到访安排", "已确认，周三上午到。", ago(5), coretest.Header("List-Id: <l.acme.example>")))
	h.put(acc, "INBOX", coretest.Msg("Acme News <news@acme.example>", acc.Email, "Acme Newsletter: October", "What is new at Acme this month.", ago(8), coretest.Header("List-Id: <l.acme.example>")))
	h.syncNow(acc)
	_, err := h.env.Svc.SaveView(context.Background(), core.SavedView{Name: "客户 Acme", Icon: "team", Doc: core.SavedViewDoc{Match: core.MatchAny,
		Conditions: []core.RuleCondition{{Field: core.FieldFrom, Op: core.OpContains, Value: "acme.example"}, {Field: core.FieldTo, Op: core.OpContains, Value: "acme.example"}},
		Groups: []core.ViewGroup{
			{ID: "r", Name: "待回复", Match: core.MatchAll, Conditions: []core.RuleCondition{{Field: core.FieldReply, Op: core.OpIs, Value: core.ViewReplyUnreplied}}},
			{ID: "w", Name: "待跟进", Match: core.MatchAll, Conditions: []core.RuleCondition{{Field: core.FieldReply, Op: core.OpOlderThan, Value: "3"}}},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	b := h.savedViewNamed("客户 Acme")
	h.click(b.Name)
	h.waitMail("the rows", func() bool { return len(h.items()) == 4 })
	h.app.mail.open(h.items(), 0)
	h.see(i18n.T("view.band.unreplied"))
	shot("SmartMailAll")
	h.app.mail.open(h.items(), 1)
	h.see(i18n.T("view.follow_up"))
	shot("SmartMailReader")
	h.app.mail.editView(b)
	h.until("the preview", func() bool { h.advance(debounce); return h.app.mail.viewEdit.prev != nil })
	shot("SmartMailEdit")
	h.app.mail.editView(h.app.mail.views[0]) // 待处理
	h.until("its preview", func() bool { h.advance(debounce); return h.app.mail.viewEdit.prev != nil })
	shot("SmartMailTodayEdit")
	h.app.mail.closeViewEditor()
	h.click(i18n.T("view.new"))
	h.see(i18n.T("view.editor.templates"))
	shot("SmartMailNew")
	h.app.mail.viewEdit.box.Doc.Conditions = []core.RuleCondition{{Field: core.FieldDate, Op: core.OpIs, Value: "7"}, {Field: core.FieldStarred, Op: core.OpIs, Value: core.RuleYes},
		{Field: core.FieldWrote, Op: core.OpIs, Value: core.RuleYes}, {Field: core.FieldDate, Op: core.OpNotContains, Value: core.ViewDateThisMonth}}
	shot("SmartMailConditions")
	h.app.back()
	h.see(i18n.T("view.leave.discard"))
	shot("SmartMailLeave")
}
