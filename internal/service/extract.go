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
		var rl *ai.RateLimitedError
		if errors.As(extractErr, &rl) {
			return nil, false, &ExtractRateLimitedError{RetryAfter: rl.RetryAfter}
		}
		return nil, false, extractErr
	}
	return candidates, result.Truncated, nil
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
