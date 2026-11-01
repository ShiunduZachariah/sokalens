package ingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

const DataBackfillQueue = "data-backfill"

func BackfillTeamHistoryJobKey() string { return "backfill.team_history" }

type DataBackfillRequest struct {
	ID              int64
	TargetFixtureID int64
	TeamID          int64
	CompetitionID   int64
	FeatureGroup    string
	MatchesNeeded   int
	Attempts        int
	Season          int
	TargetKickoffAt time.Time
}

type BackfillRequestStore interface {
	Start(context.Context, int64) (DataBackfillRequest, error)
	Count(context.Context, DataBackfillRequest) (int, error)
	RecordAttempt(context.Context, int64, string, bool) error
	Complete(context.Context, DataBackfillRequest) error
	ConfirmUnavailable(context.Context, DataBackfillRequest) error
}

type BackfillProvider interface {
	Name() string
	Supports(featureGroup string) bool
	Backfill(context.Context, jobs.Job, DataBackfillRequest) (available bool, err error)
}

type BackfillPayloadBuilder interface {
	BuildProviderPayloads(context.Context, string, DataBackfillRequest) ([][]byte, error)
}

type jobBackfillProvider struct {
	name     string
	groups   map[string]struct{}
	handler  JobHandler
	payloads BackfillPayloadBuilder
}

func NewJobBackfillProvider(
	name string,
	featureGroups []string,
	handler JobHandler,
	payloads BackfillPayloadBuilder,
) BackfillProvider {
	groups := make(map[string]struct{}, len(featureGroups))
	for _, group := range featureGroups {
		groups[group] = struct{}{}
	}
	return jobBackfillProvider{name: name, groups: groups, handler: handler, payloads: payloads}
}

func (p jobBackfillProvider) Name() string { return p.name }

func (p jobBackfillProvider) Supports(featureGroup string) bool {
	_, ok := p.groups[featureGroup]
	return ok
}

func (p jobBackfillProvider) Backfill(
	ctx context.Context,
	parent jobs.Job,
	request DataBackfillRequest,
) (bool, error) {
	if p.handler == nil || p.payloads == nil {
		return false, errors.New("job backfill provider dependencies are required")
	}
	payloads, err := p.payloads.BuildProviderPayloads(ctx, p.name, request)
	if err != nil {
		return false, err
	}
	for _, payload := range payloads {
		child := parent
		child.Payload = payload
		if err := p.handler.Run(ctx, child); err != nil {
			return false, err
		}
	}
	return len(payloads) > 0, nil
}

type BackfillTeamHistoryJobOptions struct {
	Requests  BackfillRequestStore
	Providers []BackfillProvider
}

type BackfillTeamHistoryJob struct {
	opts BackfillTeamHistoryJobOptions
}

func NewBackfillTeamHistoryJob(opts BackfillTeamHistoryJobOptions) BackfillTeamHistoryJob {
	return BackfillTeamHistoryJob{opts: opts}
}

func (j BackfillTeamHistoryJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Requests == nil {
		return errors.New("backfill team history request store is required")
	}
	var payload struct {
		RequestID int64 `json:"request_id"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return err
	}
	if payload.RequestID <= 0 {
		return errors.New("backfill team history job requires a positive request_id")
	}
	request, err := j.opts.Requests.Start(ctx, payload.RequestID)
	if errors.Is(err, sql.ErrNoRows) {
		// The request is not claimable right now, which is not a failure.
		//
		// Start is an UPDATE ... RETURNING that matches only a row that is
		// pending or in_progress, due, and unexpired. A request that has since
		// completed, expired, or simply had its next attempt pushed into the
		// future matches nothing, and the driver reports that as
		// `sql: no rows in result set`. Returning it as an error marked the job
		// failed, retried it twice more, and failed it again: 1,169 of these in
		// three hours on 2026-09-22, against 875 that succeeded.
		//
		// Nothing was wrong in any of them. The coordinator is scheduled
		// independently of when a request becomes due, so it will always
		// sometimes arrive early, and the right answer then is to do nothing
		// and let the next delivery pick it up.
		return nil
	}
	if err != nil {
		return err
	}
	count, err := j.opts.Requests.Count(ctx, request)
	if err != nil {
		return err
	}
	if count >= request.MatchesNeeded {
		return j.opts.Requests.Complete(ctx, request)
	}

	attempted := false
	for _, provider := range j.opts.Providers {
		if provider == nil || !provider.Supports(request.FeatureGroup) {
			continue
		}
		attempted = true
		available, err := provider.Backfill(ctx, job, request)
		if err != nil {
			return err
		}
		if err := j.opts.Requests.RecordAttempt(ctx, request.ID, provider.Name(), available); err != nil {
			return err
		}
		count, err = j.opts.Requests.Count(ctx, request)
		if err != nil {
			return err
		}
		if count >= request.MatchesNeeded {
			return j.opts.Requests.Complete(ctx, request)
		}
	}
	if !attempted {
		return errors.New("no provider supports the requested backfill feature group")
	}
	return j.opts.Requests.ConfirmUnavailable(ctx, request)
}
