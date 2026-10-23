package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelCatalogFetch(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"acme": {"api": "https://llm.acme.test/v1", "models": {"big": {"name": "Big One", "limit": {"context": 100000}}}}}`)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	s, err := New(t.Context(), Options{DataDir: dir, ModelsDev: srv.URL})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return s.historyBudget("https://llm.acme.test/v1", "big") == 80000 },
		5*time.Second, 10*time.Millisecond, "fetched at start: 80% of the window")
	assert.Equal(t, defaultHistoryBudget, s.historyBudget("https://llm.acme.test/v1", "small"), "an unlisted model")
	assert.Equal(t, map[string]string{"big": "Big One"}, s.catalogNames(ModelService{BaseURL: "https://llm.acme.test/v1", Models: []string{"big", "small"}}))
	require.NoError(t, s.Close())
	assert.FileExists(t, filepath.Join(dir, catalogFile))
	assert.EqualValues(t, 1, hits.Load(), "an unlisted model fetches no sooner than catalogMissing")

	s, err = New(t.Context(), Options{DataDir: dir, ModelsDev: srv.URL})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	assert.Equal(t, 80000, s.historyBudget("https://llm.acme.test/v1", "big"), "the cache, at once")
	assert.EqualValues(t, 1, hits.Load(), "a fresh cache is not fetched again")
}
