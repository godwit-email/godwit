package core

import (
	"context"
	"net/url"

	"github.com/godwit-email/godwit/internal/avatar"
)

// ModelServiceAvatar is the favicon of the site a model service talks to;
// empty Data when it has none, or the service signs in with a subscription
// (those have a mark of their own) or runs on a local host.
func (s *Service) ModelServiceAvatar(ctx context.Context, id int64) (Avatar, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return Avatar{}, err
	}
	defer end()
	s.modelMu.RLock()
	m, err := s.modelServiceBy(ctx, "id = ?", id)
	s.modelMu.RUnlock()
	if err != nil {
		return Avatar{}, err
	}
	host := modelServiceHost(m)
	if host == "" {
		return Avatar{}, nil
	}
	img, ok := avatar.FindFavicon(ctx, s.opts.ImageClient, s.images, host, nil)
	if !ok {
		return Avatar{}, ctx.Err()
	}
	return Avatar{Data: img.Data, Format: img.Format, Source: "favicon", Width: img.Width, Height: img.Height}, nil
}

// modelServiceHost is the site whose favicon stands for m: the provider's own,
// or for 其他服务 the address's host. "" for subscription providers and local hosts.
func modelServiceHost(m ModelService) string {
	switch m.Provider {
	case ProviderOpenAI:
		return "openai.com"
	case ProviderAnthropic:
		return "anthropic.com"
	case ProviderTypeSafe:
		return "typesafe.ai"
	}
	if ModelProviderLogin(m.Provider) == "oauth" {
		return ""
	}
	u, err := url.Parse(m.BaseURL)
	if err != nil || u.Hostname() == "localhost" {
		return ""
	}
	return u.Hostname() // avatar.FindFavicon drops IP addresses itself
}
