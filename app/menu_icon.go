package app

import (
	"bytes"
	"image/png"
	"log"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/theme"
)

// mailActionIcons are the icons of mail actions in a row's context menu, the
// same as the reader's toolbar.
var mailActionIcons = map[string]string{
	"reply": "reply", "reply_all": "reply-all", "forward": "forward",
	"read": "mail-open", "unread": "mail", "star": "star", "unstar": "star-fill",
	"inbox": "inbox", "archive": "archive", "junk": "spam", "delete": "trash-2",
}

var menuIcons sync.Map // icon name → *mygo.MenuIcon

// menuIconPNG draws a line icon as a template for a native menu item, at 2x
// for a high-resolution display. A template's alpha is all that counts, so the
// menu tints it whatever color it is drawn in.
func menuIconPNG(name string) ([]byte, error) {
	img := ui.Render(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Icon(c, icons.Get(name)).Size(theme.Icon, theme.Icon).TextColor(theme.Light().Foreground)
	}, theme.Icon, theme.Icon, 2)
	var b bytes.Buffer
	err := png.Encode(&b, img)
	return b.Bytes(), err
}

// menuIcon is a native menu item's icon of a name; nil, which shows none, if
// it can't be drawn.
func menuIcon(name string) *mygo.MenuIcon {
	if icon, ok := menuIcons.Load(name); ok {
		return icon.(*mygo.MenuIcon)
	}
	data, err := menuIconPNG(name)
	if err != nil {
		log.Printf("menu icon %s: %v", name, err)
		return nil
	}
	icon, err := mygo.NewMenuIcon(data, true)
	if err != nil {
		log.Printf("menu icon %s: %v", name, err)
		return nil
	}
	menuIcons.Store(name, icon)
	return icon
}
