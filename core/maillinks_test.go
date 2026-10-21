package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
		{"a link's own ! inside HTMLToText's parentheses stays", "go (https://a.com/wow!)", "go (https://a.com/wow!)"},
		{"sentence end after a parenthesis", "(see https://a.com/x).", "(see https://a.com/x)."},
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

func TestLinkRefsExpandKeepsNumbersAndReasoning(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Sale", "shop@x", longLink, "", 1_700_000_000)
	ref := linkRefs(longLink, id)
	ctx := t.Context()

	args := s.expandArgs(ctx, json.RawMessage(`{"id":9007199254740993,"url":"`+ref+`"}`))
	require.JSONEq(t, `{"id":9007199254740993,"url":"`+longLink+`"}`, string(args))
	require.Contains(t, string(args), "9007199254740993", "above 2^53, kept exact")

	in := make(chan ai.Event, 4)
	in <- ai.Event{Kind: "reasoning", Text: "the link " + ref[:20]}
	in <- ai.Event{Kind: "reasoning", Text: ref[20:]}
	in <- ai.Event{Kind: "text", Text: "done"}
	close(in)
	var kinds, texts []string
	for ev := range s.expandEvents(ctx, in) {
		kinds, texts = append(kinds, ev.Kind), append(texts, ev.Text)
	}
	require.Equal(t, "the link "+longLink, strings.Join(texts[:len(texts)-1], ""))
	require.Equal(t, "reasoning", kinds[len(kinds)-2])
	require.Equal(t, "text", kinds[len(kinds)-1])

	rs, err := parseQuickReplies(`{"replies":[{"html":"<p><a href=\"`+ref+`\">Join</a></p>"}]}`, func(h string) string { return s.expandLinks(ctx, h) })
	require.NoError(t, err)
	require.Len(t, rs, 1)
	require.Contains(t, rs[0].HTML, `href="`+strings.ReplaceAll(longLink, "&", "&amp;")+`"`, "the href survives the sanitizer")
}

func TestLinkRefsEdgeInputs(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Sale", "shop@x", longLink, "", 1_700_000_000)
	ref := linkRefs(longLink, id)
	args := s.expandArgs(t.Context(), json.RawMessage(`{"by_url":{"`+ref+`":true}}`))
	require.JSONEq(t, `{"by_url":{"`+longLink+`":true}}`, string(args), "a reference used as a key")

	start := time.Now()
	out := linkRefs("https://a.com/x"+strings.Repeat(")", 1<<20), 1)
	require.True(t, strings.HasPrefix(out, "https://a.com/x)"))
	require.Less(t, time.Since(start), 2*time.Second, "trailing parentheses are trimmed in linear time")
}
