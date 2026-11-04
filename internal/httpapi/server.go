package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rentsale/internal/config"
	"rentsale/internal/store"
	webassets "rentsale/web"
)

const maxRequestBody = 1 << 20

type Server struct {
	config config.Config
	store  *store.Store
	static http.Handler
}

func New(cfg config.Config, repository *store.Store) http.Handler {
	server := &Server{
		config: cfg,
		store:  repository,
		static: staticHandler(cfg.WebDir),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", server.handleHealth)
	mux.HandleFunc("/readyz", server.handleReady)
	mux.HandleFunc("/api/dashboard", server.handleDashboard)
	mux.HandleFunc("/api/products", server.handleProducts)
	mux.HandleFunc("/api/products/", server.handleProductByID)
	mux.HandleFunc("/api/customers", server.handleCustomers)
	mux.HandleFunc("/api/orders", server.handleOrders)
	mux.HandleFunc("/api/orders/", server.handleOrderByID)
	mux.Handle("/", server.static)
	return server.withMiddleware(mux)
}

func staticHandler(webDir string) http.Handler {
	if strings.TrimSpace(webDir) != "" {
		return http.FileServer(http.Dir(webDir))
	}
	return http.FileServer(http.FS(webassets.Files))
}

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strconv.FormatInt(time.Now().UnixNano(), 36)
		requestCtx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		r = r.WithContext(requestCtx)
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("request_id=%s panic=%v", requestID, recovered)
				writeError(w, http.StatusInternalServerError, "internal_error", "服务器处理失败，请稍后重试", nil)
			}
		}()
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'; object-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && s.config.APIKey != "" {
			provided := r.Header.Get("X-API-Key")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(s.config.APIKey)) != 1 {
				writeError(w, http.StatusUnauthorized, "unauthorized", "缺少或错误的 API Key", nil)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if err := s.store.Ping(r.Context()); err != nil {
		log.Printf("readiness check failed: %v", err)
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "数据库暂不可用", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	dashboard, err := s.store.Dashboard(r.Context())
	if err != nil {
		s.respondStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dashboard)
}

func (s *Server) handleProducts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		activeOnly := r.URL.Query().Get("include_inactive") != "true"
		products, err := s.store.ListProducts(r.Context(), r.URL.Query().Get("q"), activeOnly)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": products})
	case http.MethodPost:
		var input store.CreateProductInput
		if err := readJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
			return
		}
		product, err := s.store.CreateProduct(r.Context(), input)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, product)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleProductByID(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/products/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	productID, err := parsePositiveID(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "商品 ID 不合法", nil)
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		product, err := s.store.GetProduct(r.Context(), productID)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, product)
		return
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "availability":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		availability, err := s.store.GetAvailability(r.Context(), productID, r.URL.Query().Get("start_date"), r.URL.Query().Get("end_date"))
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, availability)
	case "stock-adjustments":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		var input store.StockAdjustmentInput
		if err := readJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
			return
		}
		idempotencyKey, err := requireIdempotencyKey(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "idempotency_key_required", err.Error(), nil)
			return
		}
		input.IdempotencyKey = idempotencyKey
		product, err := s.store.AdjustStock(r.Context(), productID, input)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, product)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleCustomers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		customers, err := s.store.ListCustomers(r.Context(), r.URL.Query().Get("q"))
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": customers})
	case http.MethodPost:
		var input store.CreateCustomerInput
		if err := readJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
			return
		}
		customer, err := s.store.CreateCustomer(r.Context(), input)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, customer)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, err := parseLimit(r.URL.Query().Get("limit"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit 必须是 1 到 100 之间的整数", nil)
			return
		}
		orders, err := s.store.ListOrders(r.Context(), store.OrderFilter{
			OrderType: r.URL.Query().Get("order_type"),
			Status:    r.URL.Query().Get("status"),
			Limit:     limit,
		})
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": orders})
	case http.MethodPost:
		var input store.CreateOrderInput
		if err := readJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
			return
		}
		idempotencyKey, err := requireIdempotencyKey(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "idempotency_key_required", err.Error(), nil)
			return
		}
		input.IdempotencyKey = idempotencyKey
		order, err := s.store.CreateOrder(r.Context(), input)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, order)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleOrderByID(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/orders/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	orderID, err := parsePositiveID(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "订单 ID 不合法", nil)
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		order, err := s.store.GetOrder(r.Context(), orderID)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, order)
		return
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "status":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		var input struct {
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		if err := readJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
			return
		}
		order, err := s.store.TransitionOrder(r.Context(), orderID, input.Status, input.Note)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, order)
	case "payments":
		s.handlePayments(w, r, orderID)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handlePayments(w http.ResponseWriter, r *http.Request, orderID int64) {
	switch r.Method {
	case http.MethodGet:
		payments, err := s.store.ListPayments(r.Context(), orderID)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": payments})
	case http.MethodPost:
		var input store.PaymentInput
		if err := readJSON(w, r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
			return
		}
		payment, err := s.store.RecordPayment(r.Context(), orderID, input)
		if err != nil {
			s.respondStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, payment)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) respondStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "记录不存在", nil)
		return
	}
	var validation store.ValidationError
	if errors.As(err, &validation) {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", validation.Message, nil)
		return
	}
	var conflict store.ConflictError
	if errors.As(err, &conflict) {
		details := map[string]any{}
		if conflict.ProductID > 0 {
			details["product_id"] = conflict.ProductID
			details["available"] = conflict.Available
		}
		writeError(w, http.StatusConflict, "conflict", conflict.Message, details)
		return
	}
	log.Printf("request failed: %v", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "服务器处理失败，请稍后重试", nil)
}

func readJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return jsonDecodeError(err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("请求体只能包含一个 JSON 对象")
	}
	return nil
}

func jsonDecodeError(err error) error {
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntaxError):
		return fmt.Errorf("JSON 格式错误（位置 %d）", syntaxError.Offset)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return fmt.Errorf("JSON 格式不完整")
	case errors.As(err, &typeError):
		return fmt.Errorf("字段 %s 的类型不正确", typeError.Field)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return fmt.Errorf("存在未定义字段")
	case errors.Is(err, io.EOF):
		return fmt.Errorf("请求体不能为空")
	case err.Error() == "http: request body too large":
		return fmt.Errorf("请求体不能超过 1MB")
	default:
		return fmt.Errorf("无法读取 JSON 请求体")
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string, details any) {
	payload := map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	if details != nil {
		payload["error"].(map[string]any)["details"] = details
	}
	writeJSON(w, status, payload)
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持该请求方法", nil)
}

func pathParts(path, prefix string) []string {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if rest == "" {
		return nil
	}
	return strings.Split(rest, "/")
}

func parsePositiveID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid ID")
	}
	return id, nil
}

func parseLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 50, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 100 {
		return 0, fmt.Errorf("invalid limit")
	}
	return limit, nil
}

func requireIdempotencyKey(r *http.Request) (string, error) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		return "", fmt.Errorf("写入操作必须携带 Idempotency-Key 请求头")
	}
	if len(key) > 128 {
		return "", fmt.Errorf("Idempotency-Key 不能超过 128 个字符")
	}
	return key, nil
}
