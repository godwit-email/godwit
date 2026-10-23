package core

// What models.dev says of models (ai.ModelCatalog): their display names and
// context windows. Fetched in the background, kept in the data directory, and
// fetched again once a day old, or six hours old when a model it does not
// list is asked about.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/store"
)

const (
	catalogFile    = "models-dev.json"
	catalogStale   = 24 * time.Hour
	catalogMissing = 6 * time.Hour // how old, and how long since the last try, before an unlisted model fetches
	catalogLook    = time.Hour     // how often the refresh looks at the cache's age
	catalogTimeout = 30 * time.Second
	catalogMax     = 64 << 20 // bytes of api.json read
)

// defaultHistoryBudget caps the runes of the conversation state sent to a
// model whose context window is unknown.
const defaultHistoryBudget = 60000

// historyBudget caps the runes of the conversation state sent to model on the
// service at baseURL: 80% of its context window, the rest left for the system
// prompt, the tools and the run's own turns.
// shortcut: a rune counted as a token, safe for CJK but trims English JSON at
// about a third of the window; count with the provider's tokens if that bites.
func (s *Service) historyBudget(baseURL, model string) int {
	if m, _ := s.catalogModel(baseURL, model); m.Context > 0 {
		return m.Context * 8 / 10
	}
	return defaultHistoryBudget
}

// refHistoryBudget is historyBudget for a conversation's model ref ("": the
// default model), without resolving its key.
func (s *Service) refHistoryBudget(ctx context.Context, ref string) int {
	baseURL, model, ok := s.refModel(ctx, ref)
	if !ok {
		return defaultHistoryBudget
	}
	return s.historyBudget(baseURL, model)
}

// ModelName is models.dev's display name for a "slug/model" ref's model; ""
// when it lists none.
func (s *Service) ModelName(ctx context.Context, ref string) string {
	baseURL, model, ok := s.refModel(ctx, ref)
	if !ok {
		return ""
	}
	m, _ := s.catalogModel(baseURL, model)
	return m.Name
}

// refModel is a model ref's ("": the default model) service address and
// model ID, without resolving its key; a plugin's or a gone service's has no
// address.
func (s *Service) refModel(ctx context.Context, ref string) (baseURL, model string, ok bool) {
	if ref == "" {
		if st, err := s.loadSettings(ctx); err == nil {
			ref = st.DefaultModel
		}
	}
	r, err := ai.ParseModelRef(ref)
	if err != nil {
		return "", "", false
	}
	s.modelMu.RLock()
	m, _ := s.modelServiceBy(ctx, "slug = ?", r.Service)
	s.modelMu.RUnlock()
	return m.BaseURL, r.Model, true
}

// catalogNames is models.dev's display names for the service's models, nil
// when it lists none.
func (s *Service) catalogNames(m ModelService) map[string]string {
	var names map[string]string
	for _, id := range m.Models {
		if c, _ := s.catalogModel(m.BaseURL, id); c.Name != "" {
			if names == nil {
				names = map[string]string{}
			}
			names[id] = c.Name
		}
	}
	return names
}

// catalogModel is model on the service at baseURL as models.dev lists it; an
// unlisted one may fetch the catalog again, in the background.
func (s *Service) catalogModel(baseURL, model string) (ai.CatalogModel, bool) {
	m, ok := s.catalog.Load().Model(baseURL, model)
	if !ok && s.opts.ModelsDev != "" && time.Since(time.Unix(0, s.catalogTry.Load())) >= catalogMissing &&
		s.catalogAge() >= catalogMissing {
		if ctx, end, err := s.begin(context.Background(), 0); err == nil {
			go func() {
				defer end()
				s.fetchCatalog(ctx)
			}()
		}
	}
	return m, ok
}

// loadCatalog applies the cached catalog, if there is one.
func (s *Service) loadCatalog() {
	data, err := os.ReadFile(filepath.Join(s.dataDir, catalogFile))
	if err != nil {
		return
	}
	if c, _, err := ai.ParseModelCatalog(data); err == nil {
		s.catalog.Store(c)
	}
}

// catalogAge is how old the cached catalog is; forever when there is none.
func (s *Service) catalogAge() time.Duration {
	st, err := os.Stat(filepath.Join(s.dataDir, catalogFile))
	if err != nil {
		return math.MaxInt64
	}
	return time.Since(st.ModTime())
}

// startCatalogRefresh fetches the catalog whenever it is stale, now and every
// catalogLook after, until Close.
func (s *Service) startCatalogRefresh() {
	ctx, end, err := s.begin(context.Background(), 0)
	if err != nil {
		return
	}
	go func() {
		defer end()
		t := time.NewTicker(catalogLook)
		defer t.Stop()
		for {
			if s.catalogAge() >= catalogStale {
				s.fetchCatalog(ctx)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// fetchCatalog fetches, caches and applies the catalog, one fetch at a time;
// a failure keeps what there was.
func (s *Service) fetchCatalog(ctx context.Context) {
	if !s.catalogBusy.CompareAndSwap(false, true) {
		return
	}
	defer s.catalogBusy.Store(false)
	s.catalogTry.Store(time.Now().UnixNano())
	if err := s.fetchCatalogOnce(ctx); err != nil {
		slog.Default().Warn("could not fetch the model catalog", "err", err)
	}
}

func (s *Service) fetchCatalogOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.opts.ModelsDev, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("models.dev: %s", res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, catalogMax))
	if err != nil {
		return err
	}
	c, trimmed, err := ai.ParseModelCatalog(data)
	if err != nil {
		return err
	}
	s.catalog.Store(c)
	return store.WriteFileAtomic(filepath.Join(s.dataDir, catalogFile), trimmed)
}
