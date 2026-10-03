package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sh5800/seat-reservation-service/internal/api"
	"github.com/sh5800/seat-reservation-service/internal/store"
)

// loadEnv reads a .env file if it exists and loads variables into the environment
func loadEnv(filepath string) {
	file, err := os.Open(filepath)
	if err != nil {
		return // File doesn't exist, ignore (e.g. in production)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue // Skip empty lines and comments
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			// Only set if not already set in the operating system environment
			if os.Getenv(key) == "" {
				os.Setenv(key, val)
			}
		}
	}
}

func main() {
	// 1. Load from .env if present
	loadEnv(".env")

	// 2. Structured JSON logging to standard output
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// 3. Read configuration
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL must be specified in .env or environment")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	slog.Info("starting seat-reservation service", slog.String("port", port))

	// 4. Initialize Postgres store with connection pooling & auto-migration
	dbStore, err := store.NewPostgresStore(ctx, dbURL)
	if err != nil {
		slog.Error("failed to initialize database store", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer dbStore.Close()
	slog.Info("database connection established and migrations applied")

	// 5. Initialize HTTP router
	router := api.NewRouter(dbStore)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// 6. Graceful shutdown handler
	shutdownChan := make(chan os.Signal, 1)
	signal.Notify(shutdownChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		slog.Info(fmt.Sprintf("HTTP server listening on http://localhost:%s", port))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server error", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	sig := <-shutdownChan
	slog.Info("shutdown signal received", slog.String("signal", sig.String()))

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", slog.String("error", err.Error()))
	} else {
		slog.Info("server stopped gracefully")
	}
}
