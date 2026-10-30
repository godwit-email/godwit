package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
	"github.com/godwit-email/godwit/internal/kit"
)

// confirmEmptyFolder opens the confirmation dialog to permanently empty a trash or spam folder.
func (p *mailPage) confirmEmptyFolder(folderID int64, role string, accID int64) {
	p.emptyFolderAsk = true
	p.emptyFolderID = folderID
	p.emptyFolderRole = role
	p.emptyFolderAcc = accID
}

// emptyFolderDialog shows the confirmation dialog for permanently emptying Trash or Spam.
func (p *mailPage) emptyFolderDialog(c *ui.Context) {
	if !p.emptyFolderAsk {
		return
	}
	title := i18n.T("mail.empty_trash.title")
	desc := i18n.T("mail.empty_trash.desc")
	btn := i18n.T("mail.empty_trash")
	if p.emptyFolderRole == "junk" {
		title = i18n.T("mail.empty_spam.title")
		desc = i18n.T("mail.empty_spam.desc")
		btn = i18n.T("mail.empty_spam")
	}
	kit.Dialog(c, &p.emptyFolderAsk, title,
		kit.DialogOpts{Size: kit.DialogSm, Alert: true, Desc: desc, Close: i18n.T("common.close")}, nil, func() {
			kit.Button(c, i18n.T("common.cancel"), kit.ButtonOpts{Variant: kit.Outline}).OnClick(func() {
				p.emptyFolderAsk = false
			})
			kit.Button(c, btn, kit.ButtonOpts{Variant: kit.Destructive}).OnClick(func() {
				p.emptyFolderAsk = false
				p.doEmptyFolder(p.emptyFolderID, p.emptyFolderRole, p.emptyFolderAcc)
			})
		})
}

// doEmptyFolder permanently purges the folder.
func (p *mailPage) doEmptyFolder(folderID int64, role string, accID int64) {
	a := p.a
	a.writes.Chain(fmt.Sprintf("account:%d", accID), func(ctx context.Context) error {
		_, err := a.svc.EmptyFolder(ctx, folderID)
		return err
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		if role == "trash" {
			a.toast(i18n.T("mail.empty_trash.toast"))
		} else {
			a.toast(i18n.T("mail.empty_spam.toast"))
		}
		p.reload()
	})
}

// markViewAsRead marks all unread messages in view v as read.
func (p *mailPage) markViewAsRead(v core.View) {
	a := p.a
	a.do(func(ctx context.Context) error {
		n, err := a.svc.MarkAllAsRead(ctx, v)
		if err != nil {
			return err
		}
		if n > 0 {
			a.toast(i18n.T("mail.mark_all_read.toast"))
		} else {
			a.toast(i18n.T("mail.no_unread_messages"))
		}
		return nil
	}, func(err error) {
		if err != nil {
			a.toastErr(err)
			return
		}
		p.reload()
	})
}

// roleFolder finds the role folder for the given account, or falls back to first/default account.
func (p *mailPage) roleFolder(accID int64, role string) core.Folder {
	if accID != 0 {
		for _, f := range p.folders[accID] {
			if f.Role == role {
				return f
			}
		}
	}
	// Try default account
	if i := slices.IndexFunc(p.a.accounts, func(x core.Account) bool { return x.IsDefault }); i >= 0 {
		defAcc := p.a.accounts[i].ID
		for _, f := range p.folders[defAcc] {
			if f.Role == role {
				return f
			}
		}
	}
	// Try any account
	for _, fs := range p.folders {
		for _, f := range fs {
			if f.Role == role {
				return f
			}
		}
	}
	return core.Folder{}
}
