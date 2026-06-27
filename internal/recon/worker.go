package recon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/ledger"
	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// WorkItem is enqueued by the webhook ingestor for async processing (TDD §2).
type WorkItem struct {
	WebhookEventID string
	Payload        *provider.WebhookPayload
	Source         string // "webhook" | "sweep"
}

// Worker reads WorkItems from a buffered channel and processes them.
type Worker struct {
	queue    chan WorkItem
	matcher  *Matcher
	txns     store.TransactionStore
	webhooks store.WebhookEventStore
	suspense store.SuspenseStore
	customers store.CustomerStore
}

func NewWorker(
	queueSize int,
	matcher *Matcher,
	txns store.TransactionStore,
	webhooks store.WebhookEventStore,
	suspense store.SuspenseStore,
	customers store.CustomerStore,
) *Worker {
	return &Worker{
		queue:    make(chan WorkItem, queueSize),
		matcher:  matcher,
		txns:     txns,
		webhooks: webhooks,
		suspense: suspense,
		customers: customers,
	}
}

// Enqueue adds a work item to the in-process queue.
// Returns false if the queue is full (the webhook handler still acks the request;
// the sweep will recover any dropped items on the next run).
func (w *Worker) Enqueue(item WorkItem) bool {
	select {
	case w.queue <- item:
		return true
	default:
		log.Printf("worker: queue full, dropping event %s (sweep will recover)", item.WebhookEventID)
		return false
	}
}

// Run starts the worker loop. It blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	log.Println("worker: started")
	for {
		select {
		case <-ctx.Done():
			log.Println("worker: stopped")
			return
		case item := <-w.queue:
			if err := w.process(ctx, item); err != nil {
				log.Printf("worker: error processing event %s: %v", item.WebhookEventID, err)
				if item.WebhookEventID != "" {
					_ = w.webhooks.MarkWebhookProcessed(ctx, item.WebhookEventID, "failed")
				}
			}
		}
	}
}

func (w *Worker) process(ctx context.Context, item WorkItem) error {
	pt := item.Payload.Transaction

	switch item.Payload.EventType {
	case "payment_success":
		return w.processCredit(ctx, item, pt)
	case "payment_reversal":
		return w.processReversal(ctx, item, pt)
	case "payment_failed":
		// Nothing to post; mark processed and move on.
		if item.WebhookEventID != "" {
			_ = w.webhooks.MarkWebhookProcessed(ctx, item.WebhookEventID, "ignored")
		}
		return nil
	default:
		log.Printf("worker: unknown event type %q — stored but not processed", item.Payload.EventType)
		if item.WebhookEventID != "" {
			_ = w.webhooks.MarkWebhookProcessed(ctx, item.WebhookEventID, "ignored")
		}
		return nil
	}
}

func (w *Worker) processCredit(ctx context.Context, item WorkItem, pt provider.ProviderTransaction) error {
	// Initial match (NUBAN / accountRef resolution and status checks)
	result, err := w.matcher.Match(ctx, pt)
	if err != nil {
		return fmt.Errorf("worker: match: %w", err)
	}

	// If matched to an active VA, run tier-limit check with identity context.
	if result.Suspense == nil && result.CustomerID != "" {
		customer, err := w.customers.GetCustomer(ctx, "", result.CustomerID)
		if err == nil && customer != nil && customer.Identity != nil {
			walletAccount := domain.CustomerWalletAccount(result.CustomerID)
			reason, err := w.matcher.CheckTierLimitsForCustomer(
				ctx, walletAccount, pt.AmountKobo, customer.Identity.KYCTier,
			)
			if err != nil {
				return fmt.Errorf("worker: tier check: %w", err)
			}
			if reason != "" {
				// Rebuild as suspense
				entries, err := ledger.CreditSuspense(pt.TransactionID, pt.AmountKobo)
				if err != nil {
					return err
				}
				result.Entries = entries
				result.Suspense = &domain.SuspenseItem{
					TransactionID: pt.TransactionID,
					Reason:        reason,
					Status:        "open",
				}
				result.CustomerID = ""
			}
		}
	}

	// Build the Transaction domain object.
	vaID := toPtr(result.VA)
	tx := &domain.Transaction{
		ID:               pt.TransactionID,
		VirtualAccountID: vaID,
		SessionID:        nullStr(pt.SessionID),
		AmountKobo:       pt.AmountKobo,
		Direction:        "credit",
		Source:           item.Source,
		Status:           "posted",
		SenderName:       nullStr(pt.SenderName),
		SenderBank:       nullStr(pt.SenderBank),
		Narration:        nullStr(pt.Narration),
		Raw:              pt.Raw,
		OccurredAt:       pt.OccurredAt,
	}

	created, err := w.txns.PostTransaction(ctx, tx, result.Entries)
	if err != nil {
		return fmt.Errorf("worker: post transaction: %w", err)
	}

	if !created {
		// Already posted (duplicate webhook or sweep race) — idempotent, no side effects.
		log.Printf("worker: transaction %s already posted, skipping", pt.TransactionID)
		if item.WebhookEventID != "" {
			_ = w.webhooks.MarkWebhookProcessed(ctx, item.WebhookEventID, "ignored")
		}
		return nil
	}

	// Create suspense item if needed.
	if result.Suspense != nil {
		if err := w.suspense.CreateSuspenseItem(ctx, result.Suspense); err != nil {
			// Log and continue — the ledger entry already exists, money is not lost.
			log.Printf("worker: create suspense item for %s: %v", pt.TransactionID, err)
		}
	}

	if item.WebhookEventID != "" {
		_ = w.webhooks.MarkWebhookProcessed(ctx, item.WebhookEventID, "processed")
	}

	if result.Suspense != nil {
		log.Printf("worker: %s → suspense(%s) ₦%d", pt.TransactionID, result.Suspense.Reason, pt.AmountKobo/100)
	} else {
		log.Printf("worker: %s → wallet(%s) ₦%d", pt.TransactionID, result.CustomerID, pt.AmountKobo/100)
	}

	return nil
}

func (w *Worker) processReversal(ctx context.Context, item WorkItem, pt provider.ProviderTransaction) error {
	result, err := w.matcher.MatchReversal(ctx, pt)
	if err != nil {
		return fmt.Errorf("worker: reversal match: %w", err)
	}

	vaID := toPtr(result.VA)
	tx := &domain.Transaction{
		ID:               pt.TransactionID,
		VirtualAccountID: vaID,
		SessionID:        nullStr(pt.SessionID),
		AmountKobo:       pt.AmountKobo,
		Direction:        "debit",
		Source:           item.Source,
		Status:           "reversed",
		SenderName:       nullStr(pt.SenderName),
		SenderBank:       nullStr(pt.SenderBank),
		Narration:        nullStr(pt.Narration),
		Raw:              pt.Raw,
		OccurredAt:       pt.OccurredAt,
	}

	created, err := w.txns.PostTransaction(ctx, tx, result.Entries)
	if err != nil {
		return fmt.Errorf("worker: post reversal: %w", err)
	}

	if !created {
		log.Printf("worker: reversal %s already posted", pt.TransactionID)
	}

	if item.WebhookEventID != "" {
		_ = w.webhooks.MarkWebhookProcessed(ctx, item.WebhookEventID, "processed")
	}

	log.Printf("worker: reversal %s posted", pt.TransactionID)
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func toPtr(va *domain.VirtualAccount) *string {
	if va == nil {
		return nil
	}
	s := va.ID
	return &s
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// JSONWorkItem is used by the sweep to enqueue synthetic WorkItems.
func NewWorkItemFromProviderTx(pt provider.ProviderTransaction, source string) WorkItem {
	raw, _ := json.Marshal(pt)
	return WorkItem{
		Payload: &provider.WebhookPayload{
			EventType:   "payment_success",
			Transaction: pt,
			Raw:         raw,
		},
		Source: source,
	}
}
