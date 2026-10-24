package core

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/gateway"
	"github.com/godwit-email/godwit/core/mail"
)

// 通过 MCP 发送's levels (Settings.MCPSend).
const (
	MCPSendOff     = "off"
	MCPSendConfirm = "confirm"
	MCPSendDirect  = "direct"
)

// mcpSendTTL is how long a send request waits for the user before it goes stale.
const mcpSendTTL = 10 * time.Minute

// maxMCPSends is how many send requests may wait for the user at once.
const maxMCPSends = 5

var (
	// ErrMCPSendOff: 通过 MCP 发送 is 不允许, or 每次确认 with no window to confirm in (--headless).
	ErrMCPSendOff = errors.New("sending through MCP is off; the user can turn it on in Godwit under Settings › Local API › Send through MCP")
	// ErrMCPSendComposing: 直接发送 of a draft open in a compose window,
	// which may hold edits not saved yet.
	ErrMCPSendComposing = errors.New("the draft is open in a compose window in Godwit; the user can send it there")
	// ErrMCPSendBusy: maxMCPSends requests wait for the user already.
	ErrMCPSendBusy = errors.New("too many sends are waiting for the user in Godwit; ask again once the user has answered them")
)

// MCPSend is a send request of godwit mcp waiting for the user, as the
// confirmation window shows it: the draft at the revision it is bound to,
// the sending account's address and the recipients that account had never
// written to when it was asked.
type MCPSend struct {
	ID        int64
	Draft     Draft
	From      string
	FirstTime []string
}

// mcpSend is a send request's record, the approval of an MCP send. It is not
// bound to a Wit run, so it is not an Approval: it lives in memory, pending
// or executing, and ends audited (local_mcp) as done, failed, rejected or
// stale, the states an approval ends in. A restart drops it unsent.
type mcpSend struct {
	draftID, revision int64
	executing         bool
	created           time.Time
	from              string
	firstTime         []string
}

func (r *mcpSend) expired() bool { return time.Since(r.created) >= mcpSendTTL }

// RequestMCPSend is godwit mcp's send_draft: what it does follows
// Settings.MCPSend. 不允许 (or 每次确认 headless) is ErrMCPSendOff. 每次确认
// records a request bound to the draft's revision, one per draft (a newer
// revision's replaces the older), at most maxMCPSends (ErrMCPSendBusy), for
// the user to confirm (PendingMCPSends, ResolveMCPSend), and announces
// mcp_sends.changed; sent is false. 直接发送 queues the draft as Send does,
// audited as an MCP send, unless a compose window has it
// (ErrMCPSendComposing); sent is true. A revision other than the draft's is
// ErrStaleDraft.
func (s *Service) RequestMCPSend(ctx context.Context, draftID, revision int64) (sent bool, err error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return false, err
	}
	defer end()
	st, err := s.loadSettings(ctx)
	if err != nil {
		return false, err
	}
	revision = max(revision, 0) // a negative one would mean any
	switch level := cmp.Or(st.MCPSend, MCPSendConfirm); {
	case level == MCPSendDirect:
		if s.opts.ComposeOpen != nil && s.opts.ComposeOpen(draftID) {
			return false, ErrMCPSendComposing
		}
		if err := s.send(ctx, draftID, revision); err != nil {
			return false, err
		}
		if err := s.auditMCPSend(ctx, draftID, "direct"); err != nil { // the mail is queued: still sent
			slog.Default().Warn("could not audit an MCP send", "draft", draftID, "err", err)
		}
		return true, nil
	case level == MCPSendOff || !s.opts.ConfirmWindow:
		return false, ErrMCPSendOff
	}
	s.editMu.Lock()
	d, err := s.GetDraft(ctx, draftID)
	s.editMu.Unlock()
	if err != nil {
		return false, err
	}
	if d.Revision != revision {
		return false, ErrStaleDraft
	}
	if _, err := recipients(d); err != nil {
		return false, err
	}
	acc, err := s.account(ctx, d.AccountID)
	if err != nil {
		return false, err
	}
	first, err := s.firstTime(ctx, acc, d)
	if err != nil {
		return false, err
	}
	s.sendReqMu.Lock()
	var older []int64 // the draft's requests this one replaces, and expired ones
	waiting := 0
	for id, r := range s.sendReqs {
		switch {
		case r.executing:
		case r.expired():
			older = append(older, id)
		case r.draftID != draftID:
			waiting++
		case r.revision == revision:
			s.sendReqMu.Unlock()
			return false, nil // asked already
		default:
			older = append(older, id)
		}
	}
	endOlder := func() {
		for _, id := range older {
			if err := s.endMCPSend(ctx, id, false, "stale"); err != nil && !errors.Is(err, ErrInvalidState) {
				slog.Default().Warn("could not audit an MCP send", "draft", draftID, "err", err)
			}
		}
	}
	if waiting >= maxMCPSends {
		s.sendReqMu.Unlock()
		endOlder()
		return false, ErrMCPSendBusy
	}
	if s.sendReqs == nil {
		s.sendReqs = map[int64]*mcpSend{}
	}
	s.sendReqID++
	s.sendReqs[s.sendReqID] = &mcpSend{draftID: draftID, revision: revision, created: time.Now(), from: acc.Email, firstTime: first}
	s.sendReqMu.Unlock()
	endOlder()
	s.bus.Publish(events.Event{Type: events.MCPSendsChanged})
	return false, nil
}

// PendingMCPSends are the send requests waiting for the user, oldest first.
// One whose draft changed or went, or that waited mcpSendTTL, goes stale
// instead.
func (s *Service) PendingMCPSends(ctx context.Context) ([]MCPSend, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	s.sendReqMu.Lock()
	ids := make([]int64, 0, len(s.sendReqs))
	for id, r := range s.sendReqs {
		if !r.executing {
			ids = append(ids, id)
		}
	}
	s.sendReqMu.Unlock()
	slices.Sort(ids)
	var out []MCPSend
	for _, id := range ids {
		r := s.mcpSendRecord(id)
		if r == nil {
			continue
		}
		s.editMu.Lock()
		d, err := s.GetDraft(ctx, r.draftID)
		s.editMu.Unlock()
		if err != nil && !errors.Is(err, errDraftNotFound) {
			return nil, err
		}
		if r.expired() || err != nil || d.Revision != r.revision {
			if err := s.endMCPSend(ctx, id, false, "stale"); err != nil && !errors.Is(err, ErrInvalidState) {
				return nil, err
			}
			continue
		}
		out = append(out, MCPSend{ID: id, Draft: d, From: r.from, FirstTime: r.firstTime})
	}
	return out, nil
}

// ResolveMCPSend sends (accept) or drops the pending send request id, exactly
// once: another decision of it is ErrInvalidState. Accepted, it sends the
// revision the request is bound to, as SendRevision does; one changed since,
// or a request that waited mcpSendTTL, is stale (ErrStaleDraft) and sends nothing.
func (s *Service) ResolveMCPSend(ctx context.Context, id int64, accept bool) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	if !accept {
		return s.endMCPSend(ctx, id, false, "rejected")
	}
	r := s.claimMCPSend(id)
	if r == nil {
		return ErrInvalidState
	}
	if r.expired() {
		return errors.Join(ErrStaleDraft, s.endMCPSend(ctx, id, true, "stale"))
	}
	serr := s.send(ctx, r.draftID, r.revision)
	decision := "executed"
	switch {
	case errors.Is(serr, ErrStaleDraft):
		decision = "stale"
	case serr != nil:
		decision = "failed"
	}
	aerr := s.endMCPSend(ctx, id, true, decision)
	if serr == nil && aerr != nil { // the mail is queued: still sent
		slog.Default().Warn("could not audit an MCP send", "draft", r.draftID, "err", aerr)
		return nil
	}
	return errors.Join(serr, aerr)
}

func (s *Service) mcpSendRecord(id int64) *mcpSend {
	s.sendReqMu.Lock()
	defer s.sendReqMu.Unlock()
	return s.sendReqs[id]
}

// claimMCPSend moves pending request id to executing; nil if it is not pending.
func (s *Service) claimMCPSend(id int64) *mcpSend {
	s.sendReqMu.Lock()
	defer s.sendReqMu.Unlock()
	r := s.sendReqs[id]
	if r == nil || r.executing {
		return nil
	}
	r.executing = true
	return r
}

// endMCPSend ends request id, if it is executing (claimed) or else pending,
// with decision, which it audits; ErrInvalidState if it is not.
func (s *Service) endMCPSend(ctx context.Context, id int64, claimed bool, decision string) error {
	s.sendReqMu.Lock()
	r := s.sendReqs[id]
	if r == nil || r.executing != claimed {
		s.sendReqMu.Unlock()
		return ErrInvalidState
	}
	delete(s.sendReqs, id)
	s.sendReqMu.Unlock()
	s.bus.Publish(events.Event{Type: events.MCPSendsChanged})
	return s.auditMCPSend(ctx, r.draftID, decision)
}

// auditMCPSend records an MCP send of draftID decided as decision.
func (s *Service) auditMCPSend(ctx context.Context, draftID int64, decision string) error {
	return s.gateway().Record(ctx, gateway.Entry{Source: AuditMCP, Tool: "send_draft", TargetKind: "draft", TargetID: draftID, Decision: decision})
}

// firstTime are d's recipients the account has never written to: no message
// of the account from its address has them in To or Cc.
func (s *Service) firstTime(ctx context.Context, acc Account, d Draft) ([]string, error) {
	rcpts, err := recipients(d)
	if err != nil {
		return nil, nil // not sendable: confirming fails anyway
	}
	self := mail.AddrKey(acc.Email)
	var out []string
	for _, a := range rcpts {
		k := mail.AddrKey(a)
		is := func(x string) bool { return mail.AddrKey(x) == k }
		ms, err := s.mailWith(ctx, acc.ID, k, 1, []string{"to_addrs", "cc_addrs"}, func(m MessageSummary) bool {
			return mail.AddrKey(m.From) == self && (slices.ContainsFunc(m.To, is) || slices.ContainsFunc(m.Cc, is))
		})
		if err != nil {
			return nil, err
		}
		if len(ms) == 0 {
			out = append(out, a)
		}
	}
	return out, nil
}
