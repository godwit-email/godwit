package webui

import (
	"context"
	"errors"

	"github.com/godwit-email/godwit/web"
)

// ErrBadMenu is returned to the Wit page for a menu with too many entries or
// levels (the page's menus are a few dozen lines, one submenu deep), or an
// icon that is not a Godwit icon: drawing one would panic.
var ErrBadMenu = errors.New("the menu is too large")

const (
	maxMenuEntries = 400
	maxMenuDepth   = 3
)

// MenuEntry is one line of a native menu the Wit page asks for (PopupMenu):
// a Separator, a Header (a group's heading: shown greyed, never chosen), a
// submenu (Items) or an item that is chosen by its ID. IDs are unique within
// the menu; native tells the page the chosen one's, which names its path
// through the submenus.
type MenuEntry struct {
	ID string `json:"id"`
	// Label is the line's text; Detail a second, smaller line under it and
	// Badge text at its trailing edge (what a command stands at, a key).
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
	Badge  string `json:"badge,omitempty"`
	// Icon is a Godwit icon name (web/icons.go), drawn as the app's own menus
	// draw theirs.
	Icon string `json:"icon,omitempty"`
	// Checked marks the item shown ticked; Radio, that it is one of a run of
	// items of which one is (a model, an effort).
	Checked   bool `json:"checked,omitempty"`
	Radio     bool `json:"radio,omitempty"`
	Disabled  bool `json:"disabled,omitempty"`
	Header    bool `json:"header,omitempty"`
	Separator bool `json:"separator,omitempty"`
	// Items are a submenu's entries; the entry itself is not chosen.
	Items []MenuEntry `json:"items,omitempty"`
}

// checkMenu is nil for entries within the limits, at depth levels down.
func checkMenu(entries []MenuEntry, depth int, left *int) error {
	if depth >= maxMenuDepth {
		return ErrBadMenu
	}
	for _, e := range entries {
		*left--
		if _, known := web.RemixIcons[e.Icon]; *left < 0 || (e.Icon != "" && !known) {
			return ErrBadMenu
		}
		if err := checkMenu(e.Items, depth+1, left); err != nil {
			return err
		}
	}
	return nil
}

// PopupMenu shows entries as a native menu at x, y, CSS pixels from the
// top-left of the Wit page, and returns the ID of the entry chosen, "" for
// none: the menus of the page's own controls, drawn as the system draws its
// menus.
func (p *WitPage) PopupMenu(ctx context.Context, entries []MenuEntry, x, y int) (string, error) {
	if _, _, err := p.w.caller(ctx); err != nil {
		return "", keyed(err)
	}
	left := maxMenuEntries
	if err := checkMenu(entries, 0, &left); err != nil {
		return "", keyed(err)
	}
	if p.w.d.PopupMenu == nil {
		return "", nil
	}
	return p.w.d.PopupMenu(entries, x, y), nil
}
