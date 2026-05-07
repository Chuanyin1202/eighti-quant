// Package priceprovider supplies live ticker prices for SaaS-side use:
//   - paper broker needs a price to "fill" against
//   - cron tick needs a price to feed into StrategyInput.LivePrice
//
// The Provider interface is small and synchronous. Implementations may
// cache aggressively (Binance public endpoints have rate limits).
package priceprovider

import "context"

// Provider returns a ticker price for the given exchange symbol.
type Provider interface {
	// Get returns the latest ticker price (USDT-quoted spot price). Implementations
	// should cache for a short TTL to avoid rate-limiting ourselves on hot tick paths.
	Get(ctx context.Context, symbol string) (float64, error)
}
