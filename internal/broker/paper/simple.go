// Package paper holds simulated brokers used by Mode="paper" instances.
//
// SimpleBroker is the C1 implementation per docs/30- §3:
//   - immediate fill at the supplied currentPrice
//   - flat 0.1% fee (configurable)
//   - no slippage, no partial fills, no rejections except for obvious
//     guard violations (dust below MinOrderUSDT, malformed cmd)
//
// C2 (per-tick slippage) and C3 (orderbook simulation) are deferred to
// Phase 14+; the Broker interface is stable so upgrading a paper instance
// to a richer simulator is a config swap, not a strategy change.
package paper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Chuanyin1202/eighti-quant/internal/broker"
)

// Clock returns the wall-clock millisecond timestamp. Injected so tests can
// freeze time without touching the simulator's logic.
type Clock func() int64

// SimpleBroker is the C1 paper broker.
type SimpleBroker struct {
	feeRate      float64
	minOrderUSDT float64
	clock        Clock
}

// NewSimple constructs a SimpleBroker. feeRate defaults to 0.001 (0.1%) when
// 0; minOrderUSDT to 10.1 (Binance spot floor + safety margin).
func NewSimple(feeRate, minOrderUSDT float64, clock Clock) *SimpleBroker {
	if feeRate <= 0 {
		feeRate = 0.001
	}
	if minOrderUSDT <= 0 {
		minOrderUSDT = 10.1
	}
	if clock == nil {
		clock = defaultClock
	}
	return &SimpleBroker{feeRate: feeRate, minOrderUSDT: minOrderUSDT, clock: clock}
}

func (b *SimpleBroker) Kind() string { return "paper-simple" }

// PlaceOrder simulates immediate execution at currentPrice.
//
// Returns Execution.Status="rejected" with a populated RejectReason for
// these terminal-but-non-error cases:
//   - currentPrice ≤ 0 (broken price feed)
//   - cmd.AmountUSDT < minOrderUSDT for BUY
//   - cmd.QtyAsset × currentPrice < minOrderUSDT for SELL
//   - unsupported action
//
// Rejections are still nil-error; the caller distinguishes by Execution.Status.
// We reserve the error channel for transport-style failures (none in paper).
func (b *SimpleBroker) PlaceOrder(_ context.Context, cmd broker.TradeCommand,
	currentPrice float64) (broker.Execution, error) {

	now := b.clock()
	exec := broker.Execution{
		ClientOrderID: cmd.ClientOrderID,
		OrderID:       generateOrderID(),
		ExecutedAtMs:  now,
	}
	if cmd.ClientOrderID == "" {
		return exec, errors.New("paper: ClientOrderID required")
	}
	if currentPrice <= 0 {
		exec.Status = broker.StatusRejected
		exec.RejectReason = "invalid current_price"
		return exec, nil
	}

	switch cmd.Action {
	case broker.ActionBUY:
		amount := cmd.AmountUSDT
		if amount < b.minOrderUSDT {
			exec.Status = broker.StatusRejected
			exec.RejectReason = fmt.Sprintf("amount_usdt %.4f < min %.4f", amount, b.minOrderUSDT)
			return exec, nil
		}
		fee := amount * b.feeRate
		fillUSDT := amount - fee // USDT left for buying after fee
		fillQty := fillUSDT / currentPrice
		exec.Status = broker.StatusFilled
		exec.FilledQty = fillQty
		exec.FilledPriceUSDT = currentPrice
		exec.FeeAmount = fee
		exec.FeeAsset = "USDT"
		return exec, nil

	case broker.ActionSELL:
		qty := cmd.QtyAsset
		gross := qty * currentPrice
		if gross < b.minOrderUSDT {
			exec.Status = broker.StatusRejected
			exec.RejectReason = fmt.Sprintf("qty×price %.4f < min %.4f", gross, b.minOrderUSDT)
			return exec, nil
		}
		fee := gross * b.feeRate
		exec.Status = broker.StatusFilled
		exec.FilledQty = qty
		exec.FilledPriceUSDT = currentPrice
		exec.FeeAmount = fee
		exec.FeeAsset = "USDT"
		return exec, nil

	default:
		exec.Status = broker.StatusRejected
		exec.RejectReason = "unsupported action: " + cmd.Action
		return exec, nil
	}
}

// generateOrderID produces a 16-hex-character random ID. Cryptographic-grade
// is overkill for paper, but we're already importing "crypto/rand" elsewhere
// (no, wait — we're not). math/rand would race and bias under concurrent
// PlaceOrder calls; crypto/rand is safer with negligible cost (~µs each).
func generateOrderID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "PAPER-" + hex.EncodeToString(buf[:])
}

// defaultClock returns wall-clock UTC milliseconds. Imported here to avoid
// adding "time" to the call site of NewSimple — the test helper supplies
// its own clock so this is only invoked in production.
//
// Wrapped in a small helper for readability; the indirection costs nothing.
func defaultClock() int64 {
	return wallClockMs()
}
