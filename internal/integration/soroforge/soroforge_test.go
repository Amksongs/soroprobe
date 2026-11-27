package soroforge_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/integration/soroforge"
)

// contract is the wire shape, written out here rather than reused from the
// package, so a change to the field tags cannot make the test agree with the
// implementation about a payload SoroForge would not send.
func contract(alias, network, id, hash string) map[string]any {
	return map[string]any{
		"alias":             alias,
		"network":           network,
		"contract_id":       id,
		"current_wasm_hash": hash,
	}
}

func page(t *testing.T, w http.ResponseWriter, body map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	// The handler runs on the server's goroutine, so a failure is reported
	// with t.Errorf rather than require, which must not call FailNow there.
	assert.NoError(t, json.NewEncoder(w).Encode(body))
}

func TestContractsSendsTheTokenAndTheNetworkFilter(t *testing.T) {
	t.Parallel()

	var gotAuth, gotNetwork, gotLimit, gotOffset string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotNetwork = r.URL.Query().Get("network")
		gotLimit = r.URL.Query().Get("limit")
		gotOffset = r.URL.Query().Get("offset")
		assert.Equal(t, "/v1/contracts", r.URL.Path)
		page(t, w, map[string]any{
			"contracts": []any{contract("token", "testnet", "CTOKEN", "aabb")},
			"limit":     100,
			"offset":    0,
			"has_more":  false,
		})
	}))
	t.Cleanup(server.Close)

	client, err := soroforge.New(soroforge.Options{URL: server.URL, Token: "read-only-token", PageSize: 100})
	require.NoError(t, err)

	contracts, err := client.Contracts(context.Background(), "testnet")
	require.NoError(t, err)

	assert.Equal(t, "Bearer read-only-token", gotAuth)
	assert.Equal(t, "testnet", gotNetwork)
	assert.Equal(t, "100", gotLimit)
	assert.Equal(t, "0", gotOffset)
	require.Len(t, contracts, 1)
	assert.Equal(t, soroforge.Contract{
		Alias:      "token",
		Network:    "testnet",
		ContractID: "CTOKEN",
		WASMHash:   "aabb",
	}, contracts[0])
}

func TestContractsFollowsPagination(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var offsets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		mu.Lock()
		offsets = append(offsets, offset)
		mu.Unlock()

		switch offset {
		case "0":
			page(t, w, map[string]any{
				"contracts": []any{
					contract("a", "testnet", "CA", "aa"),
					contract("b", "testnet", "CB", "bb"),
				},
				"limit": 2, "offset": 0, "has_more": true, "next_offset": 2,
			})
		case "2":
			page(t, w, map[string]any{
				"contracts": []any{contract("c", "testnet", "CC", "cc")},
				"limit":     2, "offset": 2, "has_more": false,
			})
		default:
			t.Errorf("unexpected offset %q", offset)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	client, err := soroforge.New(soroforge.Options{URL: server.URL, Token: "token"})
	require.NoError(t, err)

	contracts, err := client.Contracts(context.Background(), "testnet")
	require.NoError(t, err)

	assert.Equal(t, []string{"0", "2"}, offsets)
	require.Len(t, contracts, 3)
	assert.Equal(t, []string{"CA", "CB", "CC"}, []string{contracts[0].ContractID, contracts[1].ContractID, contracts[2].ContractID})
}

func TestContractsAdvancesByPageLengthWhenNextOffsetIsAbsent(t *testing.T) {
	t.Parallel()

	var offsets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		if offset == "0" {
			page(t, w, map[string]any{
				"contracts": []any{
					contract("a", "testnet", "CA", "aa"),
					contract("b", "testnet", "CB", "bb"),
				},
				"limit": 2, "offset": 0, "has_more": true,
			})
			return
		}
		page(t, w, map[string]any{
			"contracts": []any{contract("c", "testnet", "CC", "cc")},
			"limit":     2, "offset": 2, "has_more": false,
		})
	}))
	t.Cleanup(server.Close)

	client, err := soroforge.New(soroforge.Options{URL: server.URL, Token: "token"})
	require.NoError(t, err)

	contracts, err := client.Contracts(context.Background(), "testnet")
	require.NoError(t, err)

	assert.Equal(t, []string{"0", "2"}, offsets)
	assert.Len(t, contracts, 3)
}

func TestContractsRejectsPaginationThatDoesNotAdvance(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page(t, w, map[string]any{
			"contracts": []any{contract("a", "testnet", "CA", "aa")},
			"limit":     100, "offset": 0, "has_more": true, "next_offset": 0,
		})
	}))
	t.Cleanup(server.Close)

	client, err := soroforge.New(soroforge.Options{URL: server.URL, Token: "token"})
	require.NoError(t, err)

	_, err = client.Contracts(context.Background(), "testnet")
	require.Error(t, err)
	assert.ErrorIs(t, err, soroforge.ErrPagination)
	assert.Contains(t, err.Error(), "has_more")
}

func TestContractsDeduplicatesAnOverlappingPage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "0" {
			page(t, w, map[string]any{
				"contracts": []any{contract("a", "testnet", "CA", "aa")},
				"limit":     1, "offset": 0, "has_more": true,
			})
			return
		}
		// The list shifted between requests and repeated the same contract.
		page(t, w, map[string]any{
			"contracts": []any{contract("a", "testnet", "CA", "aa")},
			"limit":     1, "offset": 1, "has_more": false,
		})
	}))
	t.Cleanup(server.Close)

	client, err := soroforge.New(soroforge.Options{URL: server.URL, Token: "token", PageSize: 1})
	require.NoError(t, err)

	contracts, err := client.Contracts(context.Background(), "testnet")
	require.NoError(t, err)
	assert.Len(t, contracts, 1)
}

func TestContractsReportsAuthenticationProblemsClearly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  int
		header  map[string]string
		body    map[string]any
		want    error
		wantMsg []string
	}{
		{
			name:    "missing or unknown token",
			status:  http.StatusUnauthorized,
			body:    map[string]any{"error": "invalid bearer token"},
			want:    soroforge.ErrUnauthorized,
			wantMsg: []string{"invalid bearer token", "SOROFORGE_API_TOKEN", "--soroforge-token"},
		},
		{
			name:    "token without the read scope",
			status:  http.StatusForbidden,
			body:    map[string]any{"error": "token lacks read scope"},
			want:    soroforge.ErrForbidden,
			wantMsg: []string{"token lacks read scope", "read scope"},
		},
		{
			name:    "unknown network",
			status:  http.StatusBadRequest,
			body:    map[string]any{"error": `unknown network "staging"`},
			want:    soroforge.ErrInvalidRequest,
			wantMsg: []string{`unknown network "staging"`},
		},
		{
			name:    "rate limited",
			status:  http.StatusTooManyRequests,
			header:  map[string]string{"Retry-After": "10"},
			body:    map[string]any{"error": "rate limit exceeded"},
			want:    soroforge.ErrRateLimited,
			wantMsg: []string{"rate limit exceeded", "retry after 10"},
		},
		{
			name:    "server error",
			status:  http.StatusBadGateway,
			body:    map[string]any{"error": "database is down"},
			wantMsg: []string{"502 Bad Gateway", "database is down"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for key, value := range tc.header {
					w.Header().Set(key, value)
				}
				w.WriteHeader(tc.status)
				page(t, w, tc.body)
			}))
			t.Cleanup(server.Close)

			client, err := soroforge.New(soroforge.Options{URL: server.URL, Token: "token"})
			require.NoError(t, err)

			_, err = client.Contracts(context.Background(), "testnet")
			require.Error(t, err)
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			}
			for _, want := range tc.wantMsg {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestContractsRejectsAMalformedResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"contracts": "not a list"}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	client, err := soroforge.New(soroforge.Options{URL: server.URL, Token: "token"})
	require.NoError(t, err)

	_, err = client.Contracts(context.Background(), "testnet")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding response")
}

func TestContractsRejectsAContractWithoutAnID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page(t, w, map[string]any{
			"contracts": []any{contract("broken", "testnet", "", "aa")},
			"limit":     100, "offset": 0, "has_more": false,
		})
	}))
	t.Cleanup(server.Close)

	client, err := soroforge.New(soroforge.Options{URL: server.URL, Token: "token"})
	require.NoError(t, err)

	_, err = client.Contracts(context.Background(), "testnet")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"broken"`)
	assert.Contains(t, err.Error(), "contract_id")
}

func TestContractsWithNoRecordsReturnsAnEmptySlice(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page(t, w, map[string]any{
			"contracts": []any{},
			"limit":     100, "offset": 0, "has_more": false,
		})
	}))
	t.Cleanup(server.Close)

	client, err := soroforge.New(soroforge.Options{URL: server.URL, Token: "token"})
	require.NoError(t, err)

	contracts, err := client.Contracts(context.Background(), "testnet")
	require.NoError(t, err)
	assert.NotNil(t, contracts)
	assert.Empty(t, contracts)
}

func TestNewRejectsUnusableOptions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts soroforge.Options
		want string
	}{
		{name: "no URL", opts: soroforge.Options{Token: "token"}, want: "absolute http"},
		{name: "relative URL", opts: soroforge.Options{URL: "localhost:8080", Token: "token"}, want: "absolute http"},
		{name: "no token", opts: soroforge.Options{URL: "http://localhost:8080"}, want: "read-only token"},
		{name: "query in URL", opts: soroforge.Options{URL: "http://localhost:8080?x=1", Token: "token"}, want: "query"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := soroforge.New(tc.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestContractsStopsWhenTheContextIsCanceled(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page(t, w, map[string]any{
			"contracts": []any{contract("a", "testnet", "CA", "aa")},
			"limit":     1, "offset": 0, "has_more": true,
		})
	}))
	t.Cleanup(server.Close)

	client, err := soroforge.New(soroforge.Options{URL: server.URL, Token: "token", PageSize: 1})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Contracts(ctx, "testnet")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "want context.Canceled, got %v", err)
}

// urlValues guards against a query helper change silently dropping the
// network filter: the test server reads the raw query, so this asserts the
// exact values a SoroForge server would receive.
func TestContractsQueryIsWellFormed(t *testing.T) {
	t.Parallel()

	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		page(t, w, map[string]any{"contracts": []any{}, "limit": 500, "offset": 0, "has_more": false})
	}))
	t.Cleanup(server.Close)

	client, err := soroforge.New(soroforge.Options{URL: server.URL, Token: "token"})
	require.NoError(t, err)

	_, err = client.Contracts(context.Background(), "test net")
	require.NoError(t, err)

	assert.Equal(t, "test net", got.Get("network"))
	assert.Equal(t, strconv.Itoa(soroforge.DefaultPageSize), got.Get("limit"))
}
