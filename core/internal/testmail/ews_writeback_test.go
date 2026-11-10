package testmail_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/godwit-email/godwit/backend"
	"github.com/godwit-email/godwit/backend/backendtest"
	"github.com/godwit-email/godwit/core/internal/testmail"
)

func apply(t *testing.T, r backend.Receiver, op backend.Op) backend.Result {
	t.Helper()
	res, err := r.Apply(tctx(t), op)
	if err != nil {
		t.Fatalf("Apply(%T) = %v", op, err)
	}
	return res
}

// A move whose answer lacks the new ItemId is done but unlocated; the
// message is in the target, under an ItemId other than its old one.
func TestEWSMoveWithoutNewID(t *testing.T) {
	s := testmail.StartEWS(t)
	id := s.Deliver("INBOX", backendtest.Message("m@example.com", "m"))
	var l local
	l.sync(t, s)
	s.OmitNewItemIds()
	at := backend.Location{Folder: l.ref(t, "Inbox"), Ref: backend.MessageRef(id)}
	if res := apply(t, ewsReceiver(t, s), backend.Move{At: at, To: l.ref(t, "Archive")}); res.Outcome != backend.Unlocated {
		t.Fatalf("Move = %+v, want unlocated", res)
	}
	if got := s.Items("Archive"); len(got) != 1 || got[0] == id || len(s.Items("INBOX")) != 0 {
		t.Errorf("archive holds %v and the inbox %v, want the message under a new ItemId", got, s.Items("INBOX"))
	}
}

// An op on a message gone from the server is ErrNotFound, and does nothing.
func TestEWSApplyGone(t *testing.T) {
	s := testmail.StartEWS(t)
	id := s.Deliver("INBOX", backendtest.Message("m@example.com", "m"))
	var l local
	l.sync(t, s)
	s.Remove(id)
	at := backend.Location{Folder: l.ref(t, "Inbox"), Ref: backend.MessageRef(id)}
	if _, err := ewsReceiver(t, s).Apply(tctx(t), backend.Move{At: at, To: l.ref(t, "Archive")}); !errors.Is(err, backend.ErrNotFound) {
		t.Errorf("Move of a deleted item = %v, want ErrNotFound", err)
	}
}

// Delete moves to Deleted Items; Purge deletes for good, only once its
// Confirm agrees.
func TestEWSDeleteAndPurge(t *testing.T) {
	s := testmail.StartEWS(t)
	a := s.Deliver("INBOX", backendtest.Message("a@example.com", "a"))
	b := s.Deliver("INBOX", backendtest.Message("b@example.com", "b"))
	var l local
	l.sync(t, s)
	r, inbox := ewsReceiver(t, s), l.ref(t, "Inbox")
	if res := apply(t, r, backend.Delete{At: backend.Location{Folder: inbox, Ref: backend.MessageRef(a)}}); res.Outcome != backend.Done {
		t.Errorf("Delete = %+v", res)
	}
	if n := len(s.Items("Deleted Items")); n != 1 {
		t.Errorf("Deleted Items holds %d, want the deleted message", n)
	}
	purge := backend.Purge{At: backend.Location{Folder: inbox, Ref: backend.MessageRef(b)}, Confirm: func() bool { return false }}
	if res := apply(t, r, purge); res.Outcome != backend.Failed || res.Reason != backend.Cancelled {
		t.Errorf("a cancelled Purge = %+v, want failed: cancelled", res)
	}
	if !slices.Contains(s.Items("INBOX"), b) {
		t.Error("a cancelled Purge deleted the message")
	}
	purge.Confirm = func() bool { return true }
	if res := apply(t, r, purge); res.Outcome != backend.Done {
		t.Errorf("Purge = %+v", res)
	}
	if got := s.Items("INBOX"); len(got) != 0 || len(s.Items("Deleted Items")) != 1 {
		t.Errorf("after Purge the inbox holds %v, Deleted Items %v; want the purged message nowhere", got, s.Items("Deleted Items"))
	}
}

// Append saves the message with its flags: read, flagged, answered, and a
// draft as a draft, not as mail received.
func TestEWSAppendFlags(t *testing.T) {
	s := testmail.StartEWS(t)
	var l local
	l.sync(t, s)
	r := ewsReceiver(t, s)
	op := backend.Append{To: l.ref(t, "Drafts"), Raw: backendtest.Message("d@example.com", "d"), MessageID: "d@example.com",
		Flags: backend.Flags{Flagged: true, Answered: true, Draft: true}}
	res := apply(t, r, op)
	if res.Outcome != backend.Done || res.Ref == "" {
		t.Fatalf("Append = %+v, want done with its ItemId", res)
	}
	var got []backend.MessageAppeared
	for _, c := range changes(l.sync(t, s)) {
		if m, ok := c.(backend.MessageAppeared); ok {
			got = append(got, m)
		}
	}
	if len(got) != 1 || got[0].Ref != res.Ref || !reflect.DeepEqual(got[0].Flags, backend.Flags{Flagged: true, Answered: true}) {
		t.Errorf("the next sync shows %+v, want the draft flagged and answered, unread", got)
	}
	if !s.Unsent(string(res.Ref)) {
		t.Error("the appended draft was saved as sent mail")
	}
}

// Folders are created, renamed and deleted on the server: a renamed folder
// keeps its ref, a deleted one goes to Deleted Items with its mail.
func TestEWSFolderWriteBack(t *testing.T) {
	s := testmail.StartEWS(t)
	var l local
	l.sync(t, s)
	r := ewsReceiver(t, s)
	ref, err := r.CreateFolder(tctx(t), l.ref(t, "Inbox"), "Projects")
	if err != nil {
		t.Fatal(err)
	}
	l.sync(t, s)
	if l.names[ref] != "Inbox/Projects" {
		t.Fatalf("the created folder is %q, want Inbox/Projects", l.names[ref])
	}
	top, err := r.CreateFolder(tctx(t), "", "Top")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RenameFolder(tctx(t), ref, "Plans"); err != nil {
		t.Fatal(err)
	}
	l.sync(t, s)
	if l.names[ref] != "Inbox/Plans" || l.names[top] != "Top" {
		t.Errorf("after the rename: %q and %q, want Inbox/Plans and Top", l.names[ref], l.names[top])
	}
	s.Deliver("Inbox/Plans", backendtest.Message("p@example.com", "p"))
	if err := r.DeleteFolder(tctx(t), ref); err != nil {
		t.Fatal(err)
	}
	l.sync(t, s)
	if l.names[ref] != "Deleted Items/Plans" || len(s.Items("Deleted Items/Plans")) != 1 {
		t.Errorf("the deleted folder is %q with %v, want it with its mail in Deleted Items", l.names[ref], s.Items("Deleted Items/Plans"))
	}
	if err := r.RenameFolder(tctx(t), "no such folder", "x"); err == nil {
		t.Error("renaming a missing folder succeeded")
	}
}
