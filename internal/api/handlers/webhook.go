package handlers

import (
	"io"
	"net/http"
	"time"

	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/logger"
	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/recon"
	"github.com/systynlabs/vaultnuban/internal/store"
)

const (
	webhookCtx          = "WebhookIngestor"
	replayWindowSeconds = 300 // 5 minutes (FR-4.2)
)

// WebhookHandler handles POST /webhooks/nomba.
type WebhookHandler struct {
	prov     provider.Provider
	webhooks store.WebhookEventStore
	worker   *recon.Worker
}

func NewWebhookHandler(
	prov provider.Provider,
	webhooks store.WebhookEventStore,
	worker *recon.Worker,
) *WebhookHandler {
	return &WebhookHandler{prov: prov, webhooks: webhooks, worker: worker}
}

// HandleNombaWebhook is the ingestor entry-point (FR-4).
// Must respond within 5 seconds (FR-4.4); all side effects are async.
func (h *WebhookHandler) HandleNombaWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		problem.BadRequest(w, "failed to read body")
		return
	}

	headers := map[string]string{
		"nomba-signature":           r.Header.Get("nomba-signature"),
		"nomba-signature-algorithm": r.Header.Get("nomba-signature-algorithm"),
		"nomba-signature-version":   r.Header.Get("nomba-signature-version"),
		"nomba-timestamp":           r.Header.Get("nomba-timestamp"),
	}

	// FR-4.1: signature verification
	if err := h.prov.VerifyWebhookSignature(r.Context(), headers, body); err != nil {
		logger.Warnf(webhookCtx, "invalid signature: %v", err)
		problem.Unauthorized(w, "invalid webhook signature")
		return
	}

	// FR-4.2: replay window
	if ts := headers["nomba-timestamp"]; ts != "" {
		if age := timestampAge(ts); age > replayWindowSeconds {
			logger.Warnf(webhookCtx, "stale timestamp rejected (%ds old)", age)
			problem.Unauthorized(w, "webhook timestamp outside replay window")
			return
		}
	}

	payload, err := h.prov.ParseWebhook(r.Context(), body)
	if err != nil {
		logger.Warnf(webhookCtx, "parse error: %v", err)
		// Unknown structure — ack so Nomba doesn't retry; FR-4.5
		w.WriteHeader(http.StatusOK)
		return
	}

	dedupeKey := payload.Transaction.TransactionID + ":" + payload.EventType

	evt := &domain.WebhookEvent{
		DedupeKey:      dedupeKey,
		EventType:      payload.EventType,
		SignatureValid: true,
		Status:         "received",
		Payload:        body,
	}

	// FR-4.3: dedupe insert
	inserted, err := h.webhooks.InsertWebhookEvent(r.Context(), evt)
	if err != nil {
		logger.Errorf(webhookCtx, "insert event error for %s: %v", dedupeKey, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if !inserted {
		// Duplicate delivery — ack and do nothing (FR-4.3)
		logger.Debugf(webhookCtx, "duplicate delivery for %s — acking without reprocessing", dedupeKey)
		w.WriteHeader(http.StatusOK)
		return
	}

	logger.Logf(webhookCtx, "received %s event for txn %s",
		payload.EventType, payload.Transaction.TransactionID)

	// FR-4.4: ack immediately, process asynchronously
	w.WriteHeader(http.StatusOK)

	h.worker.Enqueue(recon.WorkItem{
		WebhookEventID: evt.ID,
		Payload:        payload,
		Source:         "webhook",
	})
}

// timestampAge parses a Unix-second string and returns its age in seconds.
func timestampAge(ts string) int64 {
	var unix int64
	for _, c := range ts {
		if c < '0' || c > '9' {
			return 0
		}
		unix = unix*10 + int64(c-'0')
	}
	return time.Now().Unix() - unix
}
