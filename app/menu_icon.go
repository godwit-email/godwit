package app

import (
	"bytes"
	"image/png"
	"log"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/godwit-email/godwit/core"
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

var menuIcons sync.Map // menuIconKey → *mygo.MenuIcon

type menuIconKey struct {
	name     string
	ink      ui.Color
	template bool
}

// menuIconPNG draws a line icon for a native menu item, at 2x for a
// high-resolution display.
func menuIconPNG(name string, ink ui.Color) ([]byte, error) {
	img := ui.Render(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Icon(c, icons.Get(name)).Size(theme.Icon, theme.Icon).TextColor(ink)
	}, theme.Icon, theme.Icon, 2)
	var b bytes.Buffer
	err := png.Encode(&b, img)
	return b.Bytes(), err
}

// cachedMenuIcon is an icon by what draws it; nil, which shows none, if it
// can't be drawn.
func cachedMenuIcon(key menuIconKey) *mygo.MenuIcon {
	if icon, ok := menuIcons.Load(key); ok {
		return icon.(*mygo.MenuIcon)
	}
	data, err := menuIconPNG(key.name, key.ink)
	if err != nil {
		log.Printf("menu icon %s: %v", key.name, err)
		return nil
	}
	icon, err := mygo.NewMenuIcon(data, key.template)
	if err != nil {
		log.Printf("menu icon %s: %v", key.name, err)
		return nil
	}
	menuIcons.Store(key, icon)
	return icon
}

// menuIcon is a template of a name, which the menu tints whatever its color.
func menuIcon(name string) *mygo.MenuIcon {
	return cachedMenuIcon(menuIconKey{name, theme.Light().Foreground, true})
}

// labelMenuIcon is a label's icon in its color for the theme shown.
func labelMenuIcon(t theme.Tokens, l core.Label) *mygo.MenuIcon {
	return cachedMenuIcon(menuIconKey{labelIcon(l.Icon), t.Label(l.Color).Text, false})
}
