package core

// The files of a plugin's models with registry-hosted assets (a local judge's
// weights and engine): their state in the model list, and the user's explicit
// download and removal. A judge call never downloads (pluginjudge.go).

import (
	"context"
	"errors"
	"fmt"

	"github.com/godwit-email/godwit/core/events"
	"github.com/godwit-email/godwit/core/plugins"
)

// modelStatuses are the file states of ms's models with assets, by model ID.
func (s *Service) modelStatuses(ctx context.Context, name string, rp *runningPlugin, ms []plugins.ProviderModel) map[string]ModelStatus {
	var out map[string]ModelStatus
	for _, m := range ms {
		if len(m.Assets) == 0 {
			continue
		}
		st, err := s.modelAssets().Status(ctx, name, rp.row.assetsDigest, m.Assets)
		if err != nil {
			st = ModelStatus{State: ModelErrorState, Error: &ModelError{Code: codeModelUnavailable, Message: s.cleanError(err).Error()}}
		}
		if out == nil {
			out = map[string]ModelStatus{}
		}
		out[m.ID] = st
	}
	return out
}

// errNoModelFiles: the model has no registry-hosted files to download or remove.
var errNoModelFiles = errors.New("this model has no files to download")

// assetModel is the plugin model ref names when it has registry-hosted files.
func (s *Service) assetModel(ctx context.Context, ref string) (string, *runningPlugin, plugins.ProviderModel, error) {
	name, rp, m, _, err := s.pluginModel(ctx, ref)
	if err != nil {
		return "", nil, m, err
	}
	if len(m.Assets) == 0 {
		return "", nil, m, fmt.Errorf("%w: %s", errNoModelFiles, ref)
	}
	return name, rp, m, nil
}

// PrepareModel downloads what plugin model ref is missing of its files and
// verifies them all, then returns their state; ModelsChanged follows.
func (s *Service) PrepareModel(ctx context.Context, ref string) (ModelStatus, error) {
	return s.PrepareModelWithProgress(ctx, ref, nil)
}

// PrepareModelWithProgress is PrepareModel reporting downloading and
// verifying through progress, called on the calling goroutine, never after return.
func (s *Service) PrepareModelWithProgress(ctx context.Context, ref string, progress func(ModelStatus)) (ModelStatus, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ModelStatus{}, err
	}
	defer end()
	name, rp, m, err := s.assetModel(ctx, ref)
	if err != nil {
		return ModelStatus{}, err
	}
	defer s.bus.Publish(events.Event{Type: events.ModelsChanged})
	_, release, err := s.modelAssets().Prepare(ctx, name, rp.row.assetsDigest, m.Assets, progress)
	if err != nil {
		return ModelStatus{}, err
	}
	release() // verified; a judge call verifies again before it loads them
	return s.modelAssets().Status(ctx, name, rp.row.assetsDigest, m.Assets)
}

// RemoveModelFiles deletes plugin model ref's own files, keeping those other
// models share; a model the plugin has loaded is in use and stays.
func (s *Service) RemoveModelFiles(ctx context.Context, ref string) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	name, rp, m, err := s.assetModel(ctx, ref)
	if err != nil {
		return err
	}
	defer s.bus.Publish(events.Event{Type: events.ModelsChanged})
	return s.modelAssets().Remove(ctx, name, rp.row.assetsDigest, m.Assets)
}
