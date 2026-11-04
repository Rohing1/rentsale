package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rentsale/internal/config"
	"rentsale/internal/database"
	"rentsale/internal/httpapi"
	"rentsale/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.Connect(ctx, cfg.DatabaseURL, cfg.BusinessTimeZone)
	if err != nil {
		log.Fatalf("database unavailable: %v", err)
	}
	defer pool.Close()

	if cfg.AutoMigrate {
		if err := database.Migrate(ctx, pool); err != nil {
			log.Fatalf("database migration failed: %v", err)
		}
	}

	handler := httpapi.New(cfg, store.New(pool))
	server := &http.Server{
		Addr:              cfg.Address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("RentSale is listening on %s", cfg.Address)
		serverErrors <- server.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	select {
	case signal := <-signals:
		log.Printf("received %s; shutting down", signal)
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server failed: %v", err)
		}
		return
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
		if err := server.Close(); err != nil {
			log.Printf("force close failed: %v", err)
		}
	}
}
