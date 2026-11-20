package app

import (
	"context"
	"errors"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/icons"
	"github.com/godwit-email/godwit/internal/kit"
	"github.com/godwit-email/godwit/internal/mcpclients"
	"github.com/godwit-email/godwit/internal/theme"
)

// clientNames are the AI apps' names, as they name themselves.
var clientNames = map[string]string{
	mcpclients.Claude: "Claude", mcpclients.ClaudeCode: "Claude Code", mcpclients.Codex: "ChatGPT (Codex)",
	mcpclients.Antigravity: "Antigravity", mcpclients.Grok: "Grok",
}

// mcpServer is how an AI app runs godwit mcp on this data directory.
func (a *App) mcpServer() mcpclients.Server {
	args := []string{"mcp"}
	if dir, custom := a.plat.DataDir(); custom {
		args = append(args, "--data-dir", dir)
	}
	v := Version
	if v == "dev" {
		v = "0.0.0" // Claude's extensions take a semver
	}
	return mcpclients.Server{Command: godwitExe(), Args: args, Version: v}
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
	var opened bool // Claude, to install the extension it was opened with
	a.do(func(context.Context) error {
		e, err := env()
		if err != nil {
			return err
		}
		if on {
			return e.Disconnect(id)
		}
		opened = e.OpensExtension(id)
		return e.Connect(id, s)
	}, func(err error) {
		switch {
		case errors.Is(err, mcpclients.ErrRemoveInClaude):
			a.toastAs(kit.ToastWarning, i18n.Tf("settings.local_api.client.remove", name))
		case err != nil:
			a.toastErr(err)
		case opened:
			a.toastAs(kit.ToastSuccess, i18n.Tf("settings.local_api.client.opened", name))
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

// clientsSection is 应用: a row per AI app on this computer, its switch on
// while it runs this Godwit, and the config to copy for any other app.
func (p *settingsPage) clientsSection(c *ui.Context, copied func(string)) {
	t, th, a := theme.Of(c), c.Theme(), p.a
	ids := slices.DeleteFunc(slices.Clone(mcpclients.IDs), func(id string) bool { return p.clients[id] == mcpclients.Absent })
	rows := make([]func(), len(ids))
	for i, id := range ids {
		rows[i] = func() {
			st, name := p.clients[id], clientNames[id]
			on := st == mcpclients.On
			row := ui.Row(c.Key(id)).FillWidth().Gap(th.Space(3)).Padding(th.Space(2), th.Space(4)).MinHeight(th.Space(12)).AlignItems(ui.Center)
			var label ui.Element
			row.Children(func() {
				kit.Tile(c, th.Space(8), th.Space(8), kit.ToneTint).Children(func() {
					ui.Icon(c, icons.BrandIcon(id+"-mono")).Size(theme.Icon, theme.Icon).TextColor(t.Foreground)
				})
				ui.Column(c).Grow(1).Shrink(1).Gap(th.Space(0.5)).Children(func() {
					label = kit.Text(c, name, t.Text.Sm)
					ui.Row(c).Gap(th.Space(1.5)).AlignItems(ui.Center).Children(func() {
						switch st {
						case mcpclients.On:
							kit.Text(c, i18n.T("settings.local_api.client.connected"), t.Text.Xs).TextColor(t.Muted)
						case mcpclients.Other: // another Godwit's, or this one's before it moved
							kit.StatusDot(c, kit.Attention)
							kit.Text(c, i18n.T("settings.local_api.client.other"), t.Text.Xs).TextColor(t.Muted)
						default:
							kit.Text(c, i18n.T("settings.local_api.client.not_connected"), t.Text.Xs).TextColor(t.Muted)
						}
					})
				})
				was := on
				if kit.Switch(c, &on).Label(name).LabelledBy(label).Changed() && on != was {
					p.toggleClient(id)
				}
			})
		}
	}
	ui.Column(c).Gap(theme.SectionGap).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			sectionTitle(c, i18n.T("settings.local_api.connect")).Grow(1)
			kit.Button(c, i18n.T("settings.local_api.copy"), kit.ButtonOpts{Variant: kit.Outline, Size: kit.SizeSm, Icon: "copy"}).OnClick(func() { copied(a.mcpConfig()) })
		})
		settingsCard(c, rows...)
		kit.Text(c, i18n.T("settings.local_api.connect.help"), t.Text.Xs).TextColor(t.Muted).PaddingX(th.Space(4))
	})
}
