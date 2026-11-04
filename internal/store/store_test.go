package store

import (
	"context"
	"testing"
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
