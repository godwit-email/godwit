package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/ai"
	gomail "github.com/godwit-email/godwit/core/mail"
)

// Long links in mail text reach models as references,
// godwit://link/<host and start of path>?m=<message id>&h=<hash of the link>,
// and every model output is expanded back before anyone sees or sends it:
// newsletters spend most of their text on tracking links. The hash, not a
// position, picks the link, so a reference made up or planted in mail can only
// expand to a link its author already knows.
const linkRefPrefix = "godwit://link/"

// maxModelURL is the longest link the model sees as itself; maxLinkHint caps
// the host and path a reference keeps so the model can tell where it points.
const (
	maxModelURL = 80
	maxLinkHint = 40
)

var (
	mailURLRe = regexp.MustCompile(`(?i:https?)://(?:\[[0-9A-Fa-f:.]+\])?[^\s<>"'\]]*`) // a bracketed IPv6 host is one link
	// The & may come back HTML-escaped in a draft's href.
	linkRefRe = regexp.MustCompile(`godwit://link/[^\s?"'<>]*\?m=(\d+)&(?:amp;)?h=([0-9a-f]{16})`)
)

// modelBody is the text of m a model reads: its plain text, else its HTML as text.
func modelBody(m Message) string {
	if strings.TrimSpace(m.Text) == "" && m.HTML != "" {
		return gomail.HTMLToText(m.HTML)
	}
	return m.Text
}

// mailLinks calls f with each link in text, tracking parameters dropped, and
// puts in its place what f returns.
func mailLinks(text string, f func(u string) string) string {
	return mailURLRe.ReplaceAllStringFunc(text, func(u string) string {
		// Sentence punctuation, then unbalanced ")" (HTMLToText's "text (url)"), are
		// not part of the link; what a ")" closes over is, so "a!)" keeps its "!".
		// Counted once and kept up to date: mail can end a link with any number of ")".
		trail, open, closed := len(u), strings.Count(u, "("), strings.Count(u, ")")
		for trail > 0 && strings.IndexByte(".,;:!?", u[trail-1]) != -1 {
			trail--
		}
		for trail > 0 && u[trail-1] == ')' && open < closed {
			closed--
			trail--
		}
		return f(dropTrackingParams(u[:trail])) + u[trail:]
	})
}

// linkRefs drops tracking parameters from the links in text of message id and
// turns those still longer than maxModelURL into references.
func linkRefs(text string, id int64) string {
	// Only references this makes reach the model: one planted in mail is defused.
	text = strings.ReplaceAll(text, linkRefPrefix, "godwit:link/")
	return mailLinks(text, func(u string) string {
		if len(u) <= maxModelURL {
			return u
		}
		hint := u[strings.Index(u, "//")+2:]
		if i := strings.IndexAny(hint, "?#"); i != -1 {
			hint = hint[:i]
		}
		if len(hint) > maxLinkHint {
			n := maxLinkHint - len("…")
			for n > 0 && !utf8.RuneStart(hint[n]) {
				n--
			}
			hint = hint[:n] + "…"
		}
		return linkRefPrefix + hint + "?m=" + strconv.FormatInt(id, 10) + "&h=" + linkHash(u)
	})
}

// maskLinks is linkRefs over texts of message id, and the replacer that puts
// the links back: texts cut into pieces may split a link, so the references
// are undone from what was masked, not from the message.
func maskLinks(texts []string, id int64) ([]string, *strings.Replacer) {
	var pairs []string
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = mailLinks(t, func(u string) string {
			r := linkRefs(u, id)
			if r != u {
				pairs = append(pairs, r, u)
			}
			return r
		})
	}
	return out, strings.NewReplacer(pairs...)
}

func linkHash(u string) string {
	sum := sha256.Sum256([]byte(u))
	return hex.EncodeToString(sum[:8]) // 64 bits: a sender cannot craft two links of one mail that share it
}

// dropTrackingParams removes query parameters that only identify a campaign
// or recipient; the rest of the query is kept byte for byte.
func dropTrackingParams(u string) string {
	q := strings.IndexByte(u, '?')
	if f := strings.IndexByte(u, '#'); q == -1 || f != -1 && f < q {
		return u // no query: a "?" after "#" is the fragment's (a hash-routed app's)
	}
	query, frag := u[q+1:], ""
	if f := strings.IndexByte(query, '#'); f != -1 {
		query, frag = query[:f], query[f:]
	}
	var kept []string
	for _, p := range strings.Split(query, "&") {
		k, _, _ := strings.Cut(p, "=")
		k = strings.ToLower(k)
		if !strings.HasPrefix(k, "utm_") && !slices.Contains(trackingParams, k) {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return u[:q] + frag
	}
	return u[:q+1] + strings.Join(kept, "&") + frag
}

var trackingParams = []string{"fbclid", "gclid", "dclid", "gbraid", "wbraid", "msclkid", "yclid", "twclid", "igshid",
	"mc_cid", "mc_eid", "_hsenc", "_hsmi", "mkt_tok", "oly_anon_id", "oly_enc_id", "vero_id", "ck_subscriber_id"}

// expandLinks puts the links back for the references in model output. One
// that resolves to nothing (its message gone) is left as it is.
func (s *Service) expandLinks(ctx context.Context, text string) string {
	return s.linkExpander().text(ctx, text)
}

// expandArgs is expandLinks for a tool call's JSON arguments.
func (s *Service) expandArgs(ctx context.Context, args json.RawMessage) json.RawMessage {
	return s.linkExpander().args(ctx, args)
}

// linkExpander expands references across one output (a stream, a
// conversation, a draft), reading each message they name once: a message is
// sanitized and turned to text to find its links. Not for concurrent use.
type linkExpander struct {
	s     *Service
	links map[int64]map[string]string // by message, then hash
}

func (s *Service) linkExpander() *linkExpander {
	return &linkExpander{s: s, links: map[int64]map[string]string{}}
}

// maxExpandURL is the longest link a reference expands to: a model repeating
// the reference to a huge link cannot blow its output up without bound.
const maxExpandURL = 8 << 10

// maxExpandMessages caps the messages one expander reads: references with
// many made-up message ids cannot make it load and scan the whole mailbox.
const maxExpandMessages = 32

// link is the link of message id with hash h, the message's links indexed once.
func (x *linkExpander) link(ctx context.Context, id int64, h string) (string, bool) {
	idx, ok := x.links[id]
	if !ok && len(x.links) >= maxExpandMessages {
		return "", false
	}
	if !ok {
		idx = map[string]string{}
		if m, err := x.s.mailForModel(ctx, id); err == nil {
			mailLinks(mailHeader(m.MessageSummary)+"\n"+modelBody(m), func(u string) string {
				if k := linkHash(u); len(u) <= maxExpandURL && idx[k] == "" {
					idx[k] = u // the first link with a hash wins
				}
				return u
			})
		}
		x.links[id] = idx
	}
	u, ok := idx[h]
	return u, ok
}

func (x *linkExpander) text(ctx context.Context, text string) string {
	if !strings.Contains(text, linkRefPrefix) {
		return text
	}
	var b strings.Builder
	last := 0
	for _, loc := range linkRefRe.FindAllStringSubmatchIndex(text, -1) {
		if loc[1] < len(text) && continuesURL(text[loc[1]]) || loc[0] > 0 && continuesURL(text[loc[0]-1]) {
			continue // part of a longer URL (more hash or query, or inside another's): left for the checks
		}
		id, _ := strconv.ParseInt(text[loc[2]:loc[3]], 10, 64)
		if u, ok := x.link(ctx, id, text[loc[4]:loc[5]]); ok {
			b.WriteString(text[last:loc[0]])
			b.WriteString(u)
			last = loc[1]
		}
	}
	b.WriteString(text[last:])
	return b.String()
}

// continuesURL is whether c after a reference would make it part of a longer URL.
func continuesURL(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || strings.IndexByte("&=#%/?+~_-@", c) != -1
}

func (x *linkExpander) args(ctx context.Context, args json.RawMessage) json.RawMessage {
	if !strings.Contains(string(args), linkRefPrefix) {
		return args
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber() // a large integer argument stays exact
	var v any
	if dec.Decode(&v) != nil {
		return args
	}
	var walk func(v any) any
	walk = func(v any) any {
		switch v := v.(type) {
		case string:
			return x.text(ctx, v)
		case []any:
			for i := range v {
				v[i] = walk(v[i])
			}
		case map[string]any: // keys too: a tool may key its arguments by URL
			m := make(map[string]any, len(v))
			for k, e := range v {
				m[x.text(ctx, k)] = walk(e)
			}
			return m
		}
		return v
	}
	b, err := json.Marshal(walk(v))
	if err != nil {
		return args
	}
	return b
}

// expandEvents expands the references in a run's events: streamed text or
// reasoning is held back while it may end in a reference cut between chunks,
// and sent before any other event.
func (s *Service) expandEvents(ctx context.Context, in <-chan ai.Event) <-chan ai.Event {
	out := make(chan ai.Event)
	x := s.linkExpander()
	go func() {
		defer close(out)
		var held, heldKind string
		for ev := range in {
			streamed := ev.Kind == "text" || ev.Kind == "reasoning"
			if held != "" && (!streamed || ev.Kind != heldKind) {
				out <- ai.Event{Kind: heldKind, Text: x.text(ctx, dropCutRef(held))}
				held = ""
			}
			if streamed {
				text := held + ev.Text
				cut := refCut(text)
				if held, heldKind = text[cut:], ev.Kind; cut > 0 {
					ev.Text = x.text(ctx, text[:cut])
					out <- ev
				}
				continue
			}
			if ev.Call != nil {
				c := *ev.Call
				c.Args = x.args(ctx, c.Args)
				ev.Call = &c
			}
			out <- ev
		}
		if held != "" {
			out <- ai.Event{Kind: heldKind, Text: x.text(ctx, dropCutRef(held))}
		}
	}()
	return out
}

// refCut is where text may start a reference not complete yet: its end, or
// the start of an unfinished godwit://link/ (or a prefix of it at the end).
func refCut(text string) int {
	if i := strings.LastIndex(text, linkRefPrefix); i != -1 {
		rest := text[i:]
		// Unfinished, or finished at the very end, where another hex digit may still come.
		if loc := linkRefRe.FindStringIndex(rest); !strings.ContainsAny(rest, " \t\r\n") && len(rest) < 512 && (loc == nil || loc[1] == len(rest)) {
			return i
		}
	}
	for k := min(len(linkRefPrefix)-1, len(text)); k > 0; k-- {
		if strings.HasSuffix(text, linkRefPrefix[:k]) {
			return len(text) - k
		}
	}
	return len(text)
}

// dropCutRef drops the unfinished reference a cut at the end of text left:
// the model could copy it, and it would never expand.
func dropCutRef(text string) string {
	i := strings.LastIndex(text, "godwit:")
	if i == -1 {
		return text
	}
	rest := text[i:]
	if (strings.HasPrefix(linkRefPrefix, rest) || strings.HasPrefix(rest, linkRefPrefix)) &&
		!strings.ContainsAny(rest, " \t\r\n") && linkRefRe.FindStringIndex(rest) == nil {
		return text[:i]
	}
	return text
}

// hasLinkRef is whether text still holds a link reference, in any case: the
// scheme is case-insensitive to whoever reads it.
func hasLinkRef(text string) bool { return strings.Contains(strings.ToLower(text), linkRefPrefix) }

// toolArgs is expandArgs for a tool call about to run: one whose arguments
// still hold a reference is refused, so no tool gets an internal link.
func (s *Service) toolArgs(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	out := s.expandArgs(ctx, args)
	if hasLinkRef(string(out)) {
		return nil, errors.New("a " + linkRefPrefix + " reference in the arguments could not be turned back into its link: copy it exactly from the email, or leave it out")
	}
	return out, nil
}

// expandDraftLinks expands the link references in d's subject and body
// before it is checked or sent: Wit's draft tools already did, but one may be
// typed or pasted back in.
func (s *Service) expandDraftLinks(ctx context.Context, d *Draft) error {
	x := s.linkExpander()
	d.Subject, d.HTML = x.text(ctx, d.Subject), x.text(ctx, d.HTML)
	if hasLinkRef(d.Subject + d.HTML) {
		return ErrLinkRef
	}
	return nil
}

// ErrLinkRef is a draft that still holds a link reference: sending it would
// mail the recipient a link that only works here.
var ErrLinkRef = errors.New("the draft has a link reference that could not be restored (" + linkRefPrefix + "…): replace the link before sending")
