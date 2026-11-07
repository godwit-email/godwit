package testmail

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// Gmail's folders, which a Gmail server starts with besides INBOX.
const (
	GmailAllMail = "[Gmail]/All Mail"
	GmailSent    = "[Gmail]/Sent Mail"
	GmailDrafts  = "[Gmail]/Drafts"
	GmailTrash   = "[Gmail]/Trash"
	GmailSpam    = "[Gmail]/Spam"
)

var gmailFolders = map[string]imap.MailboxAttr{
	GmailAllMail: imap.MailboxAttrAll, GmailSent: imap.MailboxAttrSent, GmailDrafts: imap.MailboxAttrDrafts,
	GmailTrash: imap.MailboxAttrTrash, GmailSpam: imap.MailboxAttrJunk,
}

// systemLabels are the X-GM-LABELS names of folders that are not user labels.
var systemLabels = map[string]string{"INBOX": `\Inbox`, GmailSent: `\Sent`, GmailDrafts: `\Draft`, GmailTrash: `\Trash`, GmailSpam: `\Spam`}

// Gmail makes the IMAP server behave as Gmail: it advertises X-GM-EXT-1,
// starts with INBOX and the Gmail folders (special-use in LIST), and keeps
// messages added with AddGmail as Gmail does. One such message is in several
// folders, each with its own UID; flags other than \Deleted are shared; FETCH
// answers X-GM-MSGID and X-GM-LABELS; COPY and MOVE add the destination to
// its folders (with the UID it already has there, if any), and MOVE and
// EXPUNGE take it out of the source; a MOVE to Trash or Spam takes it out of
// every other folder. Identity is never derived from content:
// two AddGmail calls make two messages, and an APPEND makes a new one.
func Gmail() Option { return func(c *config) { c.gmail = true } }

// GmailMessage is one Gmail message, in the folders it has a UID in.
type GmailMessage struct {
	ID   uint64 // X-GM-MSGID
	s    *IMAP
	uids map[string]imap.UID // folder → UID; guarded by s.gmu
}

// UID is the message's UID in folder, 0 when it is not there.
func (m *GmailMessage) UID(folder string) imap.UID {
	m.s.gmu.Lock()
	defer m.s.gmu.Unlock()
	return m.uids[folder]
}

// Folders lists the folders the message is in, sorted.
func (m *GmailMessage) Folders() []string {
	m.s.gmu.Lock()
	defer m.s.gmu.Unlock()
	return slices.Sorted(maps.Keys(m.uids))
}

// The memory backend writes FETCH responses holding its mailbox lock, and the
// connection takes gmu to add X-GM items to them, so gmu is never held across
// a call into the backend.

// AddGmail adds a new Gmail message with flags to folders, which must exist.
func (s *IMAP) AddGmail(raw string, flags []imap.Flag, folders ...string) *GmailMessage {
	uids := map[string]imap.UID{}
	for _, f := range folders {
		d, err := s.User.Append(f, strings.NewReader(raw), &imap.AppendOptions{Flags: flags})
		if err != nil {
			panic(err)
		}
		uids[f] = d.UID
	}
	return s.addGmail(uids)
}

func (s *IMAP) addGmail(uids map[string]imap.UID) *GmailMessage {
	s.gmu.Lock()
	defer s.gmu.Unlock()
	// Above MaxInt64: X-GM-MSGID is unsigned.
	m := &GmailMessage{ID: 18000000000000000001 + uint64(len(s.gm)), s: s, uids: uids}
	s.gm = append(s.gm, m)
	return m
}

// Label adds the message to folder, as another client does: a new UID there,
// with the message's flags.
func (m *GmailMessage) Label(folder string) {
	m.s.gmu.Lock()
	var from string
	var uid imap.UID
	for from, uid = range m.uids {
		break
	}
	have := m.uids[folder] != 0
	m.s.gmu.Unlock()
	if have || uid == 0 {
		return
	}
	var d *imap.CopyData
	m.s.inFolder(from, func(us *imapmemserver.UserSession) (err error) {
		d, err = us.Copy(imap.UIDSetNum(uid), folder)
		return err
	})
	n, _ := d.DestUIDs.Nums()
	m.s.gmu.Lock()
	m.uids[folder] = n[0]
	m.s.gmu.Unlock()
}

// Unlabel takes the message out of folder, as archiving or removing a label
// does in another client.
func (m *GmailMessage) Unlabel(folder string) {
	m.s.gmu.Lock()
	uid := m.uids[folder]
	delete(m.uids, folder)
	m.s.gmu.Unlock()
	if uid != 0 {
		m.s.inFolder(folder, func(us *imapmemserver.UserSession) error { return expunge(us, imap.UIDSetNum(uid)) })
	}
}

// inFolder runs f on a session of its own with folder selected, as another
// client would; other connections see what it changes.
func (s *IMAP) inFolder(folder string, f func(*imapmemserver.UserSession) error) {
	us := imapmemserver.NewUserSession(s.User)
	defer us.Close()
	if _, err := us.Select(folder, nil); err != nil {
		panic(err)
	}
	if err := f(us); err != nil {
		panic(err)
	}
}

// expunge removes uids from the selected folder of sess; the backend reports
// the expunges to every connection through its tracker.
func expunge(sess imapserver.Session, uids imap.UIDSet) error {
	del := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}
	if err := sess.Store(nil, uids, del, nil); err != nil {
		return err
	}
	return sess.Expunge(nil, &uids)
}

// at is the Gmail message with uid in folder, nil when there is none. Callers hold gmu.
func (s *IMAP) at(folder string, uid imap.UID) *GmailMessage {
	for _, m := range s.gm {
		if u, ok := m.uids[folder]; ok && u == uid {
			return m
		}
	}
	return nil
}

// selectedUIDs resolves set in the session's selected folder to UIDs, ascending.
func (s session) selectedUIDs(set imap.NumSet) ([]imap.UID, error) {
	var c imap.SearchCriteria
	switch set := set.(type) {
	case imap.SeqSet:
		c.SeqNum = []imap.SeqSet{set}
	case imap.UIDSet:
		c.UID = []imap.UIDSet{set}
	}
	d, err := s.Session.Search(imapserver.NumKindUID, &c, &imap.SearchOptions{})
	if err != nil {
		return nil, err
	}
	return d.AllUIDs(), nil
}

// gmailCopy adds the messages of set to dest: one already there answers with
// its UID there, any other is copied with its flags.
// shortcut: COPYUID lists both sets sorted, which pairs them wrongly when
// existing and new UIDs interleave; fine while Godwit moves one UID at a time.
func (s session) gmailCopy(set imap.NumSet, dest string) (*imap.CopyData, error) {
	src := s.conn.folder()
	if dest == src {
		return nil, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "Source and destination mailboxes are identical"}
	}
	st, err := s.s.User.Status(dest, &imap.StatusOptions{UIDValidity: true})
	if err != nil {
		return nil, err
	}
	uids, err := s.selectedUIDs(set)
	if err != nil {
		return nil, err
	}
	d := &imap.CopyData{UIDValidity: st.UIDValidity}
	var fresh []imap.UID
	s.s.gmu.Lock()
	for _, u := range uids {
		if m := s.s.at(src, u); m != nil && m.uids[dest] != 0 {
			d.SourceUIDs.AddNum(u)
			d.DestUIDs.AddNum(m.uids[dest])
		} else {
			fresh = append(fresh, u)
		}
	}
	s.s.gmu.Unlock()
	if len(fresh) == 0 {
		return d, nil
	}
	cd, err := s.Session.Copy(imap.UIDSetNum(fresh...), dest)
	if err != nil {
		return nil, err
	}
	got, _ := cd.DestUIDs.Nums() // in source UID order
	s.s.gmu.Lock()
	defer s.s.gmu.Unlock()
	for i, u := range fresh {
		if m := s.s.at(src, u); m != nil {
			m.uids[dest] = got[i]
		}
		d.SourceUIDs.AddNum(u)
		d.DestUIDs.AddNum(got[i])
	}
	return d, nil
}

// gmailMove is gmailCopy, then the messages leave the source folder.
func (s session) gmailMove(w *imapserver.MoveWriter, set imap.NumSet, dest string) error {
	d, err := s.gmailCopy(set, dest)
	if err != nil {
		return err
	}
	if err := expunge(s.Session, d.SourceUIDs); err != nil {
		return err
	}
	if err := s.gmailPrune(); err != nil {
		return err
	}
	if dest == GmailTrash || dest == GmailSpam { // as on Gmail, they leave every other folder
		var binned []*GmailMessage
		uids, _ := d.DestUIDs.Nums()
		s.s.gmu.Lock()
		for _, u := range uids {
			if m := s.s.at(dest, imap.UID(u)); m != nil {
				binned = append(binned, m)
			}
		}
		s.s.gmu.Unlock()
		for _, m := range binned {
			for _, f := range m.Folders() {
				if f != dest {
					m.Unlabel(f)
				}
			}
		}
	}
	return w.WriteCopyData(d) // the EXPUNGEs follow, from the tracker
}

// gmailPrune takes the messages gone from the selected folder out of it.
func (s session) gmailPrune() error {
	left, err := s.selectedUIDs(imap.UIDSet{{Start: 1, Stop: 0}})
	if err != nil {
		return err
	}
	src := s.conn.folder()
	s.s.gmu.Lock()
	defer s.s.gmu.Unlock()
	for _, m := range s.s.gm {
		if u, ok := m.uids[src]; ok && !slices.Contains(left, u) {
			delete(m.uids, src)
		}
	}
	return nil
}

// gmailShare applies a STORE on set to the messages' other folders, but for
// \Deleted, which is per folder.
func (s session) gmailShare(set imap.NumSet, f *imap.StoreFlags) error {
	shared := *f
	shared.Silent = true
	shared.Flags = slices.DeleteFunc(slices.Clone(f.Flags), func(fl imap.Flag) bool { return strings.EqualFold(string(fl), string(imap.FlagDeleted)) })
	if len(shared.Flags) == 0 && f.Op != imap.StoreFlagsSet {
		return nil
	}
	uids, err := s.selectedUIDs(set)
	if err != nil {
		return err
	}
	src := s.conn.folder()
	others := map[string][]imap.UID{}
	s.s.gmu.Lock()
	for _, u := range uids {
		if m := s.s.at(src, u); m != nil {
			for folder, uid := range m.uids {
				if folder != src {
					others[folder] = append(others[folder], uid)
				}
			}
		}
	}
	s.s.gmu.Unlock()
	for folder, uids := range others {
		s.s.inFolder(folder, func(us *imapmemserver.UserSession) error {
			return us.Store(nil, imap.UIDSetNum(uids...), &shared, nil)
		})
	}
	return nil
}

// fetchUID matches the start of a FETCH response, which the memory backend
// always begins with the UID.
var fetchUID = regexp.MustCompile(`^\* \d+ FETCH \(UID (\d+)`)

// gmailFetch adds the X-GM items want names to a FETCH response line of the
// selected folder.
func (c *tconn) gmailFetch(line []byte, want int32) []byte {
	sm := fetchUID.FindSubmatchIndex(line)
	if sm == nil || want == 0 {
		return line
	}
	uid, _ := strconv.ParseUint(string(line[sm[2]:sm[3]]), 10, 32)
	c.s.gmu.Lock()
	defer c.s.gmu.Unlock()
	m := c.s.at(c.folder(), imap.UID(uid))
	if m == nil {
		return line
	}
	var add string
	if want&wantMsgID != 0 {
		add += fmt.Sprintf(" X-GM-MSGID %d", m.ID)
	}
	if want&wantLabels != 0 {
		var labels []string
		for f := range m.uids {
			if l, ok := systemLabels[f]; ok {
				labels = append(labels, l)
			} else if f != GmailAllMail {
				labels = append(labels, strconv.Quote(f))
			}
		}
		slices.Sort(labels)
		add += " X-GM-LABELS (" + strings.Join(labels, " ") + ")"
	}
	return slices.Concat(line[:sm[1]], []byte(add), line[sm[1]:])
}

// listLine matches an untagged LIST response: its attributes and mailbox.
var listLine = regexp.MustCompile(`^(\* LIST \()([^)]*)(\) \S+ )(.*)\r\n$`)

// gmailList adds the special use of a Gmail folder to its LIST line (the
// memory backend keeps none).
func gmailList(line []byte) []byte {
	m := listLine.FindSubmatch(line)
	if m == nil {
		return line
	}
	attr, ok := gmailFolders[strings.Trim(string(m[4]), `"`)]
	if !ok {
		return line
	}
	attrs := string(attr)
	if len(m[2]) > 0 {
		attrs = string(m[2]) + " " + attrs
	}
	return slices.Concat(m[1], []byte(attrs), line[len(m[1])+len(m[2]):])
}

// X-GM items a FETCH command asked for.
const (
	wantMsgID int32 = 1 << iota
	wantLabels
)

// fetchCmd matches a FETCH command line; gmItem an X-GM item in it.
var (
	fetchCmd = regexp.MustCompile(`(?i)^[^ ]+ (UID )?FETCH `)
	gmItem   = regexp.MustCompile(`(?i)X-GM-(MSGID|LABELS)`)
)

// gmailCmd notes which X-GM items a command line asks for and replaces them
// with UID, which imapserver parses (it knows no X-GM item).
func (c *tconn) gmailCmd(line []byte) []byte {
	var want int32
	if fetchCmd.Match(line) {
		line = gmItem.ReplaceAllFunc(line, func(b []byte) []byte {
			if strings.EqualFold(string(b), "X-GM-MSGID") {
				want |= wantMsgID
			} else {
				want |= wantLabels
			}
			return []byte("UID")
		})
	}
	c.gmWant.Store(want)
	return line
}

// folder is the connection's selected folder.
func (c *tconn) folder() string {
	f, _ := c.selected.Load().(string)
	return f
}
