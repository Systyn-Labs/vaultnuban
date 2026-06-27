package recon

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/ledger"
	"github.com/systynlabs/vaultnuban/internal/logger"
	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/store"
)

const workerCtx = "ReconWorker"

// WorkItem is enqueued by the webhook ingestor for async processing (TDD §2).
type WorkItem struct {
	WebhookEventID string
	Payload        *provider.WebhookPayload
	Source         string // "webhook" | "sweep"
}

// Worker reads WorkItems from a buffered channel and processes them.
type Worker struct {
	queue     chan WorkItem
	matcher   *Matcher
	txns      store.TransactionStore
	webhooks  store.WebhookEventStore
	suspense  store.SuspenseStore
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
		queue:     make(chan WorkItem, queueSize),
		matcher:   matcher,
		txns:      txns,
		webhooks:  webhooks,
		suspense:  suspense,
		customers: customers,
	}
}

// Enqueue adds a work item to the in-process queue.
// Returns false if the queue is full — the webhook handler still acks;
// the sweep recovers any dropped items on the next run.
func (w *Worker) Enqueue(item WorkItem) bool {
	select {
	case w.queue <- item:
		return true
	default:
		logger.Warnf(workerCtx, "queue full, dropping event %s (sweep will recover)", item.WebhookEventID)
		return false
	}
}

// Run starts the worker loop. It blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	logger.Log(workerCtx, "reconciliation worker started")
	for {
		select {
		case <-ctx.Done():
			logger.Log(workerCtx, "reconciliation worker stopped")
			return
		case item := <-w.queue:
			if err := w.process(ctx, item); err != nil {
				logger.Errorf(workerCtx, "error processing event %s: %v", item.WebhookEventID, err)
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
		logger.Debugf(workerCtx, "payment_failed event %s — stored, not posted", pt.TransactionID)
		if item.WebhookEventID != "" {
			_ = w.webhooks.MarkWebhookProcessed(ctx, item.WebhookEventID, "ignored")
		}
		return nil
	default:
		logger.Warnf(workerCtx, "unknown event type %q for txn %s — stored but not processed",
			item.Payload.EventType, pt.TransactionID)
		if item.WebhookEventID != "" {
			_ = w.webhooks.MarkWebhookProcessed(ctx, item.WebhookEventID, "ignored")
		}
		return nil
	}
}

func (w *Worker) processCredit(ctx context.Context, item WorkItem, pt provider.ProviderTransaction) error {
	result, err := w.matcher.Match(ctx, pt)
	if err != nil {
		return fmt.Errorf("match: %w", err)
	}

	// Tier-limit check when matched to an active VA
	if result.Suspense == nil && result.CustomerID != "" {
		customer, err := w.customers.GetCustomer(ctx, "", result.CustomerID)
		if err == nil && customer != nil && customer.Identity != nil {
			walletAccount := domain.CustomerWalletAccount(result.CustomerID)
			reason, err := w.matcher.CheckTierLimitsForCustomer(
				ctx, walletAccount, pt.AmountKobo, customer.Identity.KYCTier,
			)
			if err != nil {
				return fmt.Errorf("tier check: %w", err)
			}
			if reason != "" {
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
		return fmt.Errorf("post transaction: %w", err)
	}

	if !created {
		logger.Debugf(workerCtx, "transaction %s already posted — skipping (duplicate)", pt.TransactionID)
		if item.WebhookEventID != "" {
			_ = w.webhooks.MarkWebhookProcessed(ctx, item.WebhookEventID, "ignored")
		}
		return nil
	}

	if result.Suspense != nil {
		if err := w.suspense.CreateSuspenseItem(ctx, result.Suspense); err != nil {
			logger.Errorf(workerCtx, "create suspense item for %s: %v", pt.TransactionID, err)
		}
		logger.Logf(workerCtx, "txn %s → suspense(%s) ₦%s",
			pt.TransactionID, result.Suspense.Reason, koboToNaira(pt.AmountKobo))
	} else {
		logger.Logf(workerCtx, "txn %s → wallet(%s) ₦%s",
			pt.TransactionID, result.CustomerID, koboToNaira(pt.AmountKobo))
	}

	if item.WebhookEventID != "" {
		_ = w.webhooks.MarkWebhookProcessed(ctx, item.WebhookEventID, "processed")
	}
	return nil
}

func (w *Worker) processReversal(ctx context.Context, item WorkItem, pt provider.ProviderTransaction) error {
	result, err := w.matcher.MatchReversal(ctx, pt)
	if err != nil {
		return fmt.Errorf("reversal match: %w", err)
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
		return fmt.Errorf("post reversal: %w", err)
	}

	if !created {
		logger.Debugf(workerCtx, "reversal %s already posted — skipping", pt.TransactionID)
	} else {
		logger.Logf(workerCtx, "reversal %s posted — ₦%s reversed from %s",
			pt.TransactionID, koboToNaira(pt.AmountKobo), result.CustomerID)
	}

	if item.WebhookEventID != "" {
		_ = w.webhooks.MarkWebhookProcessed(ctx, item.WebhookEventID, "processed")
	}
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

func koboToNaira(kobo int64) string {
	naira := kobo / 100
	return fmt.Sprintf("%d.%02d", naira, kobo%100)
}

// ProcessResult is returned by ProcessDirect so callers (e.g. sweep) can tally counts.
type ProcessResult struct {
	Skipped   bool // transaction already existed in the ledger
	Posted    bool // new debit/credit posted to a customer wallet
	Suspensed bool // new credit posted to the suspense account
}

// ProcessDirect runs the full processing pipeline synchronously, bypassing the
// internal channel. Used by the sweep runner so it can tally counts accurately.
func (w *Worker) ProcessDirect(ctx context.Context, item WorkItem) (ProcessResult, error) {
	pt := item.Payload.Transaction

	// Check existence first to avoid redundant work.
	existing, err := w.txns.GetTransaction(ctx, pt.TransactionID)
	if err != nil {
		return ProcessResult{}, fmt.Errorf("ProcessDirect: check existing: %w", err)
	}
	if existing != nil {
		return ProcessResult{Skipped: true}, nil
	}

	switch item.Payload.EventType {
	case "payment_success":
		result, suspensed, err := w.processCreditDirect(ctx, item, pt)
		return ProcessResult{Posted: result, Suspensed: suspensed}, err
	case "payment_reversal":
		if err := w.processReversal(ctx, item, pt); err != nil {
			return ProcessResult{}, err
		}
		return ProcessResult{Posted: true}, nil
	default:
		return ProcessResult{Skipped: true}, nil
	}
}

// processCreditDirect is the synchronous credit path; returns (posted, suspensed, err).
func (w *Worker) processCreditDirect(ctx context.Context, item WorkItem, pt provider.ProviderTransaction) (bool, bool, error) {
	result, err := w.matcher.Match(ctx, pt)
	if err != nil {
		return false, false, fmt.Errorf("match: %w", err)
	}

	if result.Suspense == nil && result.CustomerID != "" {
		customer, err := w.customers.GetCustomer(ctx, "", result.CustomerID)
		if err == nil && customer != nil && customer.Identity != nil {
			walletAccount := domain.CustomerWalletAccount(result.CustomerID)
			reason, err := w.matcher.CheckTierLimitsForCustomer(
				ctx, walletAccount, pt.AmountKobo, customer.Identity.KYCTier,
			)
			if err != nil {
				return false, false, fmt.Errorf("tier check: %w", err)
			}
			if reason != "" {
				entries, err := ledger.CreditSuspense(pt.TransactionID, pt.AmountKobo)
				if err != nil {
					return false, false, err
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
		return false, false, fmt.Errorf("post transaction: %w", err)
	}
	if !created {
		return false, false, nil // race with webhook; already posted
	}

	suspensed := result.Suspense != nil
	if suspensed {
		if err := w.suspense.CreateSuspenseItem(ctx, result.Suspense); err != nil {
			logger.Errorf(workerCtx, "create suspense item for %s: %v", pt.TransactionID, err)
		}
	}
	return true, suspensed, nil
}

// NewWorkItemFromProviderTx creates a WorkItem for the sweep to enqueue.
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
