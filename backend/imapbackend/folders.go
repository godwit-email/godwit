package imapbackend

import (
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/godwit-email/godwit/backend"
)

var specialUse = map[imap.MailboxAttr]backend.Role{
	imap.MailboxAttrSent: backend.RoleSent, imap.MailboxAttrTrash: backend.RoleTrash, imap.MailboxAttrDrafts: backend.RoleDrafts,
	imap.MailboxAttrJunk: backend.RoleJunk, imap.MailboxAttrArchive: backend.RoleArchive,
	// Gmail's All Mail stands in for a missing archive; its Starred is the
	// unified 已标星 view, so the sidebar shows it no row.
	imap.MailboxAttrAll: backend.RoleAll, imap.MailboxAttrFlagged: backend.RoleFlagged,
}

var roleNames = map[string]backend.Role{
	"sent": "sent", "sent items": "sent", "sent messages": "sent", "已发送": "sent", "已傳送": "sent", "寄件備份": "sent",
	"送信済み": "sent", "보낸편지함": "sent", "gesendet": "sent", "gesendete elemente": "sent", "envoyés": "sent", "messages envoyés": "sent",
	"enviados": "sent", "itens enviados": "sent", "inviati": "sent", "отправленные": "sent", "verzonden": "sent", "gönderilenler": "sent", "đã gửi": "sent",
	"trash": "trash", "deleted items": "trash", "deleted messages": "trash", "已删除": "trash", "垃圾桶": "trash",
	"削除済み": "trash", "ごみ箱": "trash", "ゴミ箱": "trash", "휴지통": "trash", "papierkorb": "trash", "gelöschte elemente": "trash",
	"corbeille": "trash", "papelera": "trash", "lixeira": "trash", "cestino": "trash", "корзина": "trash", "удаленные": "trash", "удалённые": "trash",
	"prullenbak": "trash", "çöp kutusu": "trash", "thùng rác": "trash",
	"drafts": "drafts", "草稿": "drafts", "下書き": "drafts", "임시보관함": "drafts", "entwürfe": "drafts", "brouillons": "drafts",
	"borradores": "drafts", "rascunhos": "drafts", "bozze": "drafts", "черновики": "drafts", "concepten": "drafts", "taslaklar": "drafts", "thư nháp": "drafts",
	"junk": "junk", "junk email": "junk", "spam": "junk", "垃圾邮件": "junk", "垃圾郵件": "junk", "迷惑メール": "junk", "스팸함": "junk", "스팸": "junk",
	"unerwünscht": "junk", "indésirables": "junk", "courrier indésirable": "junk", "correo no deseado": "junk", "lixo eletrônico": "junk",
	"posta indesiderata": "junk", "спам": "junk", "ongewenst": "junk", "gereksiz": "junk", "thư rác": "junk",
	"archive": "archive", "归档": "archive", "封存": "archive", "アーカイブ": "archive", "보관함": "archive", "archiv": "archive",
	"archives": "archive", "archivo": "archive", "arquivo": "archive", "archivio": "archive", "архив": "archive", "archief": "archive", "arşiv": "archive", "lưu trữ": "archive",
}

// Roles are the roles of LIST's folders, in its order: each folder's
// SPECIAL-USE role, else one guessed from its (last) name segment, unless
// another folder carries that role as SPECIAL-USE (Gmail's [Gmail]/Sent Mail
// beats a folder named Sent Messages).
func Roles(list []*imap.ListData) []backend.Role {
	out := make([]backend.Role, len(list))
	marked := map[backend.Role]bool{}
	for i, d := range list {
		if out[i] = markedRole(d); out[i] != "" && Selectable(d) { // an unusable folder reserves nothing
			marked[out[i]] = true
		}
	}
	for i, d := range list {
		if g := guessedRole(d); out[i] == "" && !marked[g] {
			out[i] = g
		}
	}
	return out
}

// markedRole is the folder's SPECIAL-USE role, or inbox for INBOX.
func markedRole(d *imap.ListData) backend.Role {
	for _, a := range d.Attrs {
		if r, ok := specialUse[a]; ok {
			return r
		}
	}
	if strings.EqualFold(d.Mailbox, "INBOX") {
		return backend.RoleInbox
	}
	return ""
}

// guessedRole is the role its (last) name segment suggests.
func guessedRole(d *imap.ListData) backend.Role {
	name := d.Mailbox
	if d.Delim != 0 {
		name = name[strings.LastIndexByte(name, byte(d.Delim))+1:]
	}
	return roleNames[strings.ToLower(name)]
}

// delim is the folder's hierarchy delimiter, "" when the server has none.
func delim(d *imap.ListData) string {
	if d.Delim == 0 {
		return ""
	}
	return string(d.Delim)
}

// Selectable reports whether the folder can hold mail: those a sync mirrors.
func Selectable(d *imap.ListData) bool {
	return !slices.Contains(d.Attrs, imap.MailboxAttrNoSelect) && !slices.Contains(d.Attrs, imap.MailboxAttrNonExistent)
}

// folderOf is the backend's view of a listed folder.
func folderOf(d *imap.ListData, role backend.Role) backend.Folder {
	f := backend.Folder{Ref: backend.FolderRef(d.Mailbox), Name: d.Mailbox, Delim: delim(d), Role: role}
	if i := strings.LastIndex(d.Mailbox, f.Delim); f.Delim != "" && i > 0 {
		f.Parent = backend.FolderRef(d.Mailbox[:i])
	}
	return f
}
