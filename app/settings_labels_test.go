package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/internal/i18n"
)

func (h *harness) labels() []core.Label {
	h.t.Helper()
	ls, err := h.env.Svc.ListLabels(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return ls
}

func (h *harness) labelsPage() {
	h.t.Helper()
	h.openSettings("labels")
	h.until("the labels page", func() bool { h.tt.Frame(); return h.tt.HasText("编辑「验证码」") })
}

// typeUnder types at the end of the field under the label text: the label is not the box.
func (h *harness) typeUnder(label, text string) {
	h.t.Helper()
	r, ok := h.tt.Find(label)
	if !ok {
		h.t.Fatalf("no field %s", label)
	}
	h.tt.ClickAt(r.X+r.W/2, r.Y+r.H+16)
	h.tt.Key(0, ui.KeyEnd)
	h.drain()
	h.typeText(text)
}

// withModel gives the account's Godwit a model, so labels can be classified.
func (h *harness) withModel() {
	h.t.Helper()
	h.env.LLM(h.t)
	h.settle()
	h.waitMail("a model for labels", func() bool { return h.app.taskModels["labels"] })
}

func TestSettingsLabelsList(t *testing.T) {
	h, _ := withAccount(t)
	h.labelsPage()
	if n := h.countTexts("预置"); n != 7 {
		t.Errorf("%d built-in marks, want 7", n)
	}
	side, _ := h.tt.Find("标签")
	models, _ := h.tt.Find("模型服务")
	if side.Y <= models.Y {
		t.Errorf("标签 at %v, want after 模型服务 at %v", side.Y, models.Y)
	}
}

func TestSettingsLabelsCreate(t *testing.T) {
	h, _ := withAccount(t)
	h.labelsPage()
	h.click("新建标签")
	h.typeUnder("名称", "家人")
	h.click("粉")
	h.click("heart-3")
	h.click("创建")
	h.until("the new label", func() bool {
		ls := h.labels()
		l := ls[len(ls)-1]
		return l.Name == "家人" && l.Color == "pink" && l.Icon == "heart-3"
	})
	h.click("新建标签")
	h.typeUnder("名称", "账单")
	h.see("已经有同名的标签")
	n := len(h.labels())
	h.click("创建")
	h.drain()
	if len(h.labels()) != n {
		t.Error("a duplicate name was created")
	}
}

func TestSettingsLabelsEdit(t *testing.T) {
	h, _ := withAccount(t)
	h.labelsPage()
	h.click("编辑「验证码」")
	h.typeUnder("说明（可选）", "。也包括邮箱验证链接")
	h.click("保存")
	h.until("the description saved", func() bool {
		l := h.labels()[0]
		return l.Slug == "otp" && strings.HasSuffix(l.Description, "也包括邮箱验证链接")
	})
}

func TestSettingsLabelsDelete(t *testing.T) {
	h, _ := withAccount(t)
	h.labelsPage()
	h.click("编辑「验证码」")
	h.click("删除标签")
	h.see("删除「验证码」？")
	h.see("已打上的 0 封邮件会去掉这个标签。")
	h.click("删除")
	h.until("the label gone", func() bool { return h.labels()[0].Slug == "bill" })
}

func TestSettingsLabelsReorder(t *testing.T) {
	h, _ := withAccount(t)
	h.labelsPage()
	first, _ := h.tt.Find("编辑「验证码」")
	second, _ := h.tt.Find("编辑「账单」")
	row := second.Y - first.Y
	x, y := first.X-row*4, first.Y+first.H/2 // on the row, left of its button
	h.tt.Move(x, y)
	h.tt.Press(x, y)
	h.tt.Move(x, y+row/2)
	h.tt.Move(x, y+row*2)
	h.tt.Release(x, y+row*2)
	h.drain()
	h.until("the new order", func() bool {
		ls := h.labels()
		return ls[0].Slug == "bill" && ls[1].Slug == "order" && ls[2].Slug == "otp"
	})
}

// TestSettingsLabelsReorderScrolls drags a label to the bottom of a list longer than the
// window allows: holding it at the list's edge scrolls the rest into reach.
func TestSettingsLabelsReorderScrolls(t *testing.T) {
	h, _ := withAccount(t)
	h.resize(1280, 600)
	h.labelsPage()
	first, _ := h.tt.Find("编辑「验证码」")
	second, _ := h.tt.Find("编辑「账单」")
	row := second.Y - first.Y
	top := first.Y + first.H/2 - row/2
	x, y := first.X-row*4, first.Y+first.H/2
	h.tt.Move(x, y)
	h.tt.Press(x, y)
	h.tt.Move(x, y+row/2)
	end := top + 300 - 4 // the list's bottom edge: half the window
	h.tt.Move(x, end)
	for range 60 {
		h.advance(16 * time.Millisecond)
		h.tt.Frame()
	}
	h.tt.Release(x, end)
	h.drain()
	h.until("the label at the end", func() bool {
		ls := h.labels()
		return ls[len(ls)-1].Slug == "otp"
	})
}

func TestSettingsLabelsAutoSwitch(t *testing.T) {
	h, _ := withAccount(t)
	h.labelsPage()
	h.click("自动给新邮件打标签")
	h.until("auto-labeling off", func() bool { return !h.app.settings.AutoLabel })
}

func TestSettingsLabelsRescan(t *testing.T) {
	h, acc := withAccount(t)
	for _, s := range []string{"a", "b", "c", "d"} {
		h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, s, s))
	}
	h.syncNow(acc)
	h.withModel()
	h.labelsPage()
	h.click("全部重新判断")
	h.click("开始回扫")
	h.until("the progress", func() bool { h.tt.Frame(); return h.tt.HasText("正在回扫 · 0 / 4 封") })
	h.see("回扫进度")
	db, err := store.Open(context.Background(), filepath.Join(h.env.DataDir, "godwit.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Execute(context.Background(), "UPDATE messages SET labeled_at = unixepoch() WHERE id = (SELECT min(id) FROM messages WHERE labeled_at = 0)")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	h.until("one done", func() bool {
		h.advance(2 * time.Second)
		h.tt.Frame()
		return h.tt.HasText("正在回扫 · 1 / 4 封")
	})
	h.click("停止")
	h.until("idle again", func() bool { h.tt.Frame(); return h.tt.HasText("开始回扫") })
	if n, _ := h.env.Svc.PendingLabels(context.Background()); n != 0 {
		t.Errorf("%d still pending", n)
	}
}

func TestSettingsLabelsRescanWithAutoOff(t *testing.T) {
	h, acc := withAccount(t)
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "a", "a"))
	h.syncNow(acc)
	h.withModel()
	h.labelsPage()
	h.click("自动给新邮件打标签")
	h.until("auto-labeling off", func() bool { return !h.app.settings.AutoLabel })
	h.click("开始回扫")
	h.until("a rescan starts anyway", func() bool { h.tt.Frame(); return h.tt.HasText("正在回扫 · 0 / 1 封") })
}

func TestSettingsSearchFindsLabels(t *testing.T) {
	h, _ := withAccount(t)
	p := h.openSettings("general")
	for _, q := range []string{"回扫", "自动标记"} {
		if m := p.matching(q); len(m.apps) != 1 || m.apps[0] != "labels" || m.accounts {
			t.Errorf("%s: %+v", q, m)
		}
	}
}

func TestSettingsLabelsRescanNeedsModel(t *testing.T) {
	h, acc := withAccount(t)
	h.put(acc, "INBOX", coretest.Msg("bob@example.com", acc.Email, "a", "a"))
	h.syncNow(acc)
	h.labelsPage()
	h.see("还没有能用来分类的模型，先在「模型服务」里添加一个")
	h.click("开始回扫")
	h.drain()
	h.tt.Frame()
	if h.tt.HasText("正在回扫") {
		t.Error("a rescan started with no model")
	}
}

func TestSettingsLabelsNameTooLong(t *testing.T) {
	h, _ := withAccount(t)
	h.labelsPage()
	h.click("新建标签")
	h.typeUnder("名称", strings.Repeat("长", 33))
	h.see("名称最多 32 个字")
	n := len(h.labels())
	h.click("创建")
	h.drain()
	h.tt.Frame()
	if len(h.labels()) != n || !h.tt.HasText("名称最多 32 个字") {
		t.Error("an over-long name was saved or the dialog closed")
	}
}

func TestSettingsLabelsDescriptionTooLong(t *testing.T) {
	h, _ := withAccount(t)
	h.labelsPage()
	h.click("新建标签")
	h.typeUnder("名称", "家人")
	h.typeUnder("说明（可选）", strings.Repeat("字", core.MaxLabelDescription+1))
	h.see("说明最多 500 个字")
	n := len(h.labels())
	h.click("创建")
	h.drain()
	if len(h.labels()) != n {
		t.Error("an over-long description was saved")
	}
}

// TestSettingsLabelsSwatchesFit: at either density the color swatches sit in
// one row across the field, from its left edge (名称's) to its right (the
// right-aligned 创建 button's).
func TestSettingsLabelsSwatchesFit(t *testing.T) {
	for _, density := range []string{"compact", "comfortable"} {
		t.Run(density, func(t *testing.T) {
			h, _ := withAccount(t)
			st, err := h.env.Svc.GetSettings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			st.Density = density
			if _, err := h.env.Svc.SetSettings(context.Background(), st); err != nil {
				t.Fatal(err)
			}
			h.waitMail(density+" density", func() bool { return h.app.settings.Density == density })
			h.labelsPage()
			h.click("新建标签")
			h.see("创建")
			name, _ := h.tt.Find("名称")
			create, _ := h.tt.Find("创建")
			var first, last ui.Rect
			for i, c := range core.LabelColors {
				label := i18n.T("label.color." + c)
				r, ok := h.tt.Find(label)
				if !ok {
					t.Fatalf("no swatch %s", label)
				}
				if i == 0 {
					first = r
				}
				if r.Y != first.Y {
					t.Errorf("swatch %s at y %v, want one row at %v", label, r.Y, first.Y)
				}
				last = r
			}
			if d := first.X - name.X; d < -1 || d > 1 {
				t.Errorf("first swatch at x %v, want the field's left edge %v", first.X, name.X)
			}
			if d := last.X + last.W - (create.X + create.W); d < -1 || d > 1 {
				t.Errorf("last swatch ends at %v, want the field's right edge %v", last.X+last.W, create.X+create.W)
			}
		})
	}
}

func TestSettingsLabelsDisplay(t *testing.T) {
	h, _ := withAccount(t)
	h.labelsPage()
	h.see("标签样式")
	h.see("图标与文本")
	h.click("图标与文本")
	h.click("仅图标")
	h.until("label display changed to icon", func() bool {
		return h.app.settings.LabelDisplay == "icon"
	})
	st, err := h.env.Svc.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.LabelDisplay != "icon" {
		t.Errorf("stored LabelDisplay %q, want icon", st.LabelDisplay)
	}
}
