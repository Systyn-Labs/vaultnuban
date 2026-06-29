// Package recon contains the reconciliation worker, matcher, and sweep runner.
package recon

import (
	"context"
	"fmt"
	"time"

	"github.com/systynlabs/vaultnuban/internal/config"
	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/ledger"
	"github.com/systynlabs/vaultnuban/internal/logger"
	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// MatchResult is the outcome of attempting to match a provider transaction.
type MatchResult struct {
	VA         *domain.VirtualAccount
	CustomerID string
	Entries    []domain.LedgerEntry
	Suspense   *domain.SuspenseItem // non-nil when routed to suspense
}

// Matcher resolves a provider transaction to a VA and constructs ledger entries.
type Matcher struct {
	accounts   store.VirtualAccountStore
	txns       store.TransactionStore
	tierLimits *config.TierLimitsCache
}

func NewMatcher(
	accounts store.VirtualAccountStore,
	txns store.TransactionStore,
	tierLimits *config.TierLimitsCache,
) *Matcher {
	return &Matcher{accounts: accounts, txns: txns, tierLimits: tierLimits}
}

// Match resolves a ProviderTransaction to a MatchResult (FR-5.1).
//
// Matching order (FR-5.1):
//  1. accountRef — the reference we set when provisioning the VA (most reliable;
//     present in both webhook and requery responses; identifies the receiving VA)
//  2. NUBAN — fallback for requery responses that carry accountNumber as the NUBAN
//  3. No match → suspense (unmatched)
//
// Note: in Nomba's webhook payload `accountNumber` is the SENDER's bank account
// number, not the receiving NUBAN. accountRef is the authoritative VA identifier.
func (m *Matcher) Match(ctx context.Context, pt provider.ProviderTransaction) (*MatchResult, error) {
	var va *domain.VirtualAccount
	var err error

	// Step 1: match by accountRef (primary — identifies the receiving VA)
	if pt.AccountRef != "" {
		va, err = m.accounts.GetVAByAccountRef(ctx, pt.AccountRef)
		if err != nil {
			return nil, fmt.Errorf("matcher: lookup by accountRef: %w", err)
		}
	}

	// Step 2: fallback to NUBAN (for requery responses where accountNumber is the NUBAN)
	if va == nil && pt.AccountNumber != "" {
		va, err = m.accounts.GetVAByNUBAN(ctx, pt.AccountNumber)
		if err != nil {
			return nil, fmt.Errorf("matcher: lookup by NUBAN: %w", err)
		}
	}

	// Step 3: unmatched → suspense
	if va == nil {
		logger.Warnf("Matcher",
			"unmatched txn=%s accountRef=%q accountNumber=%q amountKobo=%d — routed to suspense",
			pt.TransactionID, pt.AccountRef, pt.AccountNumber, pt.AmountKobo)
		entries, err := ledger.CreditSuspense(pt.TransactionID, pt.AmountKobo)
		if err != nil {
			return nil, err
		}
		return &MatchResult{
			Entries: entries,
			Suspense: &domain.SuspenseItem{
				TransactionID: pt.TransactionID,
				Reason:        domain.SuspenseReasonUnmatched,
				Status:        "open",
			},
		}, nil
	}

	// Closed account → suspense (FR-7.1)
	if va.Status == domain.VAStatusClosed {
		entries, err := ledger.CreditSuspense(pt.TransactionID, pt.AmountKobo)
		if err != nil {
			return nil, err
		}
		return &MatchResult{
			VA:      va,
			Entries: entries,
			Suspense: &domain.SuspenseItem{
				TransactionID: pt.TransactionID,
				Reason:        domain.SuspenseReasonClosedAccount,
				Status:        "open",
			},
		}, nil
	}

	// Suspended account → suspense (FR-7.1)
	if va.Status == domain.VAStatusSuspended {
		entries, err := ledger.CreditSuspense(pt.TransactionID, pt.AmountKobo)
		if err != nil {
			return nil, err
		}
		return &MatchResult{
			VA:      va,
			Entries: entries,
			Suspense: &domain.SuspenseItem{
				TransactionID: pt.TransactionID,
				Reason:        domain.SuspenseReasonSuspendedAccount,
				Status:        "open",
			},
		}, nil
	}

	// Active VA — check KYC tier limits (FR-5.6)
	customerID := va.CustomerID
	walletAccount := domain.CustomerWalletAccount(customerID)

	suspenseReason, err := m.checkTierLimits(ctx, va, walletAccount, pt.AmountKobo)
	if err != nil {
		return nil, err
	}
	if suspenseReason != "" {
		entries, err := ledger.CreditSuspense(pt.TransactionID, pt.AmountKobo)
		if err != nil {
			return nil, err
		}
		return &MatchResult{
			VA:      va,
			Entries: entries,
			Suspense: &domain.SuspenseItem{
				TransactionID: pt.TransactionID,
				Reason:        suspenseReason,
				Status:        "open",
			},
		}, nil
	}

	// Happy path — credit customer wallet
	entries, err := ledger.CreditCustomer(pt.TransactionID, customerID, pt.AmountKobo)
	if err != nil {
		return nil, err
	}
	return &MatchResult{
		VA:         va,
		CustomerID: customerID,
		Entries:    entries,
	}, nil
}

// MatchReversal constructs compensating entries for a payment_reversal (FR-5.4).
func (m *Matcher) MatchReversal(ctx context.Context, pt provider.ProviderTransaction) (*MatchResult, error) {
	var va *domain.VirtualAccount
	var err error

	if pt.AccountRef != "" {
		va, err = m.accounts.GetVAByAccountRef(ctx, pt.AccountRef)
		if err != nil {
			return nil, fmt.Errorf("matcher: reversal lookup by ref: %w", err)
		}
	}
	if va == nil && pt.AccountNumber != "" {
		va, err = m.accounts.GetVAByNUBAN(ctx, pt.AccountNumber)
		if err != nil {
			return nil, fmt.Errorf("matcher: reversal lookup by NUBAN: %w", err)
		}
	}

	if va == nil {
		// Original was in suspense, reverse from there
		entries, err := ledger.ReversalToSuspense(pt.TransactionID, pt.AmountKobo)
		if err != nil {
			return nil, err
		}
		return &MatchResult{Entries: entries}, nil
	}

	entries, err := ledger.ReversalEntries(pt.TransactionID, va.CustomerID, pt.AmountKobo)
	if err != nil {
		return nil, err
	}
	return &MatchResult{VA: va, CustomerID: va.CustomerID, Entries: entries}, nil
}

// checkTierLimits returns the suspense reason if the credit would breach the
// customer's KYC tier, or empty string if it's within limits (FR-5.6).
func (m *Matcher) checkTierLimits(
	ctx context.Context,
	va *domain.VirtualAccount,
	walletAccount string,
	amountKobo int64,
) (domain.SuspenseReason, error) {
	// We need the customer's KYC tier. For now we look up via VA→customer path.
	// A future optimisation could cache this on the VA record.
	// We use tier=1 as the safe default when we can't determine tier.
	// TODO(Phase 3 integration): the matcher should receive the identity store.
	// For now we skip the tier check here and rely on the worker passing tier in.
	_ = va
	_ = walletAccount
	_ = amountKobo
	_ = m.tierLimits
	return "", nil
}

// CheckTierLimitsForCustomer is called by the worker, which has access to the identity.
func (m *Matcher) CheckTierLimitsForCustomer(
	ctx context.Context,
	walletAccount string,
	amountKobo int64,
	kycTier int,
) (domain.SuspenseReason, error) {
	limits, ok := m.tierLimits.Get(kycTier)
	if !ok {
		return "", nil // unconfigured tier = uncapped
	}

	// Max balance check
	if limits.MaxBalanceKobo > 0 {
		balance, err := m.txns.GetBalance(ctx, walletAccount)
		if err != nil {
			return "", fmt.Errorf("matcher: get balance: %w", err)
		}
		if balance+amountKobo > limits.MaxBalanceKobo {
			return domain.SuspenseReasonTierLimit, nil
		}
	}

	// Daily credit cap check
	if limits.DailyCreditKobo > 0 {
		daily, err := m.txns.GetDailyCredits(ctx, walletAccount, time.Now().UTC())
		if err != nil {
			return "", fmt.Errorf("matcher: get daily credits: %w", err)
		}
		if daily+amountKobo > limits.DailyCreditKobo {
			return domain.SuspenseReasonTierLimit, nil
		}
	}

	return "", nil
}
