// Package ledger is the only place that constructs balanced LedgerEntry slices.
// Every credit post goes through here so the double-entry invariant (NFR-1) is
// enforced in one place.
package ledger

import (
	"fmt"

	"github.com/systynlabs/vaultnuban/internal/domain"
)

// CreditCustomer returns entries for a matched inbound transfer:
//
//	DR  nomba_settlement      amountKobo
//	CR  customer_wallet:{id}  amountKobo
func CreditCustomer(txID, customerID string, amountKobo int64) ([]domain.LedgerEntry, error) {
	if amountKobo <= 0 {
		return nil, fmt.Errorf("ledger: amount must be positive, got %d", amountKobo)
	}
	wallet := domain.CustomerWalletAccount(customerID)
	return []domain.LedgerEntry{
		{TransactionID: txID, Account: domain.AccountNombaSettlement, Direction: "debit", AmountKobo: amountKobo},
		{TransactionID: txID, Account: wallet, Direction: "credit", AmountKobo: amountKobo},
	}, nil
}

// CreditSuspense returns entries for an unmatched / policy-blocked transfer:
//
//	DR  nomba_settlement  amountKobo
//	CR  suspense          amountKobo
func CreditSuspense(txID string, amountKobo int64) ([]domain.LedgerEntry, error) {
	if amountKobo <= 0 {
		return nil, fmt.Errorf("ledger: amount must be positive, got %d", amountKobo)
	}
	return []domain.LedgerEntry{
		{TransactionID: txID, Account: domain.AccountNombaSettlement, Direction: "debit", AmountKobo: amountKobo},
		{TransactionID: txID, Account: domain.AccountSuspense, Direction: "credit", AmountKobo: amountKobo},
	}, nil
}

// ReversalEntries returns compensating entries for a payment_reversal (FR-5.4).
// The original entries are never mutated; these entries reference the *reversal*
// transactionId, not the original.
//
//	DR  customer_wallet:{id}  amountKobo
//	CR  nomba_settlement      amountKobo
func ReversalEntries(reversalTxID, customerID string, amountKobo int64) ([]domain.LedgerEntry, error) {
	if amountKobo <= 0 {
		return nil, fmt.Errorf("ledger: amount must be positive, got %d", amountKobo)
	}
	wallet := domain.CustomerWalletAccount(customerID)
	return []domain.LedgerEntry{
		{TransactionID: reversalTxID, Account: wallet, Direction: "debit", AmountKobo: amountKobo},
		{TransactionID: reversalTxID, Account: domain.AccountNombaSettlement, Direction: "credit", AmountKobo: amountKobo},
	}, nil
}

// ReversalToSuspense is used when a reversal arrives but the original credit went
// to suspense (e.g., the VA was already closed when the reversal fires).
//
//	DR  suspense          amountKobo
//	CR  nomba_settlement  amountKobo
func ReversalToSuspense(reversalTxID string, amountKobo int64) ([]domain.LedgerEntry, error) {
	if amountKobo <= 0 {
		return nil, fmt.Errorf("ledger: amount must be positive, got %d", amountKobo)
	}
	return []domain.LedgerEntry{
		{TransactionID: reversalTxID, Account: domain.AccountSuspense, Direction: "debit", AmountKobo: amountKobo},
		{TransactionID: reversalTxID, Account: domain.AccountNombaSettlement, Direction: "credit", AmountKobo: amountKobo},
	}, nil
}

// SuspenseToCustomer is used when ops resolves a suspense item by reassignment.
//
//	DR  suspense              amountKobo
//	CR  customer_wallet:{id}  amountKobo
func SuspenseToCustomer(resolutionTxID, customerID string, amountKobo int64) ([]domain.LedgerEntry, error) {
	if amountKobo <= 0 {
		return nil, fmt.Errorf("ledger: amount must be positive, got %d", amountKobo)
	}
	wallet := domain.CustomerWalletAccount(customerID)
	return []domain.LedgerEntry{
		{TransactionID: resolutionTxID, Account: domain.AccountSuspense, Direction: "debit", AmountKobo: amountKobo},
		{TransactionID: resolutionTxID, Account: wallet, Direction: "credit", AmountKobo: amountKobo},
	}, nil
}
