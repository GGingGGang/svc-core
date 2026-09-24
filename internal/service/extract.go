package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"

	"github.com/GGingGGang/svc-core/internal/ai"
	"github.com/GGingGGang/svc-core/internal/observability"
	"github.com/GGingGGang/svc-core/internal/repo"
)

var ErrExtractKeyUnavailable = errors.New("no Gemini key configured or supplied")

// ExtractSchedules calls Gemini to turn free-form text into schedule
// candidates (../../PLAN.md §6). It records exactly one ai_extractions row
// regardless of outcome — success, partial (upstream ok but the response
// didn't parse), or failed (upstream/key error) — before returning, so the
// audit trail exists even when extraction ultimately fails. The audit
// insert itself is best-effort: a DB error there is logged, not surfaced,
// since the caller's actual request (extraction) already ran to completion.
func (s *Service) ExtractSchedules(ctx context.Context, userID uuid.UUID, in ExtractInput) ([]ExtractCandidate, bool, error) {
	requestID, err := s.admitExtraction(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	defer s.finishExtraction(requestID)
	observability.AIExtractionRequestsTotal.Inc()
	start := time.Now()
	result, extractErr := s.aiClnt.Extract(ctx, in.APIKey, ai.ExtractInput{
		Text:     in.Text,
		Now:      in.Now,
		Timezone: in.Timezone,
	})
	latencyMs := int32(time.Since(start).Milliseconds())

	status := repo.AiExtractionsStatusSuccess
	var candidates []ExtractCandidate
	var resultJSON []byte
	if extractErr != nil {
		status = repo.AiExtractionsStatusFailed
	} else {
		candidates = toExtractCandidates(result.Candidates)
		var marshalErr error
		resultJSON, marshalErr = json.Marshal(result)
		if marshalErr != nil {
			log.Printf("ERROR marshal extraction result for audit row failed: %v", marshalErr)
		}
	}

	s.recordExtraction(ctx, userID, in.Text, resultJSON, status, latencyMs)

	if extractErr != nil {
		if errors.Is(extractErr, ai.ErrMissingAPIKey) {
			return nil, false, ErrExtractKeyUnavailable
		}
		if errors.Is(extractErr, ai.ErrInvalidAPIKey) {
			return nil, false, ErrExtractInvalidKey
		}
		var rl *ai.RateLimitedError
		if errors.As(extractErr, &rl) {
			return nil, false, &ExtractRateLimitedError{RetryAfter: rl.RetryAfter}
		}
		return nil, false, extractErr
	}
	return candidates, result.Truncated, nil
}

// A row lock serializes admissions across replicas; individual request rows
// enforce a sliding 60-second window and recover from a crashed worker.
func (s *Service) admitExtraction(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	requestID, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO ai_request_users (user_id) VALUES (?)", idBytes(userID)); err != nil {
		return uuid.Nil, err
	}
	var locked []byte
	if err := tx.QueryRowContext(ctx, "SELECT user_id FROM ai_request_users WHERE user_id = ? FOR UPDATE", idBytes(userID)).Scan(&locked); err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM ai_request_admissions WHERE user_id = ? AND started_at <= DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 60 SECOND)", idBytes(userID)); err != nil {
		return uuid.Nil, err
	}
	var count, active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(finished_at IS NULL AND started_at > DATE_SUB(UTC_TIMESTAMP(3), INTERVAL 20 SECOND)), 0)
FROM ai_request_admissions WHERE user_id = ?`, idBytes(userID)).Scan(&count, &active); err != nil {
		return uuid.Nil, err
	}
	if active > 0 {
		return uuid.Nil, ErrExtractBusy
	}
	if count >= 5 {
		return uuid.Nil, ErrExtractUserRateLimited
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO ai_request_admissions (id, user_id, started_at) VALUES (?, ?, UTC_TIMESTAMP(3))", idBytes(requestID), idBytes(userID)); err != nil {
		return uuid.Nil, err
	}
	if err := tx.Commit(); err != nil {
		return uuid.Nil, err
	}
	return requestID, nil
}

func (s *Service) finishExtraction(id uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, "UPDATE ai_request_admissions SET finished_at = UTC_TIMESTAMP(3) WHERE id = ?", idBytes(id)); err != nil {
		log.Printf("ERROR finish ai request admission: %v", err)
	}
}

func (s *Service) recordExtraction(ctx context.Context, userID uuid.UUID, rawText string, resultJSON []byte, status repo.AiExtractionsStatus, latencyMs int32) {
	id, err := uuid.NewV7()
	if err != nil {
		log.Printf("ERROR generate ai_extractions id failed: %v", err)
		return
	}

	var resultJSONParam json.RawMessage
	if len(resultJSON) > 0 {
		resultJSONParam = resultJSON
	}

	if err := s.q.CreateExtraction(ctx, repo.CreateExtractionParams{
		ID:         idBytes(id),
		UserID:     idBytes(userID),
		Model:      s.aiClnt.Model(),
		InputChars: int32(len(rawText)),
		RawText:    sql.NullString{String: rawText, Valid: true},
		ResultJson: resultJSONParam,
		Status:     status,
		LatencyMs:  sql.NullInt32{Int32: latencyMs, Valid: true},
	}); err != nil {
		log.Printf("ERROR record ai_extractions audit row failed: %v", err)
	}
}

func toExtractCandidates(cs []ai.Candidate) []ExtractCandidate {
	out := make([]ExtractCandidate, 0, len(cs))
	for _, c := range cs {
		out = append(out, ExtractCandidate{
			Title:             c.Title,
			StartAt:           utcPtrTime(c.StartAt),
			EndAt:             utcPtrTime(c.EndAt),
			AllDay:            c.AllDay,
			Location:          c.Location,
			Description:       c.Description,
			Confidence:        c.Confidence,
			NeedsConfirmation: c.NeedsConfirmation,
			Issues:            c.Issues,
		})
	}
	return out
}

func utcPtrTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}
