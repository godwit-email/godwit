package webui

import (
	"context"
	"fmt"
	"sync"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/internal/i18n"
)

var (
	// SignatureAccount tells the signature page which account to show: it
	// saves the shown one's pending edit first.
	SignatureAccount = mygo.NewEvent[int64]("signatureAccount")
	// SignatureFlush asks the signature page to save now; it answers with
	// SignaturePage.Saved and the same token.
	SignatureFlush = mygo.NewEvent[int64]("signatureFlush")
)

// signatureKeys are the i18n keys the signature page shows.
var signatureKeys = []string{"compose.bold", "compose.italic", "compose.underline", "compose.link",
	"compose.link.url", "compose.link.apply", "settings.account.signature.label", "error.unknown"}

// SignatureState is what the signature page needs besides a signature:
// the account shown (0 before native showed one), its copy and theme.
type SignatureState struct {
	Account int64             `json:"account"`
	Text    map[string]string `json:"text"`
	Theme   Theme             `json:"theme"`
}

// Signature keeps the signature page's state (role "signature", embedded
// in the account settings): the account native shows and the flushes
// waiting for the page. It is not bound: the page reaches it only through
// SignaturePage.
type Signature struct {
	pages *Pages

	mu      sync.Mutex
	svc     *core.Service
	account int64
	next    int64
	waiting map[int64]func(ok bool)
}

// NewSignature is the signature page's state; SetService opens it up.
func NewSignature(pages *Pages) *Signature {
	return &Signature{pages: pages, waiting: map[int64]func(bool){}}
}

// SetService gives the page the service, once the data is open.
func (s *Signature) SetService(svc *core.Service) {
	s.mu.Lock()
	s.svc = svc
	s.mu.Unlock()
}

// Show records the account native shows, for a page that loads after
// the SignatureAccount event went out.
func (s *Signature) Show(account int64) {
	s.mu.Lock()
	s.account = account
	s.mu.Unlock()
}

// Flush asks the page to save now through emit (SignatureFlush); done
// hears its answer, once, on the page's goroutine.
func (s *Signature) Flush(emit func(token int64), done func(ok bool)) {
	s.mu.Lock()
	s.next++
	tok := s.next
	s.waiting[tok] = done
	s.mu.Unlock()
	emit(tok)
}

// SignaturePage is what the signature page calls (mygo.Bind).
type SignaturePage struct{ s *Signature }

// NewSignaturePage is the bound facade of s.
func NewSignaturePage(s *Signature) *SignaturePage { return &SignaturePage{s: s} }

func (s *Signature) caller(ctx context.Context) (*core.Service, error) {
	if _, err := s.pages.Caller(ctx, "signature"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.svc == nil {
		return nil, ErrNotReady
	}
	return s.svc, nil
}

// State is the account shown, the page's copy and the theme.
func (p *SignaturePage) State(ctx context.Context) (SignatureState, error) {
	svc, err := p.s.caller(ctx)
	if err != nil {
		return SignatureState{}, err
	}
	st, err := svc.GetSettings(ctx)
	if err != nil {
		return SignatureState{}, err
	}
	text := make(map[string]string, len(signatureKeys))
	for _, k := range signatureKeys {
		text[k] = i18n.T(k)
	}
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	return SignatureState{Account: p.s.account, Text: text, Theme: ThemeOf(st)}, nil
}

// Load is account's signature as the editor's HTML: sanitized, a plain
// one escaped into a paragraph.
func (p *SignaturePage) Load(ctx context.Context, account int64) (string, error) {
	svc, err := p.s.caller(ctx)
	if err != nil {
		return "", err
	}
	accs, err := svc.ListAccounts(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range accs {
		if a.ID == account {
			return core.SignatureEditorHTML(a.Signature), nil
		}
	}
	return "", fmt.Errorf("account %d not found", account)
}

// Save stores html as account's signature, marked as editor HTML ("" for
// none); drafts sanitize it as they take it.
func (p *SignaturePage) Save(ctx context.Context, account int64, html string) error {
	svc, err := p.s.caller(ctx)
	if err != nil {
		return err
	}
	if html != "" {
		html = core.SignatureHTMLPrefix + html
	}
	_, err = svc.UpdateAccount(ctx, account, core.AccountPatch{Signature: &html})
	return err
}

// Saved answers the flush token: ok is whether the page's save succeeded.
func (p *SignaturePage) Saved(ctx context.Context, token int64, ok bool) error {
	if _, err := p.s.pages.Caller(ctx, "signature"); err != nil {
		return err
	}
	p.s.mu.Lock()
	done := p.s.waiting[token]
	delete(p.s.waiting, token)
	p.s.mu.Unlock()
	if done != nil {
		done(ok)
	}
	return nil
}
