// Package api serves an HTTP mirror of the SoroProbe CLI, intended for CI
// systems and other services.
//
// Like the CLI, the API is strictly read-only: it simulates and inspects, and
// offers no route that submits a transaction.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/probe"
)

// Server wraps the HTTP API.
type Server struct {
	prober *probe.Prober
	addr   string
	log    *slog.Logger
	router chi.Router

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
	// Timeout bounds a single request. Defaults to 60s.
	Timeout time.Duration
	// NetworkPassphrase is the network /readyz expects the RPC endpoint to
	// serve; a healthy endpoint on another network is reported unready.
	// Defaults to the testnet passphrase, matching the CLI's defaults.
	NetworkPassphrase string
	// ReadinessCacheTTL bounds how long a /readyz result is reused before
	// the RPC endpoint is probed again. Defaults to 5s.
	ReadinessCacheTTL time.Duration
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
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(opts.Timeout))
	r.Use(nosniff)
	r.Use(s.logRequests)
	if len(opts.CORSOrigins) > 0 {
		r.Use(cors(opts.CORSOrigins))
	}

	r.Get("/healthz", s.handleLiveness)
	r.Get("/readyz", s.handleReadiness)
	r.Route("/v1", func(r chi.Router) {
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
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http api listening", "addr", s.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.log.Info("shutting down http api")
		return srv.Shutdown(shutdownCtx)
	}
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
