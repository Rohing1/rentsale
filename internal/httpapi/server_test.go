package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rentsale/internal/config"
)

func TestEmbeddedUIIsServedByDefault(t *testing.T) {
	handler := New(config.Config{}, nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), "RentSale") {
		t.Fatalf("GET / response does not contain embedded application UI")
	}

	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/styles.css", nil))
	if asset.Code != http.StatusOK || !strings.Contains(asset.Body.String(), "--brand") {
		t.Fatalf("GET /styles.css did not return the embedded stylesheet")
	}
}

func TestExternalWebDirectoryOverridesEmbeddedUI(t *testing.T) {
	directory := t.TempDir()
	content := []byte("external UI")
	if err := os.WriteFile(filepath.Join(directory, "index.html"), content, 0o600); err != nil {
		t.Fatalf("write external index: %v", err)
	}
	handler := New(config.Config{WebDir: directory}, nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.Body.String() != string(content) {
		t.Fatalf("external web response = %q, want %q", recorder.Body.String(), content)
	}
}

func TestHealthAndAPIKeyGate(t *testing.T) {
	handler := New(config.Config{APIKey: "test-key", WebDir: "."}, nil)

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", health.Code, http.StatusOK)
	}

	protected := httptest.NewRecorder()
	handler.ServeHTTP(protected, httptest.NewRequest(http.MethodGet, "/api/dashboard", nil))
	if protected.Code != http.StatusUnauthorized {
		t.Fatalf("protected status = %d, want %d", protected.Code, http.StatusUnauthorized)
	}
}

func TestRejectUnknownJSONFieldBeforeStoreAccess(t *testing.T) {
	handler := New(config.Config{WebDir: "."}, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/products", strings.NewReader(`{"unexpected": true}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestOrderRequiresIdempotencyKeyBeforeStoreAccess(t *testing.T) {
	handler := New(config.Config{WebDir: "."}, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/orders", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}
