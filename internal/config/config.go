package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Address          string
	DatabaseURL      string
	WebDir           string
	APIKey           string
	BusinessTimeZone string
	AutoMigrate      bool
}

func Load() (Config, error) {
	databaseURL, err := resolveDatabaseURL()
	if err != nil {
		return Config{}, err
	}

	autoMigrate := true
	if raw := strings.TrimSpace(os.Getenv("AUTO_MIGRATE")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse AUTO_MIGRATE: %w", err)
		}
		autoMigrate = parsed
	}

	address := valueOr("APP_ADDR", "127.0.0.1:8080")
	apiKey := strings.TrimSpace(os.Getenv("APP_API_KEY"))
	if apiKey == "" && !isLoopbackAddress(address) {
		return Config{}, fmt.Errorf("APP_API_KEY is required when APP_ADDR is not loopback-only")
	}

	return Config{
		Address:          address,
		DatabaseURL:      databaseURL,
		WebDir:           strings.TrimSpace(os.Getenv("WEB_DIR")),
		APIKey:           apiKey,
		BusinessTimeZone: valueOr("BUSINESS_TIME_ZONE", "Asia/Shanghai"),
		AutoMigrate:      autoMigrate,
	}, nil
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "::1" || strings.EqualFold(host, "localhost")
}

func resolveDatabaseURL() (string, error) {
	if databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL")); databaseURL != "" {
		return databaseURL, nil
	}

	host := strings.TrimSpace(os.Getenv("DB_HOST"))
	databaseName := strings.TrimSpace(os.Getenv("DB_NAME"))
	user := strings.TrimSpace(os.Getenv("DB_USER"))
	password := os.Getenv("DB_PASSWORD")
	if host == "" || databaseName == "" || user == "" || password == "" {
		return "", fmt.Errorf("set DATABASE_URL or DB_HOST, DB_NAME, DB_USER and DB_PASSWORD")
	}
	port := valueOr("DB_PORT", "5432")
	sslMode := valueOr("DB_SSLMODE", "disable")
	connectionURL := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, port),
		Path:     databaseName,
		RawQuery: url.Values{"sslmode": []string{sslMode}}.Encode(),
	}
	return connectionURL.String(), nil
}

func valueOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
