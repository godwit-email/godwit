package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/internal/testllm"
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
		{"a bracketed IPv6 host is one link", "https://[2001:db8::1]/path?utm_source=mail", "https://[2001:db8::1]/path"},
		{"a reference planted in mail is defused", "see " + linkRefPrefix + "a.com?m=1&h=0123abcd0123abcd", "see godwit:link/a.com?m=1&h=0123abcd0123abcd"},
		{"an upper-case scheme is a link too", "HTTPS://A.com/x?UTM_SOURCE=1", "HTTPS://A.com/x"},
		{"a ? in the fragment is no query", "https://app.example/#/open?utm_source=mail", "https://app.example/#/open?utm_source=mail"},
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
	forged := ref[:len(ref)-16] + "0000000000000000"
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

	// Trimmed in one pass (a rescan per ")" took minutes on this).
	out := linkRefs("https://a.com/x"+strings.Repeat(")", 64<<10), 1)
	require.True(t, strings.HasPrefix(out, "https://a.com/x)"))
}

func TestWitExpandsLinksLiveAndOnReload(t *testing.T) {
	e := newSkillEnv(t)
	id := addMessage(t, e.s, "Sale", "shop@x", longLink, "", 1_700_000_000)
	ref := linkRefs(longLink, id)
	e.srv.Script(testllm.Turn{Text: []string{"Here: " + ref[:25], ref[25:] + " done"}})
	conv := e.askConv(t)
	var live strings.Builder
	for _, ev := range e.ask(t, conv.ID, "the link?") {
		if ev.Kind == "text" {
			live.WriteString(ev.Text)
		}
	}
	require.Equal(t, "Here: "+longLink+" done", live.String())
	c, err := e.s.GetConversation(t.Context(), conv.ID)
	require.NoError(t, err)
	require.Equal(t, "Here: "+longLink+" done", c.Messages[len(c.Messages)-1].Text)
}

func TestDropCutRef(t *testing.T) {
	ref := linkRefPrefix + "a.com/x?m=1&h=0123abcd0123abcd"
	for in, want := range map[string]string{
		"see " + ref[:30]:       "see ",
		"see godwit:/":          "see ",
		"see " + ref:            "see " + ref,
		"see " + ref + " and g": "see " + ref + " and g",
		"godwit: notes":         "godwit: notes",
	} {
		require.Equal(t, want, dropCutRef(in), in)
	}
}

func TestExpandEventsDropsUnfinishedReference(t *testing.T) {
	s := newMailContextService(t)
	in := make(chan ai.Event, 3)
	in <- ai.Event{Kind: "text", Text: "Join at " + linkRefPrefix + "zoom.us/j?m=1&h=01"}
	in <- ai.Event{Kind: "done"}
	close(in)
	var text string
	for ev := range s.expandEvents(t.Context(), in) {
		text += ev.Text
	}
	require.Equal(t, "Join at ", text, "a reference the model never finished is not shown")
}

func TestLinkRefsBounds(t *testing.T) {
	s := newMailContextService(t)
	huge := "https://a.com/" + strings.Repeat("x", maxExpandURL)
	id := addMessage(t, s, "Sale", "shop@x", longLink+"\n"+huge, "", 1_700_000_000)
	ref, hugeRef := linkRefs(longLink, id), linkRefs(huge, id)
	ctx := t.Context()

	require.Equal(t, hugeRef, s.expandLinks(ctx, hugeRef), "a link over maxExpandURL is not expanded")
	require.Equal(t, ref+"f", s.expandLinks(ctx, ref+"f"), "a longer hash is no reference")
	d := Draft{HTML: "<p>" + ref + "f</p>"}
	require.ErrorIs(t, s.expandDraftLinks(ctx, &d), ErrLinkRef, "and sending fails closed")

	in := make(chan ai.Event, 3)
	in <- ai.Event{Kind: "text", Text: "go " + ref}
	in <- ai.Event{Kind: "text", Text: "f now"}
	close(in)
	var out string
	for ev := range s.expandEvents(ctx, in) {
		out += ev.Text
	}
	require.Equal(t, "go "+ref+"f now", out, "a reference at a chunk's end waits for what follows")

	texts, links := maskLinks([]string{"see " + longLink, "short https://a.com/x?utm_source=1"}, id)
	require.Equal(t, []string{"see " + ref, "short https://a.com/x"}, texts)
	require.Equal(t, "voir "+longLink, unmask("voir "+ref, texts[0], links))
	require.Equal(t, "voir "+longLink+" "+ref, unmask("voir "+ref+" "+ref, texts[0], links), "a link comes back only as often as it was masked")
}

func TestLinkExpanderLimits(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Sale", "shop@x", longLink, "", 1_700_000_000)
	ref := linkRefs(longLink, id)
	ctx := t.Context()
	require.Equal(t, ref+"&extra=1", s.expandLinks(ctx, ref+"&extra=1"), "a query after the hash is no reference")
	require.Equal(t, `"`+longLink+`"`, s.expandLinks(ctx, `"`+ref+`"`))

	x := s.linkExpander()
	var planted strings.Builder
	for i := range 40 {
		fmt.Fprintf(&planted, "%sa.com?m=%d&h=0123abcd0123abcd ", linkRefPrefix, 1000+i)
	}
	x.text(ctx, planted.String())
	require.Len(t, x.links, maxExpandMessages, "made-up message ids load at most maxExpandMessages")

	huge := "https://a.com/" + strings.Repeat("x", 20<<10)
	var seen []string
	res, err := translateMasked(ctx, []string{"see " + huge}, id, "fr", nil, func(_ context.Context, req translateRequest) (translateResult, error) {
		out := make([]string, len(req.Texts))
		for i, t := range req.Texts {
			seen = append(seen, t)
			out[i] = "T:" + t
		}
		return translateResult{Texts: out, Source: "en"}, nil
	})
	require.NoError(t, err)
	require.Len(t, seen, 1, "masked before it is cut into pieces")
	require.NotContains(t, seen[0], strings.Repeat("x", 100))
	require.Equal(t, "T:see "+huge, res.Translations["see "+huge])
}

func TestLinkRefsFailClosed(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Deal "+longLink, "shop@x", "hi", "", 1_700_000_000)
	ref := linkRefs(longLink, id)
	ctx := t.Context()

	wrapped := "https://redirect.example/?u=" + ref
	require.Equal(t, wrapped, s.expandLinks(ctx, wrapped), "a reference inside another URL is no reference")
	require.Equal(t, "("+longLink+")", s.expandLinks(ctx, "("+ref+")"))

	items, err := s.mailContext(ctx, &gateway.Scope{}, []int64{id}, defaultMailBudget)
	require.NoError(t, err)
	require.NotContains(t, items[0].Block, "session=", "a link in the subject is a reference too")
	require.Equal(t, longLink, s.expandLinks(ctx, ref), "and it expands")

	d := Draft{Subject: "Godwit://LINK/a.com?m=1&h=0123abcd0123abcd"}
	require.ErrorIs(t, s.expandDraftLinks(ctx, &d), ErrLinkRef, "any case of the prefix is caught")

	_, err = s.linkExpander().toolArgs(ctx, json.RawMessage(`{"url":"`+linkRefPrefix+`a.com?m=999&h=0123abcd0123abcd"}`))
	require.Error(t, err, "a tool never gets a reference that did not expand")
	out, err := s.linkExpander().toolArgs(ctx, json.RawMessage(`{"url":"`+ref+`"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"url":"`+longLink+`"}`, string(out))
}

func TestLinkRefsInHeadersAndAttachments(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Deal "+longLink, "shop@x", "hi", "", 1_700_000_000)
	file := "https://files.example.com/" + strings.Repeat("f", 90) + ".pdf"
	_, err := s.db.Execute(t.Context(), "INSERT INTO attachments(message_id, filename, content_type, size, inline) VALUES (?, ?, 'application/pdf', 1, 0)", id, file)
	require.NoError(t, err)
	ctx := t.Context()

	items, err := s.mailContext(ctx, &gateway.Scope{}, []int64{id}, defaultMailBudget)
	require.NoError(t, err)
	fileRef := linkRefRe.FindAllString(items[0].Block, -1)
	require.Len(t, fileRef, 2, items[0].Block)
	require.Equal(t, file, s.expandLinks(ctx, fileRef[1]), "an attachment name's link expands")

	sum := s.summaryContext(&gateway.Scope{}, []MessageSummary{{ID: id, Subject: "Deal " + longLink, From: "shop@x"}})
	require.NotContains(t, sum[0].Block, "session=")
	sub := s.subjectContext(&gateway.Scope{}, []MessageSummary{{ID: id, Subject: "Deal " + longLink}})
	require.NotContains(t, sub[0].Block, "session=")

	require.Contains(t, linkRefs("Godwit://LINK/a.com?m=1&h=0123abcd0123abcd", 1), "godwit:link/", "planted, in any case, is defused")
}

func TestLinkRefsDottedAndSelections(t *testing.T) {
	s := newMailContextService(t)
	id := addMessage(t, s, "Sale", "shop@x", "Shop "+longLink+" now", "", 1_700_000_000)
	ref := linkRefs(longLink, id)
	ctx := t.Context()
	for _, in := range []string{ref + ".html", ref + ").x", ref + "/next"} {
		require.Equal(t, in, s.expandLinks(ctx, in), "more URL after punctuation: no reference")
	}
	for _, end := range []string{".", ". Next", "), ok", `">`, "。"} {
		require.Equal(t, longLink+end, s.expandLinks(ctx, ref+end), "sentence punctuation ends it")
	}

	in := make(chan ai.Event, 3)
	in <- ai.Event{Kind: "text", Text: "go " + ref + "."}
	in <- ai.Event{Kind: "text", Text: "html"}
	close(in)
	var out string
	for ev := range s.expandEvents(ctx, in) {
		out += ev.Text
	}
	require.Equal(t, "go "+ref+".html", out, "punctuation at a chunk's end waits for what follows")

	sel := s.selectionContext(&gateway.Scope{}, id, "Shop "+longLink+" now", defaultMailBudget)
	require.Contains(t, sel.Block, ref, "a selection's links are references too")
	require.Equal(t, longLink, s.expandLinks(ctx, ref))

	snip := snippetLinks("Shop "+longLink+" now https://a.com/cut?utm_source=x", id)
	require.Equal(t, "Shop "+ref+" now https://a.com/cut?utm_source=x", snip, "the last word may be cut: left alone")
}

func TestLinkRefsTranslationPieces(t *testing.T) {
	ref := linkRefPrefix + "a.com/x?m=1&h=0123abcd0123abcd"
	for _, lead := range []int{2730, 2725} { // the cut lands in the prefix, then in the rest
		text := strings.Repeat("字", lead) + ref + strings.Repeat("字", 100) // no spaces: a hard cut
		pieces := splitText(text, 8<<10)
		require.Greater(t, len(pieces), 1)
		whole := false
		for _, p := range pieces {
			require.False(t, strings.Contains(p.text, linkRefPrefix) && !strings.Contains(p.text, ref), "a piece holds part of the reference")
			whole = whole || strings.Contains(p.text, ref)
		}
		require.True(t, whole)
	}

	masked, _ := maskLinks([]string{"see Godwit://LINK/a.com?m=1&h=0123abcd0123abcd"}, 1)
	require.Equal(t, "see godwit:link/a.com?m=1&h=0123abcd0123abcd", masked[0], "planted, defused before translation")
}

func TestLinkRefsReplyAndArgsEdges(t *testing.T) {
	s := newMailContextService(t)
	ctx := t.Context()
	planted := "Godwit://link/a.com?m=1&h=0123abcd0123abcd"
	src := addMessage(t, s, "Re "+planted, "Bob <bob@x.example>", "see "+planted, "<p>see "+planted+"</p>", 1_700_000_000)
	d, err := s.NewDraft(ctx, 1, "reply", src)
	require.NoError(t, err)
	require.False(t, hasLinkRef(d.Subject+d.HTML), "a reference planted in the mail is defused in the quote")
	require.NoError(t, s.expandDraftLinks(ctx, &d), "so the reply can be sent")

	id := addMessage(t, s, "Sale", "shop@x", longLink, "", 1_700_000_100)
	ref := linkRefs(longLink, id)
	_, err = s.linkExpander().toolArgs(ctx, json.RawMessage(`{"by_url":{"`+ref+`":1,"`+longLink+`":2}}`))
	require.ErrorIs(t, err, errKeyCollision, "two keys that become one link refuse the call")

	var many strings.Builder
	for i := range maxIndexedLinks + 50 {
		fmt.Fprintf(&many, "https://a.com/%090d\n", i)
	}
	fmt.Fprintf(&many, "https://a.com/short\n")
	big := addMessage(t, s, "Many", "x@x", many.String(), "", 1_700_000_200)
	x := s.linkExpander()
	x.link(ctx, big, "0000000000000000")
	require.Len(t, x.links[big], maxIndexedLinks, "a message indexes a bounded number of links, long ones only")
}
