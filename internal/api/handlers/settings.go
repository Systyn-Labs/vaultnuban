package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/config"
	"github.com/systynlabs/vaultnuban/internal/store"
)

type SettingsHandler struct {
	settings   store.SettingsStore
	tierLimits *config.TierLimitsCache
}

func NewSettingsHandler(settings store.SettingsStore, tierLimits *config.TierLimitsCache) *SettingsHandler {
	return &SettingsHandler{settings: settings, tierLimits: tierLimits}
}

// PutTierLimits replaces the tier-limits setting and hot-reloads the in-process cache.
//
//	PUT /internal/settings/tier-limits
//	Authorization: Bearer <INTERNAL_SWEEP_TOKEN>
func (h *SettingsHandler) PutTierLimits(w http.ResponseWriter, r *http.Request) {
	var body map[string]config.TierLimit
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		problem.BadRequest(w, "request body must be a valid JSON object of tier limits")
		return
	}
	if len(body) == 0 {
		problem.UnprocessableEntity(w, "empty-tier-limits", "tier limits object must not be empty")
		return
	}

	raw, _ := json.Marshal(body)

	if err := h.settings.UpsertSetting(r.Context(), config.TierLimitsKey, raw); err != nil {
		problem.InternalServerError(w, err.Error())
		return
	}

	// Hot-reload the in-process cache so changes take effect immediately without restart.
	if err := h.tierLimits.Load(raw); err != nil {
		problem.InternalServerError(w, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"key":   config.TierLimitsKey,
		"value": body,
	})
}

// GetTierLimits returns the current in-process tier-limits snapshot.
//
//	GET /internal/settings/tier-limits
//	Authorization: Bearer <INTERNAL_SWEEP_TOKEN>
func (h *SettingsHandler) GetTierLimits(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"key":   config.TierLimitsKey,
		"value": h.tierLimits.Snapshot(),
	})
}
