package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/coretest"
	"github.com/godwit-email/godwit/internal/i18n"
)

// heldFetch holds each FetchAttachments until release closes or its context
// ends, telling started its IDs and ended why it returned early.
type heldFetch struct {
	Service
	started chan []int64
	ended   chan error
	release chan struct{}
	err     error // returned instead of fetching, once released
}

func holdFetches(h *harness, err error) heldFetch {
	s := heldFetch{h.app.svc, make(chan []int64, 10), make(chan error, 10), make(chan struct{}), err}
	h.app.svc = s
	return s
}

func (s heldFetch) FetchAttachments(ctx context.Context, ids []int64, fn func(string, core.AttachmentInfo) error) error {
	s.started <- ids
	select {
	case <-s.release:
	case <-ctx.Done():
		s.ended <- ctx.Err()
		return ctx.Err()
	}
	if s.err != nil {
		return s.err
	}
	return s.Service.FetchAttachments(ctx, ids, fn)
}

// openWithAttachments opens a message carrying files named names and returns
// its attachments.
func openWithAttachments(t *testing.T, names ...string) (*harness, []core.AttachmentInfo) {
	h, accs := mailHarness(t, nil, "alice@example.com")
	var opts []coretest.MsgOption
	for _, n := range names {
		opts = append(opts, coretest.Attachment(n, "application/pdf", []byte("%PDF-1.4 "+n)))
	}
	m := h.deliverOne(accs[0], coretest.Msg("bob@example.com", accs[0].Email, "Files", "attached", opts...))
	h.openThread(m.ThreadID)
	h.see(names[0])
	full, err := h.env.Svc.GetMessage(context.Background(), m.ID, false)
	if err != nil || len(full.Attachments) != len(names) {
		t.Fatalf("attachments %+v, %v", full.Attachments, err)
	}
	h.plat.mu.Lock()
	h.plat.tempDir = t.TempDir()
	h.plat.mu.Unlock()
	return h, full.Attachments
}

// recv takes the next value off ch, failing after 10 real seconds.
func recv[T any](t *testing.T, ch <-chan T, desc string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", desc)
		panic("unreachable")
	}
}

func (h *harness) calls(prefix string) int {
	return len(slices.DeleteFunc(h.plat.Calls(), func(c string) bool { return !strings.HasPrefix(c, prefix) }))
}

// Opening an attachment twice while the first is still downloading opens it once.
func TestDownloadOpenTwiceOpensOnce(t *testing.T) {
	h, _ := openWithAttachments(t, "report.pdf")
	held := holdFetches(h, nil)
	h.click("report.pdf")
	recv(t, held.started, "the fetch")
	h.click("report.pdf")
	close(held.release)
	h.callArg("OpenPath")
	h.until("the queue empties", func() bool { return !h.app.Busy() })
	if n := h.calls("OpenPath"); n != 1 {
		t.Fatalf("opened %d times", n)
	}
	if len(held.started) != 0 {
		t.Fatal("the second click fetched again")
	}
}

// A second attachment waits while the first downloads; cancelling the waiting
// one drops it unfetched, cancelling the running one stops its fetch, and
// neither opens or toasts.
func TestDownloadStateAndCancel(t *testing.T) {
	h, atts := openWithAttachments(t, "a.pdf", "b.pdf")
	held := holdFetches(h, nil)
	h.click("a.pdf")
	recv(t, held.started, "a.pdf's fetch")
	h.click("b.pdf")
	if got := [2]downloadState{h.app.downloadState(atts[0].ID), h.app.downloadState(atts[1].ID)}; got != [2]downloadState{downloadRunning, downloadQueued} {
		t.Fatalf("states %v, want running, queued", got)
	}
	h.app.cancelDownload(atts[1].ID)
	if s := h.app.downloadState(atts[1].ID); s != downloadNone {
		t.Fatalf("cancelled b.pdf is %v", s)
	}
	h.app.cancelDownload(atts[0].ID)
	if err := recv(t, held.ended, "a.pdf's fetch to stop"); !errors.Is(err, context.Canceled) {
		t.Fatalf("the fetch ended with %v", err)
	}
	h.until("the queue empties", func() bool { return !h.app.Busy() })
	if len(held.started) != 0 || h.calls("OpenPath") != 0 || len(h.app.toasts) != 0 {
		t.Fatalf("after cancel: %d more fetches, calls %q, toasts %v", len(held.started), h.plat.Calls(), h.app.toasts)
	}
}

// A failed download toasts its error and opens nothing.
func TestDownloadErrorToasts(t *testing.T) {
	h, atts := openWithAttachments(t, "report.pdf")
	held := holdFetches(h, errors.New("server said no"))
	close(held.release)
	h.click("report.pdf")
	h.see("server said no")
	if h.calls("OpenPath") != 0 || h.app.downloadState(atts[0].ID) != downloadNone {
		t.Fatalf("calls %q, state %v", h.plat.Calls(), h.app.downloadState(atts[0].ID))
	}
}

// A download keeps the hidden interface from exiting (Busy) until it ends,
// and Close stops it.
func TestDownloadKeepsInterfaceAndCloseCancels(t *testing.T) {
	h, _ := openWithAttachments(t, "report.pdf")
	held := holdFetches(h, nil)
	h.click("report.pdf")
	recv(t, held.started, "the fetch")
	if !h.app.Busy() {
		t.Fatal("not Busy while a download runs")
	}
	h.app.Close()
	if err := recv(t, held.ended, "Close to stop the fetch"); !errors.Is(err, context.Canceled) {
		t.Fatalf("the fetch ended with %v", err)
	}
}

// Opening an attachment shows its chip busy; the chip's cancel, under the
// pointer, stops the fetch and puts the chip back, opening nothing.
func TestDownloadChipBusyAndCancel(t *testing.T) {
	h, _ := openWithAttachments(t, "report.pdf")
	held := holdFetches(h, nil)
	h.click("report.pdf")
	recv(t, held.started, "the fetch")
	h.tt.Move(1, 410) // off the chip: its spinner shows
	busy := i18n.Tf("reader.attachments.downloading", "report.pdf")
	h.until("the chip busy", func() bool { _, ok := h.tt.Find(busy); return ok })
	if _, ok := h.tt.Find(i18n.T("reader.save_as")); ok {
		t.Fatal("a busy chip still offers Save as")
	}
	h.hoverOn("report.pdf")
	h.click(i18n.Tf("reader.attachments.cancel_download", "report.pdf"))
	if err := recv(t, held.ended, "the fetch to stop"); !errors.Is(err, context.Canceled) {
		t.Fatalf("the fetch ended with %v", err)
	}
	h.until("the chip back", func() bool { _, ok := h.tt.Find(busy); return !ok && !h.app.Busy() })
	h.see(i18n.T("reader.save_as"))
	if h.calls("OpenPath") != 0 || len(h.app.toasts) != 0 {
		t.Fatalf("after cancel: calls %q, toasts %v", h.plat.Calls(), h.app.toasts)
	}
}

// Saving an attachment to a second path while the first save still downloads
// writes both files.
func TestDownloadSaveToTwoPathsWritesBoth(t *testing.T) {
	h, atts := openWithAttachments(t, "report.pdf")
	held := holdFetches(h, nil)
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "one.pdf"), filepath.Join(dir, "two.pdf")}
	for i, p := range paths {
		h.plat.mu.Lock()
		h.plat.savePath = p
		h.plat.mu.Unlock()
		h.app.saveAttachment(atts[0])
		h.until("the save queued", func() bool { return len(h.app.downloads) == i+1 })
	}
	close(held.release)
	h.until("the queue empties", func() bool { return !h.app.Busy() })
	for _, p := range paths {
		if got, err := os.ReadFile(p); err != nil || string(got) != "%PDF-1.4 report.pdf" {
			t.Fatalf("%s: %q, %v", p, got, err)
		}
	}
}

// Opening an attachment again right after cancelling its download, before the
// cancelled fetch has returned, downloads and opens it.
func TestDownloadAgainAfterCancelRuns(t *testing.T) {
	h, atts := openWithAttachments(t, "report.pdf")
	held := holdFetches(h, nil)
	h.app.openAttachment(atts[0])
	recv(t, held.started, "the fetch")
	h.app.cancelDownload(atts[0].ID)
	h.app.openAttachment(atts[0])
	recv(t, held.ended, "the cancelled fetch to stop")
	close(held.release)
	h.until("the queue empties", func() bool { return !h.app.Busy() })
	if n := h.calls("OpenPath"); n != 1 {
		t.Fatalf("opened %d times", n)
	}
}

// A save that fails after the download, writing to a folder that is gone,
// still stages the now-cached attachment for its chip to drag.
func TestDownloadFailedWriteStillStages(t *testing.T) {
	h, atts := openWithAttachments(t, "report.pdf")
	h.plat.mu.Lock()
	h.plat.savePath = filepath.Join(t.TempDir(), "gone", "report.pdf")
	h.plat.mu.Unlock()
	h.app.saveAttachment(atts[0])
	h.until("the cached attachment staged", func() bool { return h.app.staged[atts[0].ID] != "" })
}

// drainFetch hands over every file, as a stream draining what it had buffered
// does, and cancels after the first.
type drainFetch struct {
	Service
	cancel context.CancelFunc
}

func (s drainFetch) FetchAttachments(ctx context.Context, ids []int64, fn func(string, core.AttachmentInfo) error) error {
	return s.Service.FetchAttachments(context.WithoutCancel(ctx), ids, func(p string, info core.AttachmentInfo) error {
		err := fn(p, info)
		s.cancel()
		return err
	})
}

// A Save all cancelled mid-batch writes nothing after the file in hand, though
// files already fetched still arrive.
func TestSaveAllWritesNothingAfterCancel(t *testing.T) {
	h, atts := openWithAttachments(t, "a.pdf", "b.pdf")
	ctx, cancel := context.WithCancel(context.Background())
	h.app.svc = drainFetch{h.app.svc, cancel}
	dir := t.TempDir()
	saved, err := h.app.writeAttachmentsToDir(ctx, dir, atts, attConflictKeepBoth, nil)
	if !errors.Is(err, context.Canceled) || saved != 1 {
		t.Fatalf("saved %d, %v; want 1, context.Canceled", saved, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.pdf")); !os.IsNotExist(err) {
		t.Errorf("b.pdf written after the cancel: %v", err)
	}
}
