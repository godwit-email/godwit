package kit

import (
	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/theme"
)

// GalleryState holds the values the gallery's controls edit.
type GalleryState struct {
	Switch, Switch2, Check              bool
	Radio, Select, Tabs                 int
	Toggle                              int
	Input, Password, Mono, Bare, Area   string
	InvalidIn, HelpIn, ErrorIn          string
	Dialog, Sheet, Popover, Collapsible bool
}

var galleryVariants = []struct {
	name string
	v    Variant
}{{"Default", Default}, {"Secondary", Secondary}, {"Outline", Outline}, {"Ghost", Ghost}, {"Destructive", Destructive}, {"Link", Link}}

// Gallery shows every kit component, section by section. Dev builds only.
func Gallery(c *ui.Context, g *GalleryState) {
	t, th := theme.Of(c), c.Theme()
	row := func(children func()) { ui.Row(c).Wrap().AlignItems(ui.Center).Gap(th.Space(2)).Children(children) }
	section := func(name string, body func()) {
		ui.Column(c).Gap(th.Space(2)).AlignItems(ui.Start).Children(func() {
			ui.Text(c, name).FontSize(t.Text.Lg).FontWeight(600).Label("section:" + name)
			body()
		})
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(th.Space(6)).Padding(th.Space(6)).Children(func() {
			section("Button", func() {
				for _, s := range []ButtonSize{SizeSm, SizeDefault, SizeLg} {
					row(func() {
						for _, v := range galleryVariants {
							Button(c, v.name, ButtonOpts{Variant: v.v, Size: s})
						}
					})
				}
				row(func() {
					for _, v := range galleryVariants {
						Button(c, v.name+" off", ButtonOpts{Variant: v.v, Disabled: true})
					}
				})
			})
			section("IconButton", func() {
				for _, v := range []Variant{Ghost, Outline} {
					row(func() {
						for i, s := range []ButtonSize{SizeXs, SizeSm, SizeDefault, SizeLg} {
							IconButton(c, "star", "Star "+galleryVariants[v].name+string(rune('1'+i)), ButtonOpts{Variant: v, Size: s})
						}
					})
				}
			})
			section("Input", func() {
				ui.Column(c).Gap(th.Space(2)).Width(theme.SheetWidth).Children(func() {
					Input(c, &g.Input, InputOpts{Placeholder: "Normal"})
					Input(c, &g.InvalidIn, InputOpts{Placeholder: "Invalid", Invalid: true})
					Input(c, &g.Password, InputOpts{Placeholder: "Password", Password: true})
					Input(c, &g.Mono, InputOpts{Placeholder: "Mono", Mono: true})
					Input(c, &g.Bare, InputOpts{Placeholder: "Bare", Bare: true})
				})
			})
			section("TextArea", func() {
				TextArea(c, &g.Area, InputOpts{Placeholder: "Write something"}).Width(theme.SheetWidth)
			})
			section("Field", func() {
				ui.Column(c).Gap(th.Space(3)).Width(theme.SheetWidth).Children(func() {
					Field(c, "With help", "Help text under the control", "", func() { Input(c, &g.HelpIn, InputOpts{}) })
					Field(c, "With error", "ignored", "Something is wrong", func() { Input(c, &g.ErrorIn, InputOpts{Invalid: true}) })
				})
			})
			section("Select", func() {
				Select(c, &g.Select, []Option[int]{{0, "First"}, {1, "Second"}}).Label("Gallery select")
			})
			section("Checkbox", func() { Checkbox(c, &g.Check, "Checkbox") })
			section("Radio", func() {
				row(func() {
					Radio(c, &g.Radio, 0, "One")
					Radio(c, &g.Radio, 1, "Two")
				})
			})
			section("Switch", func() {
				Switch(c, &g.Switch).Label("Bare switch")
				SwitchRow(c, &g.Switch2, "Switch row", "Help under the row")
			})
			section("Tabs", func() { Tabs(c, &g.Tabs, "Tab A", "Tab B", "Tab C") })
			section("ToggleGroup", func() {
				ToggleGroup(c, "Gallery toggles", func() {
					for i, l := range []string{"Left", "Middle", "Right"} {
						v := Ghost
						if g.Toggle == i {
							v = Secondary
						}
						if Button(c, l, ButtonOpts{Variant: v}).Clicked() {
							g.Toggle = i
						}
					}
				})
			})
			section("Dialog", func() {
				if Button(c, "Open Dialog", ButtonOpts{Variant: Outline}).Clicked() {
					g.Dialog = true
				}
				Dialog(c, &g.Dialog, "Dialog title", func() { ui.Text(c, "Dialog sample text") }, nil)
			})
			section("Sheet", func() {
				if Button(c, "Open Sheet", ButtonOpts{Variant: Outline}).Clicked() {
					g.Sheet = true
				}
				Sheet(c, &g.Sheet, "Sheet title", "Close sheet", func() { ui.Text(c, "Sheet sample text") })
			})
			section("Popover", func() {
				b := Button(c, "Open Popover", ButtonOpts{Variant: Outline})
				if b.Clicked() {
					g.Popover = true
				}
				Popover(c, b, &g.Popover, func() { ui.Text(c, "Popover sample text") })
			})
			section("Tooltip", func() {
				Tooltip(c, Button(c, "Hover me", ButtonOpts{Variant: Outline}), "Tooltip sample text")
			})
			section("Collapsible", func() {
				Collapsible(c, &g.Collapsible, func() { ui.Text(c, "Show details") }, func() { ui.Text(c, "Collapsible sample text") })
			})
			section("Card", func() {
				Card(c, func() {
					CardHeader(c, "Card title", "Card description")
					ui.Text(c, "Card body")
					CardFooter(c, func() { Button(c, "Card action", ButtonOpts{}) })
				}).Width(theme.SheetWidth)
			})
			section("Badge", func() { Badge(c, "Badge") })
			section("Alert", func() {
				ui.Column(c).Gap(th.Space(2)).MaxWidth(theme.ContentMaxWidth).Children(func() {
					Alert(c, Warning, "Warning alert", nil)
					Alert(c, Danger, "Danger alert", nil)
					Alert(c, Info, "Info alert", func() { Button(c, "Alert action", ButtonOpts{Variant: Outline, Size: SizeSm}) })
				})
			})
			section("Avatar", func() {
				row(func() {
					Avatar(c, "Ada Lovelace")
					Avatar(c, "")
				})
			})
			section("Separator", func() {
				Separator(c, false)
				row(func() {
					ui.Text(c, "Left")
					Separator(c, true)
					ui.Text(c, "Right")
				})
			})
			section("Skeleton", func() {
				Skeleton(c, 0, th.Space(4))
				Skeleton(c, th.Space(40), th.Space(4))
			})
			section("Spinner", func() { Spinner(c) })
			section("Kbd", func() { Kbd(c, "⌘K") })
			section("StatusDot", func() {
				row(func() {
					for _, s := range []Status{OK, Attention, Error} {
						StatusDot(c, s)
					}
				})
			})
			section("SidebarItem", func() {
				ui.Column(c).Width(theme.SheetWidth).Children(func() {
					for _, o := range []struct {
						l string
						o SidebarItemOpts
					}{{"Selected", SidebarItemOpts{Selected: true}}, {"Plain", SidebarItemOpts{}}, {"Muted", SidebarItemOpts{Muted: true}},
						{"Small", SidebarItemOpts{Small: true}}, {"Indented", SidebarItemOpts{Indent: 4}}} {
						SidebarItem(c, o.o).Children(func() { ui.Text(c, o.l+" item") })
					}
				})
			})
			section("RailButton", func() {
				row(func() {
					RailButton(c, "mail", "Rail on", true, nil)
					RailButton(c, "mail", "Rail off", false, nil)
				})
			})
			section("Pressable", func() {
				row(func() {
					for _, o := range []struct {
						l string
						o PressableOpts
					}{{"Plain press", PressableOpts{}}, {"Bordered press", PressableOpts{Bordered: true}}, {"Selected press", PressableOpts{Selected: true}}} {
						Pressable(c, o.o).Padding(th.Space(2)).Children(func() { ui.Text(c, o.l) })
					}
				})
			})
			section("Toast", func() {
				for _, k := range []struct{ title, typ string }{{"Plain toast", ""}, {"Success toast", ToastSuccess}, {"Warning toast", ToastWarning}, {"Error toast", ToastError}} {
					toastPreview(c, ui.Toast{Title: k.title, Type: k.typ}).Label("toast:" + k.title)
				}
				ui.Row(c).Gap(th.Space(2)).Children(func() {
					for _, k := range []struct{ label, typ string }{{"Plain", ""}, {"Success", ToastSuccess}, {"Warning", ToastWarning}, {"Error", ToastError}} {
						if Button(c, "Show "+k.label, ButtonOpts{Variant: Outline}).Clicked() {
							c.AddToast(ui.Toast{Title: k.label + " toast sample text", Type: k.typ})
						}
					}
				})
			})
		})
	})
}
