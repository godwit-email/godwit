package core

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/mail"
	"github.com/godwit-email/godwit/core/plugins"
)

// ActionInfo is an action a running plugin registered; ID is <plugin>/<id>.
type ActionInfo struct {
	ID, Plugin string
	Label      plugins.Text
	On         []string
}

type RunActionOptions struct {
	// ApproveEgress is the user's approval of the plugin's declared hosts
	// (EgressApprovalRequired) for this one run.
	ApproveEgress bool
}

// ActionResult is what an action returned: text, HTML sanitized for reading,
// or a draft it made for the UI to open.
type ActionResult struct {
	Text, HTML string
	DraftID    int64
}

// EgressApprovalRequired: the run may send mail content to Hosts (every host
// the plugin declares); ask the user, then run again with ApproveEgress.
type EgressApprovalRequired struct {
	Plugin string
	Hosts  []string
}

func (e *EgressApprovalRequired) Error() string {
	return "plugin " + e.Plugin + " may send data to " + strings.Join(e.Hosts, ", ") + ": approval required"
}

// Card is a plugin's declarative card for a message.
type Card struct {
	Plugin, Title string
	Fields        []CardField
	Buttons       []CardButton
}

type CardField struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// CardButton: Kind copy (Value is the text), open-url (an http or https URL)
// or run-action (Value is the action's full ID, <plugin>/<id>).
type CardButton struct {
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

const (
	cardBudget     = 5 * time.Second // match and render together
	maxCardText    = 1000            // runes, each text of a card
	maxCardFields  = 20
	maxCardButtons = 5
)

// pluginCall is a running plugin instance, taken out of pluginsMu.
type pluginCall struct {
	name string
	gen  uint64
	p    *plugins.Plugin
	row  pluginRow
}

func (s *Service) runningPlugins(keep func(*plugins.Plugin) bool) []pluginCall {
	s.pluginsMu.RLock()
	defer s.pluginsMu.RUnlock()
	var out []pluginCall
	for name, rp := range s.running {
		if keep(rp.p) {
			out = append(out, pluginCall{name, rp.gen, rp.p, rp.row})
		}
	}
	slices.SortFunc(out, func(a, b pluginCall) int { return strings.Compare(a.name, b.name) })
	return out
}

// ListActions lists the running plugins' actions that run on kind (message,
// thread, selection or draft; "" for all), by ID.
func (s *Service) ListActions(ctx context.Context, kind string) ([]ActionInfo, error) {
	_, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	var out []ActionInfo
	for _, pc := range s.runningPlugins(func(*plugins.Plugin) bool { return true }) {
		for _, a := range pc.p.Actions() {
			if kind == "" || slices.Contains(a.On, kind) {
				out = append(out, ActionInfo{ID: pc.name + "/" + a.ID, Plugin: pc.name, Label: a.Label, On: a.On})
			}
		}
	}
	slices.SortFunc(out, func(a, b ActionInfo) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// RunAction runs action id (<plugin>/<id>) on t. The plugin gets {kind, id}
// and, for a selection, text: never other mail content (it reads that itself
// with mail:read). A plugin holding net that may hold mail content (mail:read,
// or a selection) returns *EgressApprovalRequired unless opts approves. A
// failed run or an invalid result counts as a plugin failure.
func (s *Service) RunAction(ctx context.Context, id string, t Target, opts RunActionOptions) (ActionResult, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ActionResult{}, err
	}
	defer end()
	name, aid, _ := strings.Cut(id, "/")
	pcs := s.runningPlugins(func(p *plugins.Plugin) bool { return p.Name == name })
	if len(pcs) == 0 || !slices.ContainsFunc(pcs[0].p.Actions(), func(a plugins.ActionInfo) bool { return a.ID == aid && slices.Contains(a.On, t.Kind) }) {
		return ActionResult{}, fmt.Errorf("no action %s runs on a %s", id, t.Kind)
	}
	pc := pcs[0]
	target := map[string]any{"kind": t.Kind, "id": t.ID}
	if t.Kind == "selection" {
		if strings.TrimSpace(t.Text) == "" {
			return ActionResult{}, errors.New("selection is empty")
		}
		target["text"] = t.Text
	}

	m := pc.row.m
	hosts, needsApproval := plugins.EgressFor(plugins.KindAction, m, pc.row.granted == pc.row.permHash, t.Kind == "selection")
	if needsApproval && !opts.ApproveEgress {
		return ActionResult{}, &EgressApprovalRequired{Plugin: name, Hosts: hosts}
	}
	inv := plugins.Invocation{ID: "action:" + rand.Text(), Kind: plugins.KindAction,
		Write: slices.Contains(m.Permissions.Mail, "write"), EgressHosts: hosts}
	if needsApproval {
		if err := gateway.New(s.db).Record(ctx, gateway.Entry{Source: gateway.SourcePlugin, Extension: name, Tool: "plugin:" + id,
			CallID: inv.ID, TargetKind: t.Kind, TargetID: t.ID, Destinations: hosts, Decision: "approve"}); err != nil {
			return ActionResult{}, err
		}
	}

	res, err := pc.p.RunAction(ctx, inv, aid, target)
	if ctx.Err() != nil || errors.Is(err, plugins.ErrClosed) {
		return ActionResult{}, errors.Join(ctx.Err(), err) // cancelled or stopped: not the plugin's failure
	}
	var out ActionResult
	if err == nil {
		out, err = actionResult(res)
	}
	if err != nil {
		s.pluginFailed(ctx, name, pc.gen)
		return ActionResult{}, s.cleanError(err)
	}
	s.pluginSucceeded(ctx, name, pc.gen)
	return out, nil
}

// actionResult reads nothing, a string (text) or {text?, html?, draftId?}.
func actionResult(v any) (ActionResult, error) {
	bad := errors.New("an action must return nothing, a string or {text?, html?, draftId?}")
	switch v := v.(type) {
	case nil:
		return ActionResult{}, nil
	case string:
		return ActionResult{Text: v}, nil
	case map[string]any:
		var r ActionResult
		var ok bool
		if t, has := v["text"]; has {
			if r.Text, ok = t.(string); !ok {
				return ActionResult{}, bad
			}
		}
		if h, has := v["html"]; has {
			if r.HTML, ok = h.(string); !ok {
				return ActionResult{}, bad
			}
			r.HTML, _ = mail.Sanitize(r.HTML, nil, false)
		}
		if d, has := v["draftId"]; has {
			f, ok := d.(float64)
			if !ok || f <= 0 || f != float64(int64(f)) {
				return ActionResult{}, bad
			}
			r.DraftID = int64(f)
		}
		return r, nil
	}
	return ActionResult{}, bad
}

// PluginCards runs each running plugin's card on the message, by plugin
// name: match(message), then render(message) if it matched, within one 5 s
// budget, read-only and with no egress. A failed, late or invalid card is
// left out and counts as a plugin failure.
func (s *Service) PluginCards(ctx context.Context, messageID int64) ([]Card, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	msg, err := s.pluginMail(ctx, messageID)
	if err != nil {
		return nil, err
	}
	var out []Card
	for _, pc := range s.runningPlugins((*plugins.Plugin).HasCard) {
		c, ok, err := s.pluginCard(ctx, pc, msg)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, plugins.ErrClosed) {
			continue // stopped meanwhile
		}
		if err != nil {
			s.pluginFailed(ctx, pc.name, pc.gen)
			continue
		}
		s.pluginSucceeded(ctx, pc.name, pc.gen)
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Service) pluginCard(ctx context.Context, pc pluginCall, msg plugins.MailMessage) (Card, bool, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	clock := s.pluginClock
	if clock == nil {
		clock = realClock{}
	}
	defer clock.AfterFunc(cardBudget, func() { cancel(plugins.ErrTimeout) })()
	egress, _ := plugins.EgressFor(plugins.KindCard, pc.row.m, false, false) // none, whatever the plugin holds
	inv := plugins.Invocation{ID: fmt.Sprintf("card:%s:%d", pc.name, msg.ID), Kind: plugins.KindCard, EgressHosts: egress}
	res, err := pc.p.Card(ctx, inv, msg)
	if err != nil || res == nil {
		return Card{}, false, err
	}
	c, err := cardOf(pc, res)
	return c, err == nil, err
}

// cardOf validates a render result {title, fields?, buttons?}.
func cardOf(pc pluginCall, res any) (Card, error) {
	b, err := json.Marshal(res)
	if err != nil {
		return Card{}, err
	}
	var raw struct {
		Title   string       `json:"title"`
		Fields  []CardField  `json:"fields"`
		Buttons []CardButton `json:"buttons"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return Card{}, errors.New("a card must be {title, fields?, buttons?}")
	}
	if len(raw.Fields) > maxCardFields || len(raw.Buttons) > maxCardButtons {
		return Card{}, fmt.Errorf("a card has at most %d fields and %d buttons", maxCardFields, maxCardButtons)
	}
	texts := []string{raw.Title}
	for _, f := range raw.Fields {
		texts = append(texts, f.Label, f.Value)
	}
	for _, b := range raw.Buttons {
		texts = append(texts, b.Label, b.Value)
	}
	for _, t := range texts {
		if utf8.RuneCountInString(t) > maxCardText {
			return Card{}, fmt.Errorf("card text is limited to %d characters", maxCardText)
		}
	}
	c := Card{Plugin: pc.name, Title: raw.Title}
	if len(raw.Fields) > 0 {
		c.Fields = raw.Fields
	}
	for _, b := range raw.Buttons {
		switch b.Kind {
		case "copy":
		case "open-url":
			u, err := url.Parse(b.Value)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				return Card{}, errors.New("an open-url button needs an http or https URL")
			}
		case "run-action":
			if !slices.ContainsFunc(pc.p.Actions(), func(a plugins.ActionInfo) bool { return a.ID == b.Value }) {
				return Card{}, fmt.Errorf("card button runs unknown action %q", b.Value)
			}
			b.Value = pc.name + "/" + b.Value
		default:
			return Card{}, fmt.Errorf("unknown card button kind %q", b.Kind)
		}
		c.Buttons = append(c.Buttons, b)
	}
	return c, nil
}
