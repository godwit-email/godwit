package core

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/stretchr/testify/require"
)

const longLink = "https://pages.example.com/promo/2026/summer-sale?id=42&session=abcdefghijklmnopqrstuvwxyz1234567890"

func TestLinkRefs(t *testing.T) {
	ref := linkRefPrefix + "pages.example.com/promo/2026/summer-sale?m=7&h=" + linkHash(longLink)
	for _, c := range []struct{ name, in, want string }{
		{"short link stays, without tracking", "https://a.com/watch?v=1&utm_medium=mail&FBCLID=2.", "https://a.com/watch?v=1."},
		{"only tracking params", "https://a.com/x?utm_source=e#top", "https://a.com/x#top"},
		{"long link becomes a reference", "go (" + longLink + "&utm_source=email)", "go (" + ref + ")"},
		{"long hint is cut on a rune", "https://example.cn/" + strings.Repeat("新闻", 30), linkRefPrefix + "example.cn/新闻新闻新闻新闻…?m=7&h=" + linkHash("https://example.cn/"+strings.Repeat("新闻", 30))},
		{"balanced parentheses stay", "https://en.wikipedia.org/wiki/Go_(game), ok", "https://en.wikipedia.org/wiki/Go_(game), ok"},
	} {
		t.Run(c.name, func(t *testing.T) { require.Equal(t, c.want, linkRefs(c.in, 7)) })
	}
}

func TestLinkRefsExpand(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Sale", "shop@x", "", `<p>Shop <a href="`+longLink+`&amp;utm_campaign=summer">now</a>.</p>`, 1_700_000_000)
	items, err := s.mailContext(t.Context(), &gateway.Scope{}, []int64{id}, defaultMailBudget)
	require.NoError(t, err)
	require.NotContains(t, items[0].Block, "session=")
	ref := linkRefRe.FindString(items[0].Block)
	require.NotEmpty(t, ref, items[0].Block)

	ctx := t.Context()
	require.Equal(t, "Open "+longLink+" now", s.expandLinks(ctx, "Open "+ref+" now"))
	escaped := strings.Replace(ref, "&h=", "&amp;h=", 1)
	require.Equal(t, `<a href="`+longLink+`">`, s.expandLinks(ctx, `<a href="`+escaped+`">`), "an HTML draft's escaped &")
	forged := ref[:len(ref)-8] + "00000000"
	require.Equal(t, forged, s.expandLinks(ctx, forged), "a hash that is no link of the message")

	args, _ := json.Marshal(map[string]any{"html": "<p>" + ref + "</p>", "to": []any{"a@x"}})
	var got map[string]any
	require.NoError(t, json.Unmarshal(s.expandArgs(ctx, args), &got))
	require.Equal(t, "<p>"+longLink+"</p>", got["html"])

	// A reference split across streamed chunks comes out whole, and before the next event.
	in := make(chan ai.Event, 8)
	text := "See " + ref + " and go"
	for _, part := range []string{text[:6], text[6:26], text[26 : len(text)-5], text[len(text)-5:]} {
		in <- ai.Event{Kind: "text", Text: part}
	}
	in <- ai.Event{Kind: "done"}
	close(in)
	var out strings.Builder
	for ev := range s.expandEvents(ctx, in) {
		if ev.Kind == "done" {
			require.Equal(t, "See "+longLink+" and go", out.String())
			continue
		}
		require.NotContains(t, ev.Text, "godwit:")
		out.WriteString(ev.Text)
	}
	require.Equal(t, 4, refCut("abc godwit:/"), "a prefix of a reference is held")
}
