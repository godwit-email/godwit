package webui

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/egoist/mygo"
	"github.com/godwit-email/godwit/core"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/internal/i18n"
)

var (
	// SignatureShow tells the signature page which signature to show: it
	// saves the shown one's pending edit first.
	SignatureShow = mygo.NewEvent[int64]("signatureShow")
	// SignatureFlush asks the signature page to save now; it answers with
	// SignaturePage.Saved and the same token.
	SignatureFlush = mygo.NewEvent[int64]("signatureFlush")
)

// maxSignatureBytes caps a saved signature at a mail body's limit, plus its
// images as the editor sends them (data: URLs).
var maxSignatureBytes = mail.MaxBodyBytes + core.SignatureImagesMax*(base64.StdEncoding.EncodedLen(core.SignatureImageMax)+64)

// ErrSignatureGone is final: the saver (saver.ts) drops the pending edit.
var ErrSignatureGone = errors.New("signature gone")

// ErrSignatureTooLarge is returned for a signature over maxSignatureBytes.
var ErrSignatureTooLarge = errors.New("the signature is too large")

// Close forgets the page (its view closing): the flushes waiting get no
// answer, and later ones answer at once until a page runs again.
func (s *Signature) Close() {
	s.mu.Lock()
	s.live, s.waiting = false, map[int64]func(bool){}
	s.mu.Unlock()
}

// SignatureState is what the signature page needs besides a signature:
// the signature shown (0 before native showed one), its locale and theme.
type SignatureState struct {
	Signature int64  `json:"signature"`
	Locale    string `json:"locale"`
	Theme     Theme  `json:"theme"`
}

// Signature keeps the signature page's state (role "signature", embedded
// in Settings › Signatures): the signature native shows and the flushes
// waiting for the page. It is not bound: the page reaches it only through
// SignaturePage.
type Signature struct {
	pages *Pages
	pick  func(ctx context.Context) (string, error)

	// OnTall hears the page enter (true) or leave its HTML mode, which
	// needs a taller view; set before the page runs.
	OnTall func(tall bool)

	mu      sync.Mutex
	svc     *core.Service
	live    bool // the page called State: it ran, and may hold edits
	shown   int64
	next    int64
	waiting map[int64]func(ok bool)
}

// NewSignature is the signature page's state; SetService opens it up.
// pick asks for one image file, in a dialog on the calling page's window:
// its path, "" for none.
func NewSignature(pages *Pages, pick func(ctx context.Context) (string, error)) *Signature {
	return &Signature{pages: pages, pick: pick, waiting: map[int64]func(bool){}}
}

// SetService gives the page the service, once the data is open.
func (s *Signature) SetService(svc *core.Service) {
	s.mu.Lock()
	s.svc = svc
	s.mu.Unlock()
}

// Show records the signature native shows, for a page that loads after
// the SignatureShow event went out.
func (s *Signature) Show(id int64) {
	s.mu.Lock()
	s.shown = id
	s.mu.Unlock()
}

// Flush asks the page to save now through emit (SignatureFlush); done
// hears its answer, once, on the page's goroutine. A page that never ran
// holds no edit: done(true) at once.
func (s *Signature) Flush(emit func(token int64), done func(ok bool)) {
	s.mu.Lock()
	if !s.live {
		s.mu.Unlock()
		done(true)
		return
	}
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

// State is the signature shown, the locale and the theme.
func (p *SignaturePage) State(ctx context.Context) (SignatureState, error) {
	svc, err := p.s.caller(ctx)
	if err != nil {
		return SignatureState{}, err
	}
	st, err := svc.GetSettings(ctx)
	if err != nil {
		return SignatureState{}, err
	}
	locale := i18n.Language()
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	p.s.live = true
	return SignatureState{Signature: p.s.shown, Locale: locale, Theme: ThemeOf(st)}, nil
}

// Load is signature id as the editor's HTML: sanitized, a plain one
// escaped into a paragraph, its images as data: URLs.
func (p *SignaturePage) Load(ctx context.Context, id int64) (string, error) {
	svc, err := p.s.caller(ctx)
	if err != nil {
		return "", err
	}
	return svc.SignatureEditor(ctx, id)
}

// Code is signature id as stored, for the HTML mode: sanitized, its
// images as cid:.
func (p *SignaturePage) Code(ctx context.Context, id int64) (string, error) {
	svc, err := p.s.caller(ctx)
	if err != nil {
		return "", err
	}
	return svc.SignatureCode(ctx, id)
}

// Tall tells native the page entered (true) or left its HTML mode.
func (p *SignaturePage) Tall(ctx context.Context, tall bool) error {
	if _, err := p.s.caller(ctx); err != nil {
		return err
	}
	if p.s.OnTall != nil {
		p.s.OnTall(tall)
	}
	return nil
}

// PickImage asks for an image to insert and returns it as a data: URL, ""
// for none picked: a png, jpeg, gif or webp (by its content) of at most
// 512 KB, else core.ErrSignatureImage.
func (p *SignaturePage) PickImage(ctx context.Context) (string, error) {
	if _, err := p.s.caller(ctx); err != nil {
		return "", err
	}
	path, err := p.s.pick(ctx)
	if err != nil || path == "" {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// One read, capped: a file that grows after a size check cannot slip past it.
	data, err := io.ReadAll(io.LimitReader(f, core.SignatureImageMax+1))
	if err != nil {
		return "", err
	}
	return core.SignatureImageURL(data) // over SignatureImageMax: ErrSignatureImage
}

// Save stores html as signature id, marked as editor HTML ("" for empty),
// sanitized. Its data: images become the signature's (core.ErrSignatureImage,
// ErrSignatureImageCount).
func (p *SignaturePage) Save(ctx context.Context, id int64, html string) error {
	svc, err := p.s.caller(ctx)
	if err != nil {
		return err
	}
	if len(html) > maxSignatureBytes {
		return ErrSignatureTooLarge
	}
	if html != "" {
		html = core.SignatureHTMLPrefix + html
	}
	_, err = svc.UpdateSignature(ctx, id, core.SignaturePatch{HTML: &html})
	if errors.Is(err, core.ErrSignatureNotFound) {
		return ErrSignatureGone
	}
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
