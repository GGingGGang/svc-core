# k8s-test-core-server

Go 1.23 / chi — schedule domain API + AI (Gemini) text extraction.

## Status

Bootstrap stage moving into the schedule domain. HTTP server exposing
health/readiness endpoints, built and shipped through Kaniko → GHCR → ArgoCD onto
Kubernetes. Domain artifacts landed: SQL schema (`db/migrations`), sqlc queries
(`db/queries`, `sqlc.yaml`), and the Gemini extraction client (`internal/ai`).
Handlers/service/repo/event wiring follow the milestones in `PLAN.md`.

## Ports

| Port | Purpose |
|------|---------|
| `8080` | HTTP API |
| `9090` | `/metrics` (OTel Prometheus exporter) |

## Environment Variables

```bash
HTTP_PORT=8080                                   # listen port (default 8080)
JWKS_URL=http://auth.auth.svc.cluster.local:3000/.well-known/jwks.json
GEMINI_MODEL=gemini-2.0-flash                    # free tier
GEMINI_BASE_URL=https://generativelanguage.googleapis.com
GEMINI_API_KEY=...                               # secret (ExternalSecret in cluster)
# DB DSN / Redis password 도 secret 으로 주입
```

## Database

```bash
# golang-migrate (db/migrations/000001_init.{up,down}.sql)
migrate -path db/migrations -database "mysql://$DSN" up
# sqlc 코드 생성 (db/queries → internal/repo/sqlc)
sqlc generate
```

## Local Development

```bash
go mod download
docker compose up -d mysql redis kafka
go run ./cmd/server
go test ./...
```

## Build

```bash
docker build --build-arg GIT_SHA=$(git rev-parse --short HEAD) -t core .
```

## API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Liveness probe → `{"status":"ok"}` |
| GET | `/readyz` | Readiness probe → `{"status":"ready"}` |
| POST | `/schedules/extract` | `{text, now, timezone}` → Gemini → candidate schedules (not saved) |
| POST | `/schedules` | Create schedule (`source=manual\|ai`) |
| GET | `/schedules?from=&to=&status=` | List by range |
| GET / PATCH / DELETE | `/schedules/{id}` | Single schedule |
| GET / POST / DELETE | `/schedules/{id}/reminders` | Reminders |

All `/schedules*` require `Authorization: Bearer <access>`; `user_id` is the JWT `sub`.
See `../CONTRACTS.md` §3.2 and `../EVENTS.md` §3.

## Roadmap

Implemented: SQL schema + sqlc queries, Gemini extraction client (`internal/ai`).
Pending per `PLAN.md`: handlers/service/repo (M3), JWKS auth middleware (M4),
`/schedules/extract` wiring + Kafka producer `schedules.*.v1` (M5), OTel `:9090`.
