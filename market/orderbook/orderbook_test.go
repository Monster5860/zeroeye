package orderbook

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/tent-of-trials/market/types"
)

func TestSnapshotRecoverRoundTrip(t *testing.T) {
	config := Config{MaxDepth: 10, PriceDecimals: 8, VolumeDecimals: 8}
	book := NewOrderBook(types.Symbol("BTC-USD"), config)
	createdAt := time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)

	_, err := book.AddOrder(&types.Order{
		ID:           "buy-1",
		Symbol:       types.Symbol("BTC-USD"),
		Side:         types.Buy,
		Type:         types.Limit,
		Price:        decimal.NewFromInt(100),
		Quantity:     decimal.NewFromInt(2),
		RemainingQty: decimal.NewFromInt(2),
		CreatedAt:    createdAt,
	})
	if err != nil {
		t.Fatalf("AddOrder buy: %v", err)
	}
	_, err = book.AddOrder(&types.Order{
		ID:           "sell-1",
		Symbol:       types.Symbol("BTC-USD"),
		Side:         types.Sell,
		Type:         types.Limit,
		Price:        decimal.NewFromInt(101),
		Quantity:     decimal.NewFromInt(3),
		RemainingQty: decimal.NewFromInt(3),
		CreatedAt:    createdAt,
	})
	if err != nil {
		t.Fatalf("AddOrder sell: %v", err)
	}

	first, err := book.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	second, err := book.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot again: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("snapshot output should be deterministic")
	}

	recovered := NewOrderBook(types.Symbol("BTC-USD"), config)
	if err := recovered.Recover(first); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(recovered.GetBids()) != 1 {
		t.Fatalf("expected 1 bid, got %d", len(recovered.GetBids()))
	}
	if len(recovered.GetAsks()) != 1 {
		t.Fatalf("expected 1 ask, got %d", len(recovered.GetAsks()))
	}
	if recovered.orders["buy-1"] == nil {
		t.Fatal("expected buy order to be restored")
	}
	if recovered.orders["sell-1"] == nil {
		t.Fatal("expected sell order to be restored")
	}
}

func TestRecoverRejectsDifferentSymbol(t *testing.T) {
	config := Config{MaxDepth: 10, PriceDecimals: 8, VolumeDecimals: 8}
	source := NewOrderBook(types.Symbol("BTC-USD"), config)
	data, err := source.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	target := NewOrderBook(types.Symbol("ETH-USD"), config)
	if err := target.Recover(data); err != ErrSnapshotSymbolMismatch {
		t.Fatalf("expected ErrSnapshotSymbolMismatch, got %v", err)
	}
}
