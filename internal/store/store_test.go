package store

import (
	"context"
	"testing"
	"time"
)

func TestValidateProductInput(t *testing.T) {
	valid := CreateProductInput{
		SKU:            "CAM-001",
		Name:           "相机",
		RentEnabled:    true,
		SellEnabled:    true,
		DailyRentCents: 12000,
		SalePriceCents: 399900,
		StockOnHand:    3,
	}
	if err := validateProductInput(valid); err != nil {
		t.Fatalf("expected valid product, got %v", err)
	}

	invalid := valid
	invalid.RentEnabled = false
	invalid.SellEnabled = false
	if err := validateProductInput(invalid); err == nil {
		t.Fatal("expected disabled product to be rejected")
	}
}

func TestValidateOrderInput(t *testing.T) {
	valid := CreateOrderInput{
		OrderType:  "rental",
		CustomerID: 1,
		Items:      []CreateOrderItem{{ProductID: 1, Quantity: 2}},
		StartDate:  "2026-08-10",
		EndDate:    "2026-08-12",
	}
	if err := validateOrderInput(valid); err != nil {
		t.Fatalf("expected valid rental input, got %v", err)
	}

	invalid := valid
	invalid.Items = append(invalid.Items, CreateOrderItem{ProductID: 1, Quantity: 1})
	if err := validateOrderInput(invalid); err == nil {
		t.Fatal("expected duplicate product to be rejected")
	}
}

func TestMoneyAndStateTransitions(t *testing.T) {
	amount, err := multiplyMoney(12000, 2, 3)
	if err != nil || amount != 72000 {
		t.Fatalf("multiplyMoney() = %d, %v; want 72000, nil", amount, err)
	}
	if _, err := addMoney(int64(^uint64(0)>>1), 1); err == nil {
		t.Fatal("expected overflow to be rejected")
	}
	if !isAllowedTransition("rental", "confirmed", "checked_out") {
		t.Fatal("expected confirmed rental to allow check-out")
	}
	if isAllowedTransition("sale", "completed", "cancelled") {
		t.Fatal("completed sale must not be cancelled through the inventory-restoring path")
	}
}

func TestPaymentRequiresReferenceBeforeDatabaseAccess(t *testing.T) {
	repository := &Store{}
	_, err := repository.RecordPayment(context.Background(), 1, PaymentInput{Direction: "charge", AmountCents: 100})
	if err == nil {
		t.Fatal("expected payment without reference to be rejected")
	}
}

func TestBillingPeriodBounds(t *testing.T) {
	anchor := time.Date(2026, time.November, 4, 12, 0, 0, 0, time.FixedZone("test", 8*60*60))
	cases := []struct {
		period string
		start  string
		end    string
	}{
		{period: "day", start: "2026-11-04", end: "2026-11-05"},
		{period: "week", start: "2026-11-02", end: "2026-11-09"},
		{period: "month", start: "2026-11-01", end: "2026-12-01"},
		{period: "quarter", start: "2026-10-01", end: "2027-01-01"},
		{period: "year", start: "2026-01-01", end: "2027-01-01"},
	}
	for _, testCase := range cases {
		start, end, err := billingPeriodBounds(testCase.period, anchor)
		if err != nil {
			t.Fatalf("billingPeriodBounds(%q) error = %v", testCase.period, err)
		}
		if start.Format(dateLayout) != testCase.start || end.Format(dateLayout) != testCase.end {
			t.Fatalf("billingPeriodBounds(%q) = %s to %s, want %s to %s", testCase.period, start.Format(dateLayout), end.Format(dateLayout), testCase.start, testCase.end)
		}
	}
}

func TestBillingReportRejectsInvalidPeriodBeforeDatabaseAccess(t *testing.T) {
	repository := &Store{}
	_, err := repository.BillingReport(context.Background(), "fortnight", "2026-11-04")
	if err == nil {
		t.Fatal("expected invalid billing period to be rejected")
	}
}
