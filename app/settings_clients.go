package app

import (
	"context"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/mcpclients"
	"github.com/godwit-email/godwit/internal/theme"
)

// clientNames are the AI apps' names, as they name themselves.
var clientNames = map[string]string{mcpclients.Claude: "Claude", mcpclients.ClaudeCode: "Claude Code", mcpclients.Codex: "Codex"}

// mcpServer is how an AI app runs godwit mcp on this data directory.
func (a *App) mcpServer() mcpclients.Server {
	args := []string{"mcp"}
	if dir, custom := a.plat.DataDir(); custom {
		args = append(args, "--data-dir", dir)
	}
	return mcpclients.Server{Command: godwitExe(), Args: args}
}

// readClients reads how each AI app stands with godwit mcp; an app not
// on this computer is left out.
func (p *settingsPage) readClients() {
	a := p.a
	env, s := a.mcpEnv, a.mcpServer()
	got := map[string]mcpclients.State{}
	a.query(func(context.Context) error {
		e, err := env()
		if err != nil {
			return err
		}
		for _, id := range mcpclients.IDs {
			if st, err := e.State(id, s); err == nil {
				got[id] = st
			} else {
				got[id] = mcpclients.Off // a config it can't read: offered, and Connect says why
			}
		}
		return nil
	}, func(err error) {
		if err == nil {
			p.clients = got
		}
	})
}

// toggleClient connects id to godwit mcp, or disconnects it if it is.
func (p *settingsPage) toggleClient(id string) {
	a := p.a
	env, s, on := a.mcpEnv, a.mcpServer(), p.clients[id] == mcpclients.On
	name := clientNames[id]
	a.do(func(context.Context) error {
		e, err := env()
		if err != nil {
			return err
		}
		if on {
			return e.Disconnect(id)
		}
		return e.Connect(id, s)
	}, func(err error) {
		switch {
		case err != nil:
			a.toastErr(err)
		case on:
			a.toastAs(kit.ToastSuccess, i18n.Tf("settings.local_api.client.off", name))
		case id == mcpclients.ClaudeCode: // a new session reads it
			a.toastAs(kit.ToastSuccess, i18n.Tf("settings.local_api.client.on.session", name))
		default:
			a.toastAs(kit.ToastSuccess, i18n.Tf("settings.local_api.client.on", name))
		}
		p.readClients()
	})
}

// clientsRow is 接入 AI 应用: a button per AI app on this computer, pressed
// while it runs this Godwit, and the config to copy for any other app.
func (p *settingsPage) clientsRow(c *ui.Context, copied func(string)) {
	t, th, a := theme.Of(c), c.Theme(), p.a
	settingBlock(c, func() {
		ui.Row(c).FillWidth().Gap(th.Space(6)).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
				kit.Text(c, i18n.T("settings.local_api.connect"), t.Text.Body).FontWeight(500)
				kit.Text(c, i18n.T("settings.local_api.connect.help"), t.Text.Sm).TextColor(t.Muted)
			})
			if settingButton(c, i18n.T("settings.local_api.copy"), kit.Outline, false).Clicked() {
				copied(a.mcpConfig())
			}
		})
		ids := slices.DeleteFunc(slices.Clone(mcpclients.IDs), func(id string) bool { return p.clients[id] == mcpclients.Absent })
		if len(ids) == 0 {
			return
		}
		ui.Row(c).Wrap().Gap(th.Space(2)).Children(func() {
			for _, id := range ids {
				st := p.clients[id]
				b := kit.Pressable(c.Key(id), kit.PressableOpts{Bordered: true, OnClick: func() { p.toggleClient(id) }}).
					Height(th.Space(8)).PaddingX(th.Space(3)).Gap(th.Space(1.5)).AlignItems(ui.Center).Label(clientNames[id]) // a settingButton's size
				on := st == mcpclients.On
				if on {
					b.Border(theme.Stroke, t.Foreground)
				}
				b.Children(func() {
					// connected: the app's own colors; else its mark and name in gray
					if on {
						ui.Image(c, icons.BrandIcon(id)).Size(theme.Icon, theme.Icon)
						kit.Text(c, clientNames[id], t.Text.Base)
					} else {
						ui.Icon(c, icons.BrandIcon(id+"-mono")).Size(theme.Icon, theme.Icon).TextColor(t.Muted)
						kit.Text(c, clientNames[id], t.Text.Base).TextColor(t.Muted)
					}
					switch st {
					case mcpclients.On:
						ui.Icon(c, icons.Get("check")).Size(theme.IconSm, theme.IconSm)
					case mcpclients.Other: // another Godwit's, or this one's before it moved
						kit.StatusDot(c, kit.Attention)
					}
				})
				if st == mcpclients.Other {
					kit.Tooltip(c, b, i18n.T("settings.local_api.client.other"))
				}
			}
		})
	})
}
