package core

import (
	"context"

	"github.com/godwit-email/godwit/core/plugins"
)

type ProviderModel = plugins.ProviderModel

type ModelState string

const (
	ModelNotDownloaded ModelState = "not-downloaded"
	ModelDownloading   ModelState = "downloading"
	ModelVerifying     ModelState = "verifying"
	ModelDownloaded    ModelState = "downloaded"
	ModelLoading       ModelState = "loading"
	ModelReady         ModelState = "ready"
	ModelFailed        ModelState = "error"
)

type ModelError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *ModelError) Error() string    { return e.Message }
func (*ModelError) ModelRequestError() {}

type ModelStatus struct {
	State           ModelState  `json:"state"`
	DownloadedBytes int64       `json:"downloadedBytes"`
	TotalBytes      int64       `json:"totalBytes"`
	Error           *ModelError `json:"error,omitempty"`
}

type ListedModel struct {
	Ref    string        `json:"ref"`
	Model  ProviderModel `json:"model"`
	Status ModelStatus   `json:"status"`
}

// modelAssets only manages files; the caller owns loading and readiness.
type modelAssets interface {
	Status(ctx context.Context, plugin, release string, refs map[string]string) (ModelStatus, error)
	Prepare(ctx context.Context, plugin, release string, refs map[string]string, progress func(ModelStatus)) (dirs map[string]string, releaseLease func(), err error)
	Verify(ctx context.Context, plugin, release string, refs map[string]string) (dirs map[string]string, releaseLease func(), err error)
	Remove(ctx context.Context, plugin, release string, refs map[string]string) error
}

func newModelAssets(s *Service) modelAssets { return unavailableModelAssets{} }

type unavailableModelAssets struct{}

func modelAssetsUnsupported() *ModelError {
	return &ModelError{Code: "update_required", Message: "Update Godwit to prepare this model"}
}
func (unavailableModelAssets) Status(ctx context.Context, plugin, release string, refs map[string]string) (ModelStatus, error) {
	if err := ctx.Err(); err != nil {
		return ModelStatus{}, err
	}
	return ModelStatus{State: ModelFailed, Error: modelAssetsUnsupported()}, nil
}
func (unavailableModelAssets) Prepare(ctx context.Context, plugin, release string, refs map[string]string, progress func(ModelStatus)) (map[string]string, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return nil, nil, modelAssetsUnsupported()
}
func (a unavailableModelAssets) Verify(ctx context.Context, plugin, release string, refs map[string]string) (map[string]string, func(), error) {
	return a.Prepare(ctx, plugin, release, refs, nil)
}
func (unavailableModelAssets) Remove(ctx context.Context, plugin, release string, refs map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return modelAssetsUnsupported()
}
