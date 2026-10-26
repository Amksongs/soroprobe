package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/api"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// fetchSpec reads the OpenAPI document the server actually serves.
func fetchSpec(t *testing.T) map[string]any {
	t.Helper()
	fake := stellartest.NewFake(t)
	handler := newServer(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc), "openapi.json must be valid JSON")
	return doc
}

// TestOpenAPICoversEveryRoute keeps the document honest: every route the API
// serves is documented with its method, and every documented operation exists.
func TestOpenAPICoversEveryRoute(t *testing.T) {
	t.Parallel()

	paths := fetchSpec(t)["paths"].(map[string]any)

	documented := map[string]bool{}
	for path, item := range paths {
		for method := range item.(map[string]any) {
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}

	p, err := probe.New(probe.Options{
		Client:        stellartest.NewFake(t),
		SourceAccount: stellartest.SourceAccount,
	})
	require.NoError(t, err)

	server := api.New(api.Options{Prober: p})
	served := map[string]bool{}
	err = chi.Walk(server.Handler().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		served[method+" "+route] = true
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, sortedKeys(served), sortedKeys(documented),
		"openapi.json and the router disagree; document new routes in internal/api/openapi.json")
}

// TestOpenAPIRefsResolve catches a typo in a $ref.
func TestOpenAPIRefsResolve(t *testing.T) {
	t.Parallel()

	doc := fetchSpec(t)
	components := doc["components"].(map[string]any)

	var refs []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				if k == "$ref" {
					refs = append(refs, child.(string))
				}
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(doc)
	require.NotEmpty(t, refs)

	for _, ref := range refs {
		parts := strings.Split(strings.TrimPrefix(ref, "#/components/"), "/")
		require.Len(t, parts, 2, "unexpected $ref shape %q", ref)
		section, ok := components[parts[0]].(map[string]any)
		require.True(t, ok, "%s: no components.%s", ref, parts[0])
		_, ok = section[parts[1]]
		assert.True(t, ok, "%s does not resolve", ref)
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
