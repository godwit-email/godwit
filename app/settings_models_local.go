package app

// The Local models section of the Model services page: the plugin models
// whose files Godwit downloads (a local judge's weights), each with its
// state and the download, cancel or remove button.

import (
	"context"
	"errors"
	"sort"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
)

// modelDownload is a download this page started: its progress and how to stop it.
type modelDownload struct {
	cancel context.CancelFunc
	status core.ModelStatus
}

type localModel struct {
	ref, name, plugin string
	status            core.ModelStatus
}

// localModels are the running plugins' models with files, by plugin then list order.
func (m *modelsView) localModels() []localModel {
	var out []localModel
	pms := append([]core.PluginModels(nil), m.plugins...)
	sort.SliceStable(pms, func(i, j int) bool { return pms[i].Plugin < pms[j].Plugin })
	for _, p := range pms {
		for _, md := range p.Models {
			if st, ok := p.Files[md.ID]; ok {
				out = append(out, localModel{"plugin/" + p.Plugin + "/" + md.ID, md.Name, p.Plugin, st})
			}
		}
	}
	return out
}

func (m *modelsView) localSection(c *ui.Context) {
	models := m.localModels()
	if len(models) == 0 {
		return
	}
	var rows []func()
	for _, lm := range models {
		rows = append(rows, func() {
			settingRow(c.Key(lm.ref), lm.name, m.localLine(lm), func() { m.localButton(c, lm) })
		})
	}
	settingsSection(c, i18n.T("settings.models.files"), i18n.T("settings.models.files.foot"), rows...)
}

// localLine is the model's plugin, size and state.
func (m *modelsView) localLine(lm localModel) string {
	st := lm.status
	size := formatBytes(st.TotalBytes)
	if d := m.downloads[lm.ref]; d != nil {
		if d.status.State == core.ModelVerifying {
			return i18n.Tf("settings.models.files.verifying", lm.plugin, size)
		}
		pct := int64(0)
		if d.status.TotalBytes > 0 {
			pct = d.status.DownloadedBytes * 100 / d.status.TotalBytes
		}
		return i18n.Tf("settings.models.files.downloading", lm.plugin, pct, size)
	}
	switch {
	case st.State == core.ModelDownloaded || st.State == core.ModelReady:
		return i18n.Tf("settings.models.files.downloaded", lm.plugin, size)
	case st.State == core.ModelNotDownloaded:
		return i18n.Tf("settings.models.files.missing", lm.plugin, size)
	case st.Error != nil:
		switch st.Error.Code {
		case "insufficient_memory", "unsupported_platform", "update_required", "verification_failed":
			return i18n.Tf("settings.models.files.err."+st.Error.Code, lm.plugin)
		}
		return lm.plugin + " · " + st.Error.Message
	}
	return lm.plugin
}

// localButton is what the user can do with the model's files now.
func (m *modelsView) localButton(c *ui.Context, lm localModel) {
	st := lm.status
	switch {
	case m.downloads[lm.ref] != nil:
		settingButton(c, i18n.T("settings.models.files.cancel"), kit.Outline, false).OnClick(func() { m.downloads[lm.ref].cancel() })
	case st.State == core.ModelNotDownloaded:
		settingButton(c, i18n.T("settings.models.files.download"), kit.Default, false).OnClick(func() { m.download(lm.ref) })
	case st.Error != nil && st.Error.Code == "verification_failed":
		settingButton(c, i18n.T("settings.models.files.redownload"), kit.Default, false).OnClick(func() { m.download(lm.ref) })
	case st.State == core.ModelDownloaded || st.State == core.ModelReady:
		settingButton(c, i18n.T("settings.models.files.remove"), kit.Outline, false).OnClick(func() { m.removeFiles(lm.ref) })
	} // otherwise nothing to do: insufficient memory, unsupported, update required
}

// download fetches and verifies ref's files in the background; it goes on
// when the page closes, until it ends or the user cancels it.
func (m *modelsView) download(ref string) {
	a := m.a
	if m.downloads == nil {
		m.downloads = map[string]*modelDownload{}
	}
	if m.downloads[ref] != nil {
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	d := &modelDownload{cancel: cancel, status: core.ModelStatus{State: core.ModelDownloading}}
	m.downloads[ref] = d
	a.do(func(context.Context) error {
		last := int64(-1)
		_, err := a.svc.PrepareModelWithProgress(ctx, ref, func(st core.ModelStatus) {
			pct := int64(-1) // a redraw per percent and per stage, not per chunk
			if st.TotalBytes > 0 {
				pct = st.DownloadedBytes * 100 / st.TotalBytes
			}
			if st.State == core.ModelVerifying {
				pct = 101
			}
			if pct != last {
				last = pct
				a.onUI(func() { d.status = st })
			}
		})
		return err
	}, func(err error) {
		cancel()
		delete(m.downloads, ref)
		if err != nil && !errors.Is(err, context.Canceled) {
			a.toastErr(err)
		}
		m.load()
	})
}

func (m *modelsView) removeFiles(ref string) {
	a := m.a
	a.writes.Chain("models", func(ctx context.Context) error { return a.svc.RemoveModelFiles(ctx, ref) }, func(err error) {
		a.toastErr(err)
		m.load()
	})
}
