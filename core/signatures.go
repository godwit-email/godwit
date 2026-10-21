package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
)

// Signature is a signature accounts share. Name is only the user's, to tell
// them apart; HTML is as stored: SignatureHTMLPrefix and sanitized editor
// HTML, or plain text (the CLI's, the Local API's); "" is empty. Accounts are
// the accounts using it by default, in creation order.
type Signature struct {
	ID       int64
	Name     string
	HTML     string
	Accounts []int64
}

// SignaturePatch changes the signature's fields that are not nil. HTML is as
// AccountPatch.Signature: marked editor HTML (its data: images become the
// signature's), or plain text.
type SignaturePatch struct{ Name, HTML *string }

// MaxSignatureName is the most characters a signature's name has.
const MaxSignatureName = 64

var (
	// ErrSignatureNotFound is a signature id that does not exist.
	ErrSignatureNotFound = errors.New("signature not found")
	// ErrSignatureName is a name that is empty or over MaxSignatureName.
	ErrSignatureName = errors.New("a signature's name is 1 to 64 characters")
)

// ListSignatures returns every signature, oldest first.
func (s *Service) ListSignatures(ctx context.Context) ([]Signature, error) {
	return s.signatures(ctx, 0)
}

// signatures are every signature, or only id's (id > 0).
func (s *Service) signatures(ctx context.Context, id int64) ([]Signature, error) {
	rows, err := s.db.Query(ctx, "SELECT id, name, html FROM signatures WHERE ?1 = 0 OR id = ?1 ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	users, err := s.db.Query(ctx, "SELECT signature_id, id FROM accounts WHERE signature_id IS NOT NULL ORDER BY id")
	if err != nil {
		return nil, err
	}
	by := map[int64][]int64{}
	for _, r := range users.Values {
		by[store.Int(r[0])] = append(by[store.Int(r[0])], store.Int(r[1]))
	}
	out := make([]Signature, 0, len(rows.Values))
	for _, r := range rows.Values {
		sid := store.Int(r[0])
		out = append(out, Signature{ID: sid, Name: store.Str(r[1]), HTML: store.Str(r[2]), Accounts: append([]int64{}, by[sid]...)})
	}
	return out, nil
}

func (s *Service) signature(ctx context.Context, id int64) (Signature, error) {
	sigs, err := s.signatures(ctx, id)
	if err != nil {
		return Signature{}, err
	}
	if id <= 0 || len(sigs) == 0 {
		return Signature{}, fmt.Errorf("signature %d: %w", id, ErrSignatureNotFound)
	}
	return sigs[0], nil
}

func cleanSignatureName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxSignatureName {
		return "", ErrSignatureName
	}
	return name, nil
}

// CreateSignature makes an empty signature named name, used by no account.
func (s *Service) CreateSignature(ctx context.Context, name string) (Signature, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Signature{}, err
	}
	defer end()
	if name, err = cleanSignatureName(name); err != nil {
		return Signature{}, err
	}
	s.sigMu.Lock()
	defer s.sigMu.Unlock()
	res, err := s.db.Execute(ctx, "INSERT INTO signatures(name) VALUES (?)", name)
	if err != nil {
		return Signature{}, err
	}
	s.bus.Publish(events.Event{Type: events.SignaturesChanged})
	return s.signature(ctx, res.LastInsertID)
}

// UpdateSignature writes p's fields: every account using the signature
// writes with it from then on. Too many images is ErrSignatureImageCount, an
// image it cannot take ErrSignatureImage; nothing is saved then.
func (s *Service) UpdateSignature(ctx context.Context, id int64, p SignaturePatch) (Signature, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Signature{}, err
	}
	defer end()
	s.sigMu.Lock()
	defer s.sigMu.Unlock()
	if _, err := s.signature(ctx, id); err != nil {
		return Signature{}, err
	}
	var stmts []sqlite.Statement
	if p.Name != nil {
		name, err := cleanSignatureName(*p.Name)
		if err != nil {
			return Signature{}, err
		}
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE signatures SET name = ? WHERE id = ?", Args: []any{name, id}})
	}
	if p.HTML != nil {
		w, err := s.signatureWrite(ctx, id, *p.HTML)
		if err != nil {
			return Signature{}, err
		}
		stmts = append(stmts, w...)
	}
	if len(stmts) > 0 {
		if _, err := s.db.Transaction(ctx, stmts); err != nil {
			return Signature{}, err
		}
		s.bus.Publish(events.Event{Type: events.SignaturesChanged})
	}
	return s.signature(ctx, id)
}

// signatureWrite are the statements storing html as signature id's: its
// data: images become the signature's, the HTML is sanitized, and the images
// nothing shows go. Run under sigMu.
func (s *Service) signatureWrite(ctx context.Context, id int64, html string) ([]sqlite.Statement, error) {
	var stmts []sqlite.Statement
	if rest, ok := strings.CutPrefix(html, SignatureHTMLPrefix); ok {
		known, err := s.signatureCIDs(ctx)
		if err != nil {
			return nil, err
		}
		if rest, stmts, err = signatureAssets(id, rest, known); err != nil {
			return nil, err
		}
		html = SignatureHTMLPrefix + rest
	}
	return append(stmts,
		sqlite.Statement{SQL: "UPDATE signatures SET html = ? WHERE id = ?", Args: []any{html, id}},
		unusedSignatureImages), nil
}

// DeleteSignature removes the signature: the accounts using it write
// without one. Its images stay while a draft shows them.
func (s *Service) DeleteSignature(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.sigMu.Lock()
	defer s.sigMu.Unlock()
	if _, err := s.db.Transaction(ctx, []sqlite.Statement{
		{SQL: "UPDATE accounts SET signature_id = NULL WHERE signature_id = ?", Args: []any{id}},
		{SQL: "DELETE FROM signatures WHERE id = ?", Args: []any{id}},
		unusedSignatureImages,
	}); err != nil {
		return err
	}
	s.bus.Publish(events.Event{Type: events.SignaturesChanged})
	return nil
}

// accountSignature are the statements of an AccountPatch's signature fields
// for acc: SignatureID picks one (0 none, else one that exists); Signature
// rewrites the one it then has, or makes one named as the account. Run
// under sigMu.
func (s *Service) accountSignature(ctx context.Context, acc Account, p AccountPatch) ([]sqlite.Statement, error) {
	var stmts []sqlite.Statement
	sigID := acc.SignatureID
	if p.SignatureID != nil {
		if sigID = *p.SignatureID; sigID != 0 {
			if _, err := s.signature(ctx, sigID); err != nil {
				return nil, err
			}
		}
		stmts = append(stmts, sqlite.Statement{SQL: "UPDATE accounts SET signature_id = ? WHERE id = ?", Args: []any{nullID(sigID), acc.ID}})
	}
	if p.Signature == nil || sigID == 0 && *p.Signature == "" {
		return stmts, nil
	}
	if sigID == 0 { // a new one, with the next id: the images need it
		rows, err := s.db.Query(ctx, `SELECT max(COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'signatures'), 0),
			COALESCE((SELECT max(id) FROM signatures), 0)) + 1`)
		if err != nil {
			return nil, err
		}
		sigID = store.Int(rows.Values[0][0])
		if p.Name != nil { // named as the account will be
			acc.Name = *p.Name
		}
		if p.Alias != nil {
			acc.Alias = *p.Alias
		}
		stmts = append(stmts,
			sqlite.Statement{SQL: "INSERT INTO signatures(id, name) VALUES (?, ?)", Args: []any{sigID, accountLabel(acc)}},
			sqlite.Statement{SQL: "UPDATE accounts SET signature_id = ? WHERE id = ?", Args: []any{sigID, acc.ID}})
	}
	w, err := s.signatureWrite(ctx, sigID, *p.Signature)
	if err != nil {
		return nil, err
	}
	return append(stmts, w...), nil
}

// accountLabel is the account's display name: its alias, else its sender
// name, else its address (as the migration names its signature), cut to
// MaxSignatureName.
func accountLabel(acc Account) string {
	name := acc.Email
	if n := strings.TrimSpace(acc.Name); n != "" {
		name = n
	}
	if a := strings.TrimSpace(acc.Alias); a != "" {
		name = a
	}
	if r := []rune(name); len(r) > MaxSignatureName {
		name = string(r[:MaxSignatureName])
	}
	return name
}

// SignaturePreview is a stored signature's text (Signature.HTML), its first n
// non-empty lines: a list row's preview.
func SignaturePreview(html string, n int) []string {
	text := html
	if rest, ok := strings.CutPrefix(html, SignatureHTMLPrefix); ok {
		text = mail.HTMLToText(rest)
	}
	var out []string
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line != "" && len(out) < n {
			out = append(out, line)
		}
	}
	return out
}
