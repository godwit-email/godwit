package main

import (
	"testing"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/webui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Wit page's entries become native items: groups as separators and greyed
// headings, the checked one checked, a submenu its own items, each leaf
// reporting its ID when chosen.
func TestWitMenuItems(t *testing.T) {
	var chosen []string
	var drawn []string
	icon := func(name string) *mygo.MenuIcon { drawn = append(drawn, name); return nil }
	items := witMenuItems([]webui.MenuEntry{
		{Label: "Anthropic", Header: true},
		{ID: "m:a", Label: "Sonnet", Detail: "fast", Icon: "mail", Checked: true},
		{Separator: true},
		{ID: "effort", Label: "Effort", Badge: "Low", Items: []webui.MenuEntry{
			{ID: "effort:low", Label: "Low", Radio: true, Checked: true},
			{ID: "effort:high", Label: "High", Disabled: true},
		}},
	}, icon, func(id string) { chosen = append(chosen, id) })

	require.Len(t, items, 4)
	assert.True(t, items[0].Disabled, "a heading is greyed")
	assert.Nil(t, items[0].Click, "and never chosen")
	assert.Equal(t, mygo.MenuItemCheckbox, items[1].Type)
	assert.True(t, items[1].Checked)
	assert.Equal(t, "fast", items[1].Subtitle)
	assert.Equal(t, []string{"mail"}, drawn)
	assert.Equal(t, mygo.MenuItemSeparator, items[2].Type)
	assert.Equal(t, "Low", items[3].Badge)
	assert.Nil(t, items[3].Click, "a submenu is not chosen")
	require.Len(t, items[3].Submenu, 2)
	assert.Equal(t, mygo.MenuItemRadio, items[3].Submenu[0].Type)
	assert.True(t, items[3].Submenu[0].Checked)
	assert.True(t, items[3].Submenu[1].Disabled)

	items[1].Click(items[1], nil)
	items[3].Submenu[0].Click(items[3].Submenu[0], nil)
	assert.Equal(t, []string{"m:a", "effort:low"}, chosen)
}
