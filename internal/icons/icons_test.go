package icons

import "testing"

var names = []string{"mail", "sparkles", "sliders-horizontal", "panel-left", "arrow-left", "arrow-right", "arrow-up-right",
	"square-pen", "reply", "reply-all", "forward", "archive", "trash-2", "star", "mail-open", "search", "inbox",
	"folder", "send", "file-pen", "paperclip", "plus", "x", "check", "chevron-down", "chevron-right",
	"triangle-alert", "loader", "copy", "external-link", "eye", "eye-off", "image-off", "bold", "italic", "link", "list",
	"list-ordered", "quote", "undo-2", "refresh-cw", "circle-check", "circle-x", "shield", "puzzle", "lock", "monitor",
	"globe", "languages", "file-text", "wand-sparkles", "download", "ellipsis", "component", "spam", "outbox", "settings", "cpu", "info", "tag", "grip", "quill-pen", "code"}

// labelIcons are the label picker's icons, embedded as "label-<name>".
var labelIcons = []string{"shield-keyhole", "bill", "truck", "plane", "calendar-event", "notification-3", "newspaper", "shopping-bag-3",
	"folder-3", "money-dollar-circle", "book-2", "graduation-cap", "pencil", "quill-pen", "braces", "terminal-box",
	"music-2", "movie-2", "tools", "palette", "stethoscope", "first-aid-kit", "seedling", "briefcase-4",
	"bar-chart-box", "run", "sticky-note", "scales-3", "earth", "global", "hammer", "footprint",
	"flask", "brain", "heart-3", "plant", "team", "home-4", "key-2", "chat-3"}

// filled are the names with a fill style too: the rail's and the sidebars'.
var filled = []string{"mail", "sparkles", "sliders-horizontal", "component", "inbox", "star", "send", "file-pen", "trash-2",
	"archive", "spam", "folder", "outbox", "settings", "cpu", "puzzle", "shield", "info", "tag", "quill-pen"}

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
	for _, n := range labelIcons {
		if w, h := Get("label-" + n).Size(); w != 24 || h != 24 {
			t.Errorf("label-%s is %vx%v, want 24x24", n, w, h)
		}
	}
	if len(all) != len(names)+len(filled)+len(labelIcons) {
		t.Errorf("%d icons embedded, want %d", len(all), len(names)+len(filled)+len(labelIcons))
	}
	defer func() {
		if recover() == nil {
			t.Error("Get of an unknown icon did not panic")
		}
	}()
	Get("no-such-icon")
}

func TestBrands(t *testing.T) {
	for _, id := range []string{"gmail", "outlook", "qq", "netease163", "netease126", "icloud", "feishu", "lark", "dingtalk", "exmail", "yahoo", "aol"} {
		if Brand(id) == nil {
			t.Errorf("no mark for %s", id)
		}
	}
	if Brand("other") != nil {
		t.Error("other has a mark")
	}
	for _, id := range []string{"chatgpt", "grok"} {
		if BrandIcon(id) == nil {
			t.Errorf("no glyph for %s", id)
		}
	}
	if BrandIcon("openai") != nil {
		t.Error("openai has a glyph")
	}
}
