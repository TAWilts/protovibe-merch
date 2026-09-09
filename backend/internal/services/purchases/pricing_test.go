package purchases

import (
	"errors"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
)

func TestGrossFromEntered(t *testing.T) {
	if got := GrossFromEntered(10000, false, 1900); got != 11900 {
		t.Fatalf("100.00 net at 19%% = %d cents, want 11900", got)
	}
	if got := GrossFromEntered(11900, true, 1900); got != 11900 {
		t.Fatalf("gross input changed: %d", got)
	}
}

func TestNetFromGross(t *testing.T) {
	if got := NetFromGross(11900, 1900); got != 10000 {
		t.Fatalf("119.00 gross at 19%% = %d cents net, want 10000", got)
	}
}

func TestPrepareBasketCostsDistributesExactGrossTotalByQuantity(t *testing.T) {
	includeVAT := true
	total := int64(100)
	mode, costs, goodsTotal, err := prepareCosts([]Item{
		{Quantity: 2},
		{Quantity: 1},
	}, models.PurchasePriceBasket, &total, &includeVAT, nil)
	if err != nil {
		t.Fatalf("prepare basket costs: %v", err)
	}
	if mode != models.PurchasePriceBasket || goodsTotal != 100 {
		t.Fatalf("unexpected basket result: mode=%q total=%d", mode, goodsTotal)
	}
	if costs[0].LineCents != 67 || costs[1].LineCents != 33 {
		t.Fatalf("expected cent-exact 67/33 allocation, got %+v", costs)
	}
	if costs[0].UnitCents != 34 || costs[1].UnitCents != 33 {
		t.Fatalf("unexpected effective unit prices: %+v", costs)
	}
}

func TestPrepareBasketCostsConvertsNetTotalOnlyOnce(t *testing.T) {
	includeVAT := false
	rate := 1900
	total := int64(100)
	_, costs, goodsTotal, err := prepareCosts([]Item{
		{Quantity: 2},
		{Quantity: 1},
	}, models.PurchasePriceBasket, &total, &includeVAT, &rate)
	if err != nil {
		t.Fatalf("prepare basket costs: %v", err)
	}
	if goodsTotal != 119 || costs[0].LineCents+costs[1].LineCents != 119 {
		t.Fatalf("expected one 19%% conversion and exact allocation, total=%d costs=%+v", goodsTotal, costs)
	}
}

func TestPrepareCostsRejectsInvalidBasketInputs(t *testing.T) {
	includeVAT := true
	if _, _, _, err := prepareCosts([]Item{{Quantity: 1}}, models.PurchasePriceBasket, nil, &includeVAT, nil); !errors.Is(err, ErrBasketTotal) {
		t.Fatalf("missing basket total: got %v", err)
	}
	negative := int64(-1)
	if _, _, _, err := prepareCosts([]Item{{Quantity: 1}}, models.PurchasePriceBasket, &negative, &includeVAT, nil); !errors.Is(err, ErrBasketTotal) {
		t.Fatalf("negative basket total: got %v", err)
	}
	if _, _, _, err := prepareCosts([]Item{{Quantity: 1}}, "mystery", nil, &includeVAT, nil); !errors.Is(err, ErrInvalidPriceMode) {
		t.Fatalf("invalid mode: got %v", err)
	}
}
