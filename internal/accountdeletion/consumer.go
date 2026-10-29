package accountdeletion

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	requestSubject = "app.account.deletion_requested.v1"
	resultSubject  = "app.account.deletion_result.v1"
	streamName     = "ACCOUNT_LIFECYCLE"
	durableName    = "svc-core-account-deletion"
)

type CleanupFunc func(context.Context, uuid.UUID) error

type deletionRequest struct {
	EventID           string    `json:"event_id"`
	RequestID         string    `json:"request_id"`
	UserID            string    `json:"user_id"`
	CredentialVersion uint64    `json:"credential_version"`
	OccurredAt        time.Time `json:"occurred_at"`
}

type deletionResult struct {
	EventID    string    `json:"event_id"`
	RequestID  string    `json:"request_id"`
	Service    string    `json:"service"`
	Status     string    `json:"status"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Run connects a durable consumer to the auth-owned lifecycle stream. Missing
// NATS/stream configuration is retried until shutdown; it never blocks HTTP startup.
func Run(ctx context.Context, natsURL string, cleanup CleanupFunc) {
	for ctx.Err() == nil {
		nc, err := nats.Connect(natsURL, nats.Name("svc-core-account-deletion"), nats.Timeout(3*time.Second))
		if err != nil {
			waitRetry(ctx)
			continue
		}
		js, err := jetstream.New(nc)
		if err != nil {
			nc.Close()
			waitRetry(ctx)
			continue
		}
		stream, err := js.Stream(ctx, streamName)
		if err != nil {
			nc.Close()
			waitRetry(ctx)
			continue
		}
		consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
			Durable: durableName, FilterSubject: requestSubject,
			AckPolicy: jetstream.AckExplicitPolicy, AckWait: 30 * time.Second,
			MaxDeliver: -1, MaxAckPending: 1,
		})
		if err != nil {
			nc.Close()
			waitRetry(ctx)
			continue
		}
		cc, err := consumer.Consume(func(msg jetstream.Msg) {
			if err := handleMessage(ctx, js, msg, cleanup); err != nil {
				log.Printf("core account deletion delivery failed; retrying")
				_ = msg.NakWithDelay(2 * time.Second)
			}
		}, jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, _ error) {}))
		if err != nil {
			nc.Close()
			waitRetry(ctx)
			continue
		}
		<-ctx.Done()
		cc.Stop()
		nc.Close()
	}
}

func handleMessage(ctx context.Context, js jetstream.JetStream, msg jetstream.Msg, cleanup CleanupFunc) error {
	var event deletionRequest
	if err := json.Unmarshal(msg.Data(), &event); err != nil {
		// Invalid lifecycle messages cannot be safely associated with an account.
		log.Printf("discarding malformed account deletion event")
		return msg.Ack()
	}
	userID, err := uuid.Parse(event.UserID)
	if err != nil || event.EventID == "" || event.RequestID == "" {
		log.Printf("discarding invalid account deletion event")
		return msg.Ack()
	}
	status := "completed"
	if err := cleanup(ctx, userID); err != nil {
		status = "retry"
	}
	result, err := json.Marshal(deletionResult{
		EventID: event.EventID, RequestID: event.RequestID, Service: "core",
		Status: status, OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	if _, err := js.Publish(ctx, resultSubject, result, jetstream.WithMsgID(event.EventID+":core:"+status)); err != nil {
		return err
	}
	if status == "retry" {
		log.Printf("core account deletion cleanup failed; auth will retry")
	}
	return msg.Ack()
}

func waitRetry(ctx context.Context) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
