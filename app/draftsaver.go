package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
)

// saveDelay is the pause in editing after which a draft saves.
const saveDelay = time.Second

// draftSaver saves one draft: snapshots of the page's fields, one write at
// a time through the write queue (key draft:<id>), each tagged with the
// edit generation it holds. Other writes to the draft (account, attachments,
// send, delete) run through Run, after the saves before them. A stale write
// is never retried or forced: the saver keeps the local snapshot and waits
// for the user to choose (KeepMine, UseLatest). UI thread only.
type draftSaver struct {
	a         *App
	id        int64
	snapshot  func() core.Draft                                           // the page's fields now; ID and Revision are the saver's
	commit    func()                                                      // takes in what the page holds uncommitted, before a flush
	composing func() bool                                                 // an input method is composing: saving waits
	save      func(ctx context.Context, d core.Draft) (core.Draft, error) // tests wrap it
	getLatest func(ctx context.Context, id int64) (core.Draft, error)     // tests wrap it

	gen      uint64 // bumped per local edit
	savedGen uint64 // the generation the Service holds
	saving   uint64 // the generation of the save in flight; 0 for none
	rev      int64  // the stored revision, as the last write left it
	savedAt  time.Time

	busy    bool          // a write runs
	want    bool          // a save is due once the running write ends
	waiters []func(error) // Flush's, told once the newest generation is saved or cannot be
	lastErr error         // the last save's error; nil once one succeeds
	timer   func()        // stops the pending debounce

	conflict  bool        // a write was stale: no saves until the user chooses
	latest    *core.Draft // the stored draft, while in conflict; nil until read
	latestErr error       // reading it failed; Refetch tries again
	held      bool        // a delete runs: no saves meanwhile
	stopped   bool        // sent or deleted: never saves again
}

func newDraftSaver(a *App, d core.Draft) *draftSaver {
	return &draftSaver{a: a, id: d.ID, rev: d.Revision, savedAt: d.UpdatedAt, save: a.svc.SaveDraft, getLatest: a.svc.GetDraft}
}

func (s *draftSaver) key() string { return fmt.Sprintf("draft:%d", s.id) }

// Edited notes a local edit and schedules a save.
func (s *draftSaver) Edited() {
	s.gen++
	s.Schedule()
}

func (s *draftSaver) paused() bool { return s.stopped || s.held || s.conflict }

// Schedule saves once editing has paused for saveDelay, later while an
// input method composes.
func (s *draftSaver) Schedule() {
	if s.paused() {
		return
	}
	s.stopTimer()
	s.timer = s.a.after(saveDelay, func() {
		s.timer = nil
		if s.composing() {
			s.Schedule()
			return
		}
		s.requestSave()
		s.pump()
	})
}

func (s *draftSaver) stopTimer() {
	if s.timer != nil {
		s.timer()
		s.timer = nil
	}
}

// unsaved reports whether the newest edit is neither saved nor being saved.
func (s *draftSaver) unsaved() bool { return s.gen != s.savedGen && s.gen != s.saving }

func (s *draftSaver) requestSave() {
	if s.unsaved() {
		s.want = true
	}
}

// Flush cancels the debounce and calls done once the newest generation is
// saved, edits made meanwhile included; with the error that stopped it
// (ErrStaleDraft in a conflict) when it cannot be.
func (s *draftSaver) Flush(done func(err error)) {
	if s.commit != nil {
		s.commit()
	}
	s.stopTimer()
	s.requestSave()
	s.waiters = append(s.waiters, done)
	s.pump()
}

// Run flushes, then runs fn as the draft's only write: fn gets the stored
// revision and returns the draft as it left it, if it read it (its revision
// becomes the stored one, error or not). ErrStaleDraft is a conflict. done
// gets fn's error, or the flush's without running fn.
func (s *draftSaver) Run(fn func(ctx context.Context, rev int64) (core.Draft, error), done func(err error)) {
	s.Flush(func(err error) {
		if err != nil && !s.held { // a draft being deleted: its saves no longer matter
			done(err)
			return
		}
		if s.busy { // another waiter started a write first
			s.Run(fn, done)
			return
		}
		s.busy = true
		rev, get := s.rev, s.getLatest
		var d, latest core.Draft
		var lerr error
		s.submit(func(ctx context.Context) (err error) {
			d, err = fn(ctx, rev)
			if errors.Is(err, core.ErrStaleDraft) {
				latest, lerr = get(ctx, s.id)
			}
			return err
		}, func(err error) {
			s.busy = false
			s.stored(d, err, latest, lerr)
			done(err)
			s.pump()
		})
	})
}

// pump starts the due save when no write runs, or tells the waiters that
// nothing more can be saved.
func (s *draftSaver) pump() {
	if s.busy {
		return
	}
	retry := len(s.waiters) > 0 && s.unsaved() && s.lastErr == nil // edits since the flush began
	if (s.want || retry) && !s.paused() {
		s.want = false
		s.startSave()
		return
	}
	s.want = false
	ws := s.waiters
	s.waiters = nil
	for _, w := range ws {
		w(s.lastErr)
	}
}

func (s *draftSaver) startSave() {
	s.busy = true
	d := s.snapshot()
	d.ID, d.Revision = s.id, s.rev
	gen, save, get := s.gen, s.save, s.getLatest
	s.saving = gen
	var out, latest core.Draft
	var lerr error
	s.submit(func(ctx context.Context) (err error) {
		out, err = save(ctx, d)
		if errors.Is(err, core.ErrStaleDraft) {
			latest, lerr = get(ctx, d.ID)
		}
		return err
	}, func(err error) {
		s.busy, s.saving = false, 0
		s.lastErr = err
		if err == nil {
			s.savedGen, s.savedAt = gen, s.a.clock.Now()
		}
		s.stored(out, err, latest, lerr)
		s.pump()
	})
}

// stored takes in what a write left: a conflict, or the stored revision.
func (s *draftSaver) stored(d core.Draft, err error, latest core.Draft, lerr error) {
	switch {
	case errors.Is(err, core.ErrStaleDraft):
		s.conflict, s.lastErr, s.want = true, core.ErrStaleDraft, false
		s.setLatest(latest, lerr)
		s.stopTimer()
	case d.ID != 0:
		s.rev = d.Revision
	}
}

func (s *draftSaver) setLatest(d core.Draft, err error) {
	s.latest, s.latestErr = nil, err
	if err == nil {
		s.latest = &d
	}
}

// Refetch reads the stored draft again after reading it failed in a conflict.
func (s *draftSaver) Refetch() {
	var d core.Draft
	get, id := s.getLatest, s.id
	s.latestErr = nil
	s.a.do(func(ctx context.Context) (err error) {
		d, err = get(ctx, id)
		return err
	}, func(err error) {
		if s.conflict {
			s.setLatest(d, err)
		}
	})
}

func (s *draftSaver) submit(fn func(ctx context.Context) error, done func(err error)) {
	s.a.writes.Submit(s.key(), s.gen, fn, func(_ uint64, err error) { done(err) })
}

// KeepMine resolves a conflict by saving the local snapshot over the latest.
func (s *draftSaver) KeepMine() {
	if s.latest == nil {
		return
	}
	s.rev, s.conflict, s.latest, s.lastErr = s.latest.Revision, false, nil, nil
	s.gen++ // the snapshot differs from what is stored
	s.requestSave()
	s.pump()
}

// UseLatest resolves a conflict with d, the latest draft the page now
// shows: the local snapshot is dropped.
func (s *draftSaver) UseLatest(d core.Draft) {
	s.rev, s.conflict, s.latest, s.lastErr = d.Revision, false, nil, nil
	s.gen++
	s.savedGen, s.savedAt = s.gen, s.a.clock.Now()
	s.pump()
}

// Hold pauses saving while the draft is deleted; Release resumes it if the
// delete failed.
func (s *draftSaver) Hold() {
	s.held = true
	s.stopTimer()
}

func (s *draftSaver) Release() {
	s.held = false
	if s.unsaved() {
		s.Schedule()
	}
}

// Stop ends saving: the draft was sent or deleted.
func (s *draftSaver) Stop() {
	s.stopped = true
	s.stopTimer()
}

// status is the title line's note: saving, saved at, or unsaved.
func (s *draftSaver) status() string {
	switch {
	case s.saving != 0:
		return i18n.T("compose.status.saving")
	case s.gen == s.savedGen && !s.conflict:
		return i18n.Tf("compose.status.saved", s.savedAt.In(s.a.clock.Now().Location()).Format("15:04"))
	}
	return i18n.T("compose.status.unsaved")
}
