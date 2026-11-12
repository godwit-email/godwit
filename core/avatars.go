package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"

	gomail "github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/store"
	"github.com/godwit-email/godwit/internal/avatar"
	"github.com/godwit-email/godwit/internal/imagecache"
	"github.com/godwit-email/godwit/internal/imagefetch"
)

// Avatar is a sender image; empty Data means there is no permitted image.
type Avatar struct {
	Data           []byte
	Format, Source string
	Width, Height  int
}

const msgInJunk = "EXISTS (SELECT 1 FROM message_locations l JOIN folders f ON f.id = l.folder_id WHERE l.message_id = m.id AND l.shown AND " + inJunk + ")"

// SenderAvatar loads the ordinary sender avatar under the current message's
// remote-image policy. allowRemote is the user's explicit permission for this message.
// Missing or unavailable sources are normal empty results.
func (s *Service) SenderAvatar(ctx context.Context, messageID int64, allowRemote bool) (Avatar, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Avatar{}, err
	}
	defer end()
	messageID, err = s.resolveID(ctx, messageID)
	if err != nil {
		return Avatar{}, err
	}
	rows, err := s.db.Query(ctx, "SELECT m.account_id, m.from_addr, m.auth_results, m.security_status, m.security_reason, "+msgInJunk+" FROM messages m WHERE m.id = ?", messageID)
	if err != nil {
		return Avatar{}, err
	}
	if len(rows.Values) == 0 {
		return Avatar{}, fmt.Errorf("message %d not found", messageID)
	}
	r := rows.Values[0]
	if err := s.bind(ctx, store.Int(r[0])); err != nil {
		return Avatar{}, err
	}
	if err := ctx.Err(); err != nil {
		return Avatar{}, err
	}
	from := store.Str(r[1])
	authRes := store.Str(r[2])
	secStatus := store.Str(r[3])
	secReason := store.Str(r[4])
	isJunk := store.Int(r[5]) != 0

	var sec gomail.SecurityReport
	if secStatus != "" {
		sec = gomail.DBToSecurityReport(from, authRes, secStatus, secReason)
	} else {
		sec = gomail.EvaluateSecurity(from, authRes)
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return Avatar{}, nil
	}
	email := gomail.AddrKey(from)
	if email == "" {
		return Avatar{}, nil
	}
	if !isJunk && sec.Status != gomail.SecurityFraud {
		if av, _, _, err := s.carddavAvatar(ctx, email, true); err != nil {
			return Avatar{}, err
		} else if len(av.Data) > 0 {
			return av, nil
		}
	}
	allowed := func() (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if allowRemote {
			return true, nil
		}
		return s.ImagesAllowed(ctx, messageID)
	}
	result := func(img imagecache.Image, source string) (Avatar, error) {
		if ok, err := allowed(); !ok || err != nil {
			return Avatar{}, err
		}
		return Avatar{Data: img.Data, Format: img.Format, Source: source, Width: img.Width, Height: img.Height}, nil
	}
	if ok, err := allowed(); !ok || err != nil {
		return Avatar{}, err
	}
	domain := email[strings.LastIndexByte(email, '@')+1:]
	sites := avatar.FaviconSites(domain)
	if len(sites) == 0 {
		return Avatar{}, nil
	}
	var providerDomains []string
	for _, provider := range s.ListProviders() {
		providerDomains = append(providerDomains, provider.Domains...)
	}
	if avatar.PersonalDomain(domain, providerDomains) {
		var policyErr error
		canFetch := func() bool {
			var ok bool
			ok, policyErr = allowed()
			return ok && policyErr == nil
		}
		img, ok := avatar.FindBIMI(ctx, s.opts.LookupTXT, s.images, domain, canFetch)
		if policyErr != nil {
			return Avatar{}, policyErr
		}
		if ok {
			return result(img, "bimi")
		}
		// Any site's cached icon first: an unreachable sender host (account.tiktok.com)
		// must not hold up its organization's icon (tiktok.com) on every message.
		for _, host := range sites {
			if img, ok, _ := s.images.LookupAvatar(ctx, imagecache.AvatarKey("favicon", host)); ok {
				return result(img, "favicon")
			}
		}
		for _, host := range sites {
			if ok, err := allowed(); !ok || err != nil {
				return Avatar{}, err
			}
			img, ok := avatar.FindFavicon(ctx, s.opts.ImageClient, s.images, host, canFetch)
			if policyErr != nil {
				return Avatar{}, policyErr
			}
			if ok {
				return result(img, "favicon")
			}
		}
	}
	if ok, err := allowed(); !ok || err != nil {
		return Avatar{}, err
	}
	gen, src := s.images.AvatarGeneration(), avatar.GravatarURL(email)
	img, err := s.images.GetAvatar(ctx, src, "gravatar", gen)
	if err == nil {
		return result(img, "gravatar")
	}
	if ctx.Err() != nil {
		return Avatar{}, ctx.Err()
	}
	if errors.Is(err, imagecache.ErrClosed) {
		return Avatar{}, err
	}
	if !errors.Is(err, imagefetch.ErrAvatarMiss) {
		if img, ok, _ := s.images.FallbackAvatar(ctx, imagecache.AvatarKey("gravatar", src), gen); ok {
			return result(img, "gravatar")
		}
	}
	return Avatar{}, nil
}

// ContactAvatar loads a contact's avatar for the native UI: an enabled
// address-book card's cached PHOTO across the contact's connected component
// first (even with zero received messages), else SenderAvatar on the newest
// received message across that component.
func (s *Service) ContactAvatar(ctx context.Context, addr string) (Avatar, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Avatar{}, err
	}
	defer end()
	if err := ctx.Err(); err != nil {
		return Avatar{}, err
	}
	key := gomail.AddrKey(addr)
	if key == "" || !strings.Contains(key, "@") {
		return Avatar{}, nil
	}
	av, compAddrs, isOwn, err := s.carddavAvatar(ctx, key, false)
	if err != nil || isOwn {
		return Avatar{}, err
	}
	if len(av.Data) > 0 {
		return av, nil
	}
	var headers []string
	seen := map[string]bool{}
	for _, k := range compAddrs {
		q, err := s.withSenders(ctx, SearchQuery{FromAddr: k})
		if err != nil {
			return Avatar{}, err
		}
		for _, h := range q.fromHeaders {
			if !seen[h] {
				seen[h] = true
				headers = append(headers, h)
			}
		}
	}
	if len(headers) == 0 {
		return Avatar{}, nil
	}
	left, err := s.roleFolderIDs(ctx, "sent", "drafts", "trash", "junk")
	if err != nil {
		return Avatar{}, err
	}
	hb, _ := json.Marshal(headers)
	args := append([]any{string(hb)}, left...)
	rows, err := s.db.Query(ctx, "SELECT m.id FROM messages m WHERE m.from_addr IN (SELECT value FROM json_each(?)) AND "+
		shownFolder+" IS NOT NULL AND NOT "+shownIn("("+placeholders(len(left))+")")+
		" ORDER BY m.date DESC, m.id DESC LIMIT 1", args...)
	if err != nil || len(rows.Values) == 0 {
		return Avatar{}, err
	}
	return s.SenderAvatar(ctx, store.Int(rows.Values[0][0]), false)
}

func scanCardDAVPhoto(r []any) Avatar {
	raw, _ := r[0].([]byte)
	if len(raw) == 0 {
		if str, ok := r[0].(string); ok {
			raw = []byte(str)
		}
	}
	if len(raw) == 0 {
		return Avatar{}
	}
	return Avatar{
		Data:   slices.Clone(raw),
		Format: store.Str(r[1]),
		Source: "carddav",
		Width:  int(store.Int(r[2])),
		Height: int(store.Int(r[3])),
	}
}

// carddavAvatar resolves email (an AddrKey) against enabled address-book cards.
// When allowOwn is true and email belongs to a local account, only cards directly
// holding email are matched; when allowOwn is false and email belongs to a local
// account, isOwn is returned true with an empty Avatar. For non-own addresses, it
// walks the connected component of enabled cards sharing non-own addresses and
// returns the highest-priority cached PHOTO along with all non-own AddrKeys in
// that component.
func (s *Service) carddavAvatar(ctx context.Context, email string, allowOwn bool) (av Avatar, compAddrs []string, isOwn bool, err error) {
	own, err := s.ownAddrSet(ctx)
	if err != nil {
		return Avatar{}, nil, false, err
	}
	if own[email] {
		if !allowOwn {
			return Avatar{}, nil, true, nil
		}
		rows, err := s.db.Query(ctx, `SELECT c.photo, c.photo_format, c.photo_width, c.photo_height
			FROM address_book_emails e
			JOIN address_book_cards c ON c.id = e.card_id
			JOIN address_book_collections col ON col.id = c.collection_id
			JOIN address_books ab ON ab.id = col.address_book_id
			WHERE e.addr_key = ? AND col.enabled = 1 AND length(c.photo) > 0
			ORDER BY ab.sort_order ASC, ab.id ASC, col.id ASC, c.href ASC
			LIMIT 1`, email)
		if err != nil || len(rows.Values) == 0 {
			return Avatar{}, nil, true, err
		}
		return scanCardDAVPhoto(rows.Values[0]), nil, true, nil
	}

	comps, addrToComp, err := s.enabledCardComponents(ctx, own)
	if err != nil {
		return Avatar{}, nil, false, err
	}
	idx, ok := addrToComp[email]
	if !ok {
		return Avatar{}, []string{email}, false, nil
	}
	comp := comps[idx]
	cb, _ := json.Marshal(comp.cardIDs)
	pRows, err := s.db.Query(ctx, `SELECT c.photo, c.photo_format, c.photo_width, c.photo_height
		FROM address_book_cards c
		JOIN address_book_collections col ON col.id = c.collection_id
		JOIN address_books ab ON ab.id = col.address_book_id
		WHERE c.id IN (SELECT value FROM json_each(?)) AND col.enabled = 1 AND length(c.photo) > 0
		ORDER BY ab.sort_order ASC, ab.id ASC, col.id ASC, c.href ASC
		LIMIT 1`, string(cb))
	if err != nil || len(pRows.Values) == 0 {
		return Avatar{}, comp.addrs, false, err
	}
	return scanCardDAVPhoto(pRows.Values[0]), comp.addrs, false, nil
}
