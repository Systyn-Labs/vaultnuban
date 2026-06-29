package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/systynlabs/vaultnuban/internal/provider"
	"github.com/systynlabs/vaultnuban/internal/provider/nomba"
	"github.com/systynlabs/vaultnuban/internal/store"
)

type HealthHandler struct {
	healthStore store.PlatformHealthStore
	sweepStore  store.SweepStore
	vaStore     store.VirtualAccountStore
	provider    provider.Provider
}

func NewHealthHandler(hs store.PlatformHealthStore, ss store.SweepStore, vs store.VirtualAccountStore, prov provider.Provider) *HealthHandler {
	return &HealthHandler{healthStore: hs, sweepStore: ss, vaStore: vs, provider: prov}
}

func (h *HealthHandler) GetPlatformHealth(w http.ResponseWriter, r *http.Request) {
	ph, err := h.healthStore.GetPlatformHealth(r.Context())
	if err != nil {
		serverErr(w, r, "GetPlatformHealth", err)
		return
	}

	type ledgerResp struct {
		DebitsKobo  int64 `json:"debits_kobo"`
		CreditsKobo int64 `json:"credits_kobo"`
		Balanced    bool  `json:"balanced"`
	}
	type sweepResp struct {
		Posted    int    `json:"posted"`
		Found     int    `json:"found"`
		Suspensed int    `json:"suspensed"`
		RanAt     string `json:"ran_at"`
	}
	type webhookResp struct {
		Delivered int64 `json:"delivered"`
		Total     int64 `json:"total"`
	}
	type suspenseResp struct {
		AmountKobo  int64 `json:"amount_kobo"`
		ItemCount   int64 `json:"item_count"`
		TenantCount int64 `json:"tenant_count"`
	}
	type tenantHealthResp struct {
		ID               string  `json:"id"`
		Name             string  `json:"name"`
		Customers        int64   `json:"customers"`
		Accounts         int64   `json:"accounts"`
		OpenSuspenseKobo int64   `json:"open_suspense_kobo"`
		LastActivity     *string `json:"last_activity"`
		Status           string  `json:"status"`
	}
	type resp struct {
		Ledger              ledgerResp         `json:"ledger"`
		LastSweep           *sweepResp         `json:"last_sweep"`
		Webhook24h          webhookResp        `json:"webhook_24h"`
		CrossTenantSuspense suspenseResp       `json:"cross_tenant_suspense"`
		ActiveTenants       int                `json:"active_tenants"`
		TotalTenants        int                `json:"total_tenants"`
		TenantHealth        []tenantHealthResp `json:"tenant_health"`
		CheckedAt           string             `json:"checked_at"`
	}

	out := resp{
		Ledger: ledgerResp{
			DebitsKobo:  ph.Ledger.DebitsKobo,
			CreditsKobo: ph.Ledger.CreditsKobo,
			Balanced:    ph.Ledger.Balanced,
		},
		Webhook24h: webhookResp{
			Delivered: ph.Webhook24h.Delivered,
			Total:     ph.Webhook24h.Total,
		},
		CrossTenantSuspense: suspenseResp{
			AmountKobo:  ph.CrossTenantSuspense.AmountKobo,
			ItemCount:   ph.CrossTenantSuspense.ItemCount,
			TenantCount: ph.CrossTenantSuspense.TenantCount,
		},
		ActiveTenants: ph.ActiveTenants,
		TotalTenants:  ph.TotalTenants,
		CheckedAt:     ph.CheckedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}

	if ph.LastSweep != nil {
		s := ph.LastSweep
		out.LastSweep = &sweepResp{
			Posted:    s.Posted,
			Found:     s.Found,
			Suspensed: s.Suspensed,
			RanAt:     s.RanAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
	}

	for _, th := range ph.TenantHealth {
		t := tenantHealthResp{
			ID:               th.ID,
			Name:             th.Name,
			Customers:        th.Customers,
			Accounts:         th.Accounts,
			OpenSuspenseKobo: th.OpenSuspenseKobo,
			Status:           th.Status,
		}
		if th.LastActivity != nil {
			s := th.LastActivity.UTC().Format("2006-01-02T15:04:05Z")
			t.LastActivity = &s
		}
		out.TenantHealth = append(out.TenantHealth, t)
	}

	// Ensure tenant_health is never null in JSON
	if out.TenantHealth == nil {
		out.TenantHealth = []tenantHealthResp{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *HealthHandler) ListSweepRuns(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	runs, err := h.sweepStore.ListSweepRuns(r.Context(), limit)
	if err != nil {
		serverErr(w, r, "ListSweepRuns", err)
		return
	}

	type runResp struct {
		ID           string  `json:"id"`
		WindowFrom   string  `json:"window_from"`
		WindowTo     string  `json:"window_to"`
		PagesFetched int     `json:"pages_fetched"`
		Found        int     `json:"found"`
		Posted       int     `json:"posted"`
		Suspensed    int     `json:"suspensed"`
		DurationMS   *int    `json:"duration_ms"`
		Error        *string `json:"error,omitempty"`
		RanAt        string  `json:"ran_at"`
	}
	type resp struct {
		Data []runResp `json:"data"`
	}
	out := resp{Data: make([]runResp, 0, len(runs))}
	for _, run := range runs {
		out.Data = append(out.Data, runResp{
			ID:           run.ID,
			WindowFrom:   run.WindowFrom.UTC().Format("2006-01-02T15:04:05Z"),
			WindowTo:     run.WindowTo.UTC().Format("2006-01-02T15:04:05Z"),
			PagesFetched: run.PagesFetched,
			Found:        run.Found,
			Posted:       run.Posted,
			Suspensed:    run.Suspensed,
			DurationMS:   run.DurationMS,
			Error:        run.Error,
			RanAt:        run.RanAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *HealthHandler) ListAllVAs(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	cursor := r.URL.Query().Get("cursor")
	vas, next, err := h.vaStore.ListAllVAs(r.Context(), limit, cursor)
	if err != nil {
		serverErr(w, r, "ListAllVAs", err)
		return
	}

	type vaResp struct {
		ID                  string `json:"id"`
		CustomerID          string `json:"customer_id"`
		CustomerDisplayName string `json:"customer_display_name"`
		TenantName          string `json:"tenant_name"`
		NUBAN               string `json:"nuban"`
		BankName            string `json:"bank_name"`
		AccountName         string `json:"account_name"`
		Status              string `json:"status"`
		CreatedAt           string `json:"created_at"`
	}
	type resp struct {
		Data       []vaResp `json:"data"`
		NextCursor string   `json:"next_cursor,omitempty"`
	}
	out := resp{Data: make([]vaResp, 0, len(vas))}
	for _, va := range vas {
		out.Data = append(out.Data, vaResp{
			ID:                  va.ID,
			CustomerID:          va.CustomerID,
			CustomerDisplayName: va.CustomerDisplayName,
			TenantName:          va.TenantName,
			NUBAN:               va.NUBAN,
			BankName:            va.BankName,
			AccountName:         va.AccountName,
			Status:              string(va.Status),
			CreatedAt:           va.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	out.NextCursor = next
	writeJSON(w, http.StatusOK, out)
}

func (h *HealthHandler) ListNombaVAs(w http.ResponseWriter, r *http.Request) {
	cursor := r.URL.Query().Get("cursor")
	page, err := h.provider.ListVAs(r.Context(), cursor)
	if err != nil {
		var apiErr *nomba.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden {
			// Nomba does not permit listing VAs with these credentials.
			writeJSON(w, http.StatusOK, map[string]any{
				"data":        []any{},
				"unavailable": true,
				"reason":      "The Nomba account credentials do not have permission to list virtual accounts.",
			})
			return
		}
		serverErr(w, r, "ListNombaVAs", err)
		return
	}

	type vaResp struct {
		AccountRef  string `json:"account_ref"`
		NUBAN       string `json:"nuban"`
		BankName    string `json:"bank_name"`
		AccountName string `json:"account_name"`
		Status      string `json:"status"`
		CreatedAt   string `json:"created_at"`
	}
	type resp struct {
		Data       []vaResp `json:"data"`
		NextCursor string   `json:"next_cursor,omitempty"`
	}
	out := resp{Data: make([]vaResp, 0, len(page.VAs)), NextCursor: page.NextCursor}
	for _, va := range page.VAs {
		out.Data = append(out.Data, vaResp{
			AccountRef:  va.AccountRef,
			NUBAN:       va.NUBAN,
			BankName:    va.BankName,
			AccountName: va.AccountName,
			Status:      va.Status,
			CreatedAt:   va.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *HealthHandler) ListCrossTenantSuspense(w http.ResponseWriter, r *http.Request) {
	cursor := r.URL.Query().Get("cursor")
	items, next, err := h.healthStore.ListCrossTenantSuspense(r.Context(), 100, cursor)
	if err != nil {
		serverErr(w, r, "ListCrossTenantSuspense", err)
		return
	}

	type itemResp struct {
		ID         string `json:"id"`
		TenantName string `json:"tenant_name"`
		AmountKobo int64  `json:"amount_kobo"`
		Reason     string `json:"reason"`
		NUBAN      string `json:"nuban"`
		CreatedAt  string `json:"created_at"`
	}
	type resp struct {
		Data       []itemResp `json:"data"`
		NextCursor string     `json:"next_cursor,omitempty"`
	}
	out := resp{Data: []itemResp{}}
	for _, s := range items {
		out.Data = append(out.Data, itemResp{
			ID:         s.ID,
			TenantName: s.TenantName,
			AmountKobo: s.AmountKobo,
			Reason:     string(s.Reason),
			NUBAN:      s.NUBAN,
			CreatedAt:  s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	out.NextCursor = next
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

