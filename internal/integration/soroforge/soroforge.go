// Package soroforge reads the contracts a SoroForge registry tracks.
//
// SoroForge records every deploy and upgrade: the contract ID, the network and
// the Wasm hash that was live when the change was confirmed. SoroProbe can read
// that list and check each contract, which is more useful than a hand-maintained
// check file that drifts out of date — the registry is updated by the deploy
// that changed the contract.
//
// This package speaks only to the read-only listing endpoint
// (GET /v1/contracts). SoroProbe never writes to SoroForge, and the token it is
// given only needs the read scope.
package soroforge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultPageSize is the page size requested when Options.PageSize is zero.
// SoroForge's own default is 100 and its maximum is 500; requesting a larger
// page keeps the number of round trips down without exceeding the limit.
const DefaultPageSize = 500

// maxResponseBytes caps one page. A page of 500 contract records is well under
// a megabyte; anything far beyond that is not a response worth parsing.
const maxResponseBytes = 4 << 20

// maxPages bounds the pagination loop independently of the server's answers.
// A server that keeps claiming another page cannot make SoroProbe fetch
// forever; 1000 pages of 500 records is far beyond any real deployment list.
const maxPages = 1000

// Errors callers can test for. Each one is wrapped with the server's own error
// text, so a message says what happened and quotes what SoroForge answered.
var (
	// ErrUnauthorized means the API rejected the credential (HTTP 401).
	ErrUnauthorized = errors.New("soroforge: the API rejected the token")
	// ErrForbidden means the token is valid but lacks the read scope (HTTP 403).
	ErrForbidden = errors.New("soroforge: the token is not allowed to read contracts")
	// ErrInvalidRequest means the API rejected the request itself (HTTP 400),
	// usually an unknown network name.
	ErrInvalidRequest = errors.New("soroforge: the API rejected the request")
	// ErrRateLimited means the API asked the caller to slow down (HTTP 429).
	ErrRateLimited = errors.New("soroforge: the API rate-limited the request")
	// ErrPagination means the server's paging metadata could not advance, so
	// continuing would either loop forever or silently drop contracts.
	ErrPagination = errors.New("soroforge: pagination did not advance")
)

// Contract is one tracked contract, as the listing endpoint reports it.
type Contract struct {
	// Alias is SoroForge's name for the contract in its config file.
	Alias string `json:"alias"`
	// Network is the SoroForge network label the contract was tracked under.
	Network string `json:"network"`
	// ContractID is the strkey contract address ("C...").
	ContractID string `json:"contract_id"`
	// WASMHash is the hex code hash SoroForge recorded when the deploy or
	// upgrade was confirmed. It can be empty for a Stellar Asset Contract,
	// which has no uploaded bytecode.
	WASMHash string `json:"current_wasm_hash"`
}

// Source supplies the contracts a registry tracks. It is an interface so the
// checker can be driven by a literal list in a test.
type Source interface {
	// Contracts returns every contract tracked on network, following the
	// registry's pagination. A network the registry does not know is an error.
	Contracts(ctx context.Context, network string) ([]Contract, error)
}

// Options configures a Client.
type Options struct {
	// URL is the SoroForge API base URL, such as "http://localhost:8080".
	// Required.
	URL string
	// Token is a SoroForge API token. Required; a read-scoped token is enough,
	// because SoroProbe only reads the contract list.
	Token string
	// HTTP overrides the HTTP client. Nil means one with Timeout as its
	// overall deadline.
	HTTP *http.Client
	// Timeout bounds each request when HTTP is nil. Zero means 30s.
	Timeout time.Duration
	// PageSize is the page size to request. Zero means DefaultPageSize.
	PageSize int
}

// Client is the HTTP implementation of Source.
type Client struct {
	baseURL  string
	token    string
	http     *http.Client
	pageSize int
}

var _ Source = (*Client)(nil)

// New returns a Client for the SoroForge API at opts.URL.
func New(opts Options) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(opts.URL, "/"))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("soroforge url %q must be an absolute http:// or https:// URL", opts.URL)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("soroforge url %q must not carry a query or fragment", opts.URL)
	}
	if opts.Token == "" {
		return nil, errors.New("a SoroForge read-only token is required (set SOROFORGE_API_TOKEN or --soroforge-token)")
	}
	client := opts.HTTP
	if client == nil {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	return &Client{
		baseURL:  parsed.String(),
		token:    opts.Token,
		http:     client,
		pageSize: pageSize,
	}, nil
}

// page is the shape of GET /v1/contracts.
type page struct {
	Contracts []Contract `json:"contracts"`
	Limit     int        `json:"limit"`
	Offset    int        `json:"offset"`
	HasMore   bool       `json:"has_more"`
	// NextOffset is authoritative when present. Without it the next offset is
	// this page's offset plus the number of contracts it returned.
	NextOffset *int `json:"next_offset"`
}

// Contracts implements Source, following pagination until the registry reports
// no more records.
func (c *Client) Contracts(ctx context.Context, network string) ([]Contract, error) {
	var all []Contract
	offset := 0
	seen := make(map[string]struct{})
	for pages := 0; ; pages++ {
		if pages >= maxPages {
			return nil, fmt.Errorf("%w: more than %d pages at limit %d", ErrPagination, maxPages, c.pageSize)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		body, err := c.fetch(ctx, network, offset)
		if err != nil {
			return nil, err
		}

		for _, contract := range body.Contracts {
			if contract.ContractID == "" {
				return nil, fmt.Errorf("soroforge: contract %q on %s has no contract_id", contract.Alias, network)
			}
			// A registry can overlap pages if the list changes between
			// requests. The key is the tracked entry (alias and contract), not
			// the contract alone, so two aliases for one address are still both
			// checked while a repeated page entry is reported once.
			key := contract.Alias + "\x00" + contract.ContractID
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			all = append(all, contract)
		}

		if !body.HasMore {
			if all == nil {
				all = []Contract{}
			}
			return all, nil
		}

		next := offset + len(body.Contracts)
		if body.NextOffset != nil {
			next = *body.NextOffset
		}
		if next <= offset {
			return nil, fmt.Errorf("%w: page at offset %d reported has_more but advanced to offset %d", ErrPagination, offset, next)
		}
		offset = next
	}
}

// fetch reads one page and maps its status onto a sentinel error.
func (c *Client) fetch(ctx context.Context, network string, offset int) (page, error) {
	endpoint := c.baseURL + "/v1/contracts?limit=" + strconv.Itoa(c.pageSize) +
		"&offset=" + strconv.Itoa(offset)
	if network != "" {
		endpoint += "&network=" + url.QueryEscape(network)
	}

	var body page
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return body, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return body, fmt.Errorf("soroforge: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // the body has been read; a close error is not actionable here.

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return body, fmt.Errorf("soroforge: reading response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return body, fmt.Errorf("%w: %s (check that SOROFORGE_API_TOKEN or --soroforge-token holds a current token)",
			ErrUnauthorized, apiError(raw, resp.Status))
	case http.StatusForbidden:
		return body, fmt.Errorf("%w: %s (the token needs the read scope)", ErrForbidden, apiError(raw, resp.Status))
	case http.StatusBadRequest:
		return body, fmt.Errorf("%w: %s", ErrInvalidRequest, apiError(raw, resp.Status))
	case http.StatusTooManyRequests:
		message := apiError(raw, resp.Status)
		if retry := resp.Header.Get("Retry-After"); retry != "" {
			message += " (retry after " + retry + ")"
		}
		return body, fmt.Errorf("%w: %s", ErrRateLimited, message)
	default:
		return body, fmt.Errorf("soroforge: %s: %s", resp.Status, apiError(raw, resp.Status))
	}

	if err := json.Unmarshal(raw, &body); err != nil {
		return body, fmt.Errorf("soroforge: decoding response: %w", err)
	}
	if body.Contracts == nil {
		body.Contracts = []Contract{}
	}
	return body, nil
}

// apiError extracts SoroForge's {"error": "..."} body, falling back to the HTTP
// status when the body is not in that shape.
func apiError(body []byte, status string) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return status
}
