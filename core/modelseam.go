package core

// The seam between plugin judge models and registry-hosted model files
// (godwit-email/godwit#90): pluginjudge.go calls modelAssets; until those
// files land, noModelAssets says every model with assets needs an update.

import "context"

// ModelState is a model's state as the model list shows it.
type ModelState string

const (
	ModelNotDownloaded ModelState = "not-downloaded"
	ModelDownloading   ModelState = "downloading"
	ModelVerifying     ModelState = "verifying"
	ModelDownloaded    ModelState = "downloaded"
	ModelLoading       ModelState = "loading"
	ModelReady         ModelState = "ready"
	ModelErrorState    ModelState = "error"
)

// Model error codes the host sets (8.1, 9).
const (
	codeModelNotReady       = "model_not_ready"
	codeModelUnavailable    = "model_unavailable"
	codeInsufficientMemory  = "insufficient_memory"
	codeUnsupportedPlatform = "unsupported_platform"
	codeUpdateRequired      = "update_required"
	codeVerificationFailed  = "verification_failed"
)

// ModelError is why a model cannot be used now; Code is machine-readable.
type ModelError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *ModelError) Error() string { return e.Message }

// ModelStatus is a model's download state; Error is set in state error, and
// with model_not_ready in state not-downloaded.
type ModelStatus struct {
	State           ModelState  `json:"state"`
	DownloadedBytes int64       `json:"downloadedBytes"`
	TotalBytes      int64       `json:"totalBytes"`
	Error           *ModelError `json:"error,omitempty"`
}

// modelAssets only manages files; the caller owns loading and readiness.
// plugin is manifest.name; release is the installed release's assets digest
// (plugins.AssetsDigest); refs are the model's assets, group name to variant
// ID. Fixed assets (a group with one variant no ref names) come with every
// model of the release.
type modelAssets interface {
	// Status reads trusted metadata and the disk only: no download, no hash.
	Status(ctx context.Context, plugin, release string, refs map[string]string) (ModelStatus, error)
	// Prepare downloads what is missing and verifies every file; only on an
	// explicit user action. dirs are the verified directories by group name.
	Prepare(ctx context.Context, plugin, release string, refs map[string]string,
		progress func(ModelStatus)) (dirs map[string]string, releaseLease func(), err error)
	// Verify is Prepare without downloading, for each new process and switch.
	Verify(ctx context.Context, plugin, release string, refs map[string]string) (dirs map[string]string, releaseLease func(), err error)
	// Remove deletes the model's variant files no lease holds; fixed assets stay.
	Remove(ctx context.Context, plugin, release string, refs map[string]string) error
}

// noModelAssets is modelAssets until registry-hosted model files are
// implemented: a model with assets cannot be prepared yet.
type noModelAssets struct{}

func errUpdateRequired() error {
	return &ModelError{Code: codeUpdateRequired, Message: "this Godwit cannot download the model's files yet; update Godwit"}
}

func (noModelAssets) Status(context.Context, string, string, map[string]string) (ModelStatus, error) {
	return ModelStatus{State: ModelErrorState, Error: errUpdateRequired().(*ModelError)}, nil
}

func (noModelAssets) Prepare(context.Context, string, string, map[string]string, func(ModelStatus)) (map[string]string, func(), error) {
	return nil, nil, errUpdateRequired()
}

func (noModelAssets) Verify(context.Context, string, string, map[string]string) (map[string]string, func(), error) {
	return nil, nil, errUpdateRequired()
}

func (noModelAssets) Remove(context.Context, string, string, map[string]string) error { return nil }
