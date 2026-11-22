package app

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/godwit-email/godwit/core"
)

// A download (下载, ADR 0044) is a set of attachments and where they go: its
// run fetches them into the cache through FetchAttachments and writes them
// there. The App runs one at a time, oldest first; the queue lives on the UI
// thread.
type download struct {
	kind   downloadKind
	dest   any // the save path, or the folder with its conflict choice; nil to open
	ids    []int64
	run    func(ctx context.Context) error // off the UI thread
	done   func(err error)                 // UI thread
	ctx    context.Context
	cancel context.CancelFunc
}

// downloadKind is the kind of destination: the same attachments queued twice
// for one destination download once.
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
// or download, for the same destination (dest, comparable). A cancelled one
// still ending does not count. UI thread.
func (a *App) download(kind downloadKind, dest any, items []core.AttachmentInfo, run func(ctx context.Context) error, done func(error)) {
	ids := make([]int64, len(items))
	for i, at := range items {
		ids[i] = at.ID
	}
	if slices.ContainsFunc(a.downloads, func(d *download) bool {
		return d.kind == kind && d.dest == dest && slices.Equal(d.ids, ids) && d.ctx.Err() == nil
	}) {
		return
	}
	ctx, cancel := context.WithCancel(a.ctx) // Close cancels them all
	staged := func(err error) {
		if !errors.Is(err, context.Canceled) { // what reached the cache, even if writing it out failed, can drag out
			a.stageCached(items)
		}
		if done != nil {
			done(err)
		}
	}
	a.downloads = append(a.downloads, &download{kind, dest, ids, run, staged, ctx, cancel})
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

// stageCached gives each of items already in the cache a copy under its own
// name, in a directory Close removes, for its chip to drag out (ADR 0044,
// phase 1): a drag must hand over a file the moment it starts. It asks the
// cache only, so nothing downloads, and keeps a copy for the run while its
// file is there. UI thread.
func (a *App) stageCached(items []core.AttachmentInfo) {
	var files []core.AttachmentInfo
	var ids []int64
	had := map[int64]string{}
	for _, at := range items {
		if at.Inline {
			continue
		}
		files, ids = append(files, at), append(ids, at.ID)
		if p, ok := a.staged[at.ID]; ok {
			had[at.ID] = p
		}
	}
	if len(files) == 0 {
		return
	}
	plat, staged := a.plat, map[int64]string{}
	a.do(func(ctx context.Context) error {
		cached, err := a.svc.CachedAttachments(ctx, ids)
		if err != nil {
			return err
		}
		for _, at := range files {
			if p, ok := had[at.ID]; ok {
				if _, err := os.Stat(p); err == nil {
					staged[at.ID] = p
					continue
				}
			}
			src, ok := cached[at.ID]
			if !ok {
				continue
			}
			dir, err := a.tempDir(plat.TempDir())
			if err != nil {
				return err
			}
			dst := filepath.Join(dir, core.FileName(at.Filename, "attachment"))
			if cloneFile(src, dst) == nil {
				staged[at.ID] = dst
			}
		}
		return nil
	}, func(error) { // best effort: an unstaged chip still opens and saves
		if a.staged == nil {
			a.staged = map[int64]string{}
		}
		maps.Copy(a.staged, staged)
	})
}

// cancelDownload cancels every download of attachment id, with the rest of
// its batch: a waiting one leaves the queue, the running one's fetch stops
// and a Save all says how many it had saved. UI thread.
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
