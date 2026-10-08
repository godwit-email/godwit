package core

import (
	"context"
	"strconv"
	"testing"

	"errors"
	"github.com/godwit-email/godwit/core/ai"
	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
	"net/http"
	"net/http/httptest"
	"strings"
)

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func modelSvc(t *testing.T) *Service {
	t.Helper()
	keyring.MockInit()
	s, err := New(context.Background(), Options{DataDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestModelServiceCRUD(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "or", Kind: "openai", BaseURL: "https://example.com/v1", APIKey: "k1", Models: []string{"m"}})
	require.NoError(t, err)
	assert.True(t, ms.HasKey)
	assert.EqualValues(t, 1, ms.Version)
	k, err := keyring.Get(keyringService, s.installID+"/model:"+itoa(ms.ID))
	require.NoError(t, err)
	assert.Equal(t, "k1", k)

	ms2, err := s.UpdateModelService(bg, ms.ID, ModelServiceInput{Name: "or", Kind: "openai", BaseURL: "https://example.com/v1", APIKey: "k2", Models: []string{"m", "n"}})
	require.NoError(t, err)
	assert.EqualValues(t, 2, ms2.Version)
	k, err = keyring.Get(keyringService, s.installID+"/model:"+itoa(ms.ID))
	require.NoError(t, err)
	assert.Equal(t, "k2", k)

	list, err := s.ListModelServices(bg)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, []string{"m", "n"}, list[0].Models)

	require.NoError(t, s.RemoveModelService(bg, ms.ID))
	_, err = keyring.Get(keyringService, s.installID+"/model:"+itoa(ms.ID))
	assert.ErrorIs(t, err, keyring.ErrNotFound)
	list, err = s.ListModelServices(bg)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestUpdateKeepsKeyWhenEmpty(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	in := ModelServiceInput{Name: "a", Kind: "anthropic", BaseURL: "https://example.com", APIKey: "keep"}
	ms, err := s.AddModelService(bg, in)
	require.NoError(t, err)
	in.APIKey, in.Name = "", "b"
	ms, err = s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	assert.True(t, ms.HasKey)
	assert.Equal(t, "b", ms.Name)
	k, _ := keyring.Get(keyringService, s.installID+"/model:"+itoa(ms.ID))
	assert.Equal(t, "keep", k)
}

func TestFetchModelsBothKinds(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			s, bg := modelSvc(t), context.Background()
			srv := testllm.Start(t, kind)
			srv.SetModels("zeta", "alpha")
			srv.RequireKey("right")
			ms, err := s.AddModelService(bg, ModelServiceInput{Name: "x", Kind: kind, BaseURL: srv.URL, APIKey: "right"})
			require.NoError(t, err)
			got, err := s.FetchModels(bg, ms.ID)
			require.NoError(t, err)
			assert.Equal(t, []string{"alpha", "zeta"}, got)
			assert.NoError(t, s.TestModelService(bg, ms.ID))
			list, err := s.ListModelServices(bg)
			require.NoError(t, err)
			require.NotEmpty(t, list)
			assert.Empty(t, list[0].Models, "FetchModels must not save")

			bad, err := s.AddModelService(bg, ModelServiceInput{Name: "y", Kind: kind, BaseURL: srv.URL, APIKey: "wrong-key-123"})
			require.NoError(t, err)
			_, err = s.FetchModels(bg, bad.ID)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "wrong-key-123")
		})
	}
}

func TestFetchModelsAnthropicNoKey(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	srv := testllm.Start(t, "anthropic")
	srv.SetModels("m")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "gw", Kind: "anthropic", BaseURL: srv.URL})
	require.NoError(t, err)
	_, err = s.FetchModels(bg, ms.ID)
	require.NoError(t, err)
	h := srv.Headers()
	require.Len(t, h, 1)
	assert.Empty(t, h[0].Get("x-api-key"))
	assert.Empty(t, h[0].Get("Authorization"))
	assert.Equal(t, "2023-06-01", h[0].Get("anthropic-version"))
}

func TestModelServiceValidation(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	for name, in := range map[string]ModelServiceInput{
		"empty name":   {Name: "", Kind: "openai", BaseURL: "https://a.com"},
		"slash name":   {Name: "a/b", Kind: "openai", BaseURL: "https://a.com"},
		"bad kind":     {Name: "a", Kind: "gemini", BaseURL: "https://a.com"},
		"http remote":  {Name: "a", Kind: "openai", BaseURL: "http://a.com"},
		"ftp":          {Name: "a", Kind: "openai", BaseURL: "ftp://localhost"},
		"no host":      {Name: "a", Kind: "openai", BaseURL: "https://"},
		"anthropic no": {Name: "a", Kind: "anthropic", BaseURL: "http://example.org:80"},
		"userinfo":     {Name: "a", Kind: "openai", BaseURL: "https://user:pw@a.com"},
	} {
		_, err := s.AddModelService(bg, in)
		assert.Error(t, err, name)
	}
	for i, u := range []string{"http://localhost:11434/v1", "http://127.0.0.1:8080", "http://[::1]:9", "https://a.com"} {
		_, err := s.AddModelService(bg, ModelServiceInput{Name: "ok" + itoa(int64(i)), Kind: "openai", BaseURL: u})
		assert.NoError(t, err, u)
	}
}

func TestParseModelRef(t *testing.T) {
	r, err := ai.ParseModelRef("or/anthropic/claude-x")
	require.NoError(t, err)
	assert.Equal(t, ai.ModelRef{Service: "or", Model: "anthropic/claude-x"}, r)
	for _, bad := range []string{"x", "/m", "s/", ""} {
		_, err := ai.ParseModelRef(bad)
		assert.Error(t, err, bad)
	}
}

func TestResolveModelErrors(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	_, err := s.resolveModel(bg, "")
	assert.ErrorIs(t, err, ErrAINotConfigured)

	_, err = s.resolveModel(bg, "nope/m")
	assert.ErrorIs(t, err, ErrModelServiceNotFound)
	assert.Contains(t, err.Error(), "nope")

	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "svc", Kind: "openai", BaseURL: "https://a.com", APIKey: "kk", Models: []string{"m1"}})
	require.NoError(t, err)
	_, err = s.resolveModel(bg, "svc/m2")
	assert.ErrorIs(t, err, ErrModelNotFound)
	assert.Contains(t, err.Error(), "svc/m2")

	r, err := s.resolveModel(bg, "svc/m1")
	require.NoError(t, err)
	assert.Equal(t, ai.ResolvedModel{ServiceID: ms.ID, Version: 1, Kind: "openai", BaseURL: "https://a.com", Model: "m1", APIKey: "kk"}, r)

	st, _ := s.GetSettings(bg)
	st.DefaultModel = "svc/m1"
	_, err = s.SetSettings(bg, st)
	require.NoError(t, err)
	r, err = s.resolveModel(bg, "")
	require.NoError(t, err)
	assert.Equal(t, "m1", r.Model)
}

func TestUpdateFailureKeepsCredentials(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	in := ModelServiceInput{Name: "a", Kind: "openai", BaseURL: "https://a.com", APIKey: "old", Models: []string{"m"}}
	a, err := s.AddModelService(bg, in)
	require.NoError(t, err)
	in.Name = "b"
	_, err = s.AddModelService(bg, in)
	require.NoError(t, err)

	// duplicate name: error, key and version untouched
	dup := ModelServiceInput{Name: "b", Kind: "openai", BaseURL: "https://a.com", APIKey: "new"}
	_, err = s.UpdateModelService(bg, a.ID, dup)
	require.Error(t, err)
	k, _ := keyring.Get(keyringService, s.modelKeyUser(a.ID))
	assert.Equal(t, "old", k)
	got, err := s.modelServiceBy(bg, "id = ?", a.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.Version)
	assert.Equal(t, "a", got.Name)

	// keychain write failure: row rolled back
	setFor(t, &keyringSet, func(svc, user, pw string) error { return errors.New("boom") })
	_, err = s.UpdateModelService(bg, a.ID, ModelServiceInput{Name: "c", Kind: "anthropic", BaseURL: "https://c.com", APIKey: "new", Models: []string{"z"}})
	require.Error(t, err)
	got, err = s.modelServiceBy(bg, "id = ?", a.ID)
	require.NoError(t, err)
	assert.Equal(t, ModelService{ID: a.ID, Name: "a", Kind: "openai", BaseURL: "https://a.com", Models: []string{"m"}, HasKey: true, Version: 1}, got)
	k, _ = keyring.Get(keyringService, s.modelKeyUser(a.ID))
	assert.Equal(t, "old", k)
}

func TestFetchModelsNoRedirectNoLeak(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = true
	}))
	t.Cleanup(other.Close)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/models", http.StatusFound)
	}))
	t.Cleanup(redir.Close)
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "r", Kind: "openai", BaseURL: redir.URL, APIKey: "sekret-key"})
	require.NoError(t, err)
	_, err = s.FetchModels(bg, ms.ID)
	require.Error(t, err)
	assert.False(t, leaked, "redirect was followed")

	// a server echoing the key into an invalid Location header
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://%zz/"+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(bad.Close)
	ms, err = s.AddModelService(bg, ModelServiceInput{Name: "bad", Kind: "openai", BaseURL: bad.URL, APIKey: "sekret-key"})
	require.NoError(t, err)
	_, err = s.FetchModels(bg, ms.ID)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sekret-key")
}

func TestListModelServicesSurvivesKeychainError(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	_, err := s.AddModelService(bg, ModelServiceInput{Name: "a", Kind: "openai", BaseURL: "https://a.com", APIKey: "k"})
	require.NoError(t, err)
	setFor(t, &keyringGet, func(svc, user string) (string, error) { return "partial", errors.New("locked") })
	list, err := s.ListModelServices(bg)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.False(t, list[0].HasKey)
}

func TestUpdateSurvivesCancelDuringKeyWrite(t *testing.T) {
	s := modelSvc(t)
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := s.AddModelService(cctx, ModelServiceInput{Name: "a", Kind: "openai", BaseURL: "https://a.com", APIKey: "old"})
	require.NoError(t, err)
	setFor(t, &keyringSet, func(svc, user, pw string) error {
		cancel()
		return keyring.Set(svc, user, pw)
	})
	got, err := s.UpdateModelService(cctx, a.ID, ModelServiceInput{Name: "a", Kind: "openai", BaseURL: "https://a.com", APIKey: "new"})
	require.NoError(t, err)
	assert.EqualValues(t, 2, got.Version)
	k, err := keyring.Get(keyringService, s.modelKeyUser(a.ID))
	require.NoError(t, err)
	assert.Equal(t, "new", k)
}

func TestModelServiceTestGenerates(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic"} {
		t.Run(kind, func(t *testing.T) {
			s, bg := modelSvc(t), context.Background()
			srv := testllm.Start(t, kind)
			ms, err := s.AddModelService(bg, ModelServiceInput{Name: "x", Kind: kind, BaseURL: srv.URL, APIKey: "sekret-key-7", Models: []string{"m1"}})
			require.NoError(t, err)
			require.NoError(t, s.TestModelService(bg, ms.ID))
			reqs := srv.Requests()
			require.Len(t, reqs, 1, "a real generation")

			srv.Script(testllm.Turn{Status: 401})
			err = s.TestModelService(bg, ms.ID)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "sekret-key-7")
		})
	}
}
