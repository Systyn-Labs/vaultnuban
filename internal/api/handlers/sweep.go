package handlers

import (
	"crypto/subtle"
	"net/http"

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

	logger.Log(sweepHandlerCtx, "sweep triggered by cron")

	result, err := h.runner.Run(r.Context())
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
