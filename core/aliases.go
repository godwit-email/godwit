package core

import (
	"context"
	"slices"

	"github.com/godwit-email/godwit/core/store"
)

// resolve returns ids with the ID of each message merged into another (ADR
// 0034) replaced by the kept message's, so an ID held from before a merge
// still names its message. Every Service entry point that takes message IDs
// resolves them here first; ids itself is left as it was.
func (s *Service) resolve(ctx context.Context, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return ids, nil
	}
	rows, err := s.db.Query(ctx, "SELECT id, message_id FROM message_aliases WHERE id IN ("+placeholders(len(ids))+")", int64Args(ids)...)
	if err != nil || len(rows.Values) == 0 {
		return ids, err
	}
	to := map[int64]int64{}
	for _, v := range rows.Values {
		to[store.Int(v[0])] = store.Int(v[1])
	}
	out := slices.Clone(ids)
	for i, id := range out {
		if k, ok := to[id]; ok {
			out[i] = k
		}
	}
	return out, nil
}

// resolveTarget is t with a message's ID (kind message or selection) resolved.
func (s *Service) resolveTarget(ctx context.Context, t Target) (Target, error) {
	var err error
	if t.Kind == "message" || t.Kind == "selection" {
		t.ID, err = s.resolveID(ctx, t.ID)
	}
	return t, err
}

// resolveID is resolve for one ID.
func (s *Service) resolveID(ctx context.Context, id int64) (int64, error) {
	ids, err := s.resolve(ctx, []int64{id})
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}
