package sales_test

import (
	"errors"
	"testing"

	"github.com/tawilts/protovibe-merch/backend/internal/models"
	"github.com/tawilts/protovibe-merch/backend/internal/services/sales"
)

// catalogue is a stand-in price list for the pure rule tests.
func priceList() map[int64]sales.VariantPrice {
	return map[int64]sales.VariantPrice{
		1: {SalePriceCents: 1800, IsOffered: true},
		2: {SalePriceCents: 1200, IsOffered: true},
		3: {SalePriceCents: 2500, IsOffered: false},
	}
}

func counterSale(items ...sales.BasketItem) sales.Request {
	return sales.Request{
		Items:         items,
		PaymentMethod: models.PaymentMethodCash,
		IsPaid:        true,
		IsReceived:    true,
	}
}

func TestPrepareSimpleCounterSale(t *testing.T) {
	got, err := sales.Prepare(counterSale(sales.BasketItem{VariantID: 1, Quantity: 2}), priceList())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if got.TotalDueCents != 3600 {
		t.Fatalf("expected 3600 cents due, got %d", got.TotalDueCents)
	}
	if got.DeliveryStatus != models.DeliveryNotApplicable {
		t.Fatalf("a counter sale has no delivery workflow, got %q", got.DeliveryStatus)
	}
	if got.PaymentFollowUp {
		t.Fatal("a paid sale needs no payment follow-up")
	}
	if got.DonationCents != 0 {
		t.Fatalf("no donation expected, got %d", got.DonationCents)
	}
	if got.Lines[0].AmountGivenCents == nil || *got.Lines[0].AmountGivenCents != 3600 {
		t.Fatalf("the paid amount must be recorded: %+v", got.Lines[0])
	}
}

// TestDonationIsSplitAcrossTheBasket is the property that lets a single
// position be cancelled later without distorting the rest of the receipt.
func TestDonationIsSplitAcrossTheBasket(t *testing.T) {
	given := int64(3500)
	req := counterSale(
		sales.BasketItem{VariantID: 1, Quantity: 1}, // 1800
		sales.BasketItem{VariantID: 2, Quantity: 1}, // 1200
	)
	req.AmountGivenCents = &given

	got, err := sales.Prepare(req, priceList())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if got.TotalDueCents != 3000 || got.DonationCents != 500 {
		t.Fatalf("expected 3000 due and 500 donated, got %d and %d", got.TotalDueCents, got.DonationCents)
	}

	var donation, givenSum int64
	for _, line := range got.Lines {
		donation += line.DonationCents
		if line.AmountGivenCents == nil {
			t.Fatalf("a paid line must record what was given: %+v", line)
		}
		givenSum += *line.AmountGivenCents
	}
	if donation != 500 {
		t.Fatalf("the split donation must add back up to 500, got %d", donation)
	}
	if givenSum != given {
		t.Fatalf("the lines must add back up to what the customer paid, got %d", givenSum)
	}
}

// TestUnpaidSaleRecordsNoMoney pins that a stale cash field on the client
// cannot turn an unpaid booking into a donation.
func TestUnpaidSaleRecordsNoMoney(t *testing.T) {
	given := int64(5000)
	req := counterSale(sales.BasketItem{VariantID: 1, Quantity: 1})
	req.IsPaid = false
	req.AmountGivenCents = &given
	req.CustomerName = "Alex Muster"
	req.CustomerAddress = "Musterweg 1, 12345 Musterstadt"

	got, err := sales.Prepare(req, priceList())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if got.DonationCents != 0 {
		t.Fatalf("an unpaid sale cannot donate, got %d", got.DonationCents)
	}
	if got.Lines[0].AmountGivenCents != nil {
		t.Fatalf("an unpaid line must record no amount given, got %v", *got.Lines[0].AmountGivenCents)
	}
	if !got.PaymentFollowUp {
		t.Fatal("an unpaid sale must enter the payment follow-up queue")
	}
}

// TestCodePaymentsIgnoreTheCashField pins the same protection for PayPal and
// bank transfer, where the exact amount is settled by the code itself.
func TestCodePaymentsIgnoreTheCashField(t *testing.T) {
	for _, method := range []string{models.PaymentMethodPayPal, models.PaymentMethodTransfer} {
		t.Run(method, func(t *testing.T) {
			given := int64(9900)
			req := counterSale(sales.BasketItem{VariantID: 1, Quantity: 1})
			req.PaymentMethod = method
			req.AmountGivenCents = &given

			got, err := sales.Prepare(req, priceList())
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			if got.DonationCents != 0 {
				t.Fatalf("a stale cash field must not become a donation, got %d", got.DonationCents)
			}
			if *got.Lines[0].AmountGivenCents != 1800 {
				t.Fatalf("the exact amount must be recorded, got %d", *got.Lines[0].AmountGivenCents)
			}
		})
	}
}

// TestContactRequiredForOpenObligations pins that the band always knows who to
// chase for money or a parcel.
func TestContactRequiredForOpenObligations(t *testing.T) {
	cases := []struct {
		name       string
		isPaid     bool
		isReceived bool
		wantErr    bool
	}{
		{"counter sale", true, true, false},
		{"unpaid", false, true, true},
		{"not handed over", true, false, true},
		{"neither", false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := counterSale(sales.BasketItem{VariantID: 1, Quantity: 1})
			req.IsPaid, req.IsReceived = tc.isPaid, tc.isReceived

			_, err := sales.Prepare(req, priceList())
			if tc.wantErr && !errors.Is(err, sales.ErrContactRequired) {
				t.Fatalf("expected contact details to be required, got %v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestNotReceivedStartsTheShippingQueue pins the delivery workflow entry point.
func TestNotReceivedStartsTheShippingQueue(t *testing.T) {
	req := counterSale(sales.BasketItem{VariantID: 1, Quantity: 1})
	req.IsReceived = false
	req.CustomerName = "Alex Muster"
	req.CustomerAddress = "Musterweg 1"

	got, err := sales.Prepare(req, priceList())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if got.DeliveryStatus != models.DeliveryPending {
		t.Fatalf("expected the shipping queue, got %q", got.DeliveryStatus)
	}
}

// TestStockNeverBlocksASale is the deliberate business rule: the till must
// take money even when the recorded stock says there is nothing left.
func TestStockNeverBlocksASale(t *testing.T) {
	// Prepare has no stock input at all, which is the structural guarantee.
	// A huge quantity against an empty catalogue position must still book.
	got, err := sales.Prepare(counterSale(sales.BasketItem{VariantID: 1, Quantity: 9999}), priceList())
	if err != nil {
		t.Fatalf("a sale must never be blocked by stock: %v", err)
	}
	if got.TotalDueCents != 9999*1800 {
		t.Fatalf("unexpected total %d", got.TotalDueCents)
	}
}

func TestPerItemPriceOverride(t *testing.T) {
	discounted := int64(1000)
	req := counterSale(sales.BasketItem{VariantID: 1, Quantity: 2, UnitPriceCents: &discounted})

	got, err := sales.Prepare(req, priceList())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if got.TotalDueCents != 2000 {
		t.Fatalf("the override must apply, got %d", got.TotalDueCents)
	}

	free := int64(0)
	req = counterSale(sales.BasketItem{VariantID: 1, Quantity: 1, UnitPriceCents: &free})
	if _, err := sales.Prepare(req, priceList()); err != nil {
		t.Fatalf("a giveaway at zero must be allowed: %v", err)
	}

	negative := int64(-1)
	req = counterSale(sales.BasketItem{VariantID: 1, Quantity: 1, UnitPriceCents: &negative})
	if _, err := sales.Prepare(req, priceList()); !errors.Is(err, sales.ErrNegativePrice) {
		t.Fatalf("a negative price must be rejected, got %v", err)
	}
}

// TestFreeBasketDonation covers the edge case where every position is free:
// the donation has no proportional denominator and goes to the first line.
func TestFreeBasketDonation(t *testing.T) {
	free := int64(0)
	given := int64(1000)
	req := counterSale(
		sales.BasketItem{VariantID: 1, Quantity: 1, UnitPriceCents: &free},
		sales.BasketItem{VariantID: 2, Quantity: 1, UnitPriceCents: &free},
	)
	req.AmountGivenCents = &given

	got, err := sales.Prepare(req, priceList())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if got.DonationCents != 1000 {
		t.Fatalf("expected a 1000 cent donation, got %d", got.DonationCents)
	}
	if got.Lines[0].DonationCents != 1000 || got.Lines[1].DonationCents != 0 {
		t.Fatalf("a free basket donates to the first line: %+v", got.Lines)
	}
}

func TestPrepareRejectsBadInput(t *testing.T) {
	tooLittle := int64(100)

	cases := map[string]struct {
		mutate  func(*sales.Request)
		wantErr error
	}{
		"empty basket":      {func(r *sales.Request) { r.Items = nil }, sales.ErrEmptyBasket},
		"zero quantity":     {func(r *sales.Request) { r.Items[0].Quantity = 0 }, sales.ErrInvalidQuantity},
		"negative quantity": {func(r *sales.Request) { r.Items[0].Quantity = -1 }, sales.ErrInvalidQuantity},
		"unknown method":    {func(r *sales.Request) { r.PaymentMethod = "Bitcoin" }, sales.ErrUnknownPayment},
		"underpaid":         {func(r *sales.Request) { r.AmountGivenCents = &tooLittle }, sales.ErrAmountTooLow},
		"not offered":       {func(r *sales.Request) { r.Items[0].VariantID = 3 }, sales.ErrVariantNotOffered},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := counterSale(sales.BasketItem{VariantID: 1, Quantity: 1})
			tc.mutate(&req)
			if _, err := sales.Prepare(req, priceList()); !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestUnknownVariantIsRejected(t *testing.T) {
	req := counterSale(sales.BasketItem{VariantID: 999, Quantity: 1})
	if _, err := sales.Prepare(req, priceList()); !errors.Is(err, sales.ErrUnknownVariant) {
		t.Fatalf("an unknown variant must be rejected as a client error, got %v", err)
	}
}
