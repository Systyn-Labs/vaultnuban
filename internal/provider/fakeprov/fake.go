// Package fakeprov is an in-memory provider.Provider for CI and the harness.
// All state is held in maps; no network calls are made.
package fakeprov

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/systynlabs/vaultnuban/internal/provider"
)

// Fake implements provider.Provider entirely in-memory.
type Fake struct {
	mu sync.Mutex

	// virtual accounts: accountRef → VAResponse
	accounts map[string]*provider.VAResponse

	// transactions seeded for sweep simulation: transactionID → ProviderTransaction
	transactions map[string]provider.ProviderTransaction

	// WebhookSecret used by VerifyWebhookSignature; set to "" to skip verification in tests.
	WebhookSecret string

	// NextNUBAN is the NUBAN returned for the next CreateVA call; auto-increments.
	nextNUBAN int
}

func New() *Fake {
	return &Fake{
		accounts:     make(map[string]*provider.VAResponse),
		transactions: make(map[string]provider.ProviderTransaction),
		nextNUBAN:    1000000001,
	}
}

// SeedTransaction injects a transaction so the sweep can discover it.
func (f *Fake) SeedTransaction(t provider.ProviderTransaction) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transactions[t.TransactionID] = t
}

func (f *Fake) CreateVA(_ context.Context, req provider.CreateVARequest) (*provider.VAResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if existing, ok := f.accounts[req.AccountRef]; ok {
		return existing, nil
	}

	nuban := fmt.Sprintf("%010d", f.nextNUBAN)
	f.nextNUBAN++

	va := &provider.VAResponse{
		AccountRef:      req.AccountRef,
		NUBAN:           nuban,
		BankName:        "FakeBank",
		AccountName:     req.AccountName,
		AccountHolderID: "fake-holder-" + req.AccountRef,
	}
	f.accounts[req.AccountRef] = va
	return va, nil
}

func (f *Fake) UpdateVA(_ context.Context, identifier, newName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, va := range f.accounts {
		if va.AccountRef == identifier || va.NUBAN == identifier {
			va.AccountName = newName
			return nil
		}
	}
	return fmt.Errorf("fakeprov: VA not found: %s", identifier)
}

func (f *Fake) CloseVA(_ context.Context, identifier string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ref, va := range f.accounts {
		if va.AccountRef == identifier || va.NUBAN == identifier {
			delete(f.accounts, ref)
			return nil
		}
	}
	return nil // idempotent
}

func (f *Fake) SuspendVA(_ context.Context, _ string) error {
	return nil // fake accepts all suspensions
}

func (f *Fake) ListVAs(_ context.Context, _ string) (*provider.VAPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	page := &provider.VAPage{}
	for _, va := range f.accounts {
		page.VAs = append(page.VAs, provider.NombaVA{
			AccountRef:  va.AccountRef,
			NUBAN:       va.NUBAN,
			BankName:    va.BankName,
			AccountName: va.AccountName,
			Status:      "ACTIVE",
		})
	}
	return page, nil
}

func (f *Fake) ListTransactions(_ context.Context, req provider.ListTransactionsRequest) (*provider.TransactionPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []provider.ProviderTransaction
	for _, t := range f.transactions {
		if (t.OccurredAt.Equal(req.DateFrom) || t.OccurredAt.After(req.DateFrom)) &&
			t.OccurredAt.Before(req.DateTo) {
			out = append(out, t)
		}
	}
	return &provider.TransactionPage{Transactions: out}, nil
}

func (f *Fake) Requery(_ context.Context, sessionID string) (*provider.ProviderTransaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.transactions {
		if t.SessionID == sessionID {
			cp := t
			return &cp, nil
		}
	}
	return nil, errors.New("fakeprov: session not found: " + sessionID)
}

func (f *Fake) Transfer(_ context.Context, req provider.TransferRequest) (*provider.TransferResponse, error) {
	txID := fmt.Sprintf("fake-tx-%d", req.AmountKobo)
	return &provider.TransferResponse{
		TransactionID: txID,
		SessionID:     "fake-session-" + txID,
		Status:        "SUCCESSFUL",
	}, nil
}

func (f *Fake) ResolveAccount(_ context.Context, bankCode, accountNumber string) (*provider.AccountResolution, error) {
	return &provider.AccountResolution{
		AccountName:   "FAKE ACCOUNT HOLDER",
		AccountNumber: accountNumber,
		BankCode:      bankCode,
	}, nil
}

func (f *Fake) VerifyWebhookSignature(_ context.Context, _ map[string]string, _ []byte) error {
	return nil // harness skips signature verification
}

func (f *Fake) ParseWebhook(_ context.Context, body []byte) (*provider.WebhookPayload, error) {
	var p struct {
		Event string                    `json:"event"`
		Data  provider.ProviderTransaction `json:"data"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("fakeprov: parse webhook: %w", err)
	}
	if p.Data.OccurredAt.IsZero() {
		p.Data.OccurredAt = time.Now()
	}
	p.Data.Raw = body
	return &provider.WebhookPayload{
		EventType:   p.Event,
		Transaction: p.Data,
		Raw:         body,
	}, nil
}
