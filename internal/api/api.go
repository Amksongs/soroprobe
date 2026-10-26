// Package api serves an HTTP mirror of the SoroProbe CLI, intended for CI
// systems and other services.
//
// Like the CLI, the API is strictly read-only: it simulates and inspects, and
// offers no route that submits a transaction.
package api

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/probe"
)

// Server wraps the HTTP API.
type Server struct {
	prober            *probe.Prober
	addr              string
	log               *slog.Logger
	router            chi.Router
	ratePerSecond     float64
	rateBurst         float64
	trustForwardedFor bool
	tlsCert           string
	tlsKey            string
	limitMu           sync.Mutex
	clients           map[string]*clientBucket

	// networkPassphrase is the network /readyz expects the RPC endpoint to
	// serve.
	networkPassphrase string
	// readinessTTL bounds how long a /readyz result is reused before the
	// RPC endpoint is probed again.
	readinessTTL time.Duration
	// ready* cache the last readiness result, so a chatty orchestrator does
	// not multiply RPC traffic. A mutex guards the pair; it is never held
	// across an RPC call.
	readyMu  sync.Mutex
	readyAt  time.Time
	readyErr error
	readySet bool
}

// Options configures a Server.
type Options struct {
	// Prober runs the probes. Required.
	Prober *probe.Prober
	// Addr is the listen address. Defaults to config.DefaultHTTPAddr, which is
	// loopback: see the comment on that constant.
	Addr string
	// Logger receives request logs. Defaults to a discarding logger.
	Logger *slog.Logger
	// CORSOrigins is the exact-origin allow-list. Empty disables CORS.
	CORSOrigins []string
	// APIToken, when non-empty, requires "Authorization: Bearer <token>" on
	// every /v1 route. /healthz stays open, so a liveness probe needs no
	// credential. Empty keeps the API unauthenticated, exactly as before.
	// The token must meet config.MinAPITokenLength; Config.Validate enforces
	// that at startup.
	APIToken string
	// Timeout bounds a single request. Defaults to 60s.
	Timeout time.Duration
	// NetworkPassphrase is the network /readyz expects the RPC endpoint to
	// serve; a healthy endpoint on another network is reported unready.
	// Defaults to the testnet passphrase, matching the CLI's defaults.
	NetworkPassphrase string
	// ReadinessCacheTTL bounds how long a /readyz result is reused before
	// the RPC endpoint is probed again. Defaults to 5s.
	ReadinessCacheTTL  time.Duration
	RateLimitPerMinute uint32
	RateLimitBurst     uint32
	TrustForwardedFor  bool
	TLSCert            string
	TLSKey             string
}

// New builds a Server with its routes mounted.
func New(opts Options) *Server {
	if opts.Addr == "" {
		opts.Addr = config.DefaultHTTPAddr
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.RateLimitPerMinute == 0 {
		opts.RateLimitPerMinute = config.DefaultRateLimitPerMinute
	}
	if opts.RateLimitBurst == 0 {
		opts.RateLimitBurst = config.DefaultRateLimitBurst
	}
	if opts.NetworkPassphrase == "" {
		opts.NetworkPassphrase = config.DefaultNetworkPassphrase
	}
	if opts.ReadinessCacheTTL <= 0 {
		opts.ReadinessCacheTTL = 5 * time.Second
	}

	s := &Server{
		prober:            opts.Prober,
		addr:              opts.Addr,
		log:               opts.Logger,
		networkPassphrase: opts.NetworkPassphrase,
		readinessTTL:      opts.ReadinessCacheTTL,
		ratePerSecond:     float64(opts.RateLimitPerMinute) / 60,
		rateBurst:         float64(opts.RateLimitBurst),
		trustForwardedFor: opts.TrustForwardedFor,
		tlsCert:           opts.TLSCert,
		tlsKey:            opts.TLSKey,
		clients:           make(map[string]*clientBucket),
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(opts.Timeout))
	r.Use(nosniff)
	r.Use(s.logRequests)
	r.Use(s.rateLimit)
	if len(opts.CORSOrigins) > 0 {
		r.Use(cors(opts.CORSOrigins))
	}

	// /healthz stays outside the authenticated /v1 subtree on purpose: a
	// liveness probe should not need a credential to prove the process is up.
	r.Get("/healthz", s.handleLiveness)
	r.Get("/openapi.json", s.handleOpenAPI)
	r.Get("/readyz", s.handleReadiness)
	r.Route("/v1", func(r chi.Router) {
		if opts.APIToken != "" {
			r.Use(requireToken(opts.APIToken))
		}
		// Answers reflect live on-chain state, so they must not be replayed
		// from a shared or intermediary cache.
		r.Use(noStore)
		r.Post("/simulate", s.handleSimulate)
		r.Get("/inspect/{contract}", s.handleInspect)
		r.Get("/check/{contract}", s.handleCheck)
		r.Post("/checks", s.handleChecks)
		r.Get("/events/{contract}", s.handleEvents)
	})

	s.router = r
	return s
}

func cors(origins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		allowed[origin] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			_, ok := allowed[origin]
			w.Header().Add("Vary", "Origin")
			if ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				if !ok {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Handler exposes the router, which makes the API testable with httptest
// without binding a port.
func (s *Server) Handler() http.Handler { return s.router }

// ListenAndServe runs the server until ctx is canceled, then shuts it down
// gracefully.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if (s.tlsCert == "") != (s.tlsKey == "") {
		return errors.New("TLS certificate and key must be configured together")
	}
	if s.tlsCert != "" {
		if _, err := tls.LoadX509KeyPair(s.tlsCert, s.tlsKey); err != nil {
			return fmt.Errorf("load TLS certificate and key: %w", err)
		}
	}

	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, listener)
}

// Serve runs the server on listener until ctx is canceled, then shuts it down gracefully.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	srv := &http.Server{
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if s.tlsCert != "" {
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	errCh := make(chan error, 1)
	go func() {
		protocol := "http"
		if s.tlsCert != "" {
			protocol = "https"
		}
		s.log.Info(protocol+" api listening", "addr", listener.Addr())
		var err error
		if s.tlsCert == "" {
			err = srv.Serve(listener)
		} else {
			err = srv.ServeTLS(listener, s.tlsCert, s.tlsKey)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if ctx.Err() != nil {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.log.Info("shutting down http api")
		err := srv.Shutdown(shutdownCtx)
		_ = listener.Close()
		return err
	}
}

const maxRateLimitClients = 10_000

type clientBucket struct {
	tokens    float64
	capacity  float64
	updatedAt time.Time
	seenAt    time.Time
}

func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}

		weight := 1.0
		if r.Method == http.MethodPost && r.URL.Path == "/v1/checks" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
			var batch struct {
				Checks []json.RawMessage `json:"checks"`
			}
			if len(body) <= maxBodyBytes && json.Unmarshal(body, &batch) == nil &&
				len(batch.Checks) > 1 && len(batch.Checks) <= maxAPIBatchChecks {
				weight = float64(len(batch.Checks))
			}
		}

		client := s.clientAddress(r)
		retryAfter := s.takeTokens(client, weight, time.Now())
		if retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			writeError(w, http.StatusTooManyRequests, errors.New("rate limit exceeded"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) clientAddress(r *http.Request) string {
	if s.trustForwardedFor {
		if forwarded := strings.TrimSpace(strings.SplitN(r.Header.Get("X-Forwarded-For"), ",", 2)[0]); net.ParseIP(forwarded) != nil {
			return forwarded
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// takeTokens returns the whole-second delay needed before this request can pass.
func (s *Server) takeTokens(client string, cost float64, now time.Time) int {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()

	bucket, ok := s.clients[client]
	if !ok {
		if len(s.clients) >= maxRateLimitClients {
			s.evictOldestClient()
		}
		bucket = &clientBucket{tokens: s.rateBurst, capacity: s.rateBurst, updatedAt: now}
		s.clients[client] = bucket
	}
	bucket.tokens = math.Min(bucket.capacity, bucket.tokens+now.Sub(bucket.updatedAt).Seconds()*s.ratePerSecond)
	bucket.updatedAt = now
	bucket.seenAt = now
	if cost > bucket.capacity {
		bucket.capacity = cost
	}
	if bucket.tokens < cost {
		return int(math.Ceil((cost - bucket.tokens) / s.ratePerSecond))
	}
	bucket.tokens -= cost
	return 0
}

func (s *Server) evictOldestClient() {
	var oldestKey string
	var oldest time.Time
	for key, bucket := range s.clients {
		if oldestKey == "" || bucket.seenAt.Before(oldest) {
			oldestKey, oldest = key, bucket.seenAt
		}
	}
	delete(s.clients, oldestKey)
}

//go:embed openapi.json
var openAPISpec []byte

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(openAPISpec)
}

// nosniff stops a browser from reinterpreting a response as a type it did not
// ask for. The API only serves JSON, so a response rendered directly as HTML
// would be a sniffing bug, not a feature.
func nosniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// noStore marks a response as uncacheable. The /v1 answers mirror live chain
// state and a health result is only meaningful at the moment it was asked for,
// so an intermediary must not serve a stale copy.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"took", time.Since(start),
			"request_id", middleware.GetReqID(r.Context()),
		)
	})
}

// errorResponse is the body returned for any non-2xx response.
type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorResponse{Error: err.Error()})
}
