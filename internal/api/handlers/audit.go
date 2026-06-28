package handlers

import (
	"encoding/json"
	"net/http"

	mw "github.com/systynlabs/vaultnuban/internal/api/middleware"
	"github.com/systynlabs/vaultnuban/internal/store"
)

type AuditHandler struct {
	auditStore store.AuditStore
}

func NewAuditHandler(as store.AuditStore) *AuditHandler {
	return &AuditHandler{auditStore: as}
}

func (h *AuditHandler) ListAuditEntries(w http.ResponseWriter, r *http.Request) {
	tenantID := mw.TenantFromContext(r.Context()).ID
	cursor := r.URL.Query().Get("cursor")

	entries, next, err := h.auditStore.ListAuditEntries(r.Context(), tenantID, 50, cursor)
	if err != nil {
		serverErr(w, r, "ListAuditEntries", err)
		return
	}

	type entry struct {
		ID         string `json:"id"`
		Actor      string `json:"actor"`
		Action     string `json:"action"`
		EntityType string `json:"entity_type"`
		EntityID   string `json:"entity_id"`
		At         string `json:"at"`
	}
	type resp struct {
		Data       []entry `json:"data"`
		NextCursor string  `json:"next_cursor,omitempty"`
	}

	out := resp{Data: []entry{}}
	for _, e := range entries {
		out.Data = append(out.Data, entry{
			ID:         e.ID,
			Actor:      e.Actor,
			Action:     e.Action,
			EntityType: e.EntityType,
			EntityID:   e.EntityID,
			At:         e.At.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	out.NextCursor = next

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
