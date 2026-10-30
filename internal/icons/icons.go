// Package icons parses the Remix Icon icons Godwit uses (web.RemixIcons,
// embedded from the remixicon package, v4.8.0, the last Apache-2.0 release),
// the line style, and the fill style of those a selected rail button or
// sidebar row shows. Each is named for its use, as "trash-2". ui.Icon tints
// them with the text color.
package icons

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/godwit-email/godwit/web"
)

var all = func() map[string]*ui.SVG {
	m := make(map[string]*ui.SVG, len(web.RemixIcons))
	for name := range web.RemixIcons {
		b, _ := web.RemixIcon(name)
		m[name] = ui.MustParseSVG(b)
	}
	return m
}()

// Get returns the line icon of a name, as "trash-2"; an unknown name panics.
func Get(name string) *ui.SVG {
	s, ok := all[name]
	if !ok {
		panic("icons: no icon " + name)
	}
	return s
}

// Fill returns the fill style of a name's icon, as "mail"; one without a fill
// style panics.
func Fill(name string) *ui.SVG {
	if strings.HasSuffix(name, "-fill") {
		return Get(name)
	}
	return Get(name + "-fill")
}

// FillOr returns the fill style of a name's icon if one exists, otherwise the line icon.
func FillOr(name string) *ui.SVG {
	if s, ok := all[name+"-fill"]; ok {
		return s
	}
	return Get(name)
}
