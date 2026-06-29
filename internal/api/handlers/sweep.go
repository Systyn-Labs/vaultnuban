package handlers

import (
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/logger"
	"github.com/systynlabs/vaultnuban/internal/recon"
)

const sweepHandlerCtx = "SweepHandler"

// SweepHandler exposes GET /internal/sweep for the Render Cron job.
// Auth: constant-time comparison of Authorization: Bearer {INTERNAL_SWEEP_TOKEN}.
type SweepHandler struct {
	runner *recon.SweepRunner
	token  string
}

func NewSweepHandler(runner *recon.SweepRunner, token string) *SweepHandler {
	return &SweepHandler{runner: runner, token: token}
}

func (h *SweepHandler) HandleSweep(w http.ResponseWriter, r *http.Request) {
	// Constant-time token check (prevents timing attacks on the shared secret).
	provided := r.Header.Get("Authorization")
	expected := "Bearer " + h.token
	if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		logger.Warn(sweepHandlerCtx, "unauthorized sweep attempt")
		problem.Unauthorized(w, "invalid sweep token")
		return
	}

	// Optional ?from= override (RFC3339) for one-off backfills.
	var overrideFrom time.Time
	if fromStr := r.URL.Query().Get("from"); fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			overrideFrom = t
			logger.Logf(sweepHandlerCtx, "sweep triggered with explicit from=%s", fromStr)
		} else {
			problem.BadRequest(w, "from must be RFC3339, e.g. 2026-06-29T00:00:00Z")
			return
		}
	} else {
		logger.Log(sweepHandlerCtx, "sweep triggered by cron")
	}

	result, err := h.runner.Run(r.Context(), overrideFrom)
	if err != nil {
		logger.Errorf(sweepHandlerCtx, "sweep failed: %v", err)
		// Still return 200 with partial results so the cron doesn't retry
		// aggressively — the run log already captured the error.
	}

	if result == nil {
		problem.InternalServerError(w, "sweep produced no result")
		return
	}

	writeJSON(w, http.StatusOK, result)
}
