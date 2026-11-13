package core

import (
	"context"
	"errors"
)

type uiSessionKey struct{}

// uiSession holds the UI state reported by one RoleUI connection.
type uiSession struct {
	foreground bool
	ready      bool // the interface takes requests on its events (SetUIReady)
	compose    map[int64]struct{}
}

// WithUISession binds a fresh UI session to ctx's lifetime for a RoleUI
// connection; cancelling ctx clears the session's foreground and compose state.
func WithUISession(ctx context.Context, s *Service) context.Context {
	sess := &uiSession{}
	s.uiMu.Lock()
	if s.uiSessions == nil {
		s.uiSessions = map[*uiSession]struct{}{}
	}
	s.uiSessions[sess] = struct{}{}
	s.uiMu.Unlock()
	context.AfterFunc(ctx, func() { s.removeUISession(sess) })
	return context.WithValue(ctx, uiSessionKey{}, sess)
}

func uiSessionFromContext(ctx context.Context) *uiSession {
	sess, _ := ctx.Value(uiSessionKey{}).(*uiSession)
	return sess
}

func (s *Service) removeUISession(sess *uiSession) {
	s.uiMu.Lock()
	prevFG := s.uiForegroundLocked()
	delete(s.uiSessions, sess)
	nextFG := s.uiForegroundLocked()
	var stop func()
	if len(s.uiSessions) == 0 {
		stop, s.quitStop = s.quitStop, nil // the tray's 退出并停止收信: the interface has quit
	}
	s.uiMu.Unlock()
	if stop != nil {
		stop()
		return
	}
	if prevFG == nextFG {
		return
	}
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	defer end()
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	st, err := s.loadSettings(ctx)
	if err != nil {
		return
	}
	s.applyUIBackgroundSync(st)
}

func (s *Service) applyUIBackgroundSync(st Settings) {
	s.uiMu.Lock()
	s.applyUIBackgroundSyncLocked(st)
	s.uiMu.Unlock()
}

// applyUIBackgroundSyncLocked narrows background rounds to the inbox while
// no interface is in front, unless 后台同步 syncs every folder; uiMu is held.
func (s *Service) applyUIBackgroundSyncLocked(st Settings) {
	s.SetInboxOnly(!s.uiForegroundLocked() && st.BackgroundSync != BackgroundSyncAll)
}

// ErrNoUI: a Wit run or a plugin action that waits on the user's approval,
// asked over the Local API while no Godwit window is open to take it.
var ErrNoUI = errors.New("this needs the user's approval in Godwit, and no Godwit window is open; ask again once the user has opened Godwit")

// needUI is ErrNoUI for a call over the Local API (WithExternalSource) while
// no window can take the approvals it waits on: they would wait unseen.
func (s *Service) needUI(ctx context.Context) error {
	if _, ext := ctx.Value(externalSourceKey{}).(string); ext && !s.UIConnected() {
		return ErrNoUI
	}
	return nil
}

// UIConnected reports whether any RoleUI client connection is currently open.
func (s *Service) UIConnected() bool {
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	return len(s.uiSessions) > 0
}

// UIReady reports whether a connected interface takes requests on its events
// (SetUIReady): one still starting or already quitting would drop them.
func (s *Service) UIReady() bool {
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	for sess := range s.uiSessions {
		if sess.ready {
			return true
		}
	}
	return false
}

// SetUIReady records whether the calling interface takes requests on its
// events: on once it has started, off as it closes for a quit.
func (s *Service) SetUIReady(ctx context.Context, on bool) error {
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	if sess := uiSessionFromContext(ctx); sess != nil {
		if _, ok := s.uiSessions[sess]; ok {
			sess.ready = on
		}
	}
	return nil
}

// StopWhenUIQuits arms the tray's 退出并停止收信 while an interface quits
// first: stop runs once the last interface has gone or asks to stop the
// backend (StopUIBackend), whatever 后台收信 says or who started the backend.
// An interface that comes back to the front kept its quit and disarms it.
func (s *Service) StopWhenUIQuits(stop func()) {
	s.uiMu.Lock()
	s.quitStop = stop
	s.uiMu.Unlock()
}

func (s *Service) takeQuitStop() func() {
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	stop := s.quitStop
	s.quitStop = nil
	return stop
}

// UIForeground reports whether any connected RoleUI client has its main
// window in the foreground.
func (s *Service) UIForeground() bool {
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	return s.uiForegroundLocked()
}

func (s *Service) uiForegroundLocked() bool {
	for sess := range s.uiSessions {
		if sess.foreground {
			return true
		}
	}
	return false
}

// IsComposeOpen reports whether draftID currently has an open compose window in
// any connected RoleUI client.
func (s *Service) IsComposeOpen(draftID int64) bool {
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	for sess := range s.uiSessions {
		if _, ok := sess.compose[draftID]; ok {
			return true
		}
	}
	return false
}

// SetUIForeground records whether the calling UI session's main window is in
// the foreground, updating background sync narrowing accordingly. Called
// without a UI session it changes nothing.
func (s *Service) SetUIForeground(ctx context.Context, on bool) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	st, err := s.loadSettings(ctx)
	if err != nil {
		return err
	}
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	sess := uiSessionFromContext(ctx)
	if _, ok := s.uiSessions[sess]; sess == nil || !ok {
		return nil
	}
	sess.foreground = on
	if on {
		s.quitStop = nil // back in front: the user kept the tray's quit
	}
	s.applyUIBackgroundSyncLocked(st)
	return nil
}

// SetComposeOpen records the draft IDs with open compose windows in the calling
// UI session, replacing that session's previous list. Called without a UI
// session it changes nothing.
func (s *Service) SetComposeOpen(ctx context.Context, draftIDs []int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	var open map[int64]struct{}
	for _, id := range draftIDs {
		if id <= 0 {
			continue
		}
		if open == nil {
			open = make(map[int64]struct{}, len(draftIDs))
		}
		open[id] = struct{}{}
	}
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	if sess := uiSessionFromContext(ctx); sess != nil {
		if _, ok := s.uiSessions[sess]; ok {
			sess.compose = open
		}
	}
	return nil
}

// runCaller is ctx for a Wit run's call. The interface's (a ui session) is
// detached, as its pages asked in process: a page or the interface going does
// not end the run, only CancelRun or the Service closing. Other callers' end
// it with their ctx.
func runCaller(ctx context.Context) context.Context {
	if uiSessionFromContext(ctx) != nil {
		return context.WithoutCancel(ctx)
	}
	return ctx
}
