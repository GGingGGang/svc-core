> 이 애플리케이션 레포지토리는 AI 코드 에이전트가 구현했습니다.

# svc-core

Go 1.25 / chi — schedule domain API + AI (Gemini) text extraction.

Full cross-service E2E scenario (paste text → extract → confirm → event → batch reminder sent/skipped) is a
manual runbook, not an automated test — see [`E2E.md`](./E2E.md).

(Toolchain bumped from 1.23 to 1.25: the testcontainers-go integration-test
dependency's own go.mod requires it. Application code has no 1.24+/1.25+
language-feature dependency — the bump only affects the build image.)

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
JWKS_URL=http://auth.auth.svc.cluster.local:3000/.well-known/jwks.json  # default shown; in-cluster auth JWKS endpoint
JWT_ISSUER=          # required, no default (environment-specific, e.g. auth.example.com)
JWT_AUDIENCE=core    # default core, matches the fixed contract value
NATS_URL=nats://nats.data.svc.cluster.local:4222  # default shown; in-cluster JetStream broker
OTEL_TRACES_EXPORTER=none  # default none if unset; set otlp once a collector exists

GEMINI_BASE_URL=https://generativelanguage.googleapis.com  # default shown
GEMINI_MODEL=gemini-2.0-flash                               # default shown
GEMINI_API_KEY=      # optional — fallback used only when a request omits the X-Gemini-Key BYOK header; never commit
```

Auth: every `/schedules*` request requires `Authorization: Bearer <access-token>` — a JWT (ES256) issued by the auth
service, verified locally against its JWKS (in-memory cache, lazy refresh on an unknown `kid`). `iss` and `aud` are
checked against `JWT_ISSUER`/`JWT_AUDIENCE`. The authenticated user id always comes from the token's `sub` claim —
never from a request body or path parameter. Requests without a valid token get 401.

## Database

```bash
# golang-migrate (db/migrations/000001_init.{up,down}.sql)
migrate -path db/migrations -database "mysql://$DSN" up
# sqlc 코드 생성 (db/queries → internal/repo)
sqlc generate
```

## Schedule domain events (NATS JetStream)

On startup this service connects to `NATS_URL` and declares (create-or-update, idempotent — no human
action needed) a `APP_SCHEDULES` stream with exactly three subjects (never a wildcard, so it cannot
collide with the DLQ stream batch owns):

```
app.schedules.created.v1
app.schedules.updated.v1
app.schedules.deleted.v1
```

Every schedule mutation publishes the matching event after its DB write commits, using the database's
own clock (`SELECT UTC_TIMESTAMP(3)`, not application time) as `occurred_at`:

| action | subject |
|--------|---------|
| `POST /schedules` | `app.schedules.created.v1` |
| `PATCH /schedules/{id}` | `app.schedules.updated.v1` |
| `POST /schedules/{id}/reminders`, `DELETE /schedules/{id}/reminders/{reminderId}` | `app.schedules.updated.v1` (full reminders snapshot) |
| `DELETE /schedules/{id}` | `app.schedules.deleted.v1` |
| `POST /schedules/bulk-delete` | `app.schedules.deleted.v1` per id actually deleted |

Every publish carries `Content-Type: application/json`, a `Nats-Msg-Id: <subject>:<schedule_id>:<occurred_at>`
header so JetStream's server-side dedup window absorbs publish retries, and a W3C `traceparent` header
propagated from the request's trace context via the composite `TraceContext`+`Baggage` propagator.

Publishing is best-effort: if NATS is unreachable (at startup or at publish time), the HTTP request still
succeeds — the failure is logged and counted (`domain_event_publish_failed_total{subject}`), never
surfaced as a 5xx to the caller. See `internal/events` for the publisher/stream-declaration code and
`internal/events/*_integration_test.go` / `internal/api/events_integration_test.go` for testcontainers-go
coverage (dedup, header shape, reminders-snapshot semantics, per-id bulk-delete correctness).

## Metrics

`/metrics` (same port as the API, see above) exposes, alongside the Go/process default collectors:

| metric | type | labels | meaning |
|--------|------|--------|---------|
| `http_server_requests_total` | counter | `method`, `route`, `status` | RED — request rate / error rate |
| `http_server_request_duration_seconds` | histogram | `method`, `route`, `status` | RED — latency |
| `domain_event_published_total` | counter | `subject` | successful NATS publishes |
| `domain_event_publish_failed_total` | counter | `subject` | failed NATS publish attempts |

`route` is chi's matched path template (e.g. `/schedules/{id}`), never a raw path, to keep cardinality bounded.

## Local Development

```bash
go mod download
docker run -d -p 3306:3306 -e MYSQL_ROOT_PASSWORD=root -e MYSQL_DATABASE=core mysql:8
migrate -path db/migrations -database "mysql://root:root@tcp(localhost:3306)/core" up
DB_HOST=127.0.0.1 DB_USER=root DB_PASSWORD=root DB_NAME=core DB_TLS=false \
  JWT_ISSUER=http://auth.auth.svc.cluster.local:3000 go run ./cmd/server
go test ./...
```

## Testing

`go test ./...` runs unit-level checks only — no external services required. This includes
`internal/events/publisher_test.go`, which covers the nil-JetStream (NATS unreachable) fail-fast-and-count
path without Docker.

`internal/api/integration_test.go` is a full HTTP-level integration test — testcontainers-go boots a real
MySQL 8 container, applies `db/migrations/000001_init.up.sql`, and drives `/schedules` CRUD, the reminders
sub-resource, bulk-delete, cross-user 404 scoping, and JWT authentication (valid token → 200, missing/malformed/
expired/wrong-key/wrong-issuer/wrong-audience → 401) through the same router/service stack `cmd/server` uses.
The auth service is not required to be running: the test generates its own ES256 key pair, serves it as a JWKS
from a local `httptest` server, and mints tokens signed against that key.

`internal/events/stream_integration_test.go` boots a real NATS server (JetStream enabled) and covers stream
declaration (exact subjects, idempotent create-or-update) and publishing (headers, dedup, payload round-trip)
in isolation. `internal/api/events_integration_test.go` boots MySQL *and* NATS together and drives every
mutating `/schedules*` endpoint through the real HTTP/service stack, asserting the resulting
`app.schedules.*.v1` events land on the stream in order with the right subject, reminders snapshot, and
per-id bulk-delete correctness, plus that `domain_event_published_total`/`http_server_requests_total` show up
on `/metrics`.

`internal/ai/gemini_test.go` is a DB-free unit suite covering the Gemini client in isolation against a local
`httptest` stub: BYOK header takes priority over the server fallback key, no key at all fails fast without an
HTTP call, a 429/5xx is retried exactly once and then either succeeds or turns into `RateLimitedError` carrying
the upstream's `Retry-After`, and a non-retryable status (e.g. 400) fails immediately without a retry.

`internal/api/extract_integration_test.go` boots a real MySQL container and drives `POST /schedules/extract`
through the full HTTP/service stack against a local Gemini stub (never the real API): a happy-path call returns
candidates and persists a `success` `ai_extractions` row, text over 4000 chars is rejected with `413` before the
stub is ever called, an invalid `timezone` is `400`, and a stub stuck on `429` surfaces as `429` +
`Retry-After` to the caller with a `failed` audit row recorded.

All of the above are gated behind a build tag so CI without a Docker daemon still passes `go test ./...`:

```bash
go test -tags=integration ./...
```

Requires a running Docker daemon (Docker Desktop or equivalent) reachable from the test process.

### CI

`.github/workflows/test.yml` runs the full suite (`go test -tags=integration ./...`) on every push to `main`
and every PR, on `ubuntu-latest` (Docker preinstalled, no extra setup). Jenkins runs the unit-only gate
(`go test ./...`, no Docker) ahead of image build; see `../test-contract.md` for the full contract.

## Build

```bash
docker build --build-arg GIT_SHA=$(git rev-parse --short HEAD) -t core .
```

## API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Liveness probe → `{"status":"ok"}` |
| GET | `/readyz` | Readiness probe → `{"status":"ready"}` |
| GET | `/openapi.yaml` | OpenAPI 3.0 spec for this API (see below) |
| POST | `/schedules` | Create a schedule (optionally with `reminders`) |
| GET | `/schedules?from=&to=&status=` | List schedules in an RFC3339 UTC range |
| GET | `/schedules/{id}` | Get one schedule (includes reminders) |
| PATCH | `/schedules/{id}` | Partial update |
| DELETE | `/schedules/{id}` | Hard delete → 204 |
| POST | `/schedules/bulk-delete` | `{ids:[...]}` (max 100) → `{deleted: n}` |
| GET | `/schedules/{id}/reminders` | List reminders |
| POST | `/schedules/{id}/reminders` | Add a reminder |
| DELETE | `/schedules/{id}/reminders/{reminderId}` | Remove a reminder |
| POST | `/schedules/extract` | `{text, now, timezone}` → Gemini extraction → `{candidates:[...]}` (not persisted) |

Accessing another user's schedule (or a nonexistent one) always returns 404, never 403.

## Gemini text extraction

`POST /schedules/extract` (`internal/ai`) turns free-form text into schedule candidates without persisting anything — the client confirms a candidate by
POSTing it to `/schedules` with `source=ai` separately.

- **Key priority**: request header `X-Gemini-Key` (BYOK) → server env `GEMINI_API_KEY` fallback. Neither present → `502`. The key is never logged, put in
  a metric label, or stored.
- **Structured output**: the Gemini request sets `responseSchema` so the model's JSON output matches the `candidates` shape directly — no separate
  parsing/mapping step for the happy path.
- **Limits**: `text` over 4000 chars → `413` before any Gemini call. Each Gemini HTTP attempt has a 10s timeout. A `429`/5xx response is retried exactly
  once after a fixed backoff; if the retry also fails, the caller gets `429` with a `Retry-After` header (taken from Gemini's own `Retry-After` when
  present, otherwise a fixed default).
- **Audit trail**: every call — success, or failure for any reason (missing key, upstream error, malformed response) — writes exactly one `ai_extractions`
  row (`status` success/partial/failed, `latency_ms`, `raw_text`). The insert itself is best-effort: a DB error there is logged, not surfaced, since the
  caller's actual request already ran to completion by that point.
- **Timezone**: `now`/`timezone` let the model convert relative expressions ("다음주 화요일") into absolute UTC timestamps. `timezone` is validated with
  Go's `time.LoadLocation` — the binary embeds the IANA zoneinfo database (`time/tzdata`) since the distroless runtime image ships none.

## OpenAPI spec

`api/openapi.yaml` is the OpenAPI 3.0 contract for every endpoint above, hand-written to match the
implementation exactly (chi has no Fastify-swagger-style auto generator). It is embedded into the
binary at build time (`api/openapi.go`, `go:embed`) and served as-is:

```bash
curl http://localhost:8080/openapi.yaml
```

Paste that output into any OpenAPI viewer (Swagger Editor, Redocly, etc.) to browse it interactively —
this service does not bundle a UI. The bearer JWT auth documented there matches the scheme described above.
