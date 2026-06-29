package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/systynlabs/vaultnuban/internal/domain"
	"github.com/systynlabs/vaultnuban/internal/ledger"
	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/store"
)

// WithdrawalService handles outbound transfers.
type WithdrawalService struct {
	withdrawals store.WithdrawalStore
	txns        store.TransactionStore
	customers   store.CustomerStore
	accounts    store.VirtualAccountStore
	prov        provider.Provider
}

func NewWithdrawalService(
	withdrawals store.WithdrawalStore,
	txns store.TransactionStore,
	customers store.CustomerStore,
	accounts store.VirtualAccountStore,
	prov provider.Provider,
) *WithdrawalService {
	return &WithdrawalService{
		withdrawals: withdrawals,
		txns:        txns,
		customers:   customers,
		accounts:    accounts,
		prov:        prov,
	}
}

type WithdrawalRequest struct {
	CustomerID               string
	TenantID                 string
	AmountKobo               int64
	DestinationBankCode      string
	DestinationAccountNumber string
	DestinationAccountName   string
	Narration                string
}

// Initiate creates a withdrawal record and synchronously calls the provider.
func (s *WithdrawalService) Initiate(ctx context.Context, req WithdrawalRequest) (*domain.Withdrawal, error) {
	if req.AmountKobo <= 0 {
		return nil, &ValidationError{Field: "amount_kobo", Detail: "must be positive"}
	}
	if req.DestinationBankCode == "" {
		return nil, &ValidationError{Field: "destination_bank_code", Detail: "required"}
	}
	if req.DestinationAccountNumber == "" {
		return nil, &ValidationError{Field: "destination_account_number", Detail: "required"}
	}
	if req.DestinationAccountName == "" {
		return nil, &ValidationError{Field: "destination_account_name", Detail: "required"}
	}

	// Validate customer exists and belongs to tenant.
	customer, err := s.customers.GetCustomer(ctx, req.TenantID, req.CustomerID)
	if err != nil || customer == nil {
		return nil, &NotFoundError{Entity: "customer", ID: req.CustomerID}
	}

	// Check sufficient balance.
	walletAccount := domain.CustomerWalletAccount(req.CustomerID)
	balance, err := s.txns.GetBalance(ctx, walletAccount)
	if err != nil {
		return nil, fmt.Errorf("withdrawal: get balance: %w", err)
	}
	if balance < req.AmountKobo {
		return nil, &ValidationError{Field: "amount_kobo", Detail: fmt.Sprintf("insufficient balance: have %d kobo, need %d kobo", balance, req.AmountKobo)}
	}

	// Look up customer's active VA (for record-keeping).
	va, _ := s.accounts.GetActiveVA(ctx, req.CustomerID)
	var vaID *string
	if va != nil {
		id := va.ID
		vaID = &id
	}

	// Create the withdrawal record.
	w := &domain.Withdrawal{
		CustomerID:               req.CustomerID,
		VirtualAccountID:         vaID,
		AmountKobo:               req.AmountKobo,
		DestinationBankCode:      req.DestinationBankCode,
		DestinationAccountNumber: req.DestinationAccountNumber,
		DestinationAccountName:   req.DestinationAccountName,
		Narration:                req.Narration,
		Status:                   "processing",
	}
	raw, _ := json.Marshal(req)
	w.Raw = raw

	if err := s.withdrawals.CreateWithdrawal(ctx, w); err != nil {
		return nil, fmt.Errorf("withdrawal: create record: %w", err)
	}

	// Call provider.
	transferResp, err := s.prov.Transfer(ctx, provider.TransferRequest{
		AmountKobo:               req.AmountKobo,
		DestinationBankCode:      req.DestinationBankCode,
		DestinationAccountNumber: req.DestinationAccountNumber,
		DestinationAccountName:   req.DestinationAccountName,
		Narration:                req.Narration,
		Reference:                w.ID,
	})

	if err != nil {
		reason := err.Error()
		_ = s.withdrawals.UpdateWithdrawalStatus(ctx, w.ID, "failed", nil, nil, &reason)
		w.Status = "failed"
		w.FailureReason = &reason
		return w, fmt.Errorf("withdrawal: provider transfer: %w", err)
	}

	// Post ledger entries: DR customer_wallet / CR nomba_settlement
	txID := "vn:wd:" + w.ID
	entries, err := ledger.WithdrawCustomer(txID, req.CustomerID, req.AmountKobo)
	if err != nil {
		return nil, err
	}

	tx := &domain.Transaction{
		ID:               txID,
		VirtualAccountID: vaID,
		AmountKobo:       req.AmountKobo,
		Direction:        "debit",
		Source:           "internal",
		Status:           "posted",
		Narration:        strPtr(req.Narration),
		Raw:              raw,
		OccurredAt:       time.Now().UTC(),
	}
	if _, err := s.txns.PostTransaction(ctx, tx, entries); err != nil {
		return nil, fmt.Errorf("withdrawal: post ledger: %w", err)
	}

	// Update withdrawal record to completed.
	_ = s.withdrawals.UpdateWithdrawalStatus(ctx, w.ID, "completed",
		&transferResp.TransactionID, &transferResp.SessionID, nil)
	w.Status = "completed"
	w.ProviderTransactionID = &transferResp.TransactionID
	w.ProviderSessionID = &transferResp.SessionID

	return w, nil
}

// ResolveAccount calls the provider to look up an account name before withdrawal.
func (s *WithdrawalService) ResolveAccount(ctx context.Context, bankCode, accountNumber string) (*provider.AccountResolution, error) {
	res, err := s.prov.ResolveAccount(ctx, bankCode, accountNumber)
	if err != nil {
		return nil, fmt.Errorf("withdrawal: resolve account: %w", err)
	}
	return res, nil
}
