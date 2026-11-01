package ingestion

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

type FollowUpBuilder func(jobs.Job, time.Time) []jobs.NewJob

type FollowUpHandler struct {
	Handler JobHandler
	Jobs    predictionJobEnqueuer
	Build   FollowUpBuilder
	Now     func() time.Time
}

func (h FollowUpHandler) Run(ctx context.Context, job jobs.Job) error {
	if h.Handler == nil {
		return errors.New("follow-up job handler is required")
	}
	if err := h.Handler.Run(ctx, job); err != nil {
		return err
	}
	if h.Jobs == nil || h.Build == nil {
		return nil
	}
	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now().UTC()
	}
	for _, followUp := range h.Build(job, now) {
		if _, err := h.Jobs.Enqueue(ctx, followUp); err != nil {
			return fmt.Errorf("enqueue follow-up %s: %w", followUp.JobKey, err)
		}
	}
	return nil
}
