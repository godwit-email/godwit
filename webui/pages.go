package webui

import (
	"context"
	"errors"
	"sync"
)

// ErrWrongPage is returned to a caller that is not a registered page of the
// role a method serves: a window, another role's page, or none.
var ErrWrongPage = errors.New("this page may not call that")

// Target is where Wit events go: a window or a registered page. Exactly one
// is set; page IDs come from Pages.Register.
type Target struct {
	Window int
	Page   int
}

// Pages keeps the embedded pages and their roles. A page is known by an
// opaque key (its *mygo.Page in production) and gets an ID of its own.
type Pages struct {
	pageOf func(context.Context) any
	mu     sync.Mutex
	next   int
	byKey  map[any]pageEntry
	byID   map[int]any
}

type pageEntry struct {
	id   int
	role string
}

// NewPages is a registry with no pages; pageOf is the calling page's key
// (mygo.CallerPage(ctx) in production).
func NewPages(pageOf func(context.Context) any) *Pages {
	return &Pages{pageOf: pageOf, byKey: map[any]pageEntry{}, byID: map[int]any{}}
}

// Register makes the page with key one of role and returns its ID, new on
// every call; Unregister it when the page is destroyed.
func (p *Pages) Register(key any, role string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.byKey[key]; ok {
		delete(p.byID, old.id)
	}
	p.next++
	p.byKey[key] = pageEntry{p.next, role}
	p.byID[p.next] = key
	return p.next
}

// Unregister forgets the page with key: its calls get ErrWrongPage.
func (p *Pages) Unregister(key any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.byKey[key]; ok {
		delete(p.byID, e.id)
		delete(p.byKey, key)
	}
}

// Caller is the calling page's ID, if it is a registered page of role;
// else ErrWrongPage.
func (p *Pages) Caller(ctx context.Context, role string) (int, error) {
	key := p.pageOf(ctx)
	if key == nil {
		return 0, ErrWrongPage
	}
	p.mu.Lock()
	e, ok := p.byKey[key]
	p.mu.Unlock()
	if !ok || e.role != role {
		return 0, ErrWrongPage
	}
	return e.id, nil
}

// Key is the key page id was registered with, while it is.
func (p *Pages) Key(id int) (any, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	k, ok := p.byID[id]
	return k, ok
}
