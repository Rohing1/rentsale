package config

import (
	"net/url"
	"testing"
)

func TestResolveDatabaseURLFromPartsEscapesCredentials(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DB_HOST", "db")
	t.Setenv("DB_PORT", "5432")
	t.Setenv("DB_NAME", "rental platform")
	t.Setenv("DB_USER", "operator@example")
	t.Setenv("DB_PASSWORD", "p@ss:/word")
	t.Setenv("DB_SSLMODE", "require")

	rawURL, err := resolveDatabaseURL()
	if err != nil {
		t.Fatalf("resolveDatabaseURL() error = %v", err)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	password, _ := parsed.User.Password()
	if parsed.Host != "db:5432" || parsed.User.Username() != "operator@example" || password != "p@ss:/word" || parsed.Path != "/rental platform" || parsed.Query().Get("sslmode") != "require" {
		t.Fatalf("unexpected parsed connection URL: host=%q user=%q path=%q", parsed.Host, parsed.User.Username(), parsed.Path)
	}
}

func TestLoadRejectsPublicBindingWithoutAPIKey(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("APP_ADDR", "0.0.0.0:8080")
	t.Setenv("APP_API_KEY", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected public binding without APP_API_KEY to be rejected")
	}
}

func TestLoadUsesEmbeddedUIUnlessWebDirIsSet(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("WEB_DIR", "")
	t.Setenv("APP_ADDR", "127.0.0.1:8080")
	t.Setenv("APP_API_KEY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.WebDir != "" {
		t.Fatalf("WebDir = %q, want empty string for embedded UI", cfg.WebDir)
	}
}
