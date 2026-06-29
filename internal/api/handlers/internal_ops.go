package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/service"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// InternalOpsHandler serves debug and backfill endpoints under /internal.
type InternalOpsHandler struct {
	webhookStore store.WebhookEventStore
	suspenseStore store.SuspenseStore
	txnStore      store.TransactionStore
	vaStore       store.VirtualAccountStore
	customerStore store.CustomerStore
	suspenseSvc   *service.SuspenseService
	provider      provider.Provider
}

func NewInternalOpsHandler(
	webhookStore store.WebhookEventStore,
	suspenseStore store.SuspenseStore,
	txnStore store.TransactionStore,
	vaStore store.VirtualAccountStore,
	customerStore store.CustomerStore,
	suspenseSvc *service.SuspenseService,
	prov provider.Provider,
) *InternalOpsHandler {
	return &InternalOpsHandler{
		webhookStore:  webhookStore,
		suspenseStore: suspenseStore,
		txnStore:      txnStore,
		vaStore:       vaStore,
		customerStore: customerStore,
		suspenseSvc:   suspenseSvc,
		provider:      prov,
	}
}

// ListWebhookEvents returns the most recent raw webhook payloads for debugging.
// GET /internal/webhook-events?limit=50
func (h *InternalOpsHandler) ListWebhookEvents(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	events, err := h.webhookStore.ListWebhookEvents(r.Context(), limit)
	if err != nil {
		serverErr(w, r, "ListWebhookEvents", err)
		return
	}

	type eventResp struct {
		ID             string          `json:"id"`
		DedupeKey      string          `json:"dedupe_key"`
		EventType      string          `json:"event_type"`
		SignatureValid bool            `json:"signature_valid"`
		Status         string          `json:"status"`
		Payload        json.RawMessage `json:"payload"`
		CreatedAt      string          `json:"created_at"`
		ProcessedAt    *string         `json:"processed_at,omitempty"`
	}
	type resp struct {
		Data []eventResp `json:"data"`
	}
	out := resp{Data: make([]eventResp, 0, len(events))}
	for _, e := range events {
		ev := eventResp{
			ID:             e.ID,
			DedupeKey:      e.DedupeKey,
			EventType:      e.EventType,
			SignatureValid: e.SignatureValid,
			Status:         e.Status,
			Payload:        json.RawMessage(e.Payload),
			CreatedAt:      e.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		if e.ProcessedAt != nil {
			s := e.ProcessedAt.UTC().Format("2006-01-02T15:04:05Z")
			ev.ProcessedAt = &s
		}
		out.Data = append(out.Data, ev)
	}
	writeJSON(w, http.StatusOK, out)
}

// ReprocessSuspense re-runs the matcher against all open unmatched suspense items
// by requrying their session IDs. Any item that now resolves to a VA is auto-reassigned.
// POST /internal/reprocess-suspense
func (h *InternalOpsHandler) ReprocessSuspense(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	items, err := h.suspenseStore.ListOpenUnmatchedItems(ctx, 200)
	if err != nil {
		serverErr(w, r, "ReprocessSuspense:list", err)
		return
	}

	type result struct {
		ItemID        string `json:"item_id"`
		TransactionID string `json:"transaction_id"`
		Outcome       string `json:"outcome"` // "resolved" | "no_session_id" | "requery_failed" | "no_match" | "resolve_failed"
		CustomerID    string `json:"customer_id,omitempty"`
		Error         string `json:"error,omitempty"`
	}

	var results []result
	resolved, noSession, noMatch, failed := 0, 0, 0, 0

	for _, item := range items {
		res := result{ItemID: item.ID, TransactionID: item.TransactionID}

		// Extract session_id from the stored transaction raw payload.
		origTx, err := h.txnStore.GetTransaction(ctx, item.TransactionID)
		if err != nil || origTx == nil {
			res.Outcome = "resolve_failed"
			res.Error = "transaction not found"
			failed++
			results = append(results, res)
			continue
		}

		// The raw column contains the ProviderTransaction JSON with snake_case keys.
		var raw struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(origTx.Raw, &raw)

		if raw.SessionID == "" {
			res.Outcome = "no_session_id"
			noSession++
			results = append(results, res)
			continue
		}

		// Requery to get the full payload (accountRef, accountNumber).
		pt, err := h.provider.Requery(ctx, raw.SessionID)
		if err != nil {
			res.Outcome = "requery_failed"
			res.Error = err.Error()
			failed++
			results = append(results, res)
			continue
		}

		// Attempt VA lookup by accountRef then NUBAN.
		var customerID string
		if pt.AccountRef != "" {
			vaRecord, _ := h.vaStore.GetVAByAccountRef(ctx, pt.AccountRef)
			if vaRecord != nil {
				customerID = vaRecord.CustomerID
			}
		}
		if customerID == "" && pt.AccountNumber != "" {
			vaRecord, _ := h.vaStore.GetVAByNUBAN(ctx, pt.AccountNumber)
			if vaRecord != nil {
				customerID = vaRecord.CustomerID
			}
		}

		if customerID == "" {
			res.Outcome = "no_match"
			noMatch++
			results = append(results, res)
			continue
		}

		// Look up customer to get tenant_id for the resolve call.
		customer, err := h.customerStore.GetCustomerByID(ctx, customerID)
		if err != nil || customer == nil {
			res.Outcome = "resolve_failed"
			res.Error = "customer lookup failed"
			failed++
			results = append(results, res)
			continue
		}

		err = h.suspenseSvc.Resolve(ctx, item.ID, service.ResolveRequest{
			Resolution:       "reassign",
			TargetCustomerID: customerID,
			TenantID:         customer.TenantID,
			Actor:            "system:reprocess",
			Notes:            "auto-resolved by reprocess-suspense (session_id requery)",
		})
		if err != nil {
			res.Outcome = "resolve_failed"
			res.Error = err.Error()
			failed++
			results = append(results, res)
			continue
		}

		res.Outcome = "resolved"
		res.CustomerID = customerID
		resolved++
		results = append(results, res)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":      len(items),
		"resolved":   resolved,
		"no_match":   noMatch,
		"no_session": noSession,
		"failed":     failed,
		"items":      results,
	})
}
