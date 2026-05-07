package paper

import (
	"context"
	"strings"
	"testing"

	"github.com/Chuanyin1202/eighti-quant/internal/broker"
)

func fixedClock() int64 { return 1714521600000 }

func TestSimple_BUYFillsAndDeductsFee(t *testing.T) {
	b := NewSimple(0.001, 10.1, fixedClock)
	exec, err := b.PlaceOrder(context.Background(), broker.TradeCommand{
		ClientOrderID: "test-1",
		Action:        broker.ActionBUY,
		Symbol:        "BTCUSDT",
		AmountUSDT:    100,
	}, 50_000)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if exec.Status != broker.StatusFilled {
		t.Fatalf("status: %s (%s)", exec.Status, exec.RejectReason)
	}
	// 0.1% fee on 100 = 0.1; spendable 99.9; qty = 99.9 / 50000 = 0.001998
	wantQty := 99.9 / 50000.0
	if exec.FilledQty < wantQty-1e-9 || exec.FilledQty > wantQty+1e-9 {
		t.Fatalf("qty: want %v, got %v", wantQty, exec.FilledQty)
	}
	if exec.FilledPriceUSDT != 50_000 {
		t.Fatalf("filled price: %v", exec.FilledPriceUSDT)
	}
	if exec.FeeAmount != 0.1 || exec.FeeAsset != "USDT" {
		t.Fatalf("fee: %v %s", exec.FeeAmount, exec.FeeAsset)
	}
}

func TestSimple_SELLDeductsFeeFromGross(t *testing.T) {
	b := NewSimple(0.001, 10.1, fixedClock)
	exec, _ := b.PlaceOrder(context.Background(), broker.TradeCommand{
		ClientOrderID: "test-2",
		Action:        broker.ActionSELL,
		Symbol:        "BTCUSDT",
		QtyAsset:      0.001,
	}, 50_000)
	if exec.Status != broker.StatusFilled {
		t.Fatalf("status: %s", exec.Status)
	}
	// gross = 0.001 × 50000 = 50; fee = 0.05
	if exec.FeeAmount != 0.05 {
		t.Fatalf("fee: %v", exec.FeeAmount)
	}
}

func TestSimple_RejectsDustBUY(t *testing.T) {
	b := NewSimple(0.001, 10.1, fixedClock)
	exec, _ := b.PlaceOrder(context.Background(), broker.TradeCommand{
		ClientOrderID: "test-3",
		Action:        broker.ActionBUY,
		AmountUSDT:    5, // < 10.1
	}, 50_000)
	if exec.Status != broker.StatusRejected {
		t.Fatalf("expected rejected, got %s", exec.Status)
	}
	if !strings.Contains(exec.RejectReason, "amount_usdt") {
		t.Fatalf("reason: %s", exec.RejectReason)
	}
}

func TestSimple_RejectsDustSELL(t *testing.T) {
	b := NewSimple(0.001, 10.1, fixedClock)
	exec, _ := b.PlaceOrder(context.Background(), broker.TradeCommand{
		ClientOrderID: "test-4",
		Action:        broker.ActionSELL,
		QtyAsset:      0.0001, // 0.0001 × 50000 = 5 < 10.1
	}, 50_000)
	if exec.Status != broker.StatusRejected {
		t.Fatalf("expected rejected, got %s", exec.Status)
	}
}

func TestSimple_RejectsBadPrice(t *testing.T) {
	b := NewSimple(0.001, 10.1, fixedClock)
	exec, _ := b.PlaceOrder(context.Background(), broker.TradeCommand{
		ClientOrderID: "test-5",
		Action:        broker.ActionBUY,
		AmountUSDT:    100,
	}, 0)
	if exec.Status != broker.StatusRejected {
		t.Fatalf("expected rejected on price=0, got %s", exec.Status)
	}
}

func TestSimple_RequiresClientOrderID(t *testing.T) {
	b := NewSimple(0.001, 10.1, fixedClock)
	_, err := b.PlaceOrder(context.Background(), broker.TradeCommand{
		Action:     broker.ActionBUY,
		AmountUSDT: 100,
	}, 50_000)
	if err == nil {
		t.Fatalf("expected error for missing client_order_id")
	}
}

func TestSimple_OrderIDIsNonEmpty(t *testing.T) {
	b := NewSimple(0.001, 10.1, fixedClock)
	exec, _ := b.PlaceOrder(context.Background(), broker.TradeCommand{
		ClientOrderID: "x",
		Action:        broker.ActionBUY,
		AmountUSDT:    100,
	}, 50_000)
	if !strings.HasPrefix(exec.OrderID, "PAPER-") {
		t.Fatalf("OrderID: %s", exec.OrderID)
	}
}

func TestSimple_KindIsStable(t *testing.T) {
	if got := (&SimpleBroker{}).Kind(); got != "paper-simple" {
		t.Fatalf("Kind: %s", got)
	}
}
