//go:build integration

// Integration tests spin up a real MySQL container (testcontainers-go) and
// exercise the HTTP API end to end: schedule CRUD, reminders sub-resource,
// bulk-delete, and user_id scoping (cross-user access must 404, never 403
// or leak another user's data). Requires a running Docker daemon; run with
// `go test -tags=integration ./...`.
package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/GGingGGang/svc-core/internal/api"
	"github.com/GGingGGang/svc-core/internal/service"
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

// setupServer starts a MySQL container, applies the golang-migrate up
// migration via the image's docker-entrypoint-initdb.d mechanism, and
// returns an httptest server wired to the real DB through the same
// api.Router/service.Service stack cmd/server/main.go uses.
func setupServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()

	container, err := tcmysql.Run(ctx, "mysql:8.0.36",
		tcmysql.WithDatabase("core"),
		tcmysql.WithUsername("core_test"),
		tcmysql.WithPassword("core_test"),
		tcmysql.WithScripts("../../db/migrations/000001_init.up.sql"),
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

	handler := api.NewHandler(service.New(db))
	srv := httptest.NewServer(api.Router(handler))
	t.Cleanup(srv.Close)
	return srv
}

func doRequest(t *testing.T, client *http.Client, method, url, userID string, body any) (int, []byte) {
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
	if userID != "" {
		req.Header.Set("X-User-Id", userID)
	}

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, respBody
}

func TestSchedulesIntegration(t *testing.T) {
	srv := setupServer(t)
	client := srv.Client()

	userA := uuid.New().String()
	userB := uuid.New().String()

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

	t.Run("missing X-User-Id is unauthorized", func(t *testing.T) {
		status, _ := doRequest(t, client, http.MethodGet, srv.URL+"/schedules", "", nil)
		require.Equal(t, http.StatusUnauthorized, status)
	})
}
