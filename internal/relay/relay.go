// Package relay implements the tenant webhook relay (FR-11).
//
// When VaultNUBAN posts a credit to a customer wallet, it fans out a signed
// HTTP POST to every active RelayEndpoint registered by that tenant. Each
// delivery is logged; failed deliveries are retried up to 3 times with
// exponential back-off before being moved to dead_letter.
package relay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/logger"
	"github.com/systynlabs/vaultnuban/internal/store"
)

const (
	relayCtx    = "RelayDispatcher"
	maxAttempts = 3
	httpTimeout = 10 * time.Second
)

// retryDelay returns the back-off duration for a given attempt number (1-based).
// attempt 1 → 30s, attempt 2 → 5m, attempt 3 → dead_letter (no more retries).
func retryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 30 * time.Second
	case 2:
		return 5 * time.Minute
	default:
		return 0
	}
}

// Dispatcher fans out relay events to all active endpoints for a tenant.
type Dispatcher struct {
	store  store.RelayStore
	client *http.Client
}

func NewDispatcher(store store.RelayStore) *Dispatcher {
	return &Dispatcher{
		store:  store,
		client: &http.Client{Timeout: httpTimeout},
	}
}

// EventPayload is the JSON body sent to tenant relay endpoints.
type EventPayload struct {
	EventType   string          `json:"event_type"`
	OccurredAt  string          `json:"occurred_at"`
	Transaction json.RawMessage `json:"transaction"`
}

// Dispatch fans out to all active endpoints for the tenant.
// It is designed to be called from the recon worker after a successful post.
// Each delivery is fire-and-forget in a goroutine; errors are logged and stored.
func (d *Dispatcher) Dispatch(ctx context.Context, tenantID string, payload EventPayload) {
	endpoints, err := d.store.ListEndpoints(ctx, tenantID)
	if err != nil {
		logger.Errorf(relayCtx, "list endpoints for tenant %s: %v", tenantID, err)
		return
	}

	body, err := json.Marshal(payload)
	if err != nil {
		logger.Errorf(relayCtx, "marshal relay payload: %v", err)
		return
	}

	for _, ep := range endpoints {
		if !ep.Active {
			continue
		}
		ep := ep // capture for goroutine
		go func() {
			d.deliver(context.Background(), ep, payload.EventType, body, 1)
		}()
	}
}

// RetryPending is called periodically to re-attempt failed deliveries.
// In production this can be driven by the sweep cron or a separate ticker.
func (d *Dispatcher) RetryPending(ctx context.Context) {
	deliveries, err := d.store.ListPendingRetries(ctx, 100)
	if err != nil {
		logger.Errorf(relayCtx, "list pending retries: %v", err)
		return
	}
	for _, del := range deliveries {
		del := del
		ep, err := d.store.GetEndpoint(ctx, del.EndpointID)
		if err != nil || ep == nil || !ep.Active {
			continue
		}
		go func() {
			d.deliver(context.Background(), ep, del.EventType, del.Payload, del.Attempt+1)
		}()
	}
}

// deliver makes one HTTP attempt and records the result.
func (d *Dispatcher) deliver(ctx context.Context, ep *domain.RelayEndpoint, eventType string, body []byte, attempt int) {
	sig := sign(body, ep.SecretHash)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(body))
	if err != nil {
		d.recordFailure(ctx, ep, eventType, body, attempt, 0, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-VaultNUBAN-Signature", sig)
	req.Header.Set("X-VaultNUBAN-Attempt", fmt.Sprintf("%d", attempt))

	resp, err := d.client.Do(req)
	if err != nil {
		logger.Warnf(relayCtx, "delivery to %s attempt %d failed: %v", ep.URL, attempt, err)
		d.recordFailure(ctx, ep, eventType, body, attempt, 0, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		d.recordSuccess(ctx, ep, eventType, body, attempt, resp.StatusCode)
		logger.Logf(relayCtx, "delivered to %s (attempt %d, status %d)", ep.URL, attempt, resp.StatusCode)
		return
	}

	errMsg := fmt.Sprintf("non-2xx response: %d", resp.StatusCode)
	logger.Warnf(relayCtx, "delivery to %s attempt %d got %d", ep.URL, attempt, resp.StatusCode)
	d.recordFailure(ctx, ep, eventType, body, attempt, resp.StatusCode, errMsg)
}

func (d *Dispatcher) recordSuccess(ctx context.Context, ep *domain.RelayEndpoint, eventType string, body []byte, attempt, statusCode int) {
	now := time.Now().UTC()
	sc := statusCode
	del := &domain.RelayDelivery{
		EndpointID:  ep.ID,
		EventType:   eventType,
		Payload:     body,
		Attempt:     attempt,
		Status:      "delivered",
		StatusCode:  &sc,
		DeliveredAt: &now,
	}
	if err := d.store.CreateDelivery(ctx, del); err != nil {
		logger.Errorf(relayCtx, "record success delivery: %v", err)
	}
}

func (d *Dispatcher) recordFailure(ctx context.Context, ep *domain.RelayEndpoint, eventType string, body []byte, attempt, statusCode int, errMsg string) {
	status := "failed"
	var nextRetry *time.Time
	if attempt >= maxAttempts {
		status = "dead_letter"
		logger.Warnf(relayCtx, "endpoint %s exhausted after %d attempts — dead_letter", ep.URL, attempt)
	} else {
		delay := retryDelay(attempt)
		t := time.Now().UTC().Add(delay)
		nextRetry = &t
	}

	var sc *int
	if statusCode != 0 {
		sc = &statusCode
	}
	del := &domain.RelayDelivery{
		EndpointID:  ep.ID,
		EventType:   eventType,
		Payload:     body,
		Attempt:     attempt,
		Status:      status,
		StatusCode:  sc,
		Error:       &errMsg,
		NextRetryAt: nextRetry,
	}
	if err := d.store.CreateDelivery(ctx, del); err != nil {
		logger.Errorf(relayCtx, "record failed delivery: %v", err)
	}
}

// sign returns X-VaultNUBAN-Signature: HMAC-SHA256 hex of body keyed by the
// endpoint's secretHash. The tenant uses secretHash to verify on their side.
//
// Note: secretHash is the SHA-256 hex of the raw signing secret the tenant
// provided at registration. Using the hash here means the plaintext secret is
// never stored — the tenant independently hashes their secret to verify.
// For a simpler UX, tenants can also accept the hmac directly by re-hashing.
func sign(body []byte, secretHash string) string {
	mac := hmac.New(sha256.New, []byte(secretHash))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// HashSecret returns the SHA-256 hex of a plaintext secret for storage.
func HashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}
