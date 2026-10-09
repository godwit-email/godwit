package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListModelsErrorKinds(t *testing.T) {
	for _, c := range []struct {
		status int
		want   error
		text   string
	}{
		{401, ErrModelAuth, ""}, {403, ErrModelAuth, ""},
		{404, ErrNoModelList, ""}, {405, ErrNoModelList, ""}, {501, ErrNoModelList, ""},
		{500, nil, "list models: HTTP 500"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			w.Write([]byte("body-secret"))
		}))
		_, err := ListModels(context.Background(), nil, KindResponses, srv.URL, "sk-secret")
		srv.Close()
		if err == nil || (c.want != nil && !errors.Is(err, c.want)) || (c.text != "" && err.Error() != c.text) {
			t.Errorf("%d: %v", c.status, err)
			continue
		}
		if strings.Contains(err.Error(), "sk-secret") || strings.Contains(err.Error(), "body-secret") {
			t.Errorf("%d leaks: %v", c.status, err)
		}
	}
}

func TestListModelsReturnsAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"text-embedding-3-small"},{"id":"gpt-5"}]}`))
	}))
	defer srv.Close()
	ids, err := ListModels(context.Background(), nil, KindOpenAI, srv.URL, "")
	if err != nil || len(ids) != 2 || ids[0] != "gpt-5" || ids[1] != "text-embedding-3-small" {
		t.Errorf("%v %v", ids, err)
	}
}
