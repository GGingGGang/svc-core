package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/GGingGGang/svc-core/internal/api"
	"github.com/GGingGGang/svc-core/internal/config"
	"github.com/GGingGGang/svc-core/internal/db"
	"github.com/GGingGGang/svc-core/internal/service"
)

var version = "dev" // -ldflags "-X main.version=<GIT_SHA>"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	sqlDB, err := db.Connect(ctx, cfg)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer sqlDB.Close()

	handler := api.NewHandler(service.New(sqlDB))

	srv := &http.Server{
		Addr:    ":" + cfg.HTTPPort,
		Handler: api.Router(handler),
	}

	log.Printf("svc-core %s listening on :%s", version, cfg.HTTPPort)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatalf("server failed: %v", err)
	case <-ctx.Done():
		log.Printf("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
