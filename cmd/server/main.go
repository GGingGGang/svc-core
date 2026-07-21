package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/GGingGGang/svc-core/internal/api"
	"github.com/GGingGGang/svc-core/internal/config"
	"github.com/GGingGGang/svc-core/internal/db"
	authmw "github.com/GGingGGang/svc-core/internal/middleware"
	"github.com/GGingGGang/svc-core/internal/observability"
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

	shutdownTracing, err := observability.SetupTracing(ctx)
	if err != nil {
		log.Fatalf("setup tracing: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			log.Printf("tracing shutdown failed: %v", err)
		}
	}()

	sqlDB, err := db.Connect(ctx, cfg)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer sqlDB.Close()

	jwtAuth, err := authmw.NewJWTAuth(cfg.JWKSURL, cfg.JWTIssuer, cfg.JWTAudience)
	if err != nil {
		log.Fatalf("setup jwt auth: %v", err)
	}

	handler := api.NewHandler(service.New(sqlDB))

	srv := &http.Server{
		Addr:    ":" + cfg.HTTPPort,
		Handler: otelhttp.NewHandler(api.Router(handler, jwtAuth.Middleware), "svc-core"),
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
