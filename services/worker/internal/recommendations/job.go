package recommendations

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

const GenerateCandidatesJobKey = "recommendations.generate-candidates"

type Job struct {
	Service Service
}

type jobPayload struct {
	UserID        int64  `json:"user_id"`
	GenerationKey string `json:"generation_key"`
}

func (j Job) Run(ctx context.Context, job jobs.Job) error {
	if j.Service.repository == nil {
		return errors.New("recommendation service is required")
	}
	var payload jobPayload
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}
	_, err := j.Service.GenerateBatch(ctx, payload.UserID, payload.GenerationKey)
	return err
}
