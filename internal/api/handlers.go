package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
)

// maxBodyBytes caps request bodies; these payloads are tiny.
const maxBodyBytes = 64 << 10

// handleLiveness reports that the process is up. It deliberately does not call
// out to the network, so that a liveness probe does not fail because an
// upstream RPC endpoint is briefly unavailable.
func (s *Server) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadiness reports whether this instance can answer queries: unlike
// /healthz, it calls the configured RPC endpoint, which must report healthy
// and serve the configured network. An orchestrator uses it to take an
// instance out of rotation while its upstream is down — the reason for the
// split is that a liveness probe must not restart the process for an RPC
// blip.
func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	// The answer describes live upstream state, so neither an intermediary
	// nor a browser may serve a stale copy. (The few-second reuse lives in
	// the server, below, and is what keeps probes off the RPC endpoint.)
	w.Header().Set("Cache-Control", "no-store")

	if err, ok := s.cachedReadiness(); ok {
		s.writeReadiness(w, err)
		return
	}

	err := s.prober.Ready(r.Context(), s.networkPassphrase)

	s.readyMu.Lock()
	s.readyErr, s.readyAt, s.readySet = err, time.Now(), true
	s.readyMu.Unlock()

	s.writeReadiness(w, err)
}

// cachedReadiness returns the last readiness result while it is fresh, so
// frequent probes do not multiply RPC traffic. Failures are cached too: an
// orchestrator probing every second must not turn an RPC outage into an
// outage of its own.
func (s *Server) cachedReadiness() (error, bool) {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if !s.readySet || time.Since(s.readyAt) >= s.readinessTTL {
		return nil, false
	}
	return s.readyErr, true
}

// writeReadiness renders a readiness result: 200 when the RPC endpoint is
// ready, 503 with the probe's own reason otherwise.
func (s *Server) writeReadiness(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"status": "unready",
		"reason": err.Error(),
	})
}

// simulateBody is the POST /v1/simulate payload.
type simulateBody struct {
	ContractID string   `json:"contract_id"`
	Function   string   `json:"function"`
	Args       []string `json:"args"`
	// Baseline is a saved simulation report to compare the cost against.
	// It has the shape a --json report has.
	Baseline *probe.Baseline `json:"baseline,omitempty"`
	// MaxIncreasePercent is the largest tolerated percentage increase in any
	// cost figure. It only has an effect alongside a baseline.
	MaxIncreasePercent *float64 `json:"max_increase_percent,omitempty"`
}

// maxAPIBatchChecks is lower than probe.MaxBatchChecks because the whole
// request shares one deadline (Options.Timeout, 60s by default) and checks
// run sequentially, each costing a few RPC round trips. Larger batches
// belong in the CLI, or split across requests.
const maxAPIBatchChecks = 20

// handleChecks serves POST /v1/checks: several checks in one request.
func (s *Server) handleChecks(w http.ResponseWriter, r *http.Request) {
	var body probe.BatchRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if len(body.Checks) > maxAPIBatchChecks {
		writeError(w, http.StatusBadRequest, fmt.Errorf(
			"%d checks exceeds the per-request limit of %d; split the batch or use `soroprobe check --file`",
			len(body.Checks), maxAPIBatchChecks))
		return
	}

	result, err := s.prober.CheckBatch(r.Context(), body)
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}

	// As with a single check, an unhealthy contract — or one that could not
	// be checked — is reported in the body ("ok", "errored") with a 200.
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSimulate(w http.ResponseWriter, r *http.Request) {
	var body simulateBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid request body: %w", err))
		return
	}
	if body.ContractID == "" || body.Function == "" {
		writeError(w, http.StatusBadRequest, errors.New("contract_id and function are required"))
		return
	}

	result, err := s.prober.Simulate(r.Context(), probe.SimulateRequest{
		ContractID:         body.ContractID,
		Function:           body.Function,
		Args:               body.Args,
		Baseline:           body.Baseline,
		MaxIncreasePercent: body.MaxIncreasePercent,
	})
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}

	// A simulation that the contract rejects is a valid, fully-formed answer,
	// so it is still 200. Callers read the "success" field.
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleInspect(w http.ResponseWriter, r *http.Request) {
	durability, err := durabilityParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	estimateExtend, err := estimateExtendParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	result, err := s.prober.Inspect(r.Context(), probe.InspectRequest{
		ContractID:     chi.URLParam(r, "contract"),
		DataKeys:       r.URL.Query()["key"],
		DataDurability: durability,
		EstimateExtend: estimateExtend,
	})
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// estimateExtendParam reads the number of ledgers to price an extension for.
// Zero means no estimate was asked for.
func estimateExtendParam(r *http.Request) (uint32, error) {
	raw := r.URL.Query().Get("estimate_extend")
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("estimate_extend must be a number of ledgers, got %q", raw)
	}
	return uint32(value), nil
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	durability, err := durabilityParam(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	query := r.URL.Query()
	result, err := s.prober.Check(r.Context(), probe.CheckRequest{
		ContractID:     chi.URLParam(r, "contract"),
		Function:       query.Get("fn"),
		Args:           query["arg"],
		DataKeys:       query["key"],
		DataDurability: durability,
	})
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}

	// A failing check is a successful request that reports bad news, so the
	// body is returned with 200 and callers read "ok". Returning 5xx here would
	// conflate an unhealthy contract with a broken service.
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	contractID := chi.URLParam(r, "contract")
	if contractID == "" {
		writeError(w, http.StatusBadRequest, errors.New("contract id is required"))
		return
	}

	opts := probe.EventsOptions{
		Topic:  r.URL.Query().Get("topic"),
		Cursor: r.URL.Query().Get("cursor"),
	}

	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		limit, err := strconv.ParseUint(limitStr, 10, 32)
		if err != nil || limit == 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid limit %q", limitStr))
			return
		}
		opts.Limit = uint(limit)
	}

	if startStr := r.URL.Query().Get("start_ledger"); startStr != "" {
		start, err := strconv.ParseUint(startStr, 10, 32)
		if err != nil || start == 0 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid start_ledger %q", startStr))
			return
		}
		opts.StartLedger = uint32(start)
	}

	res, err := s.prober.Events(r.Context(), contractID, opts)
	if err != nil {
		writeError(w, statusForError(err), err)
		return
	}

	writeJSON(w, http.StatusOK, res)
}

func durabilityParam(r *http.Request) (health.Durability, error) {
	switch strings.ToLower(r.URL.Query().Get("durability")) {
	case "", "persistent":
		return health.DurabilityPersistent, nil
	case "temporary", "temp":
		return health.DurabilityTemporary, nil
	default:
		return "", fmt.Errorf("unknown durability %q (want persistent or temporary)", r.URL.Query().Get("durability"))
	}
}

// statusForError maps a probe error onto an HTTP status. Bad input from the
// caller is a 400; anything else is treated as an upstream failure.
func statusForError(err error) int {
	msg := err.Error()
	switch {
	case errors.Is(err, abi.ErrNoSuchFunction),
		errors.Is(err, probe.ErrInvalidBatch),
		errors.Is(err, abi.ErrArgumentCount),
		errors.Is(err, probe.ErrBaselineMismatch):
		return http.StatusBadRequest
	case strings.Contains(msg, "invalid contract id"),
		strings.Contains(msg, "argument "),
		strings.Contains(msg, "arg "),
		strings.Contains(msg, "function name is required"):
		return http.StatusBadRequest
	default:
		return http.StatusBadGateway
	}
}
