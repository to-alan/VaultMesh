package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/to-alan/vaultmesh/internal/config"
	"github.com/to-alan/vaultmesh/internal/control"
	"github.com/to-alan/vaultmesh/internal/secret"
	"github.com/to-alan/vaultmesh/internal/store"
	"github.com/to-alan/vaultmesh/internal/store/memory"
	"github.com/to-alan/vaultmesh/internal/store/postgres"
	"github.com/to-alan/vaultmesh/internal/version"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config, err := config.LoadServer()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	key, err := secret.ParseKey(config.MasterKey)
	if err != nil {
		logger.Error("invalid master key", "error", err)
		os.Exit(1)
	}
	sealer, err := secret.New(key)
	if err != nil {
		logger.Error("initialize secret encryption", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var dataStore store.Store
	if config.DatabaseURL == "" {
		logger.Warn("using in-memory metadata store; all metadata will be lost on restart")
		dataStore = memory.New()
	} else {
		postgresStore, err := postgres.Open(ctx, config.DatabaseURL)
		if err != nil {
			logger.Error("open PostgreSQL", "error", err)
			os.Exit(1)
		}
		if config.AutoMigrate {
			if err := postgresStore.Migrate(ctx); err != nil {
				logger.Error("migrate PostgreSQL", "error", err)
				os.Exit(1)
			}
		}
		dataStore = postgresStore
	}
	defer dataStore.Close()

	service := control.NewService(dataStore, sealer)
	handler := control.NewHTTPServer(service, logger, config.AdminToken, config.AllowedOrigins).Handler()
	server := &http.Server{
		Addr:              config.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("VaultMesh control plane started", "address", config.ListenAddress, "version", version.Version)
		errCh <- server.ListenAndServe()
	}()

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	case <-signalCtx.Done():
		logger.Info("shutting down")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			os.Exit(1)
		}
	}
}
