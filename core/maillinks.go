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
	mailURLRe = regexp.MustCompile(`https?://[^\s<>"'\]]+`)
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
	s      *Service
	bodies map[int64]string
}

func (s *Service) linkExpander() *linkExpander {
	return &linkExpander{s: s, bodies: map[int64]string{}}
}

func (x *linkExpander) text(ctx context.Context, text string) string {
	if !strings.Contains(text, linkRefPrefix) {
		return text
	}
	return linkRefRe.ReplaceAllStringFunc(text, func(ref string) string {
		sm := linkRefRe.FindStringSubmatch(ref)
		id, _ := strconv.ParseInt(sm[1], 10, 64)
		body, ok := x.bodies[id]
		if !ok {
			if m, err := x.s.mailForModel(ctx, id); err == nil {
				body = modelBody(m)
			}
			x.bodies[id] = body
		}
		out := ref
		mailLinks(body, func(u string) string {
			if out == ref && linkHash(u) == sm[2] {
				out = u
			}
			return u
		})
		return out
	})
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
		if !strings.ContainsAny(rest, " \t\r\n") && len(rest) < 512 && linkRefRe.FindStringIndex(rest) == nil {
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

// expandDraftLinks expands the link references in d's subject and body
// before it is checked or sent: Wit's draft tools already did, but one may be
// typed or pasted back in.
func (s *Service) expandDraftLinks(ctx context.Context, d *Draft) error {
	x := s.linkExpander()
	d.Subject, d.HTML = x.text(ctx, d.Subject), x.text(ctx, d.HTML)
	if strings.Contains(d.Subject+d.HTML, linkRefPrefix) {
		return ErrLinkRef
	}
	return nil
}

// ErrLinkRef is a draft that still holds a link reference: sending it would
// mail the recipient a link that only works here.
var ErrLinkRef = errors.New("the draft has a link reference that could not be restored (" + linkRefPrefix + "…): replace the link before sending")
