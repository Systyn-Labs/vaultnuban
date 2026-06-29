package recon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/logger"
	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/store"
)

const sweepCtx = "SweepRunner"

// SweepRunner pages the Nomba Transactions API and posts any transfers that
// were missed by the webhook ingestor (FR-6).
type SweepRunner struct {
	prov     provider.Provider
	txns     store.TransactionStore
	sweeps   store.SweepStore
	worker   *Worker
	interval time.Duration
	overlap  time.Duration
}

func NewSweepRunner(
	prov provider.Provider,
	txns store.TransactionStore,
	sweeps store.SweepStore,
	worker *Worker,
	interval, overlap time.Duration,
) *SweepRunner {
	return &SweepRunner{
		prov:     prov,
		txns:     txns,
		sweeps:   sweeps,
		worker:   worker,
		interval: interval,
		overlap:  overlap,
	}
}

// SweepResult is returned to the HTTP handler so it can write a summary response.
type SweepResult struct {
	WindowFrom   time.Time `json:"window_from"`
	WindowTo     time.Time `json:"window_to"`
	PagesFetched int       `json:"pages_fetched"`
	Found        int       `json:"found"`
	Posted       int       `json:"posted"`
	Suspensed    int       `json:"suspensed"`
	Skipped      int       `json:"skipped"`
	DurationMS   int64     `json:"duration_ms"`
}

// Run executes one full sweep and records the run log (FR-6.1 – FR-6.5).
// If overrideFrom is non-zero it is used as the window start, bypassing the
// stored last-sweep time. Useful for one-off backfills.
func (s *SweepRunner) Run(ctx context.Context, overrideFrom ...time.Time) (*SweepResult, error) {
	start := time.Now()

	windowTo := start
	var windowFrom time.Time
	if len(overrideFrom) > 0 && !overrideFrom[0].IsZero() {
		windowFrom = overrideFrom[0]
	} else {
		var err error
		windowFrom, err = s.computeWindowFrom(ctx, windowTo)
		if err != nil {
			return nil, fmt.Errorf("sweep: compute window: %w", err)
		}
	}

	logger.Logf(sweepCtx, "starting — window %s → %s",
		windowFrom.Format(time.RFC3339), windowTo.Format(time.RFC3339))

	result := &SweepResult{WindowFrom: windowFrom, WindowTo: windowTo}
	var runErr error

	cursor := ""
	for {
		page, err := s.prov.ListTransactions(ctx, provider.ListTransactionsRequest{
			DateFrom: windowFrom,
			DateTo:   windowTo,
			Cursor:   cursor,
			PageSize: 100,
		})
		if err != nil {
			runErr = fmt.Errorf("list transactions: %w", err)
			logger.Errorf(sweepCtx, "page fetch failed: %v", err)
			break
		}

		result.PagesFetched++
		result.Found += len(page.Transactions)

		for _, pt := range page.Transactions {
			// FR-6.3: requery every transaction with a sessionId to get the full
			// payload (accountRef, accountNumber, senderName) that the list API omits.
			// Without requery the matcher has no identifier to match against and every
			// sweep-caught transaction would land in suspense as "unmatched".
			if pt.SessionID != "" {
				requeried, err := s.prov.Requery(ctx, pt.SessionID)
				if err != nil {
					logger.Warnf(sweepCtx, "requery %s failed: %v", pt.SessionID, err)
				} else {
					pt = *requeried
				}
			}

			item := buildWorkItem(pt)
			res, err := s.worker.ProcessDirect(ctx, item)
			if err != nil {
				logger.Errorf(sweepCtx, "process txn %s: %v", pt.TransactionID, err)
				continue
			}
			switch {
			case res.Skipped:
				result.Skipped++
			case res.Suspensed:
				result.Suspensed++
				result.Posted++
			case res.Posted:
				result.Posted++
			}
		}

		if page.NextCursor == "" {
			break // FR-6.1: follow cursors to exhaustion
		}
		cursor = page.NextCursor
	}

	result.DurationMS = time.Since(start).Milliseconds()

	// FR-6.5: write sweep run log.
	run := &domain.SweepRun{
		WindowFrom:   windowFrom,
		WindowTo:     windowTo,
		PagesFetched: result.PagesFetched,
		Found:        result.Found,
		Posted:       result.Posted,
		Suspensed:    result.Suspensed,
		DurationMS:   toIntPtr(result.DurationMS),
	}
	if runErr != nil {
		msg := runErr.Error()
		run.Error = &msg
	}
	if err := s.sweeps.CreateSweepRun(ctx, run); err != nil {
		logger.Errorf(sweepCtx, "write run log: %v", err)
	}

	logger.Logf(sweepCtx, "complete — pages=%d found=%d posted=%d suspensed=%d skipped=%d duration=%dms",
		result.PagesFetched, result.Found, result.Posted, result.Suspensed, result.Skipped, result.DurationMS)

	return result, runErr
}

// computeWindowFrom returns the start of the sweep window.
// If a prior sweep exists: lastSweepTime − overlap.
// If no prior sweep: now − interval − overlap (covers the first run).
func (s *SweepRunner) computeWindowFrom(ctx context.Context, now time.Time) (time.Time, error) {
	last, err := s.sweeps.GetLastSweepTime(ctx)
	if err != nil {
		return time.Time{}, err
	}
	if last.IsZero() {
		return now.Add(-(s.interval + s.overlap)), nil
	}
	return last.Add(-s.overlap), nil
}

func buildWorkItem(pt provider.ProviderTransaction) WorkItem {
	raw, _ := json.Marshal(pt)
	pt.Raw = raw
	return WorkItem{
		Payload: &provider.WebhookPayload{
			EventType:   "payment_success",
			Transaction: pt,
			Raw:         raw,
		},
		Source: "sweep",
	}
}

func toIntPtr(v int64) *int {
	i := int(v)
	return &i
}
