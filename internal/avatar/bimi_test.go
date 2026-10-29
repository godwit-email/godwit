package avatar

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/godwit-email/godwit/internal/imagefetch"
)

type fakeTXT map[string][]string

func (f fakeTXT) lookup(_ context.Context, name string) ([]string, error) {
	if txts, ok := f[name]; ok {
		return txts, nil
	}
	if name == "_dmarc.broken.com" {
		return nil, &net.DNSError{Err: "server misbehaving", Name: name, IsTemporary: true}
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func TestBIMILocation(t *testing.T) {
	const logo = "https://example.com/logo.svg"
	bimi := []string{"v=BIMI1; l=" + logo + "; a=https://example.com/vmc.pem"}
	reject := []string{"v=DMARC1; p=reject; rua=mailto:d@example.com"}
	miss := imagefetch.ErrAvatarMiss
	for _, tt := range []struct {
		name, domain, org string
		dns               fakeTXT
		want              string
		err               error
	}{
		{"enforcing domain", "example.com", "example.com", fakeTXT{"default._bimi.example.com": bimi, "_dmarc.example.com": reject}, logo, nil},
		{"organization record and policy for a subdomain", "tm.example.com", "example.com", fakeTXT{"default._bimi.example.com": bimi, "_dmarc.example.com": reject}, logo, nil},
		{"subdomain record wins", "tm.example.com", "example.com", fakeTXT{"default._bimi.tm.example.com": {"v=BIMI1; l=https://tm.example.com/a.svg"}, "default._bimi.example.com": bimi, "_dmarc.example.com": reject}, "https://tm.example.com/a.svg", nil},
		{"quarantine at pct=100", "example.com", "example.com", fakeTXT{"default._bimi.example.com": bimi, "_dmarc.example.com": {"v=DMARC1; p=Quarantine; pct=100"}}, logo, nil},
		{"unrelated TXT ignored", "example.com", "example.com", fakeTXT{"default._bimi.example.com": append([]string{"google-site-verification=x"}, bimi...), "_dmarc.example.com": reject}, logo, nil},
		{"no record", "example.com", "example.com", fakeTXT{"_dmarc.example.com": reject}, "", miss},
		{"declined", "example.com", "example.com", fakeTXT{"default._bimi.example.com": {"v=BIMI1; l=;"}, "_dmarc.example.com": reject}, "", miss},
		{"plain http logo", "example.com", "example.com", fakeTXT{"default._bimi.example.com": {"v=BIMI1; l=http://example.com/l.svg"}, "_dmarc.example.com": reject}, "", miss},
		{"two records", "example.com", "example.com", fakeTXT{"default._bimi.example.com": append(bimi, bimi...), "_dmarc.example.com": reject}, "", miss},
		{"no DMARC", "example.com", "example.com", fakeTXT{"default._bimi.example.com": bimi}, "", miss},
		{"p=none", "example.com", "example.com", fakeTXT{"default._bimi.example.com": bimi, "_dmarc.example.com": {"v=DMARC1; p=none"}}, "", miss},
		{"partial pct", "example.com", "example.com", fakeTXT{"default._bimi.example.com": bimi, "_dmarc.example.com": {"v=DMARC1; p=reject; pct=50"}}, "", miss},
		{"organization sp=none", "tm.example.com", "example.com", fakeTXT{"default._bimi.example.com": bimi, "_dmarc.example.com": {"v=DMARC1; p=reject; sp=none"}}, "", miss},
		{"organization sp=none over subdomain reject", "tm.example.com", "example.com", fakeTXT{"default._bimi.example.com": bimi, "_dmarc.tm.example.com": reject, "_dmarc.example.com": {"v=DMARC1; p=reject; sp=none"}}, "", miss},
		{"subdomain p=none", "tm.example.com", "example.com", fakeTXT{"default._bimi.example.com": bimi, "_dmarc.tm.example.com": {"v=DMARC1; p=none"}, "_dmarc.example.com": reject}, "", miss},
		{"temporary DNS failure is not a miss", "broken.com", "broken.com", fakeTXT{"default._bimi.broken.com": bimi}, "", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := bimiLocation(t.Context(), tt.dns.lookup, tt.domain, tt.org)
			if got != tt.want {
				t.Errorf("location = %q, want %q", got, tt.want)
			}
			switch {
			case tt.err != nil && !errors.Is(err, tt.err):
				t.Errorf("err = %v, want %v", err, tt.err)
			case tt.want == "" && tt.err == nil && (err == nil || errors.Is(err, miss)):
				t.Errorf("err = %v, want a transient error", err)
			case tt.want != "" && err != nil:
				t.Errorf("err = %v", err)
			}
		})
	}
}
