package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embed IANA zoneinfo — the distroless runtime image ships none, but /schedules/extract validates request timezones via time.LoadLocation

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/GGingGGang/svc-core/internal/ai"
	"github.com/GGingGGang/svc-core/internal/api"
	"github.com/GGingGGang/svc-core/internal/config"
	"github.com/GGingGGang/svc-core/internal/db"
	"github.com/GGingGGang/svc-core/internal/events"
	authmw "github.com/GGingGGang/svc-core/internal/middleware"
	"github.com/GGingGGang/svc-core/internal/observability"
	"github.com/GGingGGang/svc-core/internal/service"
)

var version = "dev" // -ldflags "-X main.version=<GIT_SHA>"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal("load config failed")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if err := db.MigrateUp(ctx, cfg); err != nil {
		log.Fatal("migrate database failed")
	}

	shutdownTracing, err := observability.SetupTracing(ctx)
	if err != nil {
		log.Fatal("setup tracing failed")
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			log.Printf("tracing shutdown failed")
		}
	}()

	sqlDB, err := db.Connect(ctx, cfg)
	if err != nil {
		log.Fatal("connect db failed")
	}
	defer sqlDB.Close()
	if err := db.CheckReady(ctx, sqlDB); err != nil {
		log.Fatal("database readiness failed")
	}

	jwtAuth, err := authmw.NewJWTAuth(cfg.JWKSURL, cfg.AuthIntrospectURL, cfg.JWTIssuer, cfg.JWTAudience)
	if err != nil {
		log.Fatal("setup jwt auth failed")
	}

	publisher := setupEventPublisher(ctx, cfg.NATSURL)
	aiClient := ai.New(cfg.GeminiBaseURL, cfg.GeminiModel, cfg.GeminiAPIKey)

	coreService := service.New(sqlDB, publisher, aiClient)
	go coreService.RunOutbox(ctx)
	handler := api.NewHandler(coreService, func(ctx context.Context) error {
		return db.CheckReady(ctx, sqlDB)
	})

	srv := &http.Server{
		Addr:    ":" + cfg.HTTPPort,
		Handler: otelhttp.NewHandler(api.CORS(os.Getenv("CORS_ALLOWED_ORIGINS"))(api.Router(handler, jwtAuth.Middleware)), "svc-core"),
	}

	log.Printf("svc-core %s listening on :%s", version, cfg.HTTPPort)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-errCh:
		log.Fatal("server failed")
	case <-ctx.Done():
		log.Printf("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed")
	}
}

// setupEventPublisher connects to NATS and declares the APP_SCHEDULES
// stream (../PLAN.md §7.2, create-or-update — no human action needed). A
// connection failure here is logged, not fatal: schedule event publishing
// is best-effort (../PLAN.md §7), so this service still serves /schedules*
// while NATS is unreachable, and every publish attempt in that window fails
// fast and is counted by domain_event_publish_failed_total.
func setupEventPublisher(ctx context.Context, natsURL string) *events.Publisher {
	publisher := events.NewPublisher(nil)
	go func() {
		for ctx.Err() == nil {
			nc, js, err := events.Connect(natsURL)
			if err == nil {
				streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err = events.EnsureStream(streamCtx, js)
				cancel()
				if err == nil {
					publisher.SetJetStream(js)
					<-ctx.Done()
					publisher.SetJetStream(nil)
					nc.Close()
					return
				}
				nc.Close()
			}
			log.Printf("nats setup failed, retrying")
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	return publisher
}
