package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
)

// Local archives (ADR 0036): accounts with no server, filled by importing
// .eml files, with folders of their own.

func isArchive(acc core.Account) bool { return acc.Kind == core.KindLocalArchive }

// isArchiveID reports whether account id is a local archive.
func (a *App) isArchiveID(id int64) bool {
	acc, ok := a.accountByID(id)
	return ok && isArchive(acc)
}

// nameDialog asks for a name, as for a new archive or folder, and saves it
// with save off the UI thread.
type nameDialog struct {
	open         bool
	title, desc  string
	button, name string
	busy         bool
	save         func(ctx context.Context, name string) error
}

func (p *mailPage) askName(title, desc, button, name string, save func(ctx context.Context, name string) error) {
	p.nameDlg = nameDialog{open: true, title: title, desc: desc, button: button, name: name, save: save}
}

func (p *mailPage) nameDialogView(c *ui.Context) {
	d := &p.nameDlg
	if !d.open {
		return
	}
	a := p.a
	submit := func() {
		d.busy = true
		name := strings.TrimSpace(d.name)
		a.do(func(ctx context.Context) error { return d.save(ctx, name) }, func(err error) {
			d.busy = false
			if err != nil {
				a.toastErr(err)
				return
			}
			d.open = false
			a.refresh(nil)
		})
	}
	kit.Dialog(c, &d.open, d.title, kit.DialogOpts{Size: kit.DialogSm, Desc: d.desc, Close: i18n.T("common.close")}, func() {
		kit.Field(c, i18n.T("mail.archive.name"), "", "", func() {
			kit.Input(c, &d.name, kit.InputOpts{}).AutoFocus().Label(i18n.T("mail.archive.name"))
		})
	}, func() {
		kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() { d.open = false })
		kit.Button(c, d.button, kit.ButtonOpts{Disabled: d.busy || strings.TrimSpace(d.name) == ""}).OnClick(submit)
	})
}

// newArchive asks for a new local archive's name.
func (p *mailPage) newArchive() {
	a := p.a
	p.askName(i18n.T("mail.archive.new"), i18n.T("mail.archive.new.desc"), i18n.T("mail.archive.create"), "",
		func(ctx context.Context, name string) error {
			_, err := a.svc.AddLocalArchive(ctx, name)
			return err
		})
}

// archiveItems are an archive's menu entries: import into folderID (its
// inbox when 0), and a new folder.
func (p *mailPage) archiveItems(m *ui.Menu, acc core.Account, folderID int64) {
	if m.Item(i18n.T("mail.archive.import_files")).Chosen() {
		p.importEML(acc.ID, folderID, false)
	}
	if m.Item(i18n.T("mail.archive.import_folder")).Chosen() {
		p.importEML(acc.ID, folderID, true)
	}
	if m.Item(i18n.T("mail.folder.new")).Chosen() {
		a := p.a
		p.askName(i18n.T("mail.folder.new.title"), "", i18n.T("mail.archive.create"), "", func(ctx context.Context, name string) error {
			_, err := a.svc.CreateLocalFolder(ctx, acc.ID, name)
			return err
		})
	}
}

// localFolderItems rename or delete an archive's own folder.
func (p *mailPage) localFolderItems(m *ui.Menu, f sideFolder) {
	a := p.a
	if m.Item(i18n.T("mail.folder.rename")).Chosen() {
		p.askName(i18n.T("mail.folder.rename.title"), "", i18n.T("common.save"), f.Name, func(ctx context.Context, name string) error {
			return a.svc.RenameLocalFolder(ctx, f.ID, name)
		})
	}
	if m.Item(i18n.T("mail.folder.delete")).Disabled(f.Total > 0).Chosen() {
		a.do(func(ctx context.Context) error { return a.svc.DeleteLocalFolder(ctx, f.ID) }, func(err error) {
			if err != nil {
				a.toastErr(err)
				return
			}
			if a.route.View.FolderID == f.ID {
				a.navigate(Route{Page: PageMail, View: core.View{Unified: "inbox"}})
			}
			a.refresh(nil)
		})
	}
}

func importToastID(acc int64) string { return fmt.Sprintf("import:%d", acc) }

// importEML picks .eml files (or a folder of them) and imports them into
// folderID of archive acc, its inbox when 0. Progress shows in a toast
// that cancels it; a summary follows.
func (p *mailPage) importEML(acc, folderID int64, dir bool) {
	a := p.a
	if p.imports[acc] != nil {
		a.toast(i18n.T("mail.archive.import.busy"))
		return
	}
	if folderID == 0 {
		for _, f := range p.folders[acc] {
			if f.Role == "inbox" {
				folderID = f.ID
			}
		}
	}
	ctx, stop := context.WithCancel(a.ctx)
	p.imports[acc] = stop
	var sum core.ImportSummary
	picked := false
	a.do(func(context.Context) error {
		var paths []string
		var err error
		if dir {
			var d string
			if d, err = a.plat.OpenDirectory(); d != "" {
				paths = []string{d}
			}
		} else {
			paths, err = a.plat.OpenFiles(true)
		}
		if err != nil || len(paths) == 0 {
			return err
		}
		if folderID == 0 {
			fs, err := a.svc.ListFolders(ctx, acc)
			if err != nil {
				return err
			}
			for _, f := range fs {
				if f.Role == "inbox" {
					folderID = f.ID
				}
			}
		}
		picked = true
		sum, err = a.svc.ImportEML(ctx, acc, folderID, paths)
		return err
	}, func(err error) {
		stop()
		delete(p.imports, acc)
		a.closeToasts = append(a.closeToasts, importToastID(acc))
		switch {
		case err != nil:
			a.toastErr(err)
		case picked:
			a.toasts = append(a.toasts, importSummary(acc, sum))
		}
		p.reload()
	})
}

// importSummary is the toast after an import: how many came in, how many
// were there already, and the files that failed, which stays until closed.
func importSummary(acc int64, sum core.ImportSummary) ui.Toast {
	t := ui.Toast{ID: fmt.Sprintf("import-done:%d", acc), Title: i18n.Tf("mail.archive.import.done", sum.Imported), Type: kit.ToastSuccess}
	if sum.Canceled {
		t.Title, t.Type = i18n.Tf("mail.archive.import.canceled", sum.Imported), ""
	}
	var desc []string
	if sum.Duplicates > 0 {
		desc = append(desc, i18n.Tf("mail.archive.import.duplicates", sum.Duplicates))
	}
	if len(sum.Failed) > 0 {
		var names []string
		for _, f := range sum.Failed {
			names = append(names, filepath.Base(f.Path))
		}
		desc = append(desc, i18n.Tf("mail.archive.import.failed", strings.Join(names, i18n.T("mail.list_sep"))))
		t.Type, t.Timeout = kit.ToastWarning, -1
	}
	t.Description = strings.Join(desc, "\n")
	return t
}

// importProgress shows an import's progress, with its cancel.
func (p *mailPage) importProgress(ev events.Event) {
	stop := p.imports[ev.AccountID]
	if stop == nil { // over already
		return
	}
	p.a.toasts = append(p.a.toasts, ui.Toast{ID: importToastID(ev.AccountID), Title: i18n.Tf("mail.archive.importing", ev.Done, ev.Total),
		Timeout: -1, Action: i18n.T("common.cancel"), OnAction: stop})
}
