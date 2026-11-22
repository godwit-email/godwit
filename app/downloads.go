package app

import (
	"context"
	"slices"

	"github.com/godwit-email/godwit/core"
)

// A download (下载, ADR 0044) is a set of attachments and where they go: its
// run fetches them into the cache through FetchAttachments and writes them
// there. The App runs one at a time, oldest first; the queue lives on the UI
// thread.
type download struct {
	kind   downloadKind
	ids    []int64
	run    func(ctx context.Context) error // off the UI thread
	done   func(err error)                 // UI thread
	ctx    context.Context
	cancel context.CancelFunc
}

// downloadKind is the kind of destination: the same attachments queued twice
// for one kind download once.
type downloadKind int

const (
	downloadOpen   downloadKind = iota // a new temporary directory, then OpenPath
	downloadSave                       // the path the save panel chose
	downloadFolder                     // a folder, with the conflict choice
)

// downloadState is an attachment's place in the queue, for its chip.
type downloadState int

const (
	downloadNone downloadState = iota
	downloadQueued
	downloadRunning
)

// download queues run for items unless the same attachments already wait,
// or download, for the same kind of destination. UI thread.
func (a *App) download(kind downloadKind, items []core.AttachmentInfo, run func(ctx context.Context) error, done func(error)) {
	ids := make([]int64, len(items))
	for i, at := range items {
		ids[i] = at.ID
	}
	if slices.ContainsFunc(a.downloads, func(d *download) bool { return d.kind == kind && slices.Equal(d.ids, ids) }) {
		return
	}
	ctx, cancel := context.WithCancel(a.ctx) // Close cancels them all
	a.downloads = append(a.downloads, &download{kind, ids, run, done, ctx, cancel})
	if len(a.downloads) == 1 {
		a.runDownload()
	}
}

// runDownload runs the queue's head, which stays in the queue until it ends;
// then the next. UI thread.
func (a *App) runDownload() {
	d := a.downloads[0]
	go func() {
		err := d.run(d.ctx)
		a.onUI(func() {
			d.cancel()
			a.downloads = a.downloads[1:]
			if d.done != nil {
				d.done(err) // a cancelled one's context.Canceled toasts nothing
			}
			if len(a.downloads) > 0 {
				a.runDownload()
			}
		})
	}()
}

// downloadState says whether attachment id waits or downloads. UI thread.
func (a *App) downloadState(id int64) downloadState {
	for i, d := range a.downloads {
		if slices.Contains(d.ids, id) {
			if i == 0 {
				return downloadRunning
			}
			return downloadQueued
		}
	}
	return downloadNone
}

// cancelDownload cancels every download of attachment id: a waiting one
// leaves the queue, the running one's fetch stops. UI thread.
func (a *App) cancelDownload(id int64) {
	if len(a.downloads) == 0 {
		return
	}
	head := a.downloads[0]
	a.downloads = slices.DeleteFunc(a.downloads, func(d *download) bool {
		if !slices.Contains(d.ids, id) {
			return false
		}
		d.cancel()
		return d != head // the head leaves when its run returns
	})
}
