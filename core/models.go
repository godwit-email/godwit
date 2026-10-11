package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
	"github.com/zalando/go-keyring"
)

var (
	ErrAINotConfigured      = errors.New("no model configured")
	ErrModelServiceNotFound = errors.New("model service not found")
	ErrModelNotFound        = errors.New("model not found")
)

type ModelService struct {
	ID                  int64
	Name, Kind, BaseURL string
	Models              []string
	HasKey              bool
	Version             int64
}

type ModelServiceInput struct {
	Name, Kind, BaseURL, APIKey string
	Models                      []string
}

func (s *Service) modelKeyUser(id int64) string {
	return s.installID + "/model:" + strconv.FormatInt(id, 10)
}

func (in ModelServiceInput) validate() error {
	if in.Name == "" || strings.Contains(in.Name, "/") {
		return errors.New("service name must be non-empty and contain no \"/\"")
	}
	if in.Kind != "openai" && in.Kind != "anthropic" {
		return fmt.Errorf("invalid service kind %q", in.Kind)
	}
	u, err := url.Parse(in.BaseURL)
	if err != nil || u.User != nil || u.Hostname() == "" || !(u.Scheme == "https" || u.Scheme == "http" && isLoopback(u.Hostname())) {
		return errors.New("base URL must be https://, or http:// to a loopback address")
	}
	return nil
}

const modelCols = "id, name, kind, base_url, models, version"

func (s *Service) scanModelService(r []any) (ModelService, error) {
	m := ModelService{ID: store.Int(r[0]), Name: store.Str(r[1]), Kind: store.Str(r[2]), BaseURL: store.Str(r[3]), Version: store.Int(r[5])}
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
	if err := in.validate(); err != nil {
		return ModelService{}, err
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	res, err := s.db.Execute(ctx, "INSERT INTO model_services(name, kind, base_url, models, created_at) VALUES (?, ?, ?, ?, ?)",
		in.Name, in.Kind, in.BaseURL, modelsJSON(in.Models), time.Now().Unix())
	if err != nil {
		return ModelService{}, err
	}
	id := res.LastInsertID
	if in.APIKey != "" {
		if err := keychainSet(s.modelKeyUser(id), in.APIKey); err != nil {
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

// UpdateModelService replaces the service's fields and bumps its version; an empty
// APIKey keeps the stored key.
func (s *Service) UpdateModelService(ctx context.Context, id int64, in ModelServiceInput) (ModelService, error) {
	ctx, end, err := s.begin(ctx, 0)
	if err != nil {
		return ModelService{}, err
	}
	defer end()
	if err := in.validate(); err != nil {
		return ModelService{}, err
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	old, err := s.modelServiceBy(ctx, "id = ?", id)
	if err != nil {
		return ModelService{}, err
	}
	// Row first: a failure here (e.g. duplicate name) leaves the keychain alone.
	if _, err := s.db.Execute(ctx, "UPDATE model_services SET name = ?, kind = ?, base_url = ?, models = ?, version = version + 1 WHERE id = ?",
		in.Name, in.Kind, in.BaseURL, modelsJSON(in.Models), id); err != nil {
		return ModelService{}, err
	}
	if in.APIKey != "" {
		if err := keychainSet(s.modelKeyUser(id), in.APIKey); err != nil {
			uctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, derr := s.db.Execute(uctx, "UPDATE model_services SET name = ?, kind = ?, base_url = ?, models = ?, version = ? WHERE id = ?",
				old.Name, old.Kind, old.BaseURL, modelsJSON(old.Models), old.Version, id); derr != nil {
				err = errors.Join(err, derr)
			}
			return ModelService{}, fmt.Errorf("save API key to keychain: %w", err)
		}
	}
	s.forgetModel(id)
	// The write is done: a cancelled ctx must not turn it into an error.
	return s.modelServiceBy(context.WithoutCancel(ctx), "id = ?", id)
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
// calls. The engine redacts every key it is handed from Genkit's logs; no other
// secret reaches Genkit.
func (s *Service) aiEngine() (*ai.Engine, error) {
	s.aiMu.Lock()
	defer s.aiMu.Unlock()
	if s.ai == nil {
		e, err := ai.NewEngine(slog.Default(), nil)
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
	ref := override
	if ref == "" {
		st, err := s.loadSettings(ctx)
		if err != nil {
			return ai.ResolvedModel{}, err
		}
		if ref = st.DefaultModel; ref == "" {
			return ai.ResolvedModel{}, ErrAINotConfigured
		}
	}
	r, err := ai.ParseModelRef(ref)
	if err != nil {
		return ai.ResolvedModel{}, err
	}
	// The row and the key as one snapshot: never one service's URL with another's key.
	s.modelMu.RLock()
	defer s.modelMu.RUnlock()
	m, err := s.modelServiceBy(ctx, "name = ?", r.Service)
	if err != nil {
		return ai.ResolvedModel{}, err
	}
	found := false
	for _, x := range m.Models {
		found = found || x == r.Model
	}
	if !found {
		return ai.ResolvedModel{}, fmt.Errorf("%w: %s", ErrModelNotFound, ref)
	}
	key, err := keychainGet(s.modelKeyUser(m.ID))
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return ai.ResolvedModel{}, err
	}
	return ai.ResolvedModel{ServiceID: m.ID, Version: m.Version, Kind: m.Kind, BaseURL: m.BaseURL, Model: r.Model, APIKey: key}, nil
}
