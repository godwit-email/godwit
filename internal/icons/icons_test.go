package icons

import "testing"

var names = []string{"mail", "sparkles", "sliders-horizontal", "panel-left", "arrow-left", "arrow-right",
	"square-pen", "reply", "reply-all", "forward", "archive", "trash-2", "star", "mail-open", "search", "inbox",
	"folder", "send", "file-pen", "paperclip", "plus", "x", "check", "chevron-down", "chevron-right",
	"triangle-alert", "loader", "copy", "external-link", "image-off", "bold", "italic", "link", "list",
	"list-ordered", "quote", "undo-2", "refresh-cw", "circle-check", "circle-x", "shield", "puzzle", "server",
	"globe", "languages", "wand-sparkles", "download", "ellipsis", "component", "spam", "outbox", "settings", "cpu", "info"}

// filled are the names with a fill style too: the rail's and the sidebars'.
var filled = []string{"mail", "sparkles", "sliders-horizontal", "component", "inbox", "star", "send", "file-pen", "trash-2",
	"archive", "spam", "folder", "outbox", "settings", "cpu", "puzzle", "shield", "info"}

func TestIconsParse(t *testing.T) {
	for _, n := range names {
		s := Get(n)
		if w, h := s.Size(); w != 24 || h != 24 {
			t.Errorf("%s is %vx%v, want 24x24", n, w, h)
		}
	}
	for _, n := range filled {
		if w, h := Fill(n).Size(); w != 24 || h != 24 {
			t.Errorf("%s fill is %vx%v, want 24x24", n, w, h)
		}
	}
	if len(all) != len(names)+len(filled) {
		t.Errorf("%d icons embedded, want %d", len(all), len(names)+len(filled))
	}
	defer func() {
		if recover() == nil {
			t.Error("Get of an unknown icon did not panic")
		}
	}()
	Get("no-such-icon")
}

func TestBrands(t *testing.T) {
	for _, id := range []string{"gmail", "outlook", "qq", "netease163", "netease126", "icloud", "feishu", "dingtalk", "exmail"} {
		if Brand(id) == nil {
			t.Errorf("no mark for %s", id)
		}
	}
	if Brand("other") != nil {
		t.Error("other has a mark")
	}
}
