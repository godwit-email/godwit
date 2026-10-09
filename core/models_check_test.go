package core

import (
	"context"
	"net/http"
	"testing"

	"github.com/godwit-email/godwit/core/internal/testllm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zalando/go-keyring"
)

func keyed(hs []http.Header) (n int) {
	for _, h := range hs {
		if h.Get("Authorization") != "" || h.Get("x-api-key") != "" {
			n++
		}
	}
	return n
}

func TestCheckModelService(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	srv := testllm.Start(t, "openai")
	srv.SetModels("b", "a")
	in := ModelServiceInput{Name: "x", Kind: "openai", BaseURL: srv.URL + "/chat/completions", KeyAction: KeyReplace, APIKey: "k-123456"}

	c, err := s.CheckModelService(bg, 0, in)
	require.NoError(t, err)
	assert.Equal(t, ServiceCheck{BaseURL: srv.URL, Endpoint: srv.URL + "/chat/completions", Models: []string{"a", "b"}, ListSupported: true}, c)
	assert.Equal(t, "Bearer k-123456", srv.Headers()[0].Get("Authorization"))

	srv.SetModels()
	c, err = s.CheckModelService(bg, 0, in)
	require.NoError(t, err)
	assert.True(t, c.ListSupported)
	assert.Empty(t, c.Models)

	srv.SetListStatus(http.StatusNotFound)
	c, err = s.CheckModelService(bg, 0, in)
	require.NoError(t, err)
	assert.False(t, c.ListSupported)
	assert.Equal(t, srv.URL, c.BaseURL)

	srv.SetListStatus(http.StatusUnauthorized)
	_, err = s.CheckModelService(bg, 0, in)
	assert.ErrorIs(t, err, ErrModelAuth)

	srv.SetListStatus(http.StatusInternalServerError) // its body echoes the key
	_, err = s.CheckModelService(bg, 0, in)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "k-123456")
	assert.NotContains(t, err.Error(), "UPSTREAM-BODY")

	in.KeyAction = KeyKeep
	_, err = s.CheckModelService(bg, 0, in)
	assert.Error(t, err, "keep is invalid for a new service")

	list, err := s.ListModelServices(bg)
	require.NoError(t, err)
	assert.Empty(t, list, "check saves nothing")
}

func TestKeyNotSentToNewAddress(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	a, b := testllm.Start(t, "openai"), testllm.Start(t, "openai")
	a.SetModels("m")
	b.SetModels("m")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "x", Kind: "openai", BaseURL: a.URL, KeyAction: KeyReplace, APIKey: "key-A-123456", Models: []string{"m"}})
	require.NoError(t, err)

	toB := ModelServiceInput{Name: "x", Kind: "openai", BaseURL: b.URL, KeyAction: KeyKeep, Models: []string{"m"}}
	_, err = s.CheckModelService(bg, ms.ID, toB)
	assert.ErrorIs(t, err, ErrKeyNeedsConfirm)
	_, err = s.UpdateModelService(bg, ms.ID, toB)
	assert.ErrorIs(t, err, ErrKeyNeedsConfirm)
	assert.Empty(t, b.Headers(), "B got no request at all")
	got, err := s.modelServiceBy(bg, "id = ?", ms.ID)
	require.NoError(t, err)
	assert.Equal(t, a.URL, got.BaseURL, "refused update changed nothing")

	// same address, other protocol: the key is not reused either
	_, err = s.CheckModelService(bg, ms.ID, ModelServiceInput{Name: "x", Kind: "openai-responses", BaseURL: a.URL, KeyAction: KeyKeep, Models: []string{"m"}})
	assert.ErrorIs(t, err, ErrKeyNeedsConfirm)

	toB.KeyAction, toB.APIKey = KeyReplace, "key-B-654321"
	_, err = s.CheckModelService(bg, ms.ID, toB)
	require.NoError(t, err)
	require.Len(t, b.Headers(), 1)
	assert.Equal(t, "Bearer key-B-654321", b.Headers()[0].Get("Authorization"))

	toB.KeyAction, toB.APIKey = KeyClear, ""
	_, err = s.CheckModelService(bg, ms.ID, toB)
	require.NoError(t, err)
	require.Len(t, b.Headers(), 2)
	assert.Empty(t, b.Headers()[1].Get("Authorization"))

	saved, err := s.UpdateModelService(bg, ms.ID, toB)
	require.NoError(t, err)
	assert.False(t, saved.HasKey)
	_, err = keyring.Get(keyringService, s.modelKeyUser(ms.ID))
	assert.ErrorIs(t, err, keyring.ErrNotFound)
	_, err = s.FetchModels(bg, ms.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, keyed(b.Headers()), "only the explicit replace check carried a key to B")
	for _, h := range b.Headers() {
		assert.NotContains(t, h.Get("Authorization"), "key-A")
	}
}

func TestKeepKeySameAddress(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	a := testllm.Start(t, "openai")
	a.SetModels("m")
	a.RequireKey("key-A-123456")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "x", Kind: "openai", BaseURL: a.URL, KeyAction: KeyReplace, APIKey: "key-A-123456", Models: []string{"m"}})
	require.NoError(t, err)

	// the same address pasted with its endpoint path normalizes to the stored one
	in := ModelServiceInput{Name: "y", Kind: "openai", BaseURL: a.URL + "/chat/completions/", KeyAction: KeyKeep, Models: []string{"m"}}
	c, err := s.CheckModelService(bg, ms.ID, in)
	require.NoError(t, err)
	assert.Equal(t, []string{"m"}, c.Models)
	saved, err := s.UpdateModelService(bg, ms.ID, in)
	require.NoError(t, err)
	assert.True(t, saved.HasKey)
	assert.Equal(t, a.URL, saved.BaseURL)
	k, _ := keyring.Get(keyringService, s.modelKeyUser(ms.ID))
	assert.Equal(t, "key-A-123456", k)
}

func TestAddServiceNormalizes(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "x", Kind: "anthropic", BaseURL: " https://api.example.com/v1/messages ", Models: []string{"m"}})
	require.NoError(t, err)
	assert.Equal(t, "https://api.example.com", ms.BaseURL)
	ms, err = s.UpdateModelService(bg, ms.ID, ModelServiceInput{Name: "x", Kind: "openai-responses", BaseURL: "https://api.example.com/v1/responses", KeyAction: KeyClear, Models: []string{"m"}})
	require.NoError(t, err)
	assert.Equal(t, "openai-responses", ms.Kind)
	assert.Equal(t, "https://api.example.com/v1", ms.BaseURL)
}

func TestAddServiceNeedsModels(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	_, err := s.AddModelService(bg, ModelServiceInput{Name: "x", Kind: "openai", BaseURL: "https://a.com"})
	assert.Error(t, err)
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "x", Kind: "openai", BaseURL: "https://a.com", Models: []string{"typed-by-hand"}})
	require.NoError(t, err)
	_, err = s.UpdateModelService(bg, ms.ID, ModelServiceInput{Name: "x", Kind: "openai", BaseURL: "https://a.com", Models: []string{}})
	assert.Error(t, err)
}

func TestAddServiceKeyless(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	srv := testllm.Start(t, "openai")
	srv.SetModels("llama")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "local", Kind: "openai", BaseURL: srv.URL, Models: []string{"llama"}})
	require.NoError(t, err)
	assert.False(t, ms.HasKey)
	got, err := s.FetchModels(bg, ms.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"llama"}, got)
	assert.Zero(t, keyed(srv.Headers()))
}

// Ruling on Task 3: keep with no stored key is allowed across an address
// change; there is no key to leak.
func TestKeylessKeepNewAddress(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	a, b := testllm.Start(t, "openai"), testllm.Start(t, "openai")
	b.SetModels("m")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "x", Kind: "openai", BaseURL: a.URL, KeyAction: KeyClear, Models: []string{"m"}})
	require.NoError(t, err)
	toB := ModelServiceInput{Name: "x", Kind: "openai", BaseURL: b.URL, KeyAction: KeyKeep, Models: []string{"m"}}
	c, err := s.CheckModelService(bg, ms.ID, toB)
	require.NoError(t, err)
	assert.Equal(t, []string{"m"}, c.Models)
	saved, err := s.UpdateModelService(bg, ms.ID, toB)
	require.NoError(t, err)
	assert.Equal(t, b.URL, saved.BaseURL)
	assert.False(t, saved.HasKey)
	require.Len(t, b.Headers(), 1)
	assert.Zero(t, keyed(b.Headers()), "B got no auth header")
}
