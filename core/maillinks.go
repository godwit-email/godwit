package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	mailURLRe    = regexp.MustCompile(`(?i:https?)://(?:\[[0-9A-Fa-f:.]+\])?[^\s\p{Z}<>"'\]]*`) // a bracketed IPv6 host is one link
	plantedRefRe = regexp.MustCompile(`(?i)godwit://link/`)
	// The & may come back HTML-escaped in a draft's href.
	linkRefRe = regexp.MustCompile(`godwit://link/[^\s\p{Z}?"'<>]*\?m=(\d+)&(?:amp;)?h=([0-9a-f]{16})`)
)

// modelHead is the header of m a model reads, its attachments listed.
func modelHead(m Message) string {
	head := mailHeader(m.MessageSummary)
	var att []string
	for _, a := range m.Attachments {
		att = append(att, fmt.Sprintf("%s (attachment %d, %s, %d bytes)", a.Filename, a.ID, a.ContentType, a.Size))
	}
	if len(att) > 0 {
		head += "Attachments: " + strings.Join(att, "; ") + "\n"
	}
	return head
}

// modelBody is the text of m a model reads: what the reader shows, so the HTML
// when there is one (the stored text is then the HTML's own, hidden parts
// included, or a plain part no one sees), without what shows nothing.
func modelBody(m Message) string {
	if m.HTML != "" {
		return gomail.VisibleText(m.HTML)
	}
	return gomail.StripInvisible(m.Text)
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
	// Only references this makes reach the model: one planted in mail, in any case, is defused.
	text = defuseRefs(text)
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
func maskLinks(texts []string, id int64) ([]string, map[string]string) {
	links := map[string]string{}
	out := make([]string, len(texts))
	for i, t := range texts {
		t = defuseRefs(t) // not a link to mailLinks, but a reference to the model
		out[i] = mailLinks(t, func(u string) string {
			r := linkRefs(u, id)
			if r != u {
				links[r] = u
			}
			return r
		})
	}
	return out, links
}

// unmask puts the links of maskLinks back in text, each at most as often as
// masked holds its reference: a model repeating one cannot multiply a huge link.
func unmask(text, masked string, links map[string]string) string {
	left := map[string]int{}
	for _, r := range linkRefRe.FindAllString(masked, -1) {
		left[r]++
	}
	var b strings.Builder
	last := 0
	for _, loc := range linkRefRe.FindAllStringIndex(text, -1) {
		r := text[loc[0]:loc[1]]
		u, ok := links[r]
		if !ok || left[r] == 0 || refContinues(text, loc[1]) || insideURL(text, loc[0], 0) {
			continue // as linkExpander.text: part of a longer URL stays as written
		}
		left[r]--
		b.WriteString(text[last:loc[0]])
		b.WriteString(u)
		last = loc[1]
	}
	b.WriteString(text[last:])
	return b.String()
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
	out, _ := s.linkExpander().args(ctx, args) // shown as written when it cannot run
	return out
}

// linkExpander expands references across one output (a stream, a
// conversation, a draft), reading each message they name once: a message is
// sanitized and turned to text to find its links. Not for concurrent use.
type linkExpander struct {
	s      *Service
	mu     sync.Mutex // a run's tool calls share one, and may run at once; guards the rest
	links  map[int64]*msgLinks
	misses int // rescans of messages with more links than their index holds
}

// msgLinks is a message's long links by hash: the first maxIndexedLinks of
// them, partial when it had more.
type msgLinks struct {
	idx     map[string]string
	partial bool
}

func (s *Service) linkExpander() *linkExpander {
	return &linkExpander{s: s, links: map[int64]*msgLinks{}}
}

// maxExpandURL is the longest link a reference expands to: a model repeating
// the reference to a huge link cannot blow its output up without bound.
const maxExpandURL = 8 << 10

// maxExpandMessages caps the messages one expander reads: references with
// many made-up message ids cannot make it load and scan the whole mailbox.
const maxExpandMessages = 32

// maxIndexedLinks caps the links indexed per message, and maxExpandMisses the
// rescans for a link past that, per expander: memory and work stay bounded.
const (
	maxIndexedLinks = 512
	maxExpandMisses = 8
)

// link is the link of message id with hash h, the message's links indexed once.
func (x *linkExpander) link(ctx context.Context, id int64, h string) (string, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	ml, ok := x.links[id]
	if !ok && len(x.links) >= maxExpandMessages {
		return "", false
	}
	if !ok {
		ml = &msgLinks{idx: map[string]string{}}
		x.scan(ctx, id, func(k, u string) bool {
			if ml.idx[k] == "" {
				if ml.partial = len(ml.idx) >= maxIndexedLinks; !ml.partial {
					ml.idx[k] = u // the first link with a hash wins
				}
			}
			return !ml.partial
		})
		x.links[id] = ml
	}
	if u, ok := ml.idx[h]; ok || !ml.partial || x.misses >= maxExpandMisses {
		return u, ok
	}
	// A link past the index: look for it alone, a bounded number of times per expander.
	x.misses++
	var found string
	x.scan(ctx, id, func(k, u string) bool {
		if k == h {
			found = u
		}
		return found == ""
	})
	return found, found != ""
}

// scan calls f with the hash and link of each long link of message id the
// model reads, in order, while f returns true.
func (x *linkExpander) scan(ctx context.Context, id int64, f func(k, u string) bool) {
	m, err := x.s.mailForModel(ctx, id)
	if err != nil {
		return
	}
	more := true
	mailLinks(modelHead(m)+"\n"+modelBody(m), func(u string) string {
		if more && len(u) > maxModelURL && len(u) <= maxExpandURL { // only links linkRefs references
			more = f(linkHash(u), u)
		}
		return u
	})
}

func (x *linkExpander) text(ctx context.Context, text string) string {
	return x.textAfter(ctx, 0, text)
}

// textAfter is text for text that follows prev (0 for none) in a stream: prev
// decides whether a reference at its start is inside another URL.
func (x *linkExpander) textAfter(ctx context.Context, prev byte, text string) string {
	if !strings.Contains(text, linkRefPrefix) {
		return text
	}
	var b strings.Builder
	last := 0
	for _, loc := range linkRefRe.FindAllStringSubmatchIndex(text, -1) {
		if refContinues(text, loc[1]) || insideURL(text, loc[0], prev) {
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

// insideURL is whether a reference at start of text (after prev, 0 for none)
// sits inside another URL: a URL character before it, past any "(" or "["
// that groups it there ("?u=(ref)"); prose like "(see ref)" has a space.
func insideURL(text string, start int, prev byte) bool {
	for start > 0 && (text[start-1] == '(' || text[start-1] == '[') {
		start--
	}
	before := prev
	if start > 0 {
		before = text[start-1]
	}
	return before != 0 && continuesURL(before)
}

// urlPunct is URL punctuation that may also just end a sentence.
const urlPunct = ".,;:!*'()$[]"

// refContinues is whether text from end makes a reference ending there part
// of a longer URL: URL characters, or punctuation with more URL after it (".html").
func refContinues(text string, end int) bool {
	rest := strings.TrimLeft(text[end:], urlPunct)
	return rest != "" && continuesURL(rest[0])
}

// errKeyCollision is arguments where two keys expand to the same link: one
// would silently replace the other.
var errKeyCollision = errors.New("two argument keys hold the same link once their " + linkRefPrefix + " references are expanded")

func (x *linkExpander) args(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	if !strings.Contains(string(args), linkRefPrefix) {
		return args, nil
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber() // a large integer argument stays exact
	var v any
	if dec.Decode(&v) != nil {
		return args, nil
	}
	collided := false
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
				k = x.text(ctx, k)
				if _, dup := m[k]; dup {
					collided = true
				}
				m[k] = walk(e)
			}
			return m
		}
		return v
	}
	b, err := json.Marshal(walk(v))
	if err != nil {
		return args, nil
	}
	if collided {
		return args, errKeyCollision
	}
	return b, nil
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
		var prev byte // the last byte sent of heldKind's stream, as the model wrote it
		for ev := range in {
			streamed := ev.Kind == "text" || ev.Kind == "reasoning"
			if held != "" && (!streamed || ev.Kind != heldKind) {
				out <- ai.Event{Kind: heldKind, Text: x.textAfter(ctx, prev, dropCutRef(held))}
				held = ""
			}
			if !streamed || ev.Kind != heldKind {
				prev = 0
			}
			if streamed {
				text := held + ev.Text
				cut := refCut(text)
				if held, heldKind = text[cut:], ev.Kind; cut > 0 {
					ev.Text = x.textAfter(ctx, prev, text[:cut])
					prev = text[cut-1]
					out <- ev
				}
				continue
			}
			if ev.Call != nil {
				c := *ev.Call
				c.Args, _ = x.args(ctx, c.Args)
				ev.Call = &c
			}
			out <- ev
		}
		if held != "" {
			out <- ai.Event{Kind: heldKind, Text: x.textAfter(ctx, prev, dropCutRef(held))}
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
		if loc := linkRefRe.FindStringIndex(rest); !strings.ContainsAny(rest, " \t\r\n") && len(rest) < 512 &&
			(loc == nil || strings.Trim(rest[loc[1]:], urlPunct) == "") {
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

// snippetRunes is where mail.Snippet cuts a snippet.
const snippetRunes = 200

// snippetLinks is linkRefs over a snippet but its last word, which the
// snippet may have cut: a reference to part of a link would never expand.
func snippetLinks(snippet string, id int64) string {
	if utf8.RuneCountInString(snippet) < snippetRunes {
		return linkRefs(snippet, id) // shorter than the cut: its last word is whole
	}
	i := strings.LastIndexAny(snippet, " \t\r\n") + 1
	return linkRefs(snippet[:i], id) + defuseRefs(snippet[i:])
}

// defuseRefs turns link references in mail text, in any case, into text no
// expander reads: only references made for a model are ever expanded.
func defuseRefs(text string) string { return plantedRefRe.ReplaceAllString(text, "godwit:link/") }

// hasLinkRef is whether text still holds a link reference, in any case: the
// scheme is case-insensitive to whoever reads it.
func hasLinkRef(text string) bool { return strings.Contains(strings.ToLower(text), linkRefPrefix) }

// toolArgs is args for a tool call about to run: one whose arguments still
// hold a reference is refused, so no tool gets an internal link. A run keeps
// one expander for all its calls, so maxExpandMessages holds for the run.
func (x *linkExpander) toolArgs(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	out, err := x.args(ctx, args)
	if err != nil {
		return nil, err
	}
	if hasLinkRef(string(out)) {
		return nil, errors.New("a " + linkRefPrefix + " reference in the arguments could not be turned back into its link: copy it exactly from the email, or leave it out")
	}
	return out, nil
}

// expandDraft expands the link references in d's subject and body, and
// restores the placeholders in those and its addresses (docs/adr/0025),
// before it is checked or sent: Wit's draft tools already did, but one may be
// typed or pasted back in.
func (s *Service) expandDraft(ctx context.Context, d *Draft) error {
	x := s.linkExpander()
	d.Subject, d.HTML = x.text(ctx, d.Subject), x.text(ctx, d.HTML)
	if hasLinkRef(d.Subject + d.HTML) {
		return ErrLinkRef
	}
	m, _ := s.masker() // without it, nothing restores and any placeholder is refused
	restore := func(l []string) []string {
		out := make([]string, len(l))
		for i, v := range l {
			out[i] = m.Restore(v)
		}
		return out
	}
	d.Subject, d.ReplyTo, d.HTML = m.Restore(d.Subject), m.Restore(d.ReplyTo), m.RestoreHTML(d.HTML)
	d.To, d.Cc, d.Bcc = restore(d.To), restore(d.Cc), restore(d.Bcc)
	if m.Left(strings.Join(slices.Concat([]string{d.Subject, d.ReplyTo, d.HTML}, d.To, d.Cc, d.Bcc), "\n")) {
		return ErrPlaceholder
	}
	return nil
}

// ErrLinkRef is a draft that still holds a link reference: sending it would
// mail the recipient a link that only works here.
var ErrLinkRef = errors.New("the draft has a link reference that could not be restored (" + linkRefPrefix + "…): replace the link before sending")

// ErrPlaceholder is a draft that still holds a 脱敏 placeholder: sending it
// would mail the recipient the placeholder, not its value.
var ErrPlaceholder = errors.New("the draft has a placeholder that could not be restored ({{…}}): replace it with what it stands for before sending")
