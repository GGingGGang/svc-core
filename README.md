# k8s-test-core-server

Go 1.23 / chi — schedule domain API + AI (Gemini) text extraction.

## Status

## Ports

| Port | Purpose |
|------|---------|
| `8080` | HTTP API |
| `9090` | `/metrics` (OTel Prometheus exporter) |

## Environment Variables

```bash
HTTP_PORT=8080                                   # listen port (default 8080)
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


## Roadmap
