//go:build integration

// Every mutating /schedules* HTTP call must land the matching
// schedules.*.v1 event (../../PLAN.md §7.3/§7.4) on a real NATS JetStream
// stream, in order, with the correct dedup id and reminders snapshot. Spins
// up MySQL and NATS testcontainers together, driving the exact same
// api.Router/service.Service stack cmd/server/main.go wires up.
package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"

	"github.com/GGingGGang/svc-core/internal/events"
)

func TestSchedulesPublishDomainEvents(t *testing.T) {
	ctx := context.Background()

	natsContainer, err := tcnats.Run(ctx, "nats:2.10-alpine")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, natsContainer.Terminate(context.Background()))
	})

	natsURL, err := natsContainer.ConnectionString(ctx)
	require.NoError(t, err)

	nc, js, err := events.Connect(natsURL)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	require.NoError(t, events.EnsureStream(ctx, js))

	pub := events.NewPublisher(nil)
	srv, jwks, db := setupServerWithPublisher(t, pub, nil)
	client := srv.Client()

	userID := uuid.New().String()
	token := jwks.mint(t, userID, time.Hour)

	cons, err := js.OrderedConsumer(ctx, events.StreamName, jetstream.OrderedConsumerConfig{})
	require.NoError(t, err)
	next := func() jetstream.Msg {
		t.Helper()
		msg, err := cons.Next(jetstream.FetchMaxWait(10 * time.Second))
		require.NoError(t, err)
		return msg
	}

	// create → created.v1, with the reminder snapshot attached.
	createBody, err := json.Marshal(map[string]any{
		"title":     "이벤트 테스트",
		"start_at":  "2026-08-10T06:00:00Z",
		"reminders": []map[string]any{{"minutes_before": 15, "channel": "push"}},
	})
	require.NoError(t, err)
	createReq, err := http.NewRequest(http.MethodPost, srv.URL+"/schedules", bytes.NewReader(createBody))
	require.NoError(t, err)
	createReq.Header.Set("Authorization", "Bearer "+token)
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := client.Do(createReq)
	require.NoError(t, err)
	defer createResp.Body.Close()
	requestID := createResp.Header.Get("X-Request-ID")
	require.NotEmpty(t, requestID)
	status := createResp.StatusCode
	body, err := io.ReadAll(createResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, status, string(body))
	var created scheduleJSON
	require.NoError(t, json.Unmarshal(body, &created))
	var waiting int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM event_outbox WHERE published_at IS NULL AND attempts > 0`).Scan(&waiting))
	require.Equal(t, 1, waiting, "a publish outage must retain the committed schedule event")
	status, body = doRequest(t, client, http.MethodGet, srv.URL+"/status", "", nil)
	require.Equal(t, http.StatusOK, status)
	require.JSONEq(t, `{"schedules":"available","followup":"delayed"}`, string(body))
	pub.SetJetStream(js)

	msg := next()
	require.Equal(t, events.SubjectScheduleCreated, msg.Subject())
	require.Equal(t, "application/json", msg.Headers().Get("Content-Type"))
	require.Equal(t, requestID, msg.Headers().Get("x-request-id"))
	require.NotEmpty(t, msg.Headers().Get("Nats-Msg-Id"))
	var createdEvt events.ScheduleEvent
	require.NoError(t, json.Unmarshal(msg.Data(), &createdEvt))
	require.Equal(t, created.ID, createdEvt.ScheduleID)
	require.Equal(t, userID, createdEvt.UserID)
	require.Equal(t, "manual", createdEvt.Source)
	require.Len(t, createdEvt.Reminders, 1)
	require.Equal(t, int32(15), createdEvt.Reminders[0].MinutesBefore)

	// PATCH → updated.v1.
	status, body = doRequest(t, client, http.MethodPatch, srv.URL+"/schedules/"+created.ID, token, map[string]any{
		"title": "수정된 제목",
	})
	require.Equal(t, http.StatusOK, status, string(body))
	msg = next()
	require.Equal(t, events.SubjectScheduleUpdated, msg.Subject())
	var patchEvt events.ScheduleEvent
	require.NoError(t, json.Unmarshal(msg.Data(), &patchEvt))
	require.Equal(t, "수정된 제목", patchEvt.Title)

	// add reminder → updated.v1, snapshot now has both reminders.
	status, body = doRequest(t, client, http.MethodPost, srv.URL+"/schedules/"+created.ID+"/reminders", token, map[string]any{
		"minutes_before": 5, "channel": "email",
	})
	require.Equal(t, http.StatusCreated, status, string(body))
	var addedReminder reminderJSON
	require.NoError(t, json.Unmarshal(body, &addedReminder))

	msg = next()
	require.Equal(t, events.SubjectScheduleUpdated, msg.Subject())
	var afterAdd events.ScheduleEvent
	require.NoError(t, json.Unmarshal(msg.Data(), &afterAdd))
	require.Len(t, afterAdd.Reminders, 2, "reminders snapshot must be the full set, not a delta")

	// delete reminder → updated.v1, snapshot shrinks back to one.
	status, _ = doRequest(t, client, http.MethodDelete, srv.URL+"/schedules/"+created.ID+"/reminders/"+addedReminder.ID, token, nil)
	require.Equal(t, http.StatusNoContent, status)
	msg = next()
	require.Equal(t, events.SubjectScheduleUpdated, msg.Subject())
	var afterRemove events.ScheduleEvent
	require.NoError(t, json.Unmarshal(msg.Data(), &afterRemove))
	require.Len(t, afterRemove.Reminders, 1)

	// single delete → deleted.v1.
	status, _ = doRequest(t, client, http.MethodDelete, srv.URL+"/schedules/"+created.ID, token, nil)
	require.Equal(t, http.StatusNoContent, status)
	msg = next()
	require.Equal(t, events.SubjectScheduleDeleted, msg.Subject())
	var deletedEvt events.ScheduleDeletedEvent
	require.NoError(t, json.Unmarshal(msg.Data(), &deletedEvt))
	require.Equal(t, created.ID, deletedEvt.ScheduleID)
	require.Equal(t, userID, deletedEvt.UserID)

	// bulk-delete → one deleted.v1 per id actually owned; an id that was
	// never created (so never owned by anyone) must not get one.
	var ownedIDs []string
	for i := 0; i < 2; i++ {
		_, body := doRequest(t, client, http.MethodPost, srv.URL+"/schedules", token, map[string]any{
			"title": "일괄 삭제", "start_at": "2026-08-11T06:00:00Z",
		})
		var c scheduleJSON
		require.NoError(t, json.Unmarshal(body, &c))
		ownedIDs = append(ownedIDs, c.ID)
		next() // drain this schedule's created.v1
	}
	neverOwnedID := uuid.New().String()

	status, body = doRequest(t, client, http.MethodPost, srv.URL+"/schedules/bulk-delete", token, map[string]any{
		"ids": append(append([]string{}, ownedIDs...), neverOwnedID),
	})
	require.Equal(t, http.StatusOK, status, string(body))

	seen := map[string]bool{}
	for range ownedIDs {
		m := next()
		require.Equal(t, events.SubjectScheduleDeleted, m.Subject())
		var evt events.ScheduleDeletedEvent
		require.NoError(t, json.Unmarshal(m.Data(), &evt))
		seen[evt.ScheduleID] = true
	}
	for _, id := range ownedIDs {
		require.True(t, seen[id])
	}
	require.False(t, seen[neverOwnedID])

	// Each committed mutation creates one durable outbox row. The request
	// publishes one immediately; the background worker drains any remainder.
	// A crash before the publish mark is safe because JetStream dedups retries.
	var outboxRows, publishedRows int
	require.Eventually(t, func() bool {
		if err := db.QueryRow(`SELECT COUNT(*), COUNT(published_at) FROM event_outbox`).Scan(&outboxRows, &publishedRows); err != nil {
			return false
		}
		return outboxRows == publishedRows
	}, 5*time.Second, 100*time.Millisecond)
	require.Equal(t, 9, outboxRows)
	status, body = doRequest(t, client, http.MethodGet, srv.URL+"/status", "", nil)
	require.Equal(t, http.StatusOK, status)
	require.JSONEq(t, `{"schedules":"available","followup":"available"}`, string(body))
	_, err = db.Exec(`UPDATE event_outbox SET published_at=NULL,
 available_at=DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 1 HOUR),
 created_at=DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 6 SECOND) LIMIT 1`)
	require.NoError(t, err)
	status, body = doRequest(t, client, http.MethodGet, srv.URL+"/status", "", nil)
	require.Equal(t, http.StatusOK, status)
	require.JSONEq(t, `{"schedules":"available","followup":"delayed"}`, string(body))

	// RED + domain event counters are on the app's single /metrics port.
	status, metricsBody := doRequest(t, client, http.MethodGet, srv.URL+"/metrics", "", nil)
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, string(metricsBody), `domain_event_published_total{subject="app.schedules.created.v1"}`)
	require.Contains(t, string(metricsBody), `domain_event_published_total{subject="app.schedules.updated.v1"}`)
	require.Contains(t, string(metricsBody), `domain_event_published_total{subject="app.schedules.deleted.v1"}`)
	require.Contains(t, string(metricsBody), "http_server_requests_total{")
	require.Contains(t, string(metricsBody), "http_server_request_duration_seconds")
}
