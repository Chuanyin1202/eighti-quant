package priceprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// BinancePublic fetches ticker prices from Binance Spot's public endpoint:
//
//	GET https://api.binance.com/api/v3/ticker/price?symbol=BTCUSDT
//
// No API key is required. Public endpoints have a generous IP-based rate
// limit (~6000 weight/min); we still cache results with a short TTL because
// 4h cron ticks would otherwise hit the endpoint for every paper instance.
type BinancePublic struct {
	httpClient *http.Client
	baseURL    string
	ttl        time.Duration

	mu    sync.Mutex
	cache map[string]cachedPrice
}

type cachedPrice struct {
	price float64
	ts    time.Time
}

// NewBinancePublic constructs the provider. ttl=0 picks a sensible default (10s).
// For Phase 6 paper-mode the cron tick fires at 4h boundaries so even no-cache
// would be fine; the cache mainly protects the dev loop from accidental DOS.
func NewBinancePublic(ttl time.Duration) *BinancePublic {
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	return &BinancePublic{
		httpClient: &http.Client{Timeout: 5 * time.Second},
		baseURL:    "https://api.binance.com",
		ttl:        ttl,
		cache:      make(map[string]cachedPrice),
	}
}

// Get returns the cached or freshly-fetched ticker price.
func (b *BinancePublic) Get(ctx context.Context, symbol string) (float64, error) {
	b.mu.Lock()
	if c, ok := b.cache[symbol]; ok && time.Since(c.ts) < b.ttl {
		b.mu.Unlock()
		return c.price, nil
	}
	b.mu.Unlock()

	url := b.baseURL + "/api/v3/ticker/price?symbol=" + symbol
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("priceprovider: build request: %w", err)
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("priceprovider: fetch %s: %w", symbol, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("priceprovider: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("priceprovider: %s returned %d: %s", symbol, resp.StatusCode, string(body))
	}

	var payload struct {
		Symbol string `json:"symbol"`
		Price  string `json:"price"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, fmt.Errorf("priceprovider: decode body: %w", err)
	}
	price, err := strconv.ParseFloat(payload.Price, 64)
	if err != nil {
		return 0, fmt.Errorf("priceprovider: parse price %q: %w", payload.Price, err)
	}
	if price <= 0 {
		return 0, fmt.Errorf("priceprovider: non-positive price %v for %s", price, symbol)
	}

	b.mu.Lock()
	b.cache[symbol] = cachedPrice{price: price, ts: time.Now()}
	b.mu.Unlock()
	return price, nil
}

// Stub is a constant-price provider for tests. Always returns the configured price.
type Stub struct {
	Price float64
	Err   error
}

func (s *Stub) Get(_ context.Context, _ string) (float64, error) {
	if s.Err != nil {
		return 0, s.Err
	}
	return s.Price, nil
}
