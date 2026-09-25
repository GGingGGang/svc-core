//go:build integration

// Integration tests spin up a real MySQL container (testcontainers-go) and
// exercise the HTTP API end to end: schedule CRUD, reminders sub-resource,
// bulk-delete, user_id scoping (cross-user access must 404, never 403 or
// leak another user's data), and JWT authentication (../PLAN.md §4.3). The
// auth service does not need to be running: a self-generated ES256 key pair
// is served as a JWKS from a local httptest server, and tokens are minted
// and signed against that same key in-process. Requires a running Docker
// daemon; run with `go test -tags=integration ./...`.
package api_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/stretchr/testify/require"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/GGingGGang/svc-core/internal/ai"
	"github.com/GGingGGang/svc-core/internal/api"
	coredb "github.com/GGingGGang/svc-core/internal/db"
	"github.com/GGingGGang/svc-core/internal/events"
	authmw "github.com/GGingGGang/svc-core/internal/middleware"
	"github.com/GGingGGang/svc-core/internal/service"
)

const (
	testIssuer   = "https://auth.test.example/"
	testAudience = "core"
	testKeyID    = "test-kid-1"
)

type reminderJSON struct {
	ID            string `json:"id"`
	ScheduleID    string `json:"schedule_id"`
	MinutesBefore int32  `json:"minutes_before"`
	Channel       string `json:"channel"`
}

type scheduleJSON struct {
	ID        string         `json:"id"`
	UserID    string         `json:"user_id"`
	Title     string         `json:"title"`
	StartAt   time.Time      `json:"start_at"`
	Status    string         `json:"status"`
	Source    string         `json:"source"`
	Reminders []reminderJSON `json:"reminders"`
}

// jwksFixture is a self-signed ES256 key pair served as a JWKS from a local
// httptest server, standing in for the auth service's real
// /.well-known/jwks.json for the duration of a test.
type jwksFixture struct {
	server              *httptest.Server
	url                 string
	private             jwk.Key
	introspectionStatus *atomic.Int32
}

func newJWKSFixture(t *testing.T) *jwksFixture {
	t.Helper()

	raw, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	priv, err := jwk.FromRaw(raw)
	require.NoError(t, err)
	require.NoError(t, priv.Set(jwk.KeyIDKey, testKeyID))
	require.NoError(t, priv.Set(jwk.AlgorithmKey, jwa.ES256))

	pub, err := jwk.PublicKeyOf(priv)
	require.NoError(t, err)
	require.NoError(t, pub.Set(jwk.KeyIDKey, testKeyID))
	require.NoError(t, pub.Set(jwk.AlgorithmKey, jwa.ES256))

	set := jwk.NewSet()
	require.NoError(t, set.AddKey(pub))

	mux := http.NewServeMux()
	introspectionStatus := new(atomic.Int32)
	introspectionStatus.Store(http.StatusOK)
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(set))
	})
	mux.HandleFunc("/introspect", func(w http.ResponseWriter, r *http.Request) {
		if status := int(introspectionStatus.Load()); status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &jwksFixture{server: srv, url: srv.URL + "/.well-known/jwks.json", private: priv, introspectionStatus: introspectionStatus}
}

// mint signs an access token for subject (the user id) using the fixture's
// private key, matching the claim shape ../PLAN.md §4.1 requires from auth.
func (f *jwksFixture) mint(t *testing.T, subject string, ttl time.Duration) string {
	t.Helper()
	return f.mintWithClaims(t, subject, testIssuer, []string{testAudience}, ttl)
}

func (f *jwksFixture) mintWithClaims(t *testing.T, subject, issuer string, audience []string, ttl time.Duration) string {
	t.Helper()
	now := time.Now()
	tok, err := jwt.NewBuilder().
		Issuer(issuer).
		Subject(subject).
		Audience(audience).
		IssuedAt(now).
		Expiration(now.Add(ttl)).
		JwtID(uuid.New().String()).
		Claim("scope", "read:schedules write:schedules").
		Build()
	require.NoError(t, err)

	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.ES256, f.private))
	require.NoError(t, err)
	return string(signed)
}

// setupServer starts a MySQL container, applies the golang-migrate up
// migration via the image's docker-entrypoint-initdb.d mechanism, and
// returns an httptest server wired to the real DB and a JWKS-backed JWT auth
// middleware through the same api.Router/service.Service stack
// cmd/server/main.go uses. Schedule events are not published (pub=nil) —
// see internal/events for NATS-backed publisher coverage and
// events_integration_test.go in this package for the full HTTP+NATS path.
func setupServer(t *testing.T) (*httptest.Server, *jwksFixture) {
	t.Helper()
	srv, jwks, _ := setupServerWithPublisher(t, nil, nil)
	return srv, jwks
}

// setupServerWithPublisher wires the same api.Router/service.Service stack
// cmd/server/main.go uses, and also returns the underlying *sql.DB so tests
// can assert on rows a handler wrote as a side effect (e.g. ai_extractions —
// see extract_integration_test.go). aiClnt may be nil — schedule CRUD tests
// never call /schedules/extract, so a nil *ai.Client (never dereferenced) is
// fine; extract_integration_test.go passes a real one pointed at a local
// Gemini stub.
func setupServerWithPublisher(t *testing.T, pub *events.Publisher, aiClnt *ai.Client) (*httptest.Server, *jwksFixture, *sql.DB) {
	t.Helper()
	ctx := context.Background()

	container, err := tcmysql.Run(ctx, "mysql:8.0.36",
		tcmysql.WithDatabase("core"),
		tcmysql.WithUsername("core_test"),
		tcmysql.WithPassword("core_test"),
		tcmysql.WithScripts("../../db/migrations/000001_init.up.sql", "../../db/migrations/000002_event_outbox.up.sql", "../../db/migrations/000003_schedule_create_requests.up.sql", "../../db/migrations/000004_ai_request_limits.up.sql", "../../db/migrations/000005_schedule_revision.up.sql", "../../db/migrations/000006_schedule_mutation_requests.up.sql"),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, container.Terminate(context.Background()))
	})

	dsn, err := container.ConnectionString(ctx, "parseTime=true", "loc=UTC")
	require.NoError(t, err)

	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.Eventually(t, func() bool {
		return db.PingContext(ctx) == nil
	}, 30*time.Second, 500*time.Millisecond, "db should become reachable")

	jwks := newJWKSFixture(t)
	jwtAuth, err := authmw.NewJWTAuth(jwks.url, jwks.server.URL+"/introspect", testIssuer, testAudience)
	require.NoError(t, err)

	coreService := service.New(db, pub, aiClnt)
	if pub != nil {
		workerCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			coreService.RunOutbox(workerCtx)
			close(done)
		}()
		t.Cleanup(func() { cancel(); <-done })
	}
	handler := api.NewHandler(coreService, func(ctx context.Context) error { return coredb.CheckReady(ctx, db) })
	srv := httptest.NewServer(api.Router(handler, jwtAuth.Middleware))
	t.Cleanup(srv.Close)
	return srv, jwks, db
}

func doRequest(t *testing.T, client *http.Client, method, url, token string, body any) (int, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, url, reader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, respBody
}

func TestSchedulesIntegration(t *testing.T) {
	srv, jwks := setupServer(t)
	client := srv.Client()

	userAID := uuid.New().String()
	userBID := uuid.New().String()
	userA := jwks.mint(t, userAID, time.Hour)
	userB := jwks.mint(t, userBID, time.Hour)

	t.Run("create, get, list happy path", func(t *testing.T) {
		status, body := doRequest(t, client, http.MethodPost, srv.URL+"/schedules", userA, map[string]any{
			"title":    "회의",
			"start_at": "2026-08-01T06:00:00Z",
			"all_day":  false,
			"reminders": []map[string]any{
				{"minutes_before": 30, "channel": "push"},
			},
		})
		require.Equal(t, http.StatusCreated, status, string(body))

		var created scheduleJSON
		require.NoError(t, json.Unmarshal(body, &created))
		require.NotEmpty(t, created.ID)
		require.Equal(t, "confirmed", created.Status)
		require.Equal(t, "manual", created.Source)
		require.Len(t, created.Reminders, 1)
		require.Equal(t, int32(30), created.Reminders[0].MinutesBefore)

		// owner can fetch it back.
		status, body = doRequest(t, client, http.MethodGet, srv.URL+"/schedules/"+created.ID, userA, nil)
		require.Equal(t, http.StatusOK, status, string(body))
		var fetched scheduleJSON
		require.NoError(t, json.Unmarshal(body, &fetched))
		require.Equal(t, created.ID, fetched.ID)

		// a different user gets 404, not 403 and not the record.
		status, _ = doRequest(t, client, http.MethodGet, srv.URL+"/schedules/"+created.ID, userB, nil)
		require.Equal(t, http.StatusNotFound, status)

		// range list covers the created schedule for the owner.
		status, body = doRequest(t, client, http.MethodGet,
			srv.URL+"/schedules?from=2026-07-01T00:00:00Z&to=2026-09-01T00:00:00Z", userA, nil)
		require.Equal(t, http.StatusOK, status, string(body))
		var listResp struct {
			Schedules []scheduleJSON `json:"schedules"`
		}
		require.NoError(t, json.Unmarshal(body, &listResp))
		require.Len(t, listResp.Schedules, 1)
		require.Equal(t, created.ID, listResp.Schedules[0].ID)

		// same range is empty for another user.
		status, body = doRequest(t, client, http.MethodGet,
			srv.URL+"/schedules?from=2026-07-01T00:00:00Z&to=2026-09-01T00:00:00Z", userB, nil)
		require.Equal(t, http.StatusOK, status, string(body))
		require.NoError(t, json.Unmarshal(body, &listResp))
		require.Len(t, listResp.Schedules, 0)
	})

	t.Run("patch updates fields and is scoped to the owner", func(t *testing.T) {
		_, body := doRequest(t, client, http.MethodPost, srv.URL+"/schedules", userA, map[string]any{
			"title":    "초안 제목",
			"start_at": "2026-08-02T06:00:00Z",
		})
		var created scheduleJSON
		require.NoError(t, json.Unmarshal(body, &created))

		// another user cannot patch it.
		status, _ := doRequest(t, client, http.MethodPatch, srv.URL+"/schedules/"+created.ID, userB, map[string]any{
			"title": "탈취 시도",
		})
		require.Equal(t, http.StatusNotFound, status)

		// owner can patch a subset of fields.
		status, body = doRequest(t, client, http.MethodPatch, srv.URL+"/schedules/"+created.ID, userA, map[string]any{
			"title":  "수정된 제목",
			"status": "tentative",
		})
		require.Equal(t, http.StatusOK, status, string(body))
		var updated scheduleJSON
		require.NoError(t, json.Unmarshal(body, &updated))
		require.Equal(t, "수정된 제목", updated.Title)
		require.Equal(t, "tentative", updated.Status)
	})

	t.Run("reminders sub-resource add/list/delete", func(t *testing.T) {
		_, body := doRequest(t, client, http.MethodPost, srv.URL+"/schedules", userA, map[string]any{
			"title":    "리마인더 테스트",
			"start_at": "2026-08-03T06:00:00Z",
		})
		var created scheduleJSON
		require.NoError(t, json.Unmarshal(body, &created))

		status, body := doRequest(t, client, http.MethodPost, srv.URL+"/schedules/"+created.ID+"/reminders", userA, map[string]any{
			"minutes_before": 10,
			"channel":        "email",
		})
		require.Equal(t, http.StatusCreated, status, string(body))
		var added reminderJSON
		require.NoError(t, json.Unmarshal(body, &added))
		require.NotEmpty(t, added.ID)

		status, body = doRequest(t, client, http.MethodGet, srv.URL+"/schedules/"+created.ID+"/reminders", userA, nil)
		require.Equal(t, http.StatusOK, status, string(body))
		var listResp struct {
			Reminders []reminderJSON `json:"reminders"`
		}
		require.NoError(t, json.Unmarshal(body, &listResp))
		require.Len(t, listResp.Reminders, 1)

		// another user cannot list or delete reminders on someone else's schedule.
		status, _ = doRequest(t, client, http.MethodGet, srv.URL+"/schedules/"+created.ID+"/reminders", userB, nil)
		require.Equal(t, http.StatusNotFound, status)
		status, _ = doRequest(t, client, http.MethodDelete, srv.URL+"/schedules/"+created.ID+"/reminders/"+added.ID, userB, nil)
		require.Equal(t, http.StatusNotFound, status)

		status, _ = doRequest(t, client, http.MethodDelete, srv.URL+"/schedules/"+created.ID+"/reminders/"+added.ID, userA, nil)
		require.Equal(t, http.StatusNoContent, status)
	})

	t.Run("delete is hard delete and scoped", func(t *testing.T) {
		_, body := doRequest(t, client, http.MethodPost, srv.URL+"/schedules", userA, map[string]any{
			"title":    "삭제 대상",
			"start_at": "2026-08-04T06:00:00Z",
		})
		var created scheduleJSON
		require.NoError(t, json.Unmarshal(body, &created))

		status, _ := doRequest(t, client, http.MethodDelete, srv.URL+"/schedules/"+created.ID, userB, nil)
		require.Equal(t, http.StatusNotFound, status)

		status, _ = doRequest(t, client, http.MethodDelete, srv.URL+"/schedules/"+created.ID, userA, nil)
		require.Equal(t, http.StatusNoContent, status)

		status, _ = doRequest(t, client, http.MethodGet, srv.URL+"/schedules/"+created.ID, userA, nil)
		require.Equal(t, http.StatusNotFound, status)
	})

	t.Run("bulk-delete only removes ids owned by the caller", func(t *testing.T) {
		var ownedIDs []string
		for i := 0; i < 3; i++ {
			_, body := doRequest(t, client, http.MethodPost, srv.URL+"/schedules", userA, map[string]any{
				"title":    "일괄 삭제",
				"start_at": "2026-08-05T06:00:00Z",
			})
			var created scheduleJSON
			require.NoError(t, json.Unmarshal(body, &created))
			ownedIDs = append(ownedIDs, created.ID)
		}

		_, body := doRequest(t, client, http.MethodPost, srv.URL+"/schedules", userB, map[string]any{
			"title":    "다른 유저 일정",
			"start_at": "2026-08-05T06:00:00Z",
		})
		var foreign scheduleJSON
		require.NoError(t, json.Unmarshal(body, &foreign))

		status, body := doRequest(t, client, http.MethodPost, srv.URL+"/schedules/bulk-delete", userA, map[string]any{
			"ids": append(append([]string{}, ownedIDs...), foreign.ID),
		})
		require.Equal(t, http.StatusOK, status, string(body))
		var result struct {
			Deleted int64 `json:"deleted"`
		}
		require.NoError(t, json.Unmarshal(body, &result))
		require.Equal(t, int64(3), result.Deleted, "only the caller's own 3 schedules should be deleted")

		// the foreign schedule must still exist.
		status, _ = doRequest(t, client, http.MethodGet, srv.URL+"/schedules/"+foreign.ID, userB, nil)
		require.Equal(t, http.StatusOK, status)
	})

	t.Run("missing bearer token is unauthorized", func(t *testing.T) {
		status, _ := doRequest(t, client, http.MethodGet, srv.URL+"/schedules", "", nil)
		require.Equal(t, http.StatusUnauthorized, status)
	})

	t.Run("malformed bearer token is unauthorized", func(t *testing.T) {
		status, _ := doRequest(t, client, http.MethodGet, srv.URL+"/schedules", "not-a-jwt", nil)
		require.Equal(t, http.StatusUnauthorized, status)
	})

	t.Run("token signed by a different key is unauthorized", func(t *testing.T) {
		otherKey := newJWKSFixture(t) // its public key is never registered with the server's JWKS
		forged := otherKey.mint(t, uuid.New().String(), time.Hour)

		status, _ := doRequest(t, client, http.MethodGet, srv.URL+"/schedules", forged, nil)
		require.Equal(t, http.StatusUnauthorized, status)
	})

	t.Run("expired token is unauthorized", func(t *testing.T) {
		expired := jwks.mint(t, uuid.New().String(), -time.Minute)

		status, _ := doRequest(t, client, http.MethodGet, srv.URL+"/schedules", expired, nil)
		require.Equal(t, http.StatusUnauthorized, status)
	})

	t.Run("wrong audience is unauthorized", func(t *testing.T) {
		wrongAud := jwks.mintWithClaims(t, uuid.New().String(), testIssuer, []string{"some-other-service"}, time.Hour)

		status, _ := doRequest(t, client, http.MethodGet, srv.URL+"/schedules", wrongAud, nil)
		require.Equal(t, http.StatusUnauthorized, status)
	})

	t.Run("wrong issuer is unauthorized", func(t *testing.T) {
		wrongIss := jwks.mintWithClaims(t, uuid.New().String(), "https://not-auth.example/", []string{testAudience}, time.Hour)

		status, _ := doRequest(t, client, http.MethodGet, srv.URL+"/schedules", wrongIss, nil)
		require.Equal(t, http.StatusUnauthorized, status)
	})

	t.Run("current account status is checked after JWT verification", func(t *testing.T) {
		jwks.introspectionStatus.Store(http.StatusUnauthorized)
		status, _ := doRequest(t, client, http.MethodGet, srv.URL+"/schedules", userA, nil)
		require.Equal(t, http.StatusUnauthorized, status)
		jwks.introspectionStatus.Store(http.StatusServiceUnavailable)
		status, _ = doRequest(t, client, http.MethodGet, srv.URL+"/schedules", userA, nil)
		require.Equal(t, http.StatusServiceUnavailable, status)
		jwks.introspectionStatus.Store(http.StatusOK)
	})
}

func TestScheduleEventRevisions(t *testing.T) {
	srv, jwks, db := setupServerWithPublisher(t, nil, nil)
	token := jwks.mint(t, uuid.New().String(), time.Hour)
	status, body := doRequest(t, srv.Client(), http.MethodPost, srv.URL+"/schedules", token, map[string]any{
		"title": "meeting", "start_at": "2026-08-01T06:00:00Z",
	})
	require.Equal(t, http.StatusCreated, status, string(body))
	var created scheduleJSON
	require.NoError(t, json.Unmarshal(body, &created))
	status, body = doRequest(t, srv.Client(), http.MethodPatch, srv.URL+"/schedules/"+created.ID, token, map[string]any{"status": "cancelled"})
	require.Equal(t, http.StatusOK, status, string(body))
	status, body = doRequest(t, srv.Client(), http.MethodPost, srv.URL+"/schedules/"+created.ID+"/reminders", token, map[string]any{"minutes_before": 10, "channel": "email"})
	require.Equal(t, http.StatusCreated, status, string(body))
	var reminder reminderJSON
	require.NoError(t, json.Unmarshal(body, &reminder))
	status, body = doRequest(t, srv.Client(), http.MethodDelete, srv.URL+"/schedules/"+created.ID+"/reminders/"+reminder.ID, token, nil)
	require.Equal(t, http.StatusNoContent, status, string(body))
	status, body = doRequest(t, srv.Client(), http.MethodDelete, srv.URL+"/schedules/"+created.ID, token, nil)
	require.Equal(t, http.StatusNoContent, status, string(body))
	id := uuid.MustParse(created.ID)
	rows, err := db.Query("SELECT payload FROM event_outbox WHERE schedule_id = ?", id[:])
	require.NoError(t, err)
	defer rows.Close()
	revisions := map[int64]string{}
	for rows.Next() {
		var payload []byte
		require.NoError(t, rows.Scan(&payload))
		var event struct {
			Revision int64  `json:"revision"`
			Status   string `json:"status"`
		}
		require.NoError(t, json.Unmarshal(payload, &event))
		revisions[event.Revision] = event.Status
	}
	require.NoError(t, rows.Err())
	require.Equal(t, map[int64]string{1: "confirmed", 2: "cancelled", 3: "cancelled", 4: "cancelled", 5: ""}, revisions)
}

func TestCreateScheduleIdempotency(t *testing.T) {
	srv, jwks, db := setupServerWithPublisher(t, nil, nil)
	user := jwks.mint(t, uuid.New().String(), time.Hour)
	otherUser := jwks.mint(t, uuid.New().String(), time.Hour)
	key := uuid.New().String()
	body := `{"title":"retry me","start_at":"2026-10-01T09:00:00Z","reminders":[{"minutes_before":15,"channel":"push"}]}`
	post := func(token, payload string) (int, scheduleJSON) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/schedules", bytes.NewBufferString(payload))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var schedule scheduleJSON
		if resp.StatusCode == http.StatusCreated {
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&schedule))
		}
		return resp.StatusCode, schedule
	}

	status, first := post(user, body)
	require.Equal(t, http.StatusCreated, status)
	status, replay := post(user, body)
	require.Equal(t, http.StatusCreated, status)
	require.Equal(t, first, replay, "retry must return the original schedule and reminder IDs")

	status, _ = post(user, `{"title":"different","start_at":"2026-10-01T09:00:00Z"}`)
	require.Equal(t, http.StatusConflict, status)
	status, separate := post(otherUser, body)
	require.Equal(t, http.StatusCreated, status)
	require.NotEqual(t, first.ID, separate.ID, "the key is scoped to one user")

	var schedules, events int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM schedules").Scan(&schedules))
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM event_outbox").Scan(&events))
	require.Equal(t, 2, schedules)
	require.Equal(t, 2, events)

	status, _ = doRequest(t, srv.Client(), http.MethodDelete, srv.URL+"/schedules/"+first.ID, user, nil)
	require.Equal(t, http.StatusNoContent, status)
	status, replay = post(user, body)
	require.Equal(t, http.StatusConflict, status)
	require.Empty(t, replay.ID, "a deleted schedule must not be returned as a live result")

	_, err := db.Exec("UPDATE schedule_create_requests SET expires_at = UTC_TIMESTAMP(3) - INTERVAL 1 SECOND WHERE idempotency_key = ?", key)
	require.NoError(t, err)
	status, afterExpiry := post(user, body)
	require.Equal(t, http.StatusCreated, status)
	require.NotEqual(t, first.ID, afterExpiry.ID, "an expired key may start a new save")
}

func TestUpdateDeleteIdempotency(t *testing.T) {
	srv, jwks, db := setupServerWithPublisher(t, nil, nil)
	token := jwks.mint(t, uuid.New().String(), time.Hour)
	status, body := doRequest(t, srv.Client(), http.MethodPost, srv.URL+"/schedules", token, map[string]any{"title": "meeting", "start_at": "2026-10-01T09:00:00Z"})
	require.Equal(t, http.StatusCreated, status, string(body))
	var created scheduleJSON
	require.NoError(t, json.Unmarshal(body, &created))
	send := func(method, id, key, payload string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+"/schedules/"+id, strings.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, body
	}
	status, first := send(http.MethodPatch, created.ID, "update-key", `{"title":"changed"}`)
	require.Equal(t, http.StatusOK, status, string(first))
	status, _ = send(http.MethodPatch, created.ID, "", `{"status":"tentative"}`)
	require.Equal(t, http.StatusOK, status)
	status, replay := send(http.MethodPatch, created.ID, "update-key", `{"title":"changed"}`)
	require.Equal(t, http.StatusOK, status, string(replay))
	require.JSONEq(t, string(first), string(replay))
	status, _ = send(http.MethodPatch, created.ID, "update-key", `{"title":"different"}`)
	require.Equal(t, http.StatusConflict, status)
	status, _ = send(http.MethodDelete, created.ID, "delete-key", "")
	require.Equal(t, http.StatusNoContent, status)
	status, _ = send(http.MethodDelete, created.ID, "delete-key", "")
	require.Equal(t, http.StatusNoContent, status)
	status, _ = send(http.MethodDelete, uuid.NewString(), "delete-key", "")
	require.Equal(t, http.StatusConflict, status)
	var events int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM event_outbox").Scan(&events))
	require.Equal(t, 4, events, "the keyed retry must not emit another event")
	status, body = doRequest(t, srv.Client(), http.MethodPost, srv.URL+"/schedules", token, map[string]any{"title": "bulk target", "start_at": "2026-10-02T09:00:00Z"})
	require.Equal(t, http.StatusCreated, status, string(body))
	var bulkTarget scheduleJSON
	require.NoError(t, json.Unmarshal(body, &bulkTarget))
	bulk := func(ids []string) (int, []byte) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"ids": ids})
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/schedules/bulk-delete", bytes.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", "bulk-key")
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, body
	}
	status, first = bulk([]string{bulkTarget.ID})
	require.Equal(t, http.StatusOK, status, string(first))
	status, replay = bulk([]string{bulkTarget.ID})
	require.Equal(t, http.StatusOK, status, string(replay))
	require.JSONEq(t, string(first), string(replay))
	status, _ = bulk([]string{created.ID})
	require.Equal(t, http.StatusConflict, status)
}

func TestReminderIdempotency(t *testing.T) {
	srv, jwks, db := setupServerWithPublisher(t, nil, nil)
	token := jwks.mint(t, uuid.New().String(), time.Hour)
	status, body := doRequest(t, srv.Client(), http.MethodPost, srv.URL+"/schedules", token, map[string]any{"title": "meeting", "start_at": "2026-10-01T09:00:00Z"})
	require.Equal(t, http.StatusCreated, status, string(body))
	var schedule scheduleJSON
	require.NoError(t, json.Unmarshal(body, &schedule))
	call := func(method, suffix, key, payload string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+"/schedules/"+schedule.ID+"/reminders"+suffix, strings.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, body
	}
	status, first := call(http.MethodPost, "", "reminder-add-key", `{"minutes_before":10,"channel":"email"}`)
	require.Equal(t, http.StatusCreated, status, string(first))
	status, replay := call(http.MethodPost, "", "reminder-add-key", `{"minutes_before":10,"channel":"email"}`)
	require.Equal(t, http.StatusCreated, status, string(replay))
	require.JSONEq(t, string(first), string(replay))
	status, _ = call(http.MethodPost, "", "reminder-add-key", `{"minutes_before":20,"channel":"email"}`)
	require.Equal(t, http.StatusConflict, status)
	var added reminderJSON
	require.NoError(t, json.Unmarshal(first, &added))
	status, _ = call(http.MethodDelete, "/"+added.ID, "reminder-delete-key", "")
	require.Equal(t, http.StatusNoContent, status)
	status, _ = call(http.MethodDelete, "/"+added.ID, "reminder-delete-key", "")
	require.Equal(t, http.StatusNoContent, status)
	status, _ = call(http.MethodPost, "", "reminder-add-key", `{"minutes_before":10,"channel":"email"}`)
	require.Equal(t, http.StatusConflict, status, "deleted reminder must not appear as live")
	var events int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM event_outbox").Scan(&events))
	require.Equal(t, 3, events, "create, one reminder add and one reminder delete")
}

func TestCreateScheduleRollsBackFailedReminder(t *testing.T) {
	srv, jwks, db := setupServerWithPublisher(t, nil, nil)
	user := jwks.mint(t, uuid.New().String(), time.Hour)
	_, err := db.Exec(`ALTER TABLE schedule_reminders ADD CONSTRAINT fail_second_reminder CHECK (minutes_before <> 20)`)
	require.NoError(t, err)

	payload := map[string]any{
		"title": "atomic save", "start_at": "2026-10-01T09:00:00Z",
		"reminders": []map[string]any{
			{"minutes_before": 10, "channel": "push"},
			{"minutes_before": 20, "channel": "push"},
		},
	}
	status, _ := doRequest(t, srv.Client(), http.MethodPost, srv.URL+"/schedules", user, payload)
	require.Equal(t, http.StatusInternalServerError, status)
	for _, table := range []string{"schedules", "schedule_reminders", "event_outbox", "schedule_create_requests"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
		require.Zero(t, count, table+" must roll back with the failed reminder")
	}
}
