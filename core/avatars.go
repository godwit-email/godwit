package core

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
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

// SenderAvatar loads the ordinary sender avatar under the current message's
// remote-image policy. allowRemote is the user's explicit permission for this message.
// Missing or unavailable sources are normal empty results.
func (s *Service) SenderAvatar(ctx context.Context, messageID int64, allowRemote bool) (Avatar, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Avatar{}, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT account_id, from_addr FROM messages WHERE id = ?", messageID)
	if err != nil {
		return Avatar{}, err
	}
	if len(rows.Values) == 0 {
		return Avatar{}, fmt.Errorf("message %d not found", messageID)
	}
	if err := s.bind(ctx, store.Int(rows.Values[0][0])); err != nil {
		return Avatar{}, err
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
	from := store.Str(rows.Values[0][1])
	if _, err := mail.ParseAddress(from); err != nil {
		return Avatar{}, nil
	}
	email := gomail.AddrKey(from)
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
