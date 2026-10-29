package avatar

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/godwit-email/godwit/internal/imagecache"
	"github.com/godwit-email/godwit/internal/imagefetch"
)

// LookupTXT is (*net.Resolver).LookupTXT.
type LookupTXT func(ctx context.Context, name string) ([]string, error)

// FindBIMI returns the logo the sender domain publishes at default._bimi, shown
// like a favicon and never as verification: it reads DNS, not this message's
// authentication. The domain's DMARC must enforce (quarantine or reject, pct=100,
// no p=none or sp=none) so its spoofs are normally rejected before delivery.
// See docs/research/bimi-clients.md (option B).
func FindBIMI(ctx context.Context, lookup LookupTXT, cache *imagecache.Cache, domain string, canFetch func() bool) (img imagecache.Image, found bool) {
	sites := FaviconSites(domain)
	if lookup == nil || len(sites) == 0 || ctx.Err() != nil || canFetch != nil && !canFetch() {
		return imagecache.Image{}, false
	}
	gen := cache.AvatarGeneration()
	key := imagecache.AvatarKey("bimi", sites[0])
	if img, ok, err := cache.LookupAvatar(ctx, key); ok {
		return img, true
	} else if ctx.Err() != nil || errors.Is(err, imagecache.ErrClosed) {
		return imagecache.Image{}, false
	}
	if cache.AvatarMiss(key) || cache.AvatarUnreachable(key) {
		return imagecache.Image{}, false
	}
	callerCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	permitted := func() bool { return callerCtx.Err() == nil && (canFetch == nil || canFetch()) }
	src, err := bimiLocation(ctx, lookup, sites[0], sites[len(sites)-1])
	if err == nil && permitted() {
		img, err = cache.GetAvatar(ctx, src, "bimi", gen)
		if err == nil && permitted() {
			if img, err = cache.PutAvatar(ctx, key, img.Data, "bimi", gen); err == nil {
				return img, true
			}
		}
	}
	if !permitted() || errors.Is(err, imagecache.ErrClosed) {
		return imagecache.Image{}, false
	}
	if errors.Is(err, imagefetch.ErrAvatarMiss) {
		_ = cache.RememberAvatarMiss(key, gen)
	} else {
		cache.RememberAvatarUnreachable(key, gen)
	}
	return imagecache.Image{}, false
}

// bimiLocation is the l= URL of domain's BIMI record, or org's when domain has
// none, provided DMARC enforces for domain. Definite misses are ErrAvatarMiss.
func bimiLocation(ctx context.Context, lookup LookupTXT, domain, org string) (string, error) {
	rec, err := txtRecord(ctx, lookup, "default._bimi."+domain, "BIMI1")
	if rec == nil && err == nil && org != domain {
		rec, err = txtRecord(ctx, lookup, "default._bimi."+org, "BIMI1")
	}
	if err != nil {
		return "", err
	}
	if rec == nil {
		return "", imagefetch.ErrAvatarMiss
	}
	// An empty l= is the domain declining to show a logo.
	u, perr := url.Parse(rec["l"])
	if perr != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", imagefetch.ErrAvatarMiss
	}
	if err := dmarcEnforced(ctx, lookup, domain, org); err != nil {
		return "", err
	}
	return u.String(), nil
}

func dmarcEnforced(ctx context.Context, lookup LookupTXT, domain, org string) error {
	orgRec, err := txtRecord(ctx, lookup, "_dmarc."+org, "DMARC1")
	if err != nil {
		return err
	}
	rec, policy := orgRec, ""
	if org != domain {
		if rec, err = txtRecord(ctx, lookup, "_dmarc."+domain, "DMARC1"); err != nil {
			return err
		}
		if rec == nil && orgRec != nil {
			rec, policy = orgRec, orgRec["sp"]
		}
	}
	if rec == nil {
		return imagefetch.ErrAvatarMiss
	}
	if policy == "" {
		policy = rec["p"]
	}
	if policy = strings.ToLower(policy); policy != "quarantine" && policy != "reject" ||
		rec["pct"] != "" && rec["pct"] != "100" ||
		orgRec != nil && (strings.EqualFold(orgRec["p"], "none") || strings.EqualFold(orgRec["sp"], "none")) {
		return imagefetch.ErrAvatarMiss
	}
	return nil
}

// txtRecord parses name's one TXT record of the given version as lowercase tags.
// No record is nil; two or more are a miss, as both BIMI and DMARC require.
func txtRecord(ctx context.Context, lookup LookupTXT, name, version string) (map[string]string, error) {
	txts, err := lookup(ctx, name)
	if dnsErr := (*net.DNSError)(nil); errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec map[string]string
	for _, txt := range txts {
		tags := map[string]string{}
		for _, part := range strings.Split(txt, ";") {
			k, v, _ := strings.Cut(part, "=")
			tags[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
		if !strings.HasPrefix(strings.TrimSpace(txt), "v=") || tags["v"] != version {
			continue
		}
		if rec != nil {
			return nil, imagefetch.ErrAvatarMiss
		}
		rec = tags
	}
	return rec, nil
}
