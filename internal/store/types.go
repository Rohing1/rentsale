package store

import (
	"errors"
	"time"
)

var ErrNotFound = errors.New("record not found")

type ValidationError struct {
	Message string
}

func (e ValidationError) Error() string { return e.Message }

type ConflictError struct {
	Message   string
	ProductID int64 `json:"product_id,omitempty"`
	Available int   `json:"available"`
}

func (e ConflictError) Error() string { return e.Message }

type Product struct {
	ID             int64     `json:"id"`
	SKU            string    `json:"sku"`
	Name           string    `json:"name"`
	ProductType    string    `json:"product_type"`
	Category       string    `json:"category"`
	Location       string    `json:"location"`
	Description    string    `json:"description"`
	RentEnabled    bool      `json:"rent_enabled"`
	SellEnabled    bool      `json:"sell_enabled"`
	DailyRentCents int64     `json:"daily_rent_cents"`
	SalePriceCents int64     `json:"sale_price_cents"`
	DepositCents   int64     `json:"deposit_cents"`
	StockOnHand    int       `json:"stock_on_hand"`
	Active         bool      `json:"active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type CreateProductInput struct {
	SKU            string `json:"sku"`
	Name           string `json:"name"`
	ProductType    string `json:"product_type"`
	Category       string `json:"category"`
	Location       string `json:"location"`
	Description    string `json:"description"`
	RentEnabled    bool   `json:"rent_enabled"`
	SellEnabled    bool   `json:"sell_enabled"`
	DailyRentCents int64  `json:"daily_rent_cents"`
	SalePriceCents int64  `json:"sale_price_cents"`
	DepositCents   int64  `json:"deposit_cents"`
	StockOnHand    int    `json:"stock_on_hand"`
}

type StockAdjustmentInput struct {
	Delta          int    `json:"delta"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"-"`
}

type Customer struct {
	ID          int64     `json:"id"`
	FullName    string    `json:"full_name"`
	Phone       string    `json:"phone"`
	Email       string    `json:"email"`
	IdentityRef string    `json:"identity_ref"`
	Note        string    `json:"note"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateCustomerInput struct {
	FullName    string `json:"full_name"`
	Phone       string `json:"phone"`
	Email       string `json:"email"`
	IdentityRef string `json:"identity_ref"`
	Note        string `json:"note"`
}

type CreateOrderItem struct {
	ProductID int64 `json:"product_id"`
	Quantity  int   `json:"quantity"`
}

type CreateOrderInput struct {
	OrderType      string            `json:"order_type"`
	CustomerID     int64             `json:"customer_id"`
	Items          []CreateOrderItem `json:"items"`
	StartDate      string            `json:"start_date,omitempty"`
	EndDate        string            `json:"end_date,omitempty"`
	Notes          string            `json:"notes"`
	IdempotencyKey string            `json:"-"`
}

type OrderLine struct {
	ID                   int64  `json:"id"`
	ProductID            int64  `json:"product_id"`
	SKU                  string `json:"sku"`
	ProductName          string `json:"product_name"`
	Quantity             int    `json:"quantity"`
	UnitSalePriceCents   int64  `json:"unit_sale_price_cents"`
	UnitDailyRentCents   int64  `json:"unit_daily_rent_cents"`
	UnitDepositCents     int64  `json:"unit_deposit_cents"`
	RentalSubtotalCents  int64  `json:"rental_subtotal_cents"`
	DepositSubtotalCents int64  `json:"deposit_subtotal_cents"`
	LineTotalCents       int64  `json:"line_total_cents"`
}

type Order struct {
	ID                int64       `json:"id"`
	OrderNo           string      `json:"order_no"`
	OrderType         string      `json:"order_type"`
	CustomerID        int64       `json:"customer_id"`
	CustomerName      string      `json:"customer_name"`
	CustomerPhone     string      `json:"customer_phone"`
	Status            string      `json:"status"`
	StartDate         string      `json:"start_date,omitempty"`
	EndDate           string      `json:"end_date,omitempty"`
	RentalDays        int         `json:"rental_days"`
	RentalTotalCents  int64       `json:"rental_total_cents"`
	DepositTotalCents int64       `json:"deposit_total_cents"`
	TotalCents        int64       `json:"total_cents"`
	PaidCents         int64       `json:"paid_cents"`
	PaymentStatus     string      `json:"payment_status"`
	Notes             string      `json:"notes"`
	Version           int         `json:"version"`
	ConfirmedAt       time.Time   `json:"confirmed_at"`
	CheckedOutAt      *time.Time  `json:"checked_out_at,omitempty"`
	ReturnedAt        *time.Time  `json:"returned_at,omitempty"`
	CancelledAt       *time.Time  `json:"cancelled_at,omitempty"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
	Items             []OrderLine `json:"items"`
}

type OrderFilter struct {
	OrderType string `json:"order_type"`
	Status    string `json:"status"`
	Limit     int    `json:"limit"`
}

type Availability struct {
	ProductID    int64  `json:"product_id"`
	StartDate    string `json:"start_date"`
	EndDate      string `json:"end_date"`
	StockOnHand  int    `json:"stock_on_hand"`
	ReservedPeak int    `json:"reserved_peak"`
	Available    int    `json:"available"`
}

type PaymentInput struct {
	Direction   string `json:"direction"`
	AmountCents int64  `json:"amount_cents"`
	Reference   string `json:"reference"`
	Note        string `json:"note"`
}

type Payment struct {
	ID          int64     `json:"id"`
	OrderID     int64     `json:"order_id"`
	Direction   string    `json:"direction"`
	AmountCents int64     `json:"amount_cents"`
	Reference   string    `json:"reference"`
	Note        string    `json:"note"`
	CreatedAt   time.Time `json:"created_at"`
}

type Dashboard struct {
	ActiveProducts    int   `json:"active_products"`
	AvailableNow      int   `json:"available_now"`
	CheckedOutRentals int   `json:"checked_out_rentals"`
	UpcomingReturns   int   `json:"upcoming_returns"`
	TodaySalesCents   int64 `json:"today_sales_cents"`
	LowStockProducts  int   `json:"low_stock_products"`
}

// BillingReport is a cash-flow statement built from the immutable payment
// ledger. Amounts include both rent/sale payments and deposits because the
// current payment ledger does not split those categories yet.
type BillingReport struct {
	Period           string         `json:"period"`
	AnchorDate       string         `json:"anchor_date"`
	PeriodStart      string         `json:"period_start"`
	PeriodEnd        string         `json:"period_end"`
	BusinessTimeZone string         `json:"business_time_zone"`
	ChargeCents      int64          `json:"charge_cents"`
	RefundCents      int64          `json:"refund_cents"`
	NetCents         int64          `json:"net_cents"`
	ChargeCount      int64          `json:"charge_count"`
	RefundCount      int64          `json:"refund_count"`
	TransactionCount int64          `json:"transaction_count"`
	OrderCount       int64          `json:"order_count"`
	Truncated        bool           `json:"truncated"`
	Items            []BillingEntry `json:"items"`
}

// BillingEntry is one payment or refund in a billing report.
type BillingEntry struct {
	ID           int64     `json:"id"`
	OrderID      int64     `json:"order_id"`
	OrderNo      string    `json:"order_no"`
	OrderType    string    `json:"order_type"`
	CustomerName string    `json:"customer_name"`
	Direction    string    `json:"direction"`
	AmountCents  int64     `json:"amount_cents"`
	Reference    string    `json:"reference"`
	Note         string    `json:"note"`
	CreatedAt    time.Time `json:"created_at"`
}
