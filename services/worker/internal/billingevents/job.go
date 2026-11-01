package billingevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

const ReconcileWebhookEventsJobKey = "billing.reconcile-webhook-events"

type Job struct {
	Service Service
	Maximum int
}

type jobPayload struct {
	Limit int `json:"limit"`
}

func (j Job) Run(ctx context.Context, job jobs.Job) error {
	if j.Service.store == nil {
		return errors.New("billing reconciliation service is required")
	}
	payload := jobPayload{Limit: j.Maximum}
	if payload.Limit <= 0 {
		payload.Limit = 25
	}
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}
	if payload.Limit <= 0 || payload.Limit > 100 {
		return errors.New("billing reconciliation limit must be between 1 and 100")
	}
	for range payload.Limit {
		result, err := j.Service.ProcessNext(ctx)
		if err != nil {
			return err
		}
		if !result.Processed {
			return nil
		}
		if result.Failed {
			return fmt.Errorf("billing event %d failed reconciliation: %s", result.EventID, result.Reason)
		}
	}
	return nil
}
