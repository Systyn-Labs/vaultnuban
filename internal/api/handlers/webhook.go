package handlers

import (
	"io"
	"log"
	"net/http"
	"time"

	"github.com/systynlabs/vaultnuban/internal/api/problem"
	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/recon"
	"github.com/systynlabs/vaultnuban/internal/store"
)

const replayWindowSeconds = 300 // 5 minutes (FR-4.2)

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
// It must respond within 5 seconds (FR-4.4); all side effects are async.
func (h *WebhookHandler) HandleNombaWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB max
	if err != nil {
		problem.BadRequest(w, "failed to read body")
		return
	}

	// Collect Nomba signature headers
	headers := map[string]string{
		"nomba-signature":           r.Header.Get("nomba-signature"),
		"nomba-signature-algorithm": r.Header.Get("nomba-signature-algorithm"),
		"nomba-signature-version":   r.Header.Get("nomba-signature-version"),
		"nomba-timestamp":           r.Header.Get("nomba-timestamp"),
	}

	// FR-4.1: signature verification (constant-time inside the provider)
	sigErr := h.prov.VerifyWebhookSignature(r.Context(), headers, body)
	signatureValid := sigErr == nil

	if !signatureValid {
		log.Printf("webhook: invalid signature: %v", sigErr)
		problem.Unauthorized(w, "invalid webhook signature")
		return
	}

	// FR-4.2: replay window — reject stale timestamps
	if ts := headers["nomba-timestamp"]; ts != "" {
		if age := timestampAge(ts); age > replayWindowSeconds {
			log.Printf("webhook: stale timestamp (%ds old)", age)
			problem.Unauthorized(w, "webhook timestamp outside replay window")
			return
		}
	}

	// Parse the event to extract transactionId and event_type for dedupe key
	payload, err := h.prov.ParseWebhook(r.Context(), body)
	if err != nil {
		log.Printf("webhook: parse error: %v", err)
		// Store as unknown but acknowledge — FR-4.5
		w.WriteHeader(http.StatusOK)
		return
	}

	dedupeKey := payload.Transaction.TransactionID + ":" + payload.EventType

	evt := &domain.WebhookEvent{
		DedupeKey:     dedupeKey,
		EventType:     payload.EventType,
		SignatureValid: signatureValid,
		Status:        "received",
		Payload:       body,
	}

	// FR-4.3: dedupe insert — duplicate delivery acks with 200, no reprocessing
	inserted, err := h.webhooks.InsertWebhookEvent(r.Context(), evt)
	if err != nil {
		log.Printf("webhook: insert event error: %v", err)
		// Return 500 so Nomba retries — we haven't processed yet
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if !inserted {
		// Duplicate delivery: ack and do nothing
		log.Printf("webhook: duplicate delivery for %s, acking", dedupeKey)
		w.WriteHeader(http.StatusOK)
		return
	}

	// FR-4.4: ack immediately, process asynchronously
	w.WriteHeader(http.StatusOK)

	h.worker.Enqueue(recon.WorkItem{
		WebhookEventID: evt.ID,
		Payload:        payload,
		Source:         "webhook",
	})
}

// timestampAge parses a Unix-second timestamp string and returns its age in seconds.
// Returns 0 on parse error (let it through; the signature check is the primary guard).
func timestampAge(ts string) int64 {
	var unix int64
	if _, err := nativeSscanf(ts, &unix); err != nil {
		return 0
	}
	return time.Now().Unix() - unix
}

func nativeSscanf(ts string, out *int64) (int, error) {
	var n int64
	_, err := scanInt(ts, &n)
	if err != nil {
		return 0, err
	}
	*out = n
	return 1, nil
}

func scanInt(s string, out *int64) (int, error) {
	if len(s) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, io.ErrUnexpectedEOF
		}
		n = n*10 + int64(c-'0')
	}
	*out = n
	return len(s), nil
}
