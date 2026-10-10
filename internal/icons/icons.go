// Package icons embeds the Remix Icon icons Godwit uses (remixicon v4.8.0,
// the last Apache-2.0 release; license in svg/LICENSE-remixicon.txt), the line
// style, and the fill style of those a selected rail button or sidebar row shows. Each file is
// named for its use, as "trash-2"; its first line names the Remix icon. ui.Icon
// tints them with the text color.
package icons

import (
	"embed"
	"path"
	"strings"

	"github.com/egoist/mygo/ui"
)

//go:embed svg/*.svg
var files embed.FS

var all = func() map[string]*ui.SVG {
	entries, _ := files.ReadDir("svg")
	m := make(map[string]*ui.SVG, len(entries))
	for _, e := range entries {
		b, _ := files.ReadFile("svg/" + e.Name())
		m[strings.TrimSuffix(e.Name(), path.Ext(e.Name()))] = ui.MustParseSVG(b)
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
func Fill(name string) *ui.SVG { return Get(name + "-fill") }
