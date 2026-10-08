// Package gateway decides whether an AI tool call may run, needs approval, or
// is denied, and audits every decision. It must not import core.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
)

// Scope is one run's data provenance. The Service rebuilds it from
// runs.snapshot when a run resumes. Safe for concurrent use.
type Scope struct{ readMail atomic.Bool }

// MarkReadMail records that mail content entered this run.
func (s *Scope) MarkReadMail()  { s.readMail.Store(true) }
func (s *Scope) ReadMail() bool { return s.readMail.Load() }

const (
	SourceWit    = "wit"
	SourceSkill  = "skill"
	SourcePlugin = "plugin"
	SourceMCP    = "mcp"
	SourceHook   = "hook"
)

// Call is one tool call. Egress is the destination host of a network tool.
type Call struct {
	Source, Tool string
	Write        bool
	Egress       string
	TargetKind   string
	TargetID     int64
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
	case c.Egress != "" && readMail:
		return Approve
	}
	return Allow
}

// Check decides c under scope and writes one structured audit_log row. If the
// row cannot be written the call is denied.
func (g *Gateway) Check(ctx context.Context, scope *Scope, c Call) (Decision, error) {
	d := decide(c, scope.ReadMail())
	if err := g.Record(ctx, c.Source, c.Tool, c.TargetKind, c.TargetID, string(d)); err != nil {
		return Deny, err
	}
	return d, nil
}

// Wrap routes t through the gateway; every tool handed to ai.Run goes through
// it. Its Gate runs Check (and so audits) on every call, when the call
// happens: a scope tainted mid-run makes later egress calls interrupt for
// approval. A NeedsApproval tool counts as a write; a denied source wins over it.
func (g *Gateway) Wrap(scope *Scope, source string, t ai.ToolSpec, write bool, egress string) ai.ToolSpec {
	call := Call{Source: source, Tool: t.Name, Write: write || t.NeedsApproval, Egress: egress}
	t.NeedsApproval = false
	t.Gate = func(ctx context.Context, _ json.RawMessage) (Decision, error) { return g.Check(ctx, scope, call) }
	return t
}

// Record writes one audit_log row for a host-initiated event that is not a
// tool call, such as a skill run (decision "run").
func (g *Gateway) Record(ctx context.Context, source, tool, targetKind string, targetID int64, decision string) error {
	var target any
	if targetID != 0 {
		target = targetID
	}
	if _, err := g.db.Execute(ctx, "INSERT INTO audit_log(at, source, tool, target_kind, target_id, decision) VALUES (?, ?, ?, ?, ?, ?)",
		time.Now().Unix(), source, tool, targetKind, target, decision); err != nil {
		return fmt.Errorf("gateway: audit: %w", err)
	}
	return nil
}
