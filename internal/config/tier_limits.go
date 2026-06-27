package config

import (
	"encoding/json"
	"fmt"
	"sync"
)

const TierLimitsKey = "tier_limits"

// DefaultTierLimitsJSON is the seed value applied on first startup.
// Tier 1: 50k NGN/day, 300k NGN max balance
// Tier 2: 200k NGN/day, 5M NGN max balance
// Tier 3: uncapped
var DefaultTierLimitsJSON = []byte(`{"1":{"daily_credit_kobo":5000000,"max_balance_kobo":30000000},"2":{"daily_credit_kobo":20000000,"max_balance_kobo":500000000},"3":{"daily_credit_kobo":0,"max_balance_kobo":0}}`)

// TierLimitsCache is a thread-safe, hot-reloadable store for KYC tier limits.
type TierLimitsCache struct {
	mu     sync.RWMutex
	limits map[int]TierLimit
}

func NewTierLimitsCache() *TierLimitsCache { return &TierLimitsCache{} }

// Load replaces the in-memory limits from a raw JSON blob (e.g. DB read).
func (c *TierLimitsCache) Load(raw []byte) error {
	var strMap map[string]TierLimit
	if err := json.Unmarshal(raw, &strMap); err != nil {
		return fmt.Errorf("tier limits: invalid JSON: %w", err)
	}
	m := make(map[int]TierLimit, len(strMap))
	for k, v := range strMap {
		var tier int
		if _, err := fmt.Sscanf(k, "%d", &tier); err != nil {
			return fmt.Errorf("tier limits: key %q is not an integer tier", k)
		}
		m[tier] = v
	}
	c.mu.Lock()
	c.limits = m
	c.mu.Unlock()
	return nil
}

// Get returns the limit for the given tier. ok=false means unconfigured (uncapped).
func (c *TierLimitsCache) Get(tier int) (TierLimit, bool) {
	c.mu.RLock()
	v, ok := c.limits[tier]
	c.mu.RUnlock()
	return v, ok
}

// Snapshot returns a copy of all limits, keyed by tier number.
func (c *TierLimitsCache) Snapshot() map[int]TierLimit {
	c.mu.RLock()
	out := make(map[int]TierLimit, len(c.limits))
	for k, v := range c.limits {
		out[k] = v
	}
	c.mu.RUnlock()
	return out
}
