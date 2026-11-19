package main

import (
	"time"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/app"
	"github.com/godwit-email/godwit/webui"
)

// witMenuItems is the native menu of entries, each chosen one told to choose by
// its ID: a checked entry is a ticked item (one of a run, when Radio), a header a greyed line, one with
// Items a submenu. icon draws an entry's icon, nil for none.
func witMenuItems(entries []webui.MenuEntry, icon func(string) *mygo.MenuIcon, choose func(id string)) []*mygo.MenuItem {
	items := make([]*mygo.MenuItem, 0, len(entries))
	for _, e := range entries {
		if e.Separator {
			items = append(items, mygo.Separator())
			continue
		}
		it := &mygo.MenuItem{Label: e.Label, Subtitle: e.Detail, Badge: e.Badge, Disabled: e.Disabled || e.Header}
		if e.Icon != "" && icon != nil {
			it.Icon = icon(e.Icon)
		}
		switch {
		case len(e.Items) > 0:
			it.Submenu = witMenuItems(e.Items, icon, choose)
		case e.Radio:
			it.Type, it.Checked = mygo.MenuItemRadio, e.Checked
		case e.Checked:
			it.Type, it.Checked = mygo.MenuItemCheckbox, true
		}
		if len(e.Items) == 0 && !e.Header {
			id := e.ID
			it.Click = func(*mygo.MenuItem, *mygo.Window) { choose(id) }
		}
		items = append(items, it)
	}
	return items
}

// witPopupMenu shows entries as a native menu at x, y of window w's page and
// returns the ID chosen, "" for none or for a window that is gone.
func witPopupMenu(w *mygo.Window, entries []webui.MenuEntry, x, y int) string {
	if w == nil || w.IsDestroyed() {
		return ""
	}
	chosen := make(chan string, 1)
	mygo.NewMenu(witMenuItems(entries, app.MenuIcon, func(id string) { chosen <- id })).PopupAt(w, x, y) // returns once the menu is closed
	select {
	case id := <-chosen:
		return id
	// shortcut: AppKit clicks the item before the popup returns; a platform
	// that delivers it later than this gets "none", as popupMenu does. Needs a
	// chosen item from mygo's Menu.PopupAt to drop the wait.
	case <-time.After(200 * time.Millisecond):
		return ""
	}
}
