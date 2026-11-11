package imapbackend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"slices"
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/godwit-email/godwit/backend"
)

// Find lists the messages in folder, examined, whose Message-ID is exactly
// messageID and, when size > 0, of that size. SEARCH HEADER matches
// substrings and ignores case, so each hit is checked.
func (r *Receiver) Find(ctx context.Context, folder backend.FolderRef, messageID string, size int64) (_ []backend.MessageRef, err error) {
	defer r.trace(&err)
	c, err := r.client(ctx)
	if err != nil {
		return nil, err
	}
	return find(c, folder, messageID, size)
}

func find(c *imapclient.Client, folder backend.FolderRef, messageID string, size int64) ([]backend.MessageRef, error) {
	sel, err := c.Select(string(folder), &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, err
	}
	crit := &imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: messageID}}}
	if size > 0 {
		crit.Larger, crit.Smaller = size-1, size+1
	}
	res, err := c.UIDSearch(crit, nil).Wait()
	if err != nil {
		return nil, err
	}
	uids := res.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}
	sec := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"Message-ID"}, Peek: true}
	msgs, err := c.Fetch(imap.UIDSetNum(uids...), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{sec}}).Collect()
	if err != nil {
		return nil, err
	}
	var refs []backend.MessageRef
	for _, m := range msgs {
		h, err := netmail.ReadMessage(bytes.NewReader(append(m.FindBodySection(sec), "\r\n\r\n"...)))
		if err == nil && strings.Trim(strings.TrimSpace(h.Header.Get("Message-Id")), "<>") == messageID {
			refs = append(refs, backend.MessageRef(Encode(sel.UIDValidity, uint32(m.UID))))
		}
	}
	return refs, nil
}

// opFeature is the feature each op needs.
func opFeature(op backend.Op) backend.Feature {
	switch op.(type) {
	case backend.Move:
		return backend.FeatureMove
	case backend.Copy:
		return backend.FeatureCopy
	case backend.AddToFolder:
		return backend.FeatureAddToFolder
	case backend.RemoveFromFolder:
		return backend.FeatureRemoveFromFolder
	case backend.Purge:
		return backend.FeaturePurge
	case backend.Append:
		return backend.FeatureAppend
	}
	return ""
}

// Apply writes one op back. A server's NO is Failed; a location whose folder
// was recreated (another UIDVALIDITY) is backend.ErrNotFound.
//
// shortcut: Delete is not done yet (nothing queues it); the verb that needs
// it adds it.
func (r *Receiver) Apply(ctx context.Context, op backend.Op) (_ backend.Result, err error) {
	defer r.trace(&err)
	c, err := r.client(ctx)
	if err != nil {
		return backend.Result{}, err
	}
	if s := describe(c.Caps()).Features[opFeature(op)]; s.Level == backend.LevelUnsupported {
		return backend.Result{Outcome: backend.Unsupported, Reason: s.Reason}, nil
	}
	var res backend.Result
	switch op := op.(type) {
	case backend.SetFlags:
		res, err = setFlags(c, op)
	case backend.Move:
		if c.Caps().Has(GmailFeature) && !c.Caps().Has(imap.CapMove) {
			res, err = labelMove(c, op.At, op.To, false)
		} else {
			res, err = move(c, op)
		}
		if v, _ := Decode(string(res.Ref)); err == nil && res.Ref != "" && op.Cursor != "" {
			if want, _ := Decode(string(op.Cursor)); v != want {
				res = backend.Result{Outcome: backend.Unlocated}
			}
		}
	case backend.Copy:
		res, err = copyTo(c, op)
	case backend.AddToFolder:
		res, err = labelMove(c, op.At, op.To, true)
	case backend.RemoveFromFolder:
		res, err = labelMove(c, op.At, "", false)
	case backend.Purge:
		res, err = purge(c, op)
	case backend.Append:
		res, err = appendTo(c, op)
	default:
		return backend.Result{}, fmt.Errorf("imapbackend: %T not supported yet", op)
	}
	var refused *imap.Error
	var folder *backend.FolderError
	if !errors.As(err, &folder) && errors.As(err, &refused) {
		return backend.Result{Outcome: backend.Failed, Reason: refused.Error()}, nil
	}
	return res, err
}

var done = backend.Result{Outcome: backend.Done}

// appendTo APPENDs op.Raw unless op.To already holds it (its Message-ID and
// size). Without UIDPLUS's APPENDUID the new location is not known.
func appendTo(c *imapclient.Client, op backend.Append) (backend.Result, error) {
	if op.MessageID != "" {
		found, err := find(c, op.To, op.MessageID, int64(len(op.Raw)))
		if err != nil {
			return backend.Result{}, err
		}
		if len(found) > 0 {
			return backend.Result{Outcome: backend.Done, Ref: found[0]}, nil
		}
	}
	opts := &imap.AppendOptions{Flags: imapFlags(op.Flags), Time: op.Date}
	cmd := c.Append(string(op.To), int64(len(op.Raw)), opts)
	if _, err := cmd.Write(op.Raw); err != nil {
		cmd.Close()
		return backend.Result{}, err
	}
	if err := cmd.Close(); err != nil {
		return backend.Result{}, err
	}
	data, err := cmd.Wait()
	if err != nil {
		return backend.Result{}, err
	}
	if data.UID == 0 || data.UIDValidity == 0 {
		return backend.Result{Outcome: backend.Unlocated}, nil
	}
	return backend.Result{Outcome: backend.Done, Ref: backend.MessageRef(Encode(data.UIDValidity, uint32(data.UID)))}, nil
}

// selectAt selects at's folder read-write and decodes its UID; another
// UIDVALIDITY is backend.ErrFolderReset.
func selectAt(c *imapclient.Client, at backend.Location) (imap.UID, error) {
	validity, uid := Decode(string(at.Ref))
	sel, err := c.Select(string(at.Folder), nil).Wait()
	if err != nil {
		return 0, err
	}
	if sel.UIDValidity != validity {
		return 0, fmt.Errorf("%w: folder %q was recreated", backend.ErrFolderReset, at.Folder)
	}
	return imap.UID(uid), nil
}

func imapFlags(f backend.Flags) []imap.Flag {
	var out []imap.Flag
	for _, s := range []struct {
		on   bool
		flag imap.Flag
	}{{f.Seen, imap.FlagSeen}, {f.Flagged, imap.FlagFlagged}, {f.Answered, imap.FlagAnswered}, {f.Draft, imap.FlagDraft}} {
		if s.on {
			out = append(out, s.flag)
		}
	}
	for _, k := range f.Keywords {
		out = append(out, imap.Flag(k))
	}
	return out
}

func storeFlags(c *imapclient.Client, uid imap.UID, op imap.StoreFlagsOp, flags []imap.Flag) error {
	if len(flags) == 0 {
		return nil
	}
	return c.Store(imap.UIDSetNum(uid), &imap.StoreFlags{Op: op, Silent: true, Flags: flags}, nil).Close()
}

func setFlags(c *imapclient.Client, op backend.SetFlags) (backend.Result, error) {
	uid, err := selectAt(c, op.At)
	if err != nil {
		return backend.Result{}, err
	}
	if err := storeFlags(c, uid, imap.StoreFlagsAdd, imapFlags(op.Add)); err != nil {
		return backend.Result{}, err
	}
	if err := storeFlags(c, uid, imap.StoreFlagsDel, imapFlags(op.Remove)); err != nil {
		return backend.Result{}, err
	}
	if c.Caps().Has(GmailFeature) { // a silent STORE to a label another client removed matches nothing
		got, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true}).Collect()
		if err != nil {
			return backend.Result{}, err
		}
		if len(got) == 0 {
			return backend.Result{}, fmt.Errorf("%w: the message left folder %q", backend.ErrNotFound, op.At.Folder)
		}
	}
	return done, nil
}

// move moves the message with MOVE, else COPY, STORE \Deleted and UID
// EXPUNGE. Every attempt first checks what an earlier one whose reply was
// lost may have done: a message already gone from the source was moved
// (Unlocated); without MOVE, a source whose copy is already in the
// destination got through COPY (and maybe STORE) before the connection
// dropped, and copying again would leave two, so only STORE \Deleted and
// UID EXPUNGE remain. \Deleted alone is no proof, another client may have set
// it, so the copy must be there.
func move(c *imapclient.Client, op backend.Move) (backend.Result, error) {
	uid, err := selectAt(c, op.At)
	if err != nil {
		return backend.Result{}, err
	}
	if op.To == op.At.Folder {
		return backend.Result{Outcome: backend.Done, Ref: op.At.Ref}, nil
	}
	set := imap.UIDSetNum(uid)
	got, err := c.Fetch(set, &imap.FetchOptions{UID: true, Flags: true, Envelope: true, RFC822Size: true}).Collect()
	if err != nil {
		return backend.Result{}, err
	}
	if len(got) == 0 {
		return backend.Result{Outcome: backend.Unlocated}, nil
	}
	if !c.Caps().Has(imap.CapMove) && got[0].Envelope != nil && got[0].Envelope.MessageID != "" {
		copies, err := find(c, op.To, got[0].Envelope.MessageID, got[0].RFC822Size)
		if err == nil {
			_, err = selectAt(c, op.At)
		}
		if err == nil && len(copies) > 0 {
			if !slices.Contains(got[0].Flags, imap.FlagDeleted) {
				err = storeFlags(c, uid, imap.StoreFlagsAdd, []imap.Flag{imap.FlagDeleted})
			}
			if err == nil {
				err = c.UIDExpunge(set).Close()
			}
			if err == nil {
				return backend.Result{Outcome: backend.Unlocated}, nil
			}
		}
		if err != nil {
			return backend.Result{}, err
		}
	}
	validity, dst, err := moveUID(c, set, string(op.To))
	if err != nil || dst == 0 {
		return backend.Result{Outcome: backend.Unlocated}, err
	}
	return backend.Result{Outcome: backend.Done, Ref: backend.MessageRef(Encode(validity, dst))}, nil
}

// moveUID moves the message in the selected folder to dst and returns the
// COPYUID destination, or zeros when the server gave none. Without MOVE it
// runs COPY, STORE \Deleted and UID EXPUNGE one after another, each waiting
// for the previous OK; not Client.Move's fallback, which pipelines them and
// sends a plain EXPUNGE (removing every \Deleted message in the folder) when
// UIDPLUS is missing.
func moveUID(c *imapclient.Client, set imap.UIDSet, dst string) (validity, uid uint32, err error) {
	if c.Caps().Has(imap.CapMove) {
		d, err := c.Move(set, dst).Wait()
		if err != nil {
			return 0, 0, err
		}
		return d.UIDValidity, single(d.DestUIDs), nil
	}
	d, err := c.Copy(set, dst).Wait()
	if err != nil {
		return 0, 0, err
	}
	if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		return 0, 0, err
	}
	if err := c.UIDExpunge(set).Close(); err != nil {
		return 0, 0, err
	}
	return d.UIDValidity, single(d.DestUIDs), nil
}

// copyTo UID COPYs the message; without COPYUID the copy's location is not known.
func copyTo(c *imapclient.Client, op backend.Copy) (backend.Result, error) {
	uid, err := selectAt(c, op.At)
	if err != nil {
		return backend.Result{}, err
	}
	d, err := c.Copy(imap.UIDSetNum(uid), string(op.To)).Wait()
	if err != nil {
		return backend.Result{}, err
	}
	if dst := single(d.DestUIDs); dst != 0 && d.UIDValidity != 0 {
		return backend.Result{Outcome: backend.Done, Ref: backend.MessageRef(Encode(d.UIDValidity, dst))}, nil
	}
	return backend.Result{Outcome: backend.Unlocated}, nil
}

// purge sets \Deleted and UID EXPUNGEs the message, asking op.Confirm before
// each; a no removes \Deleted again, which an earlier attempt may have set.
// The one window left is the UID EXPUNGE's own round trip.
func purge(c *imapclient.Client, op backend.Purge) (backend.Result, error) {
	uid, err := selectAt(c, op.At)
	if err != nil {
		return backend.Result{}, err
	}
	confirm := func() bool { return op.Confirm == nil || op.Confirm() }
	cancel := func() (backend.Result, error) {
		if err := storeFlags(c, uid, imap.StoreFlagsDel, []imap.Flag{imap.FlagDeleted}); err != nil {
			return backend.Result{}, err
		}
		return backend.Result{Outcome: backend.Failed, Reason: backend.Cancelled}, nil
	}
	if !confirm() {
		return cancel()
	}
	if err := storeFlags(c, uid, imap.StoreFlagsAdd, []imap.Flag{imap.FlagDeleted}); err != nil {
		storeFlags(c, uid, imap.StoreFlagsDel, []imap.Flag{imap.FlagDeleted}) // best effort; a rescue's move removes it too
		return backend.Result{}, err
	}
	if !confirm() {
		return cancel()
	}
	if err := c.UIDExpunge(imap.UIDSetNum(uid)).Close(); err != nil {
		storeFlags(c, uid, imap.StoreFlagsDel, []imap.Flag{imap.FlagDeleted}) // best effort, as above
		return backend.Result{}, err
	}
	return done, nil
}

// uncertain is a server's NO to a read of folder before any label write as
// a backend.FolderError, not Failed: the read proves nothing about the move,
// which a later attempt reads again.
func uncertain(folder backend.FolderRef, err error) error {
	var refused *imap.Error
	if errors.As(err, &refused) {
		return &backend.FolderError{Folder: folder, Err: err}
	}
	return err
}

// labelMove moves the message at at to folder to with Gmail's additive label
// writes (to "": only takes at's label away; add: only adds to's). Every
// attempt reads membership by X-GM-MSGID first, so one after a lost reply
// writes only what is missing; a refused read is uncertain. All Mail is
// implicit membership, never a label to remove; adding Trash or Spam is
// Gmail's special transition, which removes every other label itself.
func labelMove(c *imapclient.Client, at backend.Location, to backend.FolderRef, add bool) (backend.Result, error) {
	id, err := gmailIdentity(c, at)
	if err != nil {
		return backend.Result{}, uncertain(at.Folder, err)
	}
	src, srcRole, err := label(c, at.Folder)
	if err != nil {
		return backend.Result{}, uncertain(at.Folder, err)
	}
	addOnly, removeOnly := add || srcRole == backend.RoleAll, to == ""
	srcValidity, srcUID, err := gmailLocation(c, at.Folder, id)
	if err != nil {
		return backend.Result{}, uncertain(at.Folder, err)
	}
	var dst string
	var dstRole backend.Role
	var dstValidity, dstUID uint32
	if to != "" {
		if dst, dstRole, err = label(c, to); err != nil {
			return backend.Result{}, uncertain(to, err)
		}
		removeOnly = dstRole == backend.RoleAll
		if dstValidity, dstUID, err = gmailLocation(c, to, id); err != nil {
			return backend.Result{}, uncertain(to, err)
		}
		if dstValidity == 0 {
			return backend.Result{Outcome: backend.Failed, Reason: "the destination folder no longer exists"}, nil
		}
	}
	moved := func() backend.Result {
		if dstUID == 0 {
			return done
		}
		return backend.Result{Outcome: backend.Done, Ref: backend.MessageRef(Encode(dstValidity, dstUID))}
	}
	if (addOnly && dstUID != 0) || at.Folder == to {
		return moved(), nil
	}
	if srcUID == 0 {
		if !addOnly && (removeOnly || dstUID != 0) {
			return moved(), nil
		}
		return backend.Result{}, fmt.Errorf("%w: the message left folder %q", backend.ErrNotFound, at.Folder)
	}
	sel, err := c.Select(string(at.Folder), nil).Wait()
	if err != nil {
		return backend.Result{}, uncertain(at.Folder, err)
	}
	if sel.UIDValidity != srcValidity {
		return backend.Result{}, fmt.Errorf("folder %q was recreated on the server", at.Folder)
	}
	set := imap.UIDSetNum(imap.UID(srcUID))
	binned := dstRole == backend.RoleTrash || dstRole == backend.RoleJunk
	if !removeOnly && (dstUID == 0 || binned) {
		if err := c.StoreGmailLabels(set, &imap.StoreGmailLabels{Op: imap.StoreFlagsAdd, Silent: true, Labels: []string{dst}}).Close(); err != nil {
			return backend.Result{}, err
		}
	}
	if !binned && !addOnly {
		if err := c.StoreGmailLabels(set, &imap.StoreGmailLabels{Op: imap.StoreFlagsDel, Silent: true, Labels: []string{src}}).Close(); err != nil {
			return backend.Result{}, err
		}
	}
	// A successful STORE may match no UID if another client moved it meanwhile.
	_, source, err := gmailLocation(c, at.Folder, id)
	if err != nil {
		return backend.Result{}, err
	}
	if to != "" {
		if dstValidity, dstUID, err = gmailLocation(c, to, id); err != nil {
			return backend.Result{}, err
		}
	}
	if (!addOnly && source != 0) || (!removeOnly && dstUID == 0) {
		// Gmail answered OK to a write whose result is not there.
		return backend.Result{Outcome: backend.Failed, Reason: "Gmail move postcondition was not observed"}, nil
	}
	return moved(), nil
}

// gmailIdentity is the X-GM-MSGID of the message at at: its ServerID, else
// fetched at its location, for a row stored before its identity was.
func gmailIdentity(c *imapclient.Client, at backend.Location) (uint64, error) {
	if id, err := strconv.ParseUint(at.ServerID, 10, 64); err == nil && id != 0 {
		return id, nil
	}
	uid, err := selectAt(c, at)
	if err != nil {
		return 0, err
	}
	got, err := c.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{UID: true, GmailMsgID: true}).Collect()
	if err != nil {
		return 0, err
	}
	if len(got) == 0 || got[0].GmailMsgID == 0 {
		return 0, fmt.Errorf("%w: the message's Gmail identity is unknown", backend.ErrNotFound)
	}
	return got[0].GmailMsgID, nil
}

// label is the Gmail label that names folder in X-GM-LABELS, and its role.
func label(c *imapclient.Client, folder backend.FolderRef) (string, backend.Role, error) {
	list, err := c.List("", string(folder), nil).Collect()
	if err != nil {
		return "", "", err
	}
	var role backend.Role
	for _, d := range list {
		if d.Mailbox == string(folder) {
			role = markedRole(d)
		}
	}
	switch role {
	case backend.RoleInbox:
		return `\Inbox`, role, nil
	case backend.RoleSent:
		return `\Sent`, role, nil
	case backend.RoleDrafts:
		return `\Draft`, role, nil
	case backend.RoleTrash:
		return `\Trash`, role, nil
	case backend.RoleJunk:
		return `\Spam`, role, nil
	}
	return string(folder), role, nil
}

// gmailLocation finds message id in folder, never in a substitute: its
// UIDVALIDITY and UID, or a 0 UID when it is not there and a 0 UIDVALIDITY
// when the folder is not either.
func gmailLocation(c *imapclient.Client, folder backend.FolderRef, id uint64) (validity, uid uint32, err error) {
	sel, err := c.Select(string(folder), nil).Wait()
	if err != nil {
		var refused *imap.Error
		if errors.As(err, &refused) && refused.Type == imap.StatusResponseTypeNo {
			listed, listErr := c.List("", string(folder), nil).Collect()
			if listErr != nil {
				return 0, 0, listErr
			}
			if !slices.ContainsFunc(listed, func(f *imap.ListData) bool { return f.Mailbox == string(folder) && Selectable(f) }) {
				return 0, 0, nil
			}
		}
		return 0, 0, err
	}
	found, err := c.UIDSearch(&imap.SearchCriteria{GmailMsgID: []uint64{id}}, nil).Wait()
	if err != nil {
		return 0, 0, err
	}
	return sel.UIDValidity, single(found.All), nil
}
