package app

import "github.com/egoist/mygo"

// MenuIcon is the icon of a native menu item for a Godwit icon name
// (web/icons.go), a template the menu tints, as the app's own menus draw
// theirs; the Wit page's menus use it.
func MenuIcon(name string) *mygo.MenuIcon { return menuIcon(name) }
