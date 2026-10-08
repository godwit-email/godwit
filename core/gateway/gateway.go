// Package gateway decides whether an AI tool call may run, needs approval, or
// is denied, and audits every decision. It must not import core.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
)

var (
	ErrDenied           = errors.New("gateway: tool call denied")
	ErrApprovalRequired = errors.New("gateway: approval required")
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

type Decision string

const (
	Allow   Decision = "allow"
	Approve Decision = "approve"
	Deny    Decision = "deny"
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
	var target any
	if c.TargetID != 0 {
		target = c.TargetID
	}
	if _, err := g.db.Execute(ctx, "INSERT INTO audit_log(at, source, tool, target_kind, target_id, decision) VALUES (?, ?, ?, ?, ?, ?)",
		time.Now().Unix(), c.Source, c.Tool, c.TargetKind, target, string(d)); err != nil {
		return Deny, fmt.Errorf("gateway: audit: %w", err)
	}
	return d, nil
}

// Wrap routes t through the gateway; every tool handed to ai.Run goes through
// it. Writes from an allowed source always need approval (NeedsApproval). Run
// checks at call time, so a scope tainted mid-run affects later calls: denied
// calls and egress that now needs approval fail closed with an error result.
func (g *Gateway) Wrap(scope *Scope, source string, t ai.ToolSpec, write bool, egress string) ai.ToolSpec {
	call := Call{Source: source, Tool: t.Name, Write: write, Egress: egress}
	t.NeedsApproval = t.NeedsApproval || decide(call, false) == Approve
	run := t.Run
	t.Run = func(ctx context.Context, args json.RawMessage) (any, error) {
		d, err := g.Check(ctx, scope, call)
		switch {
		case err != nil:
			return nil, err
		case d == Deny:
			return nil, ErrDenied
		case d == Approve && !write:
			// ponytail: egress approval fails closed; plan 4 adds the approval card with the host.
			return nil, ErrApprovalRequired
		case run == nil:
			return nil, ErrDenied
		}
		// Allow, or a write the user approved and the host now executes.
		return run(ctx, args)
	}
	return t
}
