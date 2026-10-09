// Package gateway decides whether an AI tool call may run, needs approval, or
// is denied, and audits every decision. It must not import core.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
)

// Scope is one run's data provenance. The Service rebuilds it from
// runs.snapshot when a run resumes. Safe for concurrent use.
type Scope struct{ readMail atomic.Bool }

// MarkReadMail records that mail content entered this run.
func (s *Scope) MarkReadMail()  { s.readMail.Store(true) }
func (s *Scope) ReadMail() bool { return s != nil && s.readMail.Load() } // nil: nothing read

const (
	SourceWit    = "wit"
	SourceSkill  = "skill"
	SourcePlugin = "plugin"
	SourceMCP    = "mcp"
	SourceHook   = "hook"
)

// Call is one tool call. Egress lists the destinations of a network tool.
// Extension is the plugin name or MCP server ID of an extension's tool.
type Call struct {
	Source, Extension, Tool, CallID string
	Write                           bool
	Egress                          []string
	TargetKind                      string
	TargetID                        int64
}

type Decision = ai.Decision

const (
	Allow   = ai.Allow
	Approve = ai.Approve
	Deny    = ai.Deny
)

type Gateway struct{ db *store.DB }

func New(db *store.DB) *Gateway { return &Gateway{db} }

func decide(c Call, readMail bool) Decision {
	switch {
	case c.Source != SourceWit && c.Source != SourcePlugin && c.Source != SourceMCP && c.Source != SourceHook:
		return Deny // skill (no tools, no network) and anything unknown
	case c.Write:
		return Approve
	case len(c.Egress) > 0 && readMail:
		return Approve
	}
	return Allow
}

// Check decides c under scope and writes one structured audit_log row. If the
// row cannot be written the call is denied. A nil scope has read no mail.
func (g *Gateway) Check(ctx context.Context, scope *Scope, c Call) (Decision, error) {
	return g.record(ctx, c, decide(c, scope.ReadMail()))
}

func (g *Gateway) record(ctx context.Context, c Call, d Decision) (Decision, error) {
	e := Entry{Source: c.Source, Extension: c.Extension, Tool: c.Tool, CallID: c.CallID, TargetKind: c.TargetKind,
		TargetID: c.TargetID, Destinations: c.Egress, Decision: string(d)}
	if d == Deny {
		e.Reason = "source_denied" // the only reason decide denies
	}
	if err := g.Record(ctx, e); err != nil {
		return Deny, err
	}
	return d, nil
}

// Wrap routes t through the gateway as c (Source, Extension, Write); every
// tool handed to ai.Run goes through it. Its Gate decides every call when it
// happens, with egress(scope) as its destinations: a scope tainted mid-run
// makes later egress calls interrupt for approval. The gateway decides first
// and a deny wins; then t's own NeedsApproval or Gate can only make it
// stricter. The final decision is audited with the call ID.
func (g *Gateway) Wrap(scope *Scope, c Call, t ai.ToolSpec, egress func(*Scope) []string) ai.ToolSpec {
	c.Tool, c.Write = t.Name, c.Write || t.NeedsApproval
	own := t.Gate
	t.NeedsApproval = false
	t.Gate = func(ctx context.Context, args json.RawMessage) (Decision, error) {
		c := c
		c.CallID = ai.CallID(ctx)
		if egress != nil {
			c.Egress = egress(scope)
		}
		d := decide(c, scope.ReadMail())
		if d != Deny && own != nil {
			od, err := own(ctx, args)
			if err != nil {
				_, rerr := g.record(ctx, c, Deny)
				return Deny, errors.Join(err, rerr)
			}
			if od == Deny || od == Approve {
				d = od
			}
		}
		return g.record(ctx, c, d)
	}
	return t
}

// Entry is one audit_log row. Destinations are reduced to host[:port] (see
// Host); no URL, query, credential or body is ever stored.
type Entry struct {
	Source, Extension, Tool, CallID string
	TargetKind                      string
	TargetID                        int64
	Destinations                    []string
	Decision, Reason                string
}

// Record writes one audit_log row, such as for a host-initiated event that is
// not a tool call, like a skill run (decision "run").
func (g *Gateway) Record(ctx context.Context, e Entry) error {
	st := Audit(e)
	if _, err := g.db.Execute(ctx, st.SQL, st.Args...); err != nil {
		return fmt.Errorf("gateway: audit: %w", err)
	}
	return nil
}

// Audit is Record's audit_log row as a statement, for a caller that writes it
// in the same transaction as the event, such as an approval executing.
func Audit(e Entry) sqlite.Statement {
	var target any
	if e.TargetID != 0 {
		target = e.TargetID
	}
	hosts := make([]string, 0, len(e.Destinations))
	for _, d := range e.Destinations {
		hosts = append(hosts, Host(d))
	}
	return sqlite.Statement{SQL: `INSERT INTO audit_log(at, source, tool, target_kind, target_id, decision, extension, call_id,
		destination, reason) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		Args: []any{time.Now().Unix(), e.Source, e.Tool, e.TargetKind, target, e.Decision, null(e.Extension), null(e.CallID),
			null(strings.Join(hosts, ",")), null(e.Reason)}}
}

func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Host reduces a destination to its host[:port]: a URL loses its scheme,
// userinfo, path and query; "mcp:<server>" style labels stay as they are.
func Host(d string) string {
	if u, err := url.Parse(d); err == nil && u.Host != "" {
		return u.Host
	}
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	if i := strings.LastIndexByte(d, '@'); i >= 0 {
		d = d[i+1:]
	}
	return d
}
