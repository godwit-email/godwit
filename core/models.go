package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
	"github.com/zalando/go-keyring"
)

var errNoModels = errors.New("add at least one model")

var (
	ErrAINotConfigured      = errors.New("no model configured")
	ErrModelServiceNotFound = errors.New("model service not found")
	ErrModelNotFound        = errors.New("model not found")
)

type ModelService struct {
	ID                  int64
	Name, Kind, BaseURL string
	Slug                string // the service part of its "slug/model" references; kept on rename
	Models              []string
	HasKey              bool
	Version             int64
}

// Key actions for ModelServiceInput.KeyAction.
const (
	KeyKeep    = "keep"    // reuse the stored key; only while kind and address are unchanged
	KeyReplace = "replace" // use APIKey
	KeyClear   = "clear"   // no key
)

var (
	ErrModelAuth       = ai.ErrModelAuth
	ErrBaseURLInvalid  = ai.ErrBaseURLInvalid
	ErrBaseURLScheme   = ai.ErrBaseURLScheme
	ErrKeyNeedsConfirm = errors.New("the address or protocol changed: enter the key again or choose no key")
)

// ModelServiceInput is a service as the user entered it. An empty KeyAction
// means replace when APIKey is set, else keep (update) or none (add).
type ModelServiceInput struct {
	Name, Kind, BaseURL, APIKey string
	KeyAction                   string
	Models                      []string
}

func (s *Service) modelKeyUser(id int64) string {
	return s.installID + "/model:" + strconv.FormatInt(id, 10)
}

// prepare validates in, normalizes its address, and resolves its key action
// against service id (0: a new one) into the key requests should carry. The
// caller holds modelMu. A stored key is only kept for the address and protocol
// it was saved with: keys only go to the address they belong to.
func (s *Service) prepare(ctx context.Context, id int64, in *ModelServiceInput) (action, key string, err error) {
	if in.Name == "" || strings.Contains(in.Name, "/") {
		return "", "", errors.New("service name must be non-empty and contain no \"/\"")
	}
	if !ai.ValidKind(in.Kind) {
		return "", "", fmt.Errorf("invalid service kind %q", in.Kind)
	}
	if in.BaseURL, err = ai.NormalizeBaseURL(in.Kind, in.BaseURL); err != nil {
		return "", "", err
	}
	action = in.KeyAction
	if action == "" {
		action = KeyKeep
		if in.APIKey != "" {
			action = KeyReplace
		} else if id == 0 {
			action = KeyClear
		}
	}
	switch action {
	case KeyReplace:
		if in.APIKey == "" {
			return "", "", errors.New("enter an API key or choose no key")
		}
		return action, in.APIKey, nil
	case KeyClear:
		return action, "", nil
	case KeyKeep:
		if id == 0 {
			return "", "", errors.New("a new service has no stored key to keep")
		}
		old, err := s.modelServiceBy(ctx, "id = ?", id)
		if err != nil {
			return "", "", err
		}
		key, err := keychainGet(s.modelKeyUser(id))
		if errors.Is(err, keyring.ErrNotFound) || err == nil && key == "" {
			return action, "", nil // nothing to send anywhere
		}
		if err != nil {
			return "", "", err
		}
		oldURL, nerr := ai.NormalizeBaseURL(old.Kind, old.BaseURL)
		if nerr != nil || old.Kind != in.Kind || oldURL != in.BaseURL {
			return "", "", ErrKeyNeedsConfirm
		}
		return action, key, nil
	}
	return "", "", fmt.Errorf("invalid key action %q", action)
}

const modelCols = "id, name, kind, base_url, models, version, slug"

func (s *Service) scanModelService(r []any) (ModelService, error) {
	m := ModelService{ID: store.Int(r[0]), Name: store.Str(r[1]), Kind: store.Str(r[2]), BaseURL: store.Str(r[3]), Version: store.Int(r[5]), Slug: store.Str(r[6])}
	if err := json.Unmarshal([]byte(store.Str(r[4])), &m.Models); err != nil {
		return m, fmt.Errorf("stored models: %w", err)
	}
	k, kerr := keychainGet(s.modelKeyUser(m.ID)) // a keychain error is just "no key" here
	m.HasKey = kerr == nil && k != ""
	return m, nil
}

func (s *Service) modelServiceBy(ctx context.Context, where string, arg any) (ModelService, error) {
	rows, err := s.db.Query(ctx, "SELECT "+modelCols+" FROM model_services WHERE "+where, arg)
	if err != nil {
		return ModelService{}, err
	}
	if len(rows.Values) == 0 {
		return ModelService{}, fmt.Errorf("%w: %v", ErrModelServiceNotFound, arg)
	}
	return s.scanModelService(rows.Values[0])
}

func (s *Service) ListModelServices(ctx context.Context) ([]ModelService, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	rows, err := s.db.Query(ctx, "SELECT "+modelCols+" FROM model_services ORDER BY name")
	if err != nil {
		return nil, err
	}
	out := []ModelService{}
	for _, r := range rows.Values {
		m, err := s.scanModelService(r)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func modelsJSON(m []string) string {
	if m == nil {
		m = []string{}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// AddModelService stores the service, then its key in the keychain; if the
// keychain fails the row is removed again.
func (s *Service) AddModelService(ctx context.Context, in ModelServiceInput) (ModelService, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ModelService{}, err
	}
	defer end()
	if len(in.Models) == 0 {
		return ModelService{}, errNoModels
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	_, key, err := s.prepare(ctx, 0, &in)
	if err != nil {
		return ModelService{}, err
	}
	if err := s.checkNameFree(ctx, in.Name, 0); err != nil {
		return ModelService{}, err
	}
	slug, err := s.newSlug(ctx, in.Name, 0)
	if err != nil {
		return ModelService{}, err
	}
	res, err := s.db.Execute(ctx, "INSERT INTO model_services(name, kind, base_url, models, slug, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		in.Name, in.Kind, in.BaseURL, modelsJSON(in.Models), slug, time.Now().Unix())
	if err != nil {
		return ModelService{}, err
	}
	id := res.LastInsertID
	if key != "" {
		if err := keychainSet(s.modelKeyUser(id), key); err != nil {
			uctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, derr := s.db.Execute(uctx, "DELETE FROM model_services WHERE id = ?", id); derr != nil {
				err = errors.Join(err, derr)
			}
			return ModelService{}, fmt.Errorf("save API key to keychain: %w", err)
		}
	}
	return s.modelServiceBy(ctx, "id = ?", id)
}

// UpdateModelService replaces the service's fields, applies the key action and
// bumps its version.
func (s *Service) UpdateModelService(ctx context.Context, id int64, in ModelServiceInput) (ModelService, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ModelService{}, err
	}
	defer end()
	if len(in.Models) == 0 {
		return ModelService{}, errNoModels
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	old, err := s.modelServiceBy(ctx, "id = ?", id)
	if err != nil {
		return ModelService{}, err
	}
	action, key, err := s.prepare(ctx, id, &in)
	if err != nil {
		return ModelService{}, err
	}
	if err := s.checkNameFree(ctx, in.Name, id); err != nil {
		return ModelService{}, err
	}
	// Row first: a failure here (e.g. duplicate name) leaves the keychain alone.
	if _, err := s.db.Execute(ctx, "UPDATE model_services SET name = ?, kind = ?, base_url = ?, models = ?, version = version + 1 WHERE id = ?",
		in.Name, in.Kind, in.BaseURL, modelsJSON(in.Models), id); err != nil {
		return ModelService{}, err
	}
	switch action {
	case KeyReplace:
		err = keychainSet(s.modelKeyUser(id), key)
	case KeyClear:
		if err = keychainDelete(s.modelKeyUser(id)); errors.Is(err, keyring.ErrNotFound) {
			err = nil
		}
	}
	if err != nil {
		uctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, derr := s.db.Execute(uctx, "UPDATE model_services SET name = ?, kind = ?, base_url = ?, models = ?, version = ? WHERE id = ?",
			old.Name, old.Kind, old.BaseURL, modelsJSON(old.Models), old.Version, id); derr != nil {
			err = errors.Join(err, derr)
		}
		return ModelService{}, fmt.Errorf("update API key in keychain: %w", err)
	}
	s.forgetModel(id)
	// The write is done: a cancelled ctx must not turn it into an error.
	return s.modelServiceBy(context.WithoutCancel(ctx), "id = ?", id)
}

// checkNameFree refuses a name that is another service's slug: references
// resolve by slug first, so that service would answer to it.
func (s *Service) checkNameFree(ctx context.Context, name string, id int64) error {
	rows, err := s.db.Query(ctx, "SELECT 1 FROM model_services WHERE slug = ? AND id != ?", name, id)
	if err != nil {
		return err
	}
	if len(rows.Values) > 0 {
		return fmt.Errorf("service name %q is another service's reference name", name)
	}
	return nil
}

func (s *Service) RemoveModelService(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	if _, err := s.db.Execute(ctx, "DELETE FROM model_services WHERE id = ?", id); err != nil {
		return err
	}
	s.forgetModel(id)
	if err := keychainDelete(s.modelKeyUser(id)); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}

// FetchModels lists the service's models, sorted. It saves nothing.
func (s *Service) FetchModels(ctx context.Context, id int64) ([]string, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer end()
	return s.fetchModels(ctx, id)
}

func (s *Service) fetchModels(ctx context.Context, id int64) ([]string, error) {
	m, key, err := s.modelServiceKey(ctx, id)
	if err != nil {
		return nil, err
	}
	return ai.ListModels(ctx, s.modelHTTP, m.Kind, m.BaseURL, key)
}

// modelServiceKey reads the service and its key as one snapshot (modelMu).
func (s *Service) modelServiceKey(ctx context.Context, id int64) (ModelService, string, error) {
	s.modelMu.RLock()
	defer s.modelMu.RUnlock()
	m, err := s.modelServiceBy(ctx, "id = ?", id)
	if err != nil {
		return m, "", err
	}
	key, err := keychainGet(s.modelKeyUser(id))
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return m, "", err
	}
	return m, key, nil
}

// TestModelService runs the shortest real generation on the service's first
// saved model, or the first one it lists when none is saved.
func (s *Service) TestModelService(ctx context.Context, id int64) error {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return err
	}
	defer end()
	m, key, err := s.modelServiceKey(ctx, id)
	if err != nil {
		return err
	}
	models := m.Models
	if len(models) == 0 {
		if models, err = ai.ListModels(ctx, s.modelHTTP, m.Kind, m.BaseURL, key); err != nil {
			return err
		}
		if len(models) == 0 {
			return fmt.Errorf("%w: the service lists no models", ErrModelNotFound)
		}
	}
	e, err := s.aiEngine()
	if err != nil {
		return err
	}
	events, wait := e.Run(ctx, ai.Request{
		Model:    ai.ResolvedModel{ServiceID: m.ID, Version: m.Version, Kind: m.Kind, BaseURL: m.BaseURL, Model: models[0], APIKey: key},
		Prompt:   "Reply with OK.",
		MaxTurns: 1,
	})
	for range events {
	}
	if r := wait(); r.Err != nil {
		return r.Err
	}
	return nil
}

// aiEngine creates the engine on first use, so GENKIT_ENV=dev fails only AI
// calls. The engine redacts every model key it is handed, and every extension
// secret in s.secrets, from Genkit's logs, tool errors and run errors.
func (s *Service) aiEngine() (*ai.Engine, error) {
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	if s.ai == nil {
		e, err := ai.NewEngine(slog.Default(), s.secrets.Values)
		if err != nil {
			return nil, err
		}
		// No modelMu here: an update holds it while it calls Forget.
		e.Current = func(ctx context.Context, id int64) (int64, error) {
			rows, err := s.db.Query(context.WithoutCancel(ctx), "SELECT version FROM model_services WHERE id = ?", id)
			if err != nil || len(rows.Values) == 0 {
				return 0, cmp.Or(err, ErrModelServiceNotFound)
			}
			return store.Int(rows.Values[0][0]), nil
		}
		s.ai = e
	}
	return s.ai, nil
}

// forgetModel drops the service's Genkit instance after a change or removal.
func (s *Service) forgetModel(id int64) {
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	if s.ai != nil {
		s.ai.Forget(id)
	}
}

// resolveModel resolves override, or Settings.DefaultModel when empty.
func (s *Service) resolveModel(ctx context.Context, override string) (ai.ResolvedModel, error) {
	m, _, err := s.resolveRef(ctx, override)
	return m, err
}

// resolveTask resolves a skill's or quick replies' model: override, else
// SmallModel when small, else DefaultModel. A SmallModel that is unset or names
// a removed service or model falls back to DefaultModel; other errors return.
func (s *Service) resolveTask(ctx context.Context, override string, small bool) (ai.ResolvedModel, string, error) {
	if override == "" && small {
		st, err := s.loadSettings(ctx)
		if err != nil {
			return ai.ResolvedModel{}, "", err
		}
		if st.SmallModel != "" {
			m, ref, err := s.resolveRef(ctx, st.SmallModel)
			if !errors.Is(err, ErrModelServiceNotFound) && !errors.Is(err, ErrModelNotFound) {
				return m, ref, err
			}
		}
	}
	return s.resolveRef(ctx, override)
}

// resolveRef is resolveModel plus the reference in its "slug/model" form. The
// service part matches a slug, else a name (references from before slugs).
func (s *Service) resolveRef(ctx context.Context, override string) (ai.ResolvedModel, string, error) {
	ref := override
	if ref == "" {
		st, err := s.loadSettings(ctx)
		if err != nil {
			return ai.ResolvedModel{}, "", err
		}
		if ref = st.DefaultModel; ref == "" {
			return ai.ResolvedModel{}, "", ErrAINotConfigured
		}
	}
	r, err := ai.ParseModelRef(ref)
	if err != nil {
		return ai.ResolvedModel{}, "", err
	}
	// The row and the key as one snapshot: never one service's URL with another's key.
	s.modelMu.RLock()
	defer s.modelMu.RUnlock()
	m, err := s.modelServiceBy(ctx, "slug = ?", r.Service)
	if errors.Is(err, ErrModelServiceNotFound) {
		m, err = s.modelServiceBy(ctx, "name = ?", r.Service)
	}
	if err != nil {
		return ai.ResolvedModel{}, "", err
	}
	found := false
	for _, x := range m.Models {
		found = found || x == r.Model
	}
	if !found {
		return ai.ResolvedModel{}, "", fmt.Errorf("%w: %s", ErrModelNotFound, ref)
	}
	key, err := keychainGet(s.modelKeyUser(m.ID))
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return ai.ResolvedModel{}, "", err
	}
	return ai.ResolvedModel{ServiceID: m.ID, Version: m.Version, Kind: m.Kind, BaseURL: m.BaseURL, Model: r.Model, APIKey: key},
		m.Slug + "/" + r.Model, nil
}
