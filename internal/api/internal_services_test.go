package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GGingGGang/svc-core/internal/service"
	"github.com/stretchr/testify/require"
)

func TestValidInternalBearer(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer shared-secret")
	require.True(t, validInternalBearer(req, "shared-secret"))
	require.False(t, validInternalBearer(req, "other-secret"))
	require.False(t, validInternalBearer(req, ""))
	req.Header.Set("Authorization", "shared-secret")
	require.False(t, validInternalBearer(req, "shared-secret"))
}

func TestReminderInstantMatchesDispatcherRules(t *testing.T) {
	timed, err := reminderInstant(&service.Schedule{StartAt: time.Date(2026, 10, 1, 15, 30, 0, 0, time.UTC)}, 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, "2026-10-01T15:00:00Z", timed.Format(time.RFC3339))

	date := "2026-10-02"
	allDay, err := reminderInstant(&service.Schedule{AllDay: true, Timezone: "Asia/Seoul", StartDate: &date}, 30*time.Minute)
	require.NoError(t, err)
	require.Equal(t, "2026-10-01T23:30:00Z", allDay.UTC().Format(time.RFC3339))
}

func TestReminderValidityRequiresInternalToken(t *testing.T) {
	t.Run("missing configuration is unavailable", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/internal/reminders/00000000-0000-0000-0000-000000000000/validity?user_id=00000000-0000-0000-0000-000000000001&revision=1&minutes_before=10&channel=telegram", nil)
		response := httptest.NewRecorder()
		(&Handler{}).reminderValidity(response, req)
		require.Equal(t, 503, response.Code)
	})
	t.Run("bad credentials rejected before lookup", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/internal/reminders/00000000-0000-0000-0000-000000000000/validity?user_id=00000000-0000-0000-0000-000000000001&revision=1&minutes_before=10&channel=telegram", nil)
		response := httptest.NewRecorder()
		handler := &Handler{internalToken: "shared-secret"}
		handler.reminderValidity(response, req)
		require.Equal(t, 401, response.Code)
	})
}
