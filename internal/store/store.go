package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const dateLayout = "2006-01-02"
const maxInventoryQuantity int64 = 2_147_483_647

const productColumns = `
id, sku, name, product_type, category, location, description,
rent_enabled, sell_enabled, daily_rent_cents, sale_price_cents,
deposit_cents, stock_on_hand, active, created_at, updated_at`

type rowScanner interface {
	Scan(...any) error
}

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func rollbackQuietly(tx pgx.Tx) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(cleanupCtx)
}

func (s *Store) CreateProduct(ctx context.Context, input CreateProductInput) (Product, error) {
	input.SKU = strings.TrimSpace(input.SKU)
	input.Name = strings.TrimSpace(input.Name)
	input.ProductType = strings.TrimSpace(input.ProductType)
	input.Category = strings.TrimSpace(input.Category)
	input.Location = strings.TrimSpace(input.Location)
	input.Description = strings.TrimSpace(input.Description)
	if input.ProductType == "" {
		input.ProductType = "general"
	}
	if err := validateProductInput(input); err != nil {
		return Product{}, err
	}

	query := `INSERT INTO products (
		sku, name, product_type, category, location, description,
		rent_enabled, sell_enabled, daily_rent_cents, sale_price_cents,
		deposit_cents, stock_on_hand
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	RETURNING ` + productColumns
	product, err := scanProduct(s.pool.QueryRow(ctx, query,
		input.SKU, input.Name, input.ProductType, input.Category, input.Location, input.Description,
		input.RentEnabled, input.SellEnabled, input.DailyRentCents, input.SalePriceCents,
		input.DepositCents, input.StockOnHand,
	))
	if err != nil {
		return Product{}, mapDatabaseError(err)
	}
	return product, nil
}

func (s *Store) ListProducts(ctx context.Context, keyword string, activeOnly bool) ([]Product, error) {
	keyword = strings.TrimSpace(keyword)
	rows, err := s.pool.Query(ctx, `SELECT `+productColumns+`
		FROM products
		WHERE ($1 = '' OR sku ILIKE '%' || $1 || '%' OR name ILIKE '%' || $1 || '%' OR category ILIKE '%' || $1 || '%')
		  AND (NOT $2 OR active = TRUE)
		ORDER BY active DESC, name ASC, id DESC`, keyword, activeOnly)
	if err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	defer rows.Close()

	products := make([]Product, 0)
	for rows.Next() {
		product, err := scanProduct(rows)
		if err != nil {
			return nil, fmt.Errorf("scan product: %w", err)
		}
		products = append(products, product)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate products: %w", err)
	}
	return products, nil
}

func (s *Store) GetProduct(ctx context.Context, id int64) (Product, error) {
	product, err := scanProduct(s.pool.QueryRow(ctx, `SELECT `+productColumns+` FROM products WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, ErrNotFound
	}
	if err != nil {
		return Product{}, fmt.Errorf("get product: %w", err)
	}
	return product, nil
}

func (s *Store) AdjustStock(ctx context.Context, productID int64, input StockAdjustmentInput) (Product, error) {
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.Delta == 0 {
		return Product{}, ValidationError{Message: "库存调整数量不能为 0"}
	}
	if int64(input.Delta) > maxInventoryQuantity || int64(input.Delta) < -maxInventoryQuantity {
		return Product{}, ValidationError{Message: "库存调整数量超出系统支持范围"}
	}
	if len(strings.TrimSpace(input.Reason)) > 500 {
		return Product{}, ValidationError{Message: "库存调整原因不能超过 500 个字符"}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Product{}, fmt.Errorf("start inventory adjustment: %w", err)
	}
	defer rollbackQuietly(tx)
	replayID, replay, err := claimIdempotency(ctx, tx, "stock.adjust", input.IdempotencyKey)
	if err != nil {
		return Product{}, err
	}
	if replay {
		if err := tx.Commit(ctx); err != nil {
			return Product{}, fmt.Errorf("commit idempotent stock replay: %w", err)
		}
		return s.GetProduct(ctx, replayID)
	}

	product, err := loadProductForUpdate(ctx, tx, productID)
	if err != nil {
		return Product{}, err
	}
	newStock64 := int64(product.StockOnHand) + int64(input.Delta)
	if newStock64 < 0 {
		return Product{}, ValidationError{Message: "调整后库存不能小于 0"}
	}
	if newStock64 > maxInventoryQuantity {
		return Product{}, ValidationError{Message: "调整后库存超出系统支持范围"}
	}
	newStock := int(newStock64)
	peak, err := rentalPeakFromToday(ctx, tx, product.ID)
	if err != nil {
		return Product{}, err
	}
	if newStock < peak {
		return Product{}, ConflictError{
			Message:   "调整后库存会破坏已确认租赁的库存承诺",
			ProductID: product.ID,
			Available: product.StockOnHand - peak,
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE products SET stock_on_hand = $1, updated_at = NOW() WHERE id = $2`, newStock, product.ID); err != nil {
		return Product{}, fmt.Errorf("update stock: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO inventory_movements(product_id, movement_type, quantity_delta, reason) VALUES ($1, 'manual_adjustment', $2, $3)`, product.ID, input.Delta, strings.TrimSpace(input.Reason)); err != nil {
		return Product{}, fmt.Errorf("write inventory movement: %w", err)
	}
	if err := completeIdempotency(ctx, tx, "stock.adjust", input.IdempotencyKey, product.ID); err != nil {
		return Product{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Product{}, fmt.Errorf("commit inventory adjustment: %w", err)
	}
	return s.GetProduct(ctx, product.ID)
}

func (s *Store) CreateCustomer(ctx context.Context, input CreateCustomerInput) (Customer, error) {
	input.FullName = strings.TrimSpace(input.FullName)
	input.Phone = strings.TrimSpace(input.Phone)
	input.Email = strings.TrimSpace(input.Email)
	input.IdentityRef = strings.TrimSpace(input.IdentityRef)
	input.Note = strings.TrimSpace(input.Note)
	if input.FullName == "" {
		return Customer{}, ValidationError{Message: "客户姓名不能为空"}
	}
	if input.Phone == "" {
		return Customer{}, ValidationError{Message: "客户手机号不能为空"}
	}
	if runeCount(input.FullName) > 120 || runeCount(input.Phone) > 40 || runeCount(input.Email) > 180 || runeCount(input.IdentityRef) > 120 || runeCount(input.Note) > 2000 {
		return Customer{}, ValidationError{Message: "客户信息长度超出限制"}
	}
	customer, err := scanCustomer(s.pool.QueryRow(ctx, `INSERT INTO customers(full_name, phone, email, identity_ref, note)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, full_name, phone, email, identity_ref, note, created_at, updated_at`,
		input.FullName, input.Phone, input.Email, input.IdentityRef, input.Note))
	if err != nil {
		return Customer{}, mapDatabaseError(err)
	}
	return customer, nil
}

func (s *Store) ListCustomers(ctx context.Context, keyword string) ([]Customer, error) {
	keyword = strings.TrimSpace(keyword)
	rows, err := s.pool.Query(ctx, `SELECT id, full_name, phone, email, identity_ref, note, created_at, updated_at
		FROM customers
		WHERE ($1 = '' OR full_name ILIKE '%' || $1 || '%' OR phone ILIKE '%' || $1 || '%' OR email ILIKE '%' || $1 || '%')
		ORDER BY created_at DESC`, keyword)
	if err != nil {
		return nil, fmt.Errorf("list customers: %w", err)
	}
	defer rows.Close()
	customers := make([]Customer, 0)
	for rows.Next() {
		customer, err := scanCustomer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan customer: %w", err)
		}
		customers = append(customers, customer)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate customers: %w", err)
	}
	return customers, nil
}

func (s *Store) CreateOrder(ctx context.Context, input CreateOrderInput) (Order, error) {
	input.OrderType = strings.ToLower(strings.TrimSpace(input.OrderType))
	input.Notes = strings.TrimSpace(input.Notes)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if err := validateOrderInput(input); err != nil {
		return Order{}, err
	}

	var startDate, endDate time.Time
	var rentalDays int
	if input.OrderType == "rental" {
		var err error
		startDate, err = parseDate(input.StartDate)
		if err != nil {
			return Order{}, ValidationError{Message: "起租日期必须为 YYYY-MM-DD"}
		}
		endDate, err = parseDate(input.EndDate)
		if err != nil {
			return Order{}, ValidationError{Message: "归还日期必须为 YYYY-MM-DD"}
		}
		rentalDays = int(endDate.Sub(startDate).Hours() / 24)
		if rentalDays <= 0 {
			return Order{}, ValidationError{Message: "归还日期必须晚于起租日期"}
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Order{}, fmt.Errorf("start order: %w", err)
	}
	defer rollbackQuietly(tx)
	replayID, replay, err := claimIdempotency(ctx, tx, "order.create", input.IdempotencyKey)
	if err != nil {
		return Order{}, err
	}
	if replay {
		if err := tx.Commit(ctx); err != nil {
			return Order{}, fmt.Errorf("commit idempotent order replay: %w", err)
		}
		return s.GetOrder(ctx, replayID)
	}

	var customerID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM customers WHERE id = $1`, input.CustomerID).Scan(&customerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Order{}, ValidationError{Message: "客户不存在"}
		}
		return Order{}, fmt.Errorf("load customer: %w", err)
	}
	if input.OrderType == "rental" {
		var today string
		if err := tx.QueryRow(ctx, `SELECT CURRENT_DATE::text`).Scan(&today); err != nil {
			return Order{}, fmt.Errorf("get business date: %w", err)
		}
		if startDate.Format(dateLayout) < today {
			return Order{}, ValidationError{Message: "起租日期不能早于当前业务日期"}
		}
	}

	items := append([]CreateOrderItem(nil), input.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].ProductID < items[j].ProductID })
	lines := make([]pricedLine, 0, len(items))
	var rentalTotal, depositTotal, total int64
	for _, item := range items {
		product, err := loadProductForUpdate(ctx, tx, item.ProductID)
		if err != nil {
			return Order{}, err
		}
		if !product.Active {
			return Order{}, ValidationError{Message: fmt.Sprintf("商品 %s 已下架", product.Name)}
		}
		if input.OrderType == "rental" && !product.RentEnabled {
			return Order{}, ValidationError{Message: fmt.Sprintf("商品 %s 不支持租赁", product.Name)}
		}
		if input.OrderType == "sale" && !product.SellEnabled {
			return Order{}, ValidationError{Message: fmt.Sprintf("商品 %s 不支持售卖", product.Name)}
		}

		line := pricedLine{product: product, quantity: item.Quantity}
		if input.OrderType == "rental" {
			peak, err := rentalPeakForPeriod(ctx, tx, product.ID, startDate.Format(dateLayout), endDate.Format(dateLayout))
			if err != nil {
				return Order{}, err
			}
			available := product.StockOnHand - peak
			if available < item.Quantity {
				return Order{}, ConflictError{Message: fmt.Sprintf("商品 %s 在所选租期库存不足", product.Name), ProductID: product.ID, Available: maxInt(available, 0)}
			}
			line.rentalSubtotal, err = multiplyMoney(product.DailyRentCents, int64(item.Quantity), int64(rentalDays))
			if err != nil {
				return Order{}, err
			}
			line.depositSubtotal, err = multiplyMoney(product.DepositCents, int64(item.Quantity))
			if err != nil {
				return Order{}, err
			}
			line.total, err = addMoney(line.rentalSubtotal, line.depositSubtotal)
			if err != nil {
				return Order{}, err
			}
			rentalTotal, err = addMoney(rentalTotal, line.rentalSubtotal)
			if err != nil {
				return Order{}, err
			}
			depositTotal, err = addMoney(depositTotal, line.depositSubtotal)
			if err != nil {
				return Order{}, err
			}
		} else {
			peak, err := rentalPeakFromToday(ctx, tx, product.ID)
			if err != nil {
				return Order{}, err
			}
			if product.StockOnHand-item.Quantity < peak {
				return Order{}, ConflictError{Message: fmt.Sprintf("商品 %s 库存不足，需为已确认租赁保留库存", product.Name), ProductID: product.ID, Available: maxInt(product.StockOnHand-peak, 0)}
			}
			line.saleSubtotal, err = multiplyMoney(product.SalePriceCents, int64(item.Quantity))
			if err != nil {
				return Order{}, err
			}
			line.total = line.saleSubtotal
		}
		total, err = addMoney(total, line.total)
		if err != nil {
			return Order{}, err
		}
		lines = append(lines, line)
	}

	var startParam, endParam any
	if input.OrderType == "rental" {
		startParam = startDate.Format(dateLayout)
		endParam = endDate.Format(dateLayout)
	}
	orderID, err := insertOrder(ctx, tx, input, startParam, endParam, rentalDays, rentalTotal, depositTotal, total)
	if err != nil {
		return Order{}, err
	}
	for _, line := range lines {
		if _, err := tx.Exec(ctx, `INSERT INTO order_lines(
			order_id, product_id, sku_snapshot, product_name_snapshot, quantity,
			unit_sale_price_cents, unit_daily_rent_cents, unit_deposit_cents,
			rental_subtotal_cents, deposit_subtotal_cents, line_total_cents
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			orderID, line.product.ID, line.product.SKU, line.product.Name, line.quantity,
			line.product.SalePriceCents, line.product.DailyRentCents, line.product.DepositCents,
			line.rentalSubtotal, line.depositSubtotal, line.total,
		); err != nil {
			return Order{}, fmt.Errorf("insert order line: %w", err)
		}

		if input.OrderType == "rental" {
			if _, err := tx.Exec(ctx, `INSERT INTO inventory_commitments(product_id, order_id, commitment_type, state, period, quantity)
				VALUES ($1, $2, 'rental', 'confirmed', daterange($3::date, $4::date, '[)'), $5)`,
				line.product.ID, orderID, startDate.Format(dateLayout), endDate.Format(dateLayout), line.quantity); err != nil {
				return Order{}, fmt.Errorf("create rental commitment: %w", err)
			}
		} else {
			result, err := tx.Exec(ctx, `UPDATE products
				SET stock_on_hand = stock_on_hand - $1, updated_at = NOW()
				WHERE id = $2 AND stock_on_hand >= $1`, line.quantity, line.product.ID)
			if err != nil {
				return Order{}, fmt.Errorf("deduct sale inventory: %w", err)
			}
			if result.RowsAffected() != 1 {
				return Order{}, ConflictError{Message: fmt.Sprintf("商品 %s 库存不足", line.product.Name), ProductID: line.product.ID, Available: 0}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO inventory_commitments(product_id, order_id, commitment_type, state, period, quantity)
				VALUES ($1, $2, 'sale', 'consumed', daterange(CURRENT_DATE, NULL, '[)'), $3)`, line.product.ID, orderID, line.quantity); err != nil {
				return Order{}, fmt.Errorf("record sale commitment: %w", err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO inventory_movements(product_id, order_id, movement_type, quantity_delta, reason)
				VALUES ($1, $2, 'sale', $3, '销售订单确认')`, line.product.ID, orderID, -line.quantity); err != nil {
				return Order{}, fmt.Errorf("record sale inventory movement: %w", err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO order_status_events(order_id, to_status, note) VALUES ($1, 'confirmed', '订单创建并确认库存')`, orderID); err != nil {
		return Order{}, fmt.Errorf("record order event: %w", err)
	}
	if err := completeIdempotency(ctx, tx, "order.create", input.IdempotencyKey, orderID); err != nil {
		return Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, fmt.Errorf("commit order: %w", err)
	}
	return s.GetOrder(ctx, orderID)
}

func (s *Store) GetAvailability(ctx context.Context, productID int64, startDateRaw, endDateRaw string) (Availability, error) {
	startDate, err := parseDate(startDateRaw)
	if err != nil {
		return Availability{}, ValidationError{Message: "起租日期必须为 YYYY-MM-DD"}
	}
	endDate, err := parseDate(endDateRaw)
	if err != nil {
		return Availability{}, ValidationError{Message: "归还日期必须为 YYYY-MM-DD"}
	}
	if !endDate.After(startDate) {
		return Availability{}, ValidationError{Message: "归还日期必须晚于起租日期"}
	}
	product, err := s.GetProduct(ctx, productID)
	if err != nil {
		return Availability{}, err
	}
	peak, err := rentalPeakForPeriod(ctx, s.pool, productID, startDate.Format(dateLayout), endDate.Format(dateLayout))
	if err != nil {
		return Availability{}, err
	}
	return Availability{
		ProductID:    productID,
		StartDate:    startDate.Format(dateLayout),
		EndDate:      endDate.Format(dateLayout),
		StockOnHand:  product.StockOnHand,
		ReservedPeak: peak,
		Available:    maxInt(product.StockOnHand-peak, 0),
	}, nil
}

func (s *Store) GetOrder(ctx context.Context, id int64) (Order, error) {
	order, err := scanOrder(s.pool.QueryRow(ctx, orderSelectQuery+` WHERE o.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("get order: %w", err)
	}
	items, err := s.listOrderLines(ctx, id)
	if err != nil {
		return Order{}, err
	}
	order.Items = items
	return order, nil
}

func (s *Store) ListOrders(ctx context.Context, filter OrderFilter) ([]Order, error) {
	filter.OrderType = strings.ToLower(strings.TrimSpace(filter.OrderType))
	filter.Status = strings.ToLower(strings.TrimSpace(filter.Status))
	if filter.OrderType != "" && filter.OrderType != "rental" && filter.OrderType != "sale" {
		return nil, ValidationError{Message: "order_type 只能为 rental 或 sale"}
	}
	if filter.Status != "" && !isOrderStatus(filter.Status) {
		return nil, ValidationError{Message: "status 参数不合法"}
	}
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 50
	}
	query := orderSelectQuery + ` WHERE 1 = 1`
	args := make([]any, 0, 3)
	if filter.OrderType != "" {
		args = append(args, filter.OrderType)
		query += fmt.Sprintf(" AND o.order_type = $%d", len(args))
	}
	if filter.Status != "" {
		args = append(args, filter.Status)
		query += fmt.Sprintf(" AND o.status = $%d", len(args))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY o.created_at DESC, o.id DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()
	orders := make([]Order, 0)
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("scan order: %w", err)
		}
		items, err := s.listOrderLines(ctx, order.ID)
		if err != nil {
			return nil, err
		}
		order.Items = items
		orders = append(orders, order)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate orders: %w", err)
	}
	return orders, nil
}

func (s *Store) TransitionOrder(ctx context.Context, orderID int64, targetStatus, note string) (Order, error) {
	targetStatus = strings.ToLower(strings.TrimSpace(targetStatus))
	note = strings.TrimSpace(note)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Order{}, fmt.Errorf("start order transition: %w", err)
	}
	defer rollbackQuietly(tx)

	var orderType, currentStatus, startDate, endDate string
	var paidCents int64
	if err := tx.QueryRow(ctx, `SELECT order_type, status, paid_cents, COALESCE(start_date::text, ''), COALESCE(end_date::text, '') FROM orders WHERE id = $1 FOR UPDATE`, orderID).Scan(&orderType, &currentStatus, &paidCents, &startDate, &endDate); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Order{}, ErrNotFound
		}
		return Order{}, fmt.Errorf("lock order: %w", err)
	}
	// A client may lose the HTTP response after the status transaction commits.
	// Treat a retry of an already-completed transition as success instead of
	// asking an operator to guess whether the first request took effect.
	if currentStatus == targetStatus && targetStatus != "confirmed" {
		if err := tx.Commit(ctx); err != nil {
			return Order{}, fmt.Errorf("commit idempotent order transition replay: %w", err)
		}
		return s.GetOrder(ctx, orderID)
	}
	if !isAllowedTransition(orderType, currentStatus, targetStatus) {
		return Order{}, ValidationError{Message: fmt.Sprintf("订单状态不能从 %s 变更为 %s", currentStatus, targetStatus)}
	}
	if targetStatus == "cancelled" && paidCents > 0 {
		return Order{}, ValidationError{Message: "订单已有收款，请先登记退款后再取消"}
	}

	lines, err := loadOrderProductQuantities(ctx, tx, orderID)
	if err != nil {
		return Order{}, err
	}
	if orderType == "rental" && targetStatus == "checked_out" {
		var today string
		if err := tx.QueryRow(ctx, `SELECT CURRENT_DATE::text`).Scan(&today); err != nil {
			return Order{}, fmt.Errorf("get business date: %w", err)
		}
		if today < startDate {
			return Order{}, ValidationError{Message: "尚未到起租日期，不能提前取货"}
		}
		if today >= endDate {
			return Order{}, ValidationError{Message: "该租赁订单已过期，请先走续租或改期流程"}
		}
		for _, line := range lines {
			product, err := loadProductForUpdate(ctx, tx, line.ProductID)
			if err != nil {
				return Order{}, err
			}
			peak, err := rentalPeakForPeriod(ctx, tx, product.ID, startDate, endDate)
			if err != nil {
				return Order{}, err
			}
			if product.StockOnHand < peak {
				return Order{}, ConflictError{Message: fmt.Sprintf("商品 %s 的已确认租赁库存已失效", product.Name), ProductID: product.ID, Available: maxInt(product.StockOnHand-peak+line.Quantity, 0)}
			}
		}
	}

	if orderType == "sale" && targetStatus == "cancelled" {
		for _, line := range lines {
			product, err := loadProductForUpdate(ctx, tx, line.ProductID)
			if err != nil {
				return Order{}, err
			}
			if int64(product.StockOnHand)+int64(line.Quantity) > maxInventoryQuantity {
				return Order{}, ValidationError{Message: fmt.Sprintf("取消销售订单后商品 %s 的库存会超出系统支持范围", product.Name)}
			}
			if _, err := tx.Exec(ctx, `UPDATE products SET stock_on_hand = stock_on_hand + $1, updated_at = NOW() WHERE id = $2`, line.Quantity, line.ProductID); err != nil {
				return Order{}, fmt.Errorf("restore cancelled sale inventory: %w", err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO inventory_movements(product_id, order_id, movement_type, quantity_delta, reason)
				VALUES ($1, $2, 'sale_cancellation', $3, '销售订单取消，库存恢复')`, line.ProductID, orderID, line.Quantity); err != nil {
				return Order{}, fmt.Errorf("record restored sale inventory: %w", err)
			}
		}
		result, err := tx.Exec(ctx, `UPDATE inventory_commitments SET state = 'released', released_at = NOW() WHERE order_id = $1 AND state = 'consumed'`, orderID)
		if err != nil {
			return Order{}, fmt.Errorf("release sale commitment: %w", err)
		}
		if result.RowsAffected() != int64(len(lines)) {
			return Order{}, ConflictError{Message: "销售库存承诺状态异常，无法取消"}
		}
	}
	if orderType == "rental" {
		switch targetStatus {
		case "checked_out":
			result, err := tx.Exec(ctx, `UPDATE inventory_commitments SET state = 'active' WHERE order_id = $1 AND state = 'confirmed'`, orderID)
			if err != nil {
				return Order{}, fmt.Errorf("activate rental commitment: %w", err)
			}
			if result.RowsAffected() != int64(len(lines)) {
				return Order{}, ConflictError{Message: "租赁库存承诺状态异常，无法取货"}
			}
		case "returned", "cancelled":
			result, err := tx.Exec(ctx, `UPDATE inventory_commitments SET state = 'released', released_at = NOW() WHERE order_id = $1 AND state IN ('confirmed', 'active')`, orderID)
			if err != nil {
				return Order{}, fmt.Errorf("release rental commitment: %w", err)
			}
			if result.RowsAffected() != int64(len(lines)) {
				return Order{}, ConflictError{Message: "租赁库存承诺状态异常，无法释放"}
			}
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE orders SET
		status = $1,
		version = version + 1,
		updated_at = NOW(),
		checked_out_at = CASE WHEN $1 = 'checked_out' THEN NOW() ELSE checked_out_at END,
		returned_at = CASE WHEN $1 = 'returned' THEN NOW() ELSE returned_at END,
		cancelled_at = CASE WHEN $1 = 'cancelled' THEN NOW() ELSE cancelled_at END
		WHERE id = $2`, targetStatus, orderID); err != nil {
		return Order{}, fmt.Errorf("update order status: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO order_status_events(order_id, from_status, to_status, note) VALUES ($1, $2, $3, $4)`, orderID, currentStatus, targetStatus, note); err != nil {
		return Order{}, fmt.Errorf("record order transition: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, fmt.Errorf("commit order transition: %w", err)
	}
	return s.GetOrder(ctx, orderID)
}

func (s *Store) RecordPayment(ctx context.Context, orderID int64, input PaymentInput) (Payment, error) {
	input.Direction = strings.ToLower(strings.TrimSpace(input.Direction))
	input.Reference = strings.TrimSpace(input.Reference)
	input.Note = strings.TrimSpace(input.Note)
	if input.Direction != "charge" && input.Direction != "refund" {
		return Payment{}, ValidationError{Message: "direction 只能为 charge 或 refund"}
	}
	if input.AmountCents <= 0 {
		return Payment{}, ValidationError{Message: "收退款金额必须大于 0"}
	}
	if input.Reference == "" {
		return Payment{}, ValidationError{Message: "收退款必须填写不可重复的流水号或业务参考号"}
	}
	if runeCount(input.Reference) > 120 || runeCount(input.Note) > 2000 {
		return Payment{}, ValidationError{Message: "支付流水号或备注长度超出限制"}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Payment{}, fmt.Errorf("start payment: %w", err)
	}
	defer rollbackQuietly(tx)
	var total, paid int64
	var orderStatus string
	if err := tx.QueryRow(ctx, `SELECT total_cents, paid_cents, status FROM orders WHERE id = $1 FOR UPDATE`, orderID).Scan(&total, &paid, &orderStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Payment{}, ErrNotFound
		}
		return Payment{}, fmt.Errorf("lock payment order: %w", err)
	}
	existingPayment, err := scanPayment(tx.QueryRow(ctx, `SELECT id, order_id, direction, amount_cents, reference, note, created_at
		FROM payments WHERE reference = $1`, input.Reference))
	if err == nil {
		if existingPayment.OrderID != orderID {
			return Payment{}, ConflictError{Message: "该支付流水号已用于其他订单，不能重复入账"}
		}
		if existingPayment.Direction != input.Direction || existingPayment.AmountCents != input.AmountCents {
			return Payment{}, ValidationError{Message: "该支付流水号已用于不同的收退款记录"}
		}
		if err := tx.Commit(ctx); err != nil {
			return Payment{}, fmt.Errorf("commit idempotent payment replay: %w", err)
		}
		return existingPayment, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, fmt.Errorf("check payment reference: %w", err)
	}
	if orderStatus == "cancelled" && input.Direction == "charge" {
		return Payment{}, ValidationError{Message: "已取消订单不能继续收款"}
	}
	newPaid := paid
	if input.Direction == "charge" {
		if total-paid < input.AmountCents {
			return Payment{}, ValidationError{Message: "收款金额超过订单待收金额"}
		}
		newPaid += input.AmountCents
	} else {
		if paid < input.AmountCents {
			return Payment{}, ValidationError{Message: "退款金额超过已收金额"}
		}
		newPaid -= input.AmountCents
	}
	paymentStatus := "partial"
	switch {
	case newPaid == total:
		paymentStatus = "paid"
	case newPaid == 0 && input.Direction == "refund":
		paymentStatus = "refunded"
	case newPaid == 0:
		paymentStatus = "unpaid"
	}

	payment, err := scanPayment(tx.QueryRow(ctx, `INSERT INTO payments(order_id, direction, amount_cents, reference, note)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, order_id, direction, amount_cents, reference, note, created_at`, orderID, input.Direction, input.AmountCents, input.Reference, input.Note))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Payment{}, ConflictError{Message: "该支付流水号已用于其他订单，不能重复入账"}
		}
		return Payment{}, fmt.Errorf("record payment: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET paid_cents = $1, payment_status = $2, updated_at = NOW(), version = version + 1 WHERE id = $3`, newPaid, paymentStatus, orderID); err != nil {
		return Payment{}, fmt.Errorf("update payment summary: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Payment{}, fmt.Errorf("commit payment: %w", err)
	}
	return payment, nil
}

func (s *Store) ListPayments(ctx context.Context, orderID int64) ([]Payment, error) {
	var existingOrderID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM orders WHERE id = $1`, orderID).Scan(&existingOrderID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("check payment order: %w", err)
	}
	rows, err := s.pool.Query(ctx, `SELECT id, order_id, direction, amount_cents, reference, note, created_at
		FROM payments WHERE order_id = $1 ORDER BY created_at DESC, id DESC`, orderID)
	if err != nil {
		return nil, fmt.Errorf("list payments: %w", err)
	}
	defer rows.Close()
	payments := make([]Payment, 0)
	for rows.Next() {
		payment, err := scanPayment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan payment: %w", err)
		}
		payments = append(payments, payment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate payments: %w", err)
	}
	return payments, nil
}

// BillingReport returns the cash ledger for one business period. Dates are
// evaluated by PostgreSQL in the connection's configured business time zone.
// The end date shown to callers is inclusive; the SQL query uses the following
// day as an exclusive boundary so entries at 00:00 are assigned correctly.
func (s *Store) BillingReport(ctx context.Context, period, anchorDateRaw string) (BillingReport, error) {
	period = strings.ToLower(strings.TrimSpace(period))
	if period == "" {
		period = "day"
	}
	if !isBillingPeriod(period) {
		return BillingReport{}, ValidationError{Message: "period 只能为 day、week、month、quarter 或 year"}
	}

	var anchorDate time.Time
	if strings.TrimSpace(anchorDateRaw) == "" {
		var today string
		if err := s.pool.QueryRow(ctx, `SELECT CURRENT_DATE::text`).Scan(&today); err != nil {
			return BillingReport{}, fmt.Errorf("get billing business date: %w", err)
		}
		parsed, err := parseDate(today)
		if err != nil {
			return BillingReport{}, fmt.Errorf("parse billing business date: %w", err)
		}
		anchorDate = parsed
	} else {
		parsed, err := parseDate(anchorDateRaw)
		if err != nil {
			return BillingReport{}, ValidationError{Message: "date 必须为 YYYY-MM-DD"}
		}
		anchorDate = parsed
	}

	startDate, endExclusive, err := billingPeriodBounds(period, anchorDate)
	if err != nil {
		return BillingReport{}, err
	}
	report := BillingReport{
		Period:      period,
		AnchorDate:  anchorDate.Format(dateLayout),
		PeriodStart: startDate.Format(dateLayout),
		PeriodEnd:   endExclusive.AddDate(0, 0, -1).Format(dateLayout),
		Items:       make([]BillingEntry, 0),
	}

	if err := s.pool.QueryRow(ctx, `SELECT
		COALESCE(SUM(CASE WHEN direction = 'charge' THEN amount_cents ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN direction = 'refund' THEN amount_cents ELSE 0 END), 0),
		COUNT(*) FILTER (WHERE direction = 'charge'),
		COUNT(*) FILTER (WHERE direction = 'refund'),
		COUNT(*),
		COUNT(DISTINCT order_id)
		FROM payments
		WHERE created_at >= $1::date AND created_at < $2::date`,
		startDate.Format(dateLayout), endExclusive.Format(dateLayout),
	).Scan(
		&report.ChargeCents,
		&report.RefundCents,
		&report.ChargeCount,
		&report.RefundCount,
		&report.TransactionCount,
		&report.OrderCount,
	); err != nil {
		return BillingReport{}, fmt.Errorf("summarize billing report: %w", err)
	}
	if report.ChargeCents >= report.RefundCents {
		report.NetCents = report.ChargeCents - report.RefundCents
	} else {
		report.NetCents = -(report.RefundCents - report.ChargeCents)
	}

	const billingReportItemLimit = 500
	rows, err := s.pool.Query(ctx, `SELECT
		p.id, p.order_id, o.order_no, o.order_type, c.full_name,
		p.direction, p.amount_cents, p.reference, p.note, p.created_at
		FROM payments p
		JOIN orders o ON o.id = p.order_id
		JOIN customers c ON c.id = o.customer_id
		WHERE p.created_at >= $1::date AND p.created_at < $2::date
		ORDER BY p.created_at DESC, p.id DESC
		LIMIT $3`,
		startDate.Format(dateLayout), endExclusive.Format(dateLayout), billingReportItemLimit)
	if err != nil {
		return BillingReport{}, fmt.Errorf("list billing entries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var entry BillingEntry
		if err := rows.Scan(
			&entry.ID,
			&entry.OrderID,
			&entry.OrderNo,
			&entry.OrderType,
			&entry.CustomerName,
			&entry.Direction,
			&entry.AmountCents,
			&entry.Reference,
			&entry.Note,
			&entry.CreatedAt,
		); err != nil {
			return BillingReport{}, fmt.Errorf("scan billing entry: %w", err)
		}
		report.Items = append(report.Items, entry)
	}
	if err := rows.Err(); err != nil {
		return BillingReport{}, fmt.Errorf("iterate billing entries: %w", err)
	}
	report.Truncated = report.TransactionCount > int64(len(report.Items))
	return report, nil
}

func billingPeriodBounds(period string, anchorDate time.Time) (time.Time, time.Time, error) {
	anchorDate = time.Date(anchorDate.Year(), anchorDate.Month(), anchorDate.Day(), 0, 0, 0, 0, time.UTC)
	switch period {
	case "day":
		return anchorDate, anchorDate.AddDate(0, 0, 1), nil
	case "week":
		daysSinceMonday := (int(anchorDate.Weekday()) + 6) % 7
		start := anchorDate.AddDate(0, 0, -daysSinceMonday)
		return start, start.AddDate(0, 0, 7), nil
	case "month":
		start := time.Date(anchorDate.Year(), anchorDate.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0), nil
	case "quarter":
		quarterMonth := time.Month(((int(anchorDate.Month()) - 1) / 3 * 3) + 1)
		start := time.Date(anchorDate.Year(), quarterMonth, 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 3, 0), nil
	case "year":
		start := time.Date(anchorDate.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(1, 0, 0), nil
	default:
		return time.Time{}, time.Time{}, ValidationError{Message: "period 只能为 day、week、month、quarter 或 year"}
	}
}

func isBillingPeriod(period string) bool {
	switch period {
	case "day", "week", "month", "quarter", "year":
		return true
	default:
		return false
	}
}

func (s *Store) Dashboard(ctx context.Context) (Dashboard, error) {
	var dashboard Dashboard
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM products WHERE active = TRUE`).Scan(&dashboard.ActiveProducts); err != nil {
		return Dashboard{}, fmt.Errorf("dashboard active products: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(GREATEST(p.stock_on_hand - COALESCE(r.reserved, 0), 0)), 0)
		FROM products p
		LEFT JOIN (
			SELECT product_id, SUM(quantity)::INTEGER AS reserved
			FROM inventory_commitments
			WHERE commitment_type = 'rental' AND state IN ('confirmed', 'active')
			  AND (period @> CURRENT_DATE OR (state = 'active' AND upper(period) <= CURRENT_DATE))
			GROUP BY product_id
		) r ON r.product_id = p.id
		WHERE p.active = TRUE`).Scan(&dashboard.AvailableNow); err != nil {
		return Dashboard{}, fmt.Errorf("dashboard available inventory: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM orders WHERE order_type = 'rental' AND status = 'checked_out'`).Scan(&dashboard.CheckedOutRentals); err != nil {
		return Dashboard{}, fmt.Errorf("dashboard checked out rentals: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM orders
		WHERE order_type = 'rental' AND status = 'checked_out' AND end_date >= CURRENT_DATE AND end_date < CURRENT_DATE + 3`).Scan(&dashboard.UpcomingReturns); err != nil {
		return Dashboard{}, fmt.Errorf("dashboard upcoming returns: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(total_cents), 0) FROM orders
		WHERE order_type = 'sale' AND status IN ('confirmed', 'completed') AND created_at::date = CURRENT_DATE`).Scan(&dashboard.TodaySalesCents); err != nil {
		return Dashboard{}, fmt.Errorf("dashboard today sales: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM products WHERE active = TRUE AND stock_on_hand <= 2`).Scan(&dashboard.LowStockProducts); err != nil {
		return Dashboard{}, fmt.Errorf("dashboard low stock products: %w", err)
	}
	return dashboard, nil
}

func validateProductInput(input CreateProductInput) error {
	if input.SKU == "" || input.Name == "" {
		return ValidationError{Message: "SKU 和商品名称不能为空"}
	}
	if runeCount(input.SKU) > 64 || runeCount(input.Name) > 160 || runeCount(input.ProductType) > 40 || runeCount(input.Category) > 80 || runeCount(input.Location) > 120 || runeCount(input.Description) > 5000 {
		return ValidationError{Message: "商品信息长度超出限制"}
	}
	if !input.RentEnabled && !input.SellEnabled {
		return ValidationError{Message: "商品至少要开启租赁或售卖中的一种"}
	}
	if input.DailyRentCents < 0 || input.SalePriceCents < 0 || input.DepositCents < 0 || input.StockOnHand < 0 {
		return ValidationError{Message: "价格和库存不能为负数"}
	}
	if int64(input.StockOnHand) > maxInventoryQuantity {
		return ValidationError{Message: "现货数量超出系统支持范围"}
	}
	return nil
}

func validateOrderInput(input CreateOrderInput) error {
	if input.OrderType != "rental" && input.OrderType != "sale" {
		return ValidationError{Message: "order_type 只能为 rental 或 sale"}
	}
	if input.CustomerID <= 0 {
		return ValidationError{Message: "必须选择客户"}
	}
	if len(input.Items) == 0 || len(input.Items) > 30 {
		return ValidationError{Message: "订单商品数量必须在 1 到 30 之间"}
	}
	seen := make(map[int64]struct{}, len(input.Items))
	for _, item := range input.Items {
		if item.ProductID <= 0 || item.Quantity <= 0 || item.Quantity > 100000 {
			return ValidationError{Message: "订单商品或数量不合法"}
		}
		if _, exists := seen[item.ProductID]; exists {
			return ValidationError{Message: "同一商品请合并为一条订单明细"}
		}
		seen[item.ProductID] = struct{}{}
	}
	if runeCount(input.Notes) > 2000 {
		return ValidationError{Message: "订单备注不能超过 2000 个字符"}
	}
	if input.OrderType == "sale" && (strings.TrimSpace(input.StartDate) != "" || strings.TrimSpace(input.EndDate) != "") {
		return ValidationError{Message: "销售订单不应填写租赁日期"}
	}
	if input.OrderType == "rental" && (strings.TrimSpace(input.StartDate) == "" || strings.TrimSpace(input.EndDate) == "") {
		return ValidationError{Message: "租赁订单必须填写起租和归还日期"}
	}
	return nil
}

func loadProductForUpdate(ctx context.Context, tx pgx.Tx, id int64) (Product, error) {
	product, err := scanProduct(tx.QueryRow(ctx, `SELECT `+productColumns+` FROM products WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, ValidationError{Message: "商品不存在"}
	}
	if err != nil {
		return Product{}, fmt.Errorf("lock product: %w", err)
	}
	return product, nil
}

func claimIdempotency(ctx context.Context, tx pgx.Tx, scope, key string) (responseID int64, replay bool, err error) {
	if key == "" {
		return 0, false, nil
	}
	if runeCount(key) > 128 {
		return 0, false, ValidationError{Message: "Idempotency-Key 不能超过 128 个字符"}
	}
	result, err := tx.Exec(ctx, `INSERT INTO idempotency_keys(scope, idempotency_key) VALUES ($1, $2) ON CONFLICT DO NOTHING`, scope, key)
	if err != nil {
		return 0, false, fmt.Errorf("claim idempotency key: %w", err)
	}
	if result.RowsAffected() == 1 {
		return 0, false, nil
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(response_id, 0) FROM idempotency_keys WHERE scope = $1 AND idempotency_key = $2`, scope, key).Scan(&responseID); err != nil {
		return 0, false, fmt.Errorf("read idempotency key: %w", err)
	}
	if responseID == 0 {
		return 0, false, ConflictError{Message: "相同幂等键的请求仍在处理中，请稍后重试"}
	}
	return responseID, true, nil
}

func completeIdempotency(ctx context.Context, tx pgx.Tx, scope, key string, responseID int64) error {
	if key == "" {
		return nil
	}
	result, err := tx.Exec(ctx, `UPDATE idempotency_keys SET response_id = $3 WHERE scope = $1 AND idempotency_key = $2 AND response_id IS NULL`, scope, key, responseID)
	if err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ConflictError{Message: "幂等请求状态异常，请使用新的 Idempotency-Key 重试"}
	}
	return nil
}

type commitmentQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func rentalPeakForPeriod(ctx context.Context, queryer commitmentQueryer, productID int64, startDate, endDate string) (int, error) {
	var peak int64
	err := queryer.QueryRow(ctx, `
		WITH relevant AS (
			SELECT CASE
				WHEN state = 'active' AND upper(period) <= CURRENT_DATE THEN daterange(lower(period), NULL, '[)')
				ELSE period
			END AS effective_period, quantity
			FROM inventory_commitments
			WHERE product_id = $1
			  AND commitment_type = 'rental'
			  AND state IN ('confirmed', 'active')
			  AND (period && daterange($2::date, $3::date, '[)') OR (state = 'active' AND upper(period) <= CURRENT_DATE))
		), points AS (
			SELECT $2::date AS point
			UNION SELECT lower(effective_period) FROM relevant
			UNION SELECT upper(effective_period) FROM relevant WHERE NOT upper_inf(effective_period)
		), usage AS (
			SELECT p.point, COALESCE(SUM(r.quantity) FILTER (WHERE r.effective_period @> p.point), 0) AS reserved
			FROM points p LEFT JOIN relevant r ON TRUE
			WHERE p.point >= $2::date AND p.point < $3::date
			GROUP BY p.point
		)
		SELECT COALESCE(MAX(reserved), 0) FROM usage`, productID, startDate, endDate).Scan(&peak)
	if err != nil {
		return 0, fmt.Errorf("calculate rental availability: %w", err)
	}
	return int(peak), nil
}

func rentalPeakFromToday(ctx context.Context, queryer commitmentQueryer, productID int64) (int, error) {
	var peak int64
	err := queryer.QueryRow(ctx, `
		WITH relevant AS (
			SELECT CASE
				WHEN state = 'active' AND upper(period) <= CURRENT_DATE THEN daterange(lower(period), NULL, '[)')
				ELSE period
			END AS effective_period, quantity
			FROM inventory_commitments
			WHERE product_id = $1
			  AND commitment_type = 'rental'
			  AND state IN ('confirmed', 'active')
			  AND (upper(period) > CURRENT_DATE OR state = 'active')
		), points AS (
			SELECT CURRENT_DATE AS point
			UNION SELECT lower(effective_period) FROM relevant WHERE lower(effective_period) >= CURRENT_DATE
			UNION SELECT upper(effective_period) FROM relevant WHERE NOT upper_inf(effective_period)
		), usage AS (
			SELECT p.point, COALESCE(SUM(r.quantity) FILTER (WHERE r.effective_period @> p.point), 0) AS reserved
			FROM points p LEFT JOIN relevant r ON TRUE
			WHERE p.point >= CURRENT_DATE
			GROUP BY p.point
		)
		SELECT COALESCE(MAX(reserved), 0) FROM usage`, productID).Scan(&peak)
	if err != nil {
		return 0, fmt.Errorf("calculate future rental commitments: %w", err)
	}
	return int(peak), nil
}

type pricedLine struct {
	product         Product
	quantity        int
	saleSubtotal    int64
	rentalSubtotal  int64
	depositSubtotal int64
	total           int64
}

func insertOrder(ctx context.Context, tx pgx.Tx, input CreateOrderInput, startDate, endDate any, rentalDays int, rentalTotal, depositTotal, total int64) (int64, error) {
	var orderID int64
	err := tx.QueryRow(ctx, `INSERT INTO orders(
		order_no, order_type, customer_id, status, start_date, end_date, rental_days,
		rental_total_cents, deposit_total_cents, total_cents, notes
	) VALUES (
		CONCAT('RS-', TO_CHAR(CURRENT_DATE, 'YYYYMMDD'), '-', LPAD(nextval('order_number_seq')::TEXT, 10, '0')),
		$1,$2,'confirmed',$3,$4,$5,$6,$7,$8,$9
	)
	RETURNING id`, input.OrderType, input.CustomerID, startDate, endDate, rentalDays, rentalTotal, depositTotal, total, input.Notes).Scan(&orderID)
	if err != nil {
		return 0, fmt.Errorf("insert order: %w", err)
	}
	return orderID, nil
}

func isAllowedTransition(orderType, from, to string) bool {
	switch orderType {
	case "rental":
		return (from == "confirmed" && (to == "checked_out" || to == "cancelled")) ||
			(from == "checked_out" && to == "returned")
	case "sale":
		return from == "confirmed" && (to == "completed" || to == "cancelled")
	default:
		return false
	}
}

func isOrderStatus(status string) bool {
	switch status {
	case "confirmed", "checked_out", "returned", "completed", "cancelled":
		return true
	default:
		return false
	}
}

type orderProductQuantity struct {
	ProductID int64
	Quantity  int
}

func loadOrderProductQuantities(ctx context.Context, tx pgx.Tx, orderID int64) ([]orderProductQuantity, error) {
	rows, err := tx.Query(ctx, `SELECT product_id, quantity FROM order_lines WHERE order_id = $1 ORDER BY product_id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("load order lines: %w", err)
	}
	defer rows.Close()
	lines := make([]orderProductQuantity, 0)
	for rows.Next() {
		var line orderProductQuantity
		if err := rows.Scan(&line.ProductID, &line.Quantity); err != nil {
			return nil, fmt.Errorf("scan order line: %w", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate order lines: %w", err)
	}
	if len(lines) == 0 {
		return nil, ConflictError{Message: "订单没有可履约的商品明细"}
	}
	return lines, nil
}

func (s *Store) listOrderLines(ctx context.Context, orderID int64) ([]OrderLine, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, product_id, sku_snapshot, product_name_snapshot, quantity,
		unit_sale_price_cents, unit_daily_rent_cents, unit_deposit_cents,
		rental_subtotal_cents, deposit_subtotal_cents, line_total_cents
		FROM order_lines WHERE order_id = $1 ORDER BY id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("list order lines: %w", err)
	}
	defer rows.Close()
	items := make([]OrderLine, 0)
	for rows.Next() {
		var item OrderLine
		if err := rows.Scan(&item.ID, &item.ProductID, &item.SKU, &item.ProductName, &item.Quantity,
			&item.UnitSalePriceCents, &item.UnitDailyRentCents, &item.UnitDepositCents,
			&item.RentalSubtotalCents, &item.DepositSubtotalCents, &item.LineTotalCents); err != nil {
			return nil, fmt.Errorf("scan order line: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate order lines: %w", err)
	}
	return items, nil
}

const orderSelectQuery = `SELECT
	o.id, o.order_no, o.order_type, o.customer_id, c.full_name, c.phone, o.status,
	COALESCE(o.start_date::text, ''), COALESCE(o.end_date::text, ''), o.rental_days,
	o.rental_total_cents, o.deposit_total_cents, o.total_cents, o.paid_cents,
	o.payment_status, o.notes, o.version, o.confirmed_at, o.checked_out_at,
	o.returned_at, o.cancelled_at, o.created_at, o.updated_at
	FROM orders o JOIN customers c ON c.id = o.customer_id`

func scanProduct(row rowScanner) (Product, error) {
	var product Product
	err := row.Scan(
		&product.ID, &product.SKU, &product.Name, &product.ProductType, &product.Category, &product.Location, &product.Description,
		&product.RentEnabled, &product.SellEnabled, &product.DailyRentCents, &product.SalePriceCents,
		&product.DepositCents, &product.StockOnHand, &product.Active, &product.CreatedAt, &product.UpdatedAt,
	)
	return product, err
}

func scanCustomer(row rowScanner) (Customer, error) {
	var customer Customer
	err := row.Scan(&customer.ID, &customer.FullName, &customer.Phone, &customer.Email, &customer.IdentityRef, &customer.Note, &customer.CreatedAt, &customer.UpdatedAt)
	return customer, err
}

func scanOrder(row rowScanner) (Order, error) {
	var order Order
	err := row.Scan(
		&order.ID, &order.OrderNo, &order.OrderType, &order.CustomerID, &order.CustomerName, &order.CustomerPhone, &order.Status,
		&order.StartDate, &order.EndDate, &order.RentalDays, &order.RentalTotalCents, &order.DepositTotalCents,
		&order.TotalCents, &order.PaidCents, &order.PaymentStatus, &order.Notes, &order.Version,
		&order.ConfirmedAt, &order.CheckedOutAt, &order.ReturnedAt, &order.CancelledAt, &order.CreatedAt, &order.UpdatedAt,
	)
	return order, err
}

func scanPayment(row rowScanner) (Payment, error) {
	var payment Payment
	err := row.Scan(&payment.ID, &payment.OrderID, &payment.Direction, &payment.AmountCents, &payment.Reference, &payment.Note, &payment.CreatedAt)
	return payment, err
}

func parseDate(raw string) (time.Time, error) {
	return time.Parse(dateLayout, strings.TrimSpace(raw))
}

func mapDatabaseError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ConflictError{Message: "记录已存在，请检查 SKU 或手机号是否重复"}
		case "23503":
			return ValidationError{Message: "关联记录不存在或仍被使用"}
		case "23514":
			return ValidationError{Message: "提交的数据不符合业务约束"}
		case "22001":
			return ValidationError{Message: "字段长度超出限制"}
		case "22003":
			return ValidationError{Message: "数字超出系统支持范围"}
		}
	}
	return err
}

func multiplyMoney(unit int64, factors ...int64) (int64, error) {
	if unit < 0 {
		return 0, ValidationError{Message: "金额不能为负数"}
	}
	result := unit
	const maxInt64 = int64(^uint64(0) >> 1)
	for _, factor := range factors {
		if factor < 0 {
			return 0, ValidationError{Message: "数量不能为负数"}
		}
		if result != 0 && factor > maxInt64/result {
			return 0, ValidationError{Message: "金额超出系统支持范围"}
		}
		result *= factor
	}
	return result, nil
}

func addMoney(a, b int64) (int64, error) {
	const maxInt64 = int64(^uint64(0) >> 1)
	if a < 0 || b < 0 || a > maxInt64-b {
		return 0, ValidationError{Message: "金额超出系统支持范围"}
	}
	return a + b, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func runeCount(value string) int {
	return utf8.RuneCountInString(value)
}
