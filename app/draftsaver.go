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
// send, delete) run through Run, after the saves before them. A stale save
// is never retried or forced: the saver keeps the local snapshot and waits
// for the user to choose (KeepMine, UseLatest). UI thread only.
type draftSaver struct {
	a         *App
	id        int64
	snapshot  func() core.Draft                                           // the page's fields now; ID and Revision are the saver's
	composing func() bool                                                 // an input method is composing: saving waits
	save      func(ctx context.Context, d core.Draft) (core.Draft, error) // tests wrap it

	gen      uint64 // bumped per local edit
	savedGen uint64 // the generation the Service holds
	saving   uint64 // the generation of the save in flight; 0 for none
	rev      int64  // the stored revision, as the last write left it
	savedAt  time.Time

	busy    bool          // a write runs
	want    bool          // a save is due once the running write ends
	waiters []func(error) // Flush's, told once nothing is left to write
	lastErr error         // the last save's error; nil once one succeeds
	timer   func()        // stops the pending debounce

	conflict *core.Draft // the stored draft, newer than the snapshot; nil without a conflict
	stopped  bool        // sent or discarded: never saves again
}

func newDraftSaver(a *App, d core.Draft, snapshot func() core.Draft, composing func() bool) *draftSaver {
	return &draftSaver{a: a, id: d.ID, rev: d.Revision, savedAt: d.UpdatedAt, snapshot: snapshot, composing: composing, save: a.svc.SaveDraft}
}

func (s *draftSaver) key() string { return fmt.Sprintf("draft:%d", s.id) }

// Edited notes a local edit and schedules a save.
func (s *draftSaver) Edited() {
	s.gen++
	s.Schedule()
}

// Schedule saves once editing has paused for saveDelay, later while an
// input method composes.
func (s *draftSaver) Schedule() {
	if s.stopped || s.conflict != nil {
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

// Flush cancels the debounce, waits for the write in flight and saves the
// newest snapshot; done gets the last save's error (ErrStaleDraft in a
// conflict), nil once everything is saved.
func (s *draftSaver) Flush(done func(err error)) {
	s.stopTimer()
	s.requestSave()
	s.waiters = append(s.waiters, done)
	s.pump()
}

// Run flushes, then runs fn as the draft's only write: fn gets the stored
// revision and returns the draft as it left it (a zero Draft leaves the
// revision alone; ErrStaleDraft with a draft is a conflict with it). done
// gets fn's error, or the flush's without running fn.
func (s *draftSaver) Run(fn func(ctx context.Context, rev int64) (core.Draft, error), done func(err error)) {
	s.Flush(func(err error) {
		if err != nil && !s.stopped { // a stopped draft is being discarded: its saves no longer matter
			done(err)
			return
		}
		if s.busy { // another waiter started a write first
			s.Run(fn, done)
			return
		}
		s.busy = true
		rev := s.rev
		var d core.Draft
		s.submit(func(ctx context.Context) (err error) {
			d, err = fn(ctx, rev)
			return err
		}, func(err error) {
			s.busy = false
			s.stored(d, err)
			done(err)
			s.pump()
		})
	})
}

// pump starts the due save when no write runs, or tells the waiters that
// nothing is left.
func (s *draftSaver) pump() {
	if s.busy {
		return
	}
	if s.want && !s.stopped && s.conflict == nil {
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
	gen, save := s.gen, s.save
	s.saving = gen
	var out core.Draft
	s.submit(func(ctx context.Context) (err error) {
		out, err = save(ctx, d)
		return staleLatest(ctx, s.a.svc, d.ID, &out, err)
	}, func(err error) {
		s.busy, s.saving = false, 0
		s.lastErr = err
		if err == nil {
			s.savedGen, s.savedAt = gen, s.a.clock.Now()
		}
		s.stored(out, err)
		s.pump()
	})
}

// stored takes in the draft a write left: its revision, or a conflict.
func (s *draftSaver) stored(d core.Draft, err error) {
	switch {
	case d.ID == 0:
	case err == nil:
		s.rev = d.Revision
	case errors.Is(err, core.ErrStaleDraft):
		s.conflict, s.lastErr, s.want = &d, core.ErrStaleDraft, false
		s.stopTimer()
	}
}

// staleLatest reads the stored draft into *latest when err is ErrStaleDraft.
func staleLatest(ctx context.Context, svc *core.Service, id int64, latest *core.Draft, err error) error {
	if errors.Is(err, core.ErrStaleDraft) {
		if d, gerr := svc.GetDraft(ctx, id); gerr == nil {
			*latest = d
		}
	}
	return err
}

func (s *draftSaver) submit(fn func(ctx context.Context) error, done func(err error)) {
	s.a.writes.Submit(s.key(), s.gen, fn, func(_ uint64, err error) { done(err) })
}

// KeepMine resolves a conflict by saving the local snapshot over the latest.
func (s *draftSaver) KeepMine() {
	if s.conflict == nil {
		return
	}
	s.rev, s.conflict, s.lastErr = s.conflict.Revision, nil, nil
	s.gen++ // the snapshot differs from what is stored
	s.requestSave()
	s.pump()
}

// UseLatest resolves a conflict by dropping the local snapshot; the page
// shows the latest draft, which it returns.
func (s *draftSaver) UseLatest() core.Draft {
	d := *s.conflict
	s.rev, s.conflict, s.lastErr = d.Revision, nil, nil
	s.gen++
	s.savedGen, s.savedAt = s.gen, s.a.clock.Now()
	return d
}

// Stop ends saving: the draft was sent or is being discarded.
func (s *draftSaver) Stop() {
	s.stopped = true
	s.stopTimer()
}

// status is the title line's note: saving, saved at, or unsaved.
func (s *draftSaver) status() string {
	switch {
	case s.saving != 0:
		return i18n.T("compose.status.saving")
	case s.gen == s.savedGen && s.conflict == nil:
		return i18n.Tf("compose.status.saved", s.savedAt.In(s.a.clock.Now().Location()).Format("15:04"))
	}
	return i18n.T("compose.status.unsaved")
}
