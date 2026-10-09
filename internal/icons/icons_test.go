package icons

import "testing"

var names = []string{"mail", "sparkles", "sliders-horizontal", "panel-left", "arrow-left", "arrow-right",
	"square-pen", "reply", "reply-all", "forward", "archive", "trash-2", "star", "mail-open", "search", "inbox",
	"folder", "send", "file-pen", "paperclip", "plus", "x", "check", "chevron-down", "chevron-right",
	"triangle-alert", "loader", "copy", "external-link", "image-off", "bold", "italic", "link", "list",
	"list-ordered", "quote", "undo-2", "refresh-cw", "circle-check", "circle-x", "shield", "puzzle", "server",
	"globe", "languages", "wand-sparkles", "download"}

func TestIconsParse(t *testing.T) {
	for _, n := range names {
		s := Get(n)
		if w, h := s.Size(); w != 24 || h != 24 {
			t.Errorf("%s is %vx%v, want 24x24", n, w, h)
		}
	}
	if len(all) != len(names) {
		t.Errorf("%d icons embedded, want %d", len(all), len(names))
	}
	defer func() {
		if recover() == nil {
			t.Error("Get of an unknown icon did not panic")
		}
	}()
	Get("no-such-icon")
}
