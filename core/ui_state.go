package core

import "context"

type uiSessionKey struct{}

// uiSession holds the UI state reported by one RoleUI connection (or localUI
// when called directly in-process).
type uiSession struct {
	foreground bool
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
	s.uiMu.Unlock()
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
	fg := s.uiForegroundLocked()
	s.SetInboxOnly(!fg && st.BackgroundSync != BackgroundSyncAll)
	s.uiMu.Unlock()
}

// UIConnected reports whether any RoleUI client connection is currently open.
func (s *Service) UIConnected() bool {
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	return len(s.uiSessions) > 0
}

// UIForeground reports whether any connected RoleUI client (or an in-process UI
// caller) has its main window in the foreground.
func (s *Service) UIForeground() bool {
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	return s.uiForegroundLocked()
}

func (s *Service) uiForegroundLocked() bool {
	if s.localUI.foreground {
		return true
	}
	for sess := range s.uiSessions {
		if sess.foreground {
			return true
		}
	}
	return false
}

// IsComposeOpen reports whether draftID currently has an open compose window in
// any connected RoleUI client (or in-process UI).
func (s *Service) IsComposeOpen(draftID int64) bool {
	if s.opts.ComposeOpen != nil && s.opts.ComposeOpen(draftID) {
		return true
	}
	s.uiMu.Lock()
	defer s.uiMu.Unlock()
	if _, ok := s.localUI.compose[draftID]; ok {
		return true
	}
	for sess := range s.uiSessions {
		if _, ok := sess.compose[draftID]; ok {
			return true
		}
	}
	return false
}

// SetUIForeground records whether the calling UI session's main window is in
// the foreground, updating background sync narrowing accordingly.
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
	if sess := uiSessionFromContext(ctx); sess != nil {
		if _, ok := s.uiSessions[sess]; !ok {
			s.uiMu.Unlock()
			return nil
		}
		sess.foreground = on
	} else {
		s.localUI.foreground = on
	}
	fg := s.uiForegroundLocked()
	s.SetInboxOnly(!fg && st.BackgroundSync != BackgroundSyncAll)
	s.uiMu.Unlock()
	return nil
}

// SetComposeOpen records the draft IDs with open compose windows in the calling
// UI session, replacing that session's previous list.
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
		if _, ok := s.uiSessions[sess]; !ok {
			return nil
		}
		sess.compose = open
	} else {
		s.localUI.compose = open
	}
	return nil
}
