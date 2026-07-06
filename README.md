# svc-core

Go 1.23 / chi — schedule domain API + AI (Gemini) text extraction.

## Ports

| Port | Purpose |
|------|---------|
| `8080` | HTTP API + `/metrics` (single port) |

## Environment Variables

```bash
HTTP_PORT=8080       # listen port (default 8080)
DB_HOST=             # required
DB_PORT=3306         # default 3306
DB_USER=             # required
DB_PASSWORD=         # required, no default — never commit
DB_NAME=             # required
DB_TLS=true          # default true (HeatWave requires TLS); set false for local/testcontainers MySQL
```

Auth (1M only): every `/schedules*` request requires an `X-User-Id: <uuid>` header (temporary — replaced by JWKS validation in 2M).

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
| POST | `/schedules` | Create a schedule (optionally with `reminders`) |
| GET | `/schedules?from=&to=&status=` | List schedules in an RFC3339 UTC range |
| GET | `/schedules/{id}` | Get one schedule (includes reminders) |
| PATCH | `/schedules/{id}` | Partial update |
| DELETE | `/schedules/{id}` | Hard delete → 204 |
| POST | `/schedules/bulk-delete` | `{ids:[...]}` (max 100) → `{deleted: n}` |
| GET | `/schedules/{id}/reminders` | List reminders |
| POST | `/schedules/{id}/reminders` | Add a reminder |
| DELETE | `/schedules/{id}/reminders/{reminderId}` | Remove a reminder |

Accessing another user's schedule (or a nonexistent one) always returns 404, never 403.
