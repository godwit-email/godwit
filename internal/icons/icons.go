// Package icons embeds the Lucide icons Godwit uses (lucide-static v1.53.0,
// ISC license in svg/LICENSE-lucide.txt). ui.Icon tints them with the text color.
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

// Get returns the icon of a Lucide name, as "trash-2"; an unknown name panics.
func Get(name string) *ui.SVG {
	s, ok := all[name]
	if !ok {
		panic("icons: no icon " + name)
	}
	return s
}
