package scheduler

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/acca"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/billingevents"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/maintenance"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/predictions"
)

func TestPlannerDoesNotRaceAccaInventoryAgainstPredictionGeneration(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{ProviderMode: ProviderAPIFootball, ProviderInterval: 4 * time.Hour, PredictionInterval: 15 * time.Minute})
	foundGeneration := false
	for _, scheduled := range planner.Plan(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)) {
		if scheduled.Job.JobKey == acca.GenerateInventoryJobKey {
			t.Fatal("ACCA inventory was queued before prediction generation completed")
		}
		if scheduled.Job.JobKey == predictions.GenerateUpcomingJobKey {
			foundGeneration = true
		}
	}
	if !foundGeneration {
		t.Fatal("scheduled prediction generation is required to trigger ACCA inventory")
	}
}

func TestPlannerUsesPlanSafeAPIFootballRollingWindows(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderAPIFootball,
		EnableRealAPIFootball: true,
		ProviderInterval:      4 * time.Hour,
		PredictionInterval:    15 * time.Minute,
	})
	now := time.Date(2026, time.July, 14, 13, 37, 0, 0, time.UTC)

	plan := planner.Plan(now)

	if got, want := payloadDates(plan, ingestion.FixturesJobKey()), []string{"2026-07-11", "2026-07-12", "2026-07-13", "2026-07-14", "2026-07-15", "2026-07-16", "2026-07-17", "2026-07-18", "2026-07-19", "2026-07-20", "2026-07-21"}; !equalStrings(got, want) {
		t.Fatalf("fixture dates = %v, want %v", got, want)
	}
	if got, want := payloadDates(plan, ingestion.OddsJobKey()), []string{"2026-07-14", "2026-07-15"}; !equalStrings(got, want) {
		t.Fatalf("odds dates = %v, want %v", got, want)
	}
	for _, scheduled := range plan {
		if strings.Contains(string(scheduled.Job.Payload), `"last"`) {
			t.Fatalf("plan contains API-Football Free-plan-blocked last parameter: %s", scheduled.Job.Payload)
		}
	}
}

func TestPlannerGatesProviderSchedulesBySelectedRoleAndCallFlags(t *testing.T) {
	tests := []struct {
		name          string
		opts          PlanOptions
		wantProviders map[string]int
	}{
		{
			name:          "disabled real API-Football",
			opts:          PlanOptions{ProviderMode: ProviderAPIFootball},
			wantProviders: map[string]int{},
		},
		{
			// countries + the live poll: the two routes the mock serves.
			name:          "explicit mock API-Football",
			opts:          PlanOptions{ProviderMode: ProviderAPIFootball, EnableMockAPIFootball: true},
			wantProviders: map[string]int{ProviderAPIFootball: 2},
		},
		{
			name:          "enabled football-data.org",
			opts:          PlanOptions{ProviderMode: ProviderFootballDataOrg, EnableRealFootballDataOrg: true},
			wantProviders: map[string]int{ProviderFootballDataOrg: 10},
		},
		{
			name: "hybrid with both providers enabled",
			opts: PlanOptions{
				ProviderMode:              ProviderHybrid,
				EnableRealAPIFootball:     true,
				EnableRealFootballDataOrg: true,
			},
			wantProviders: map[string]int{ProviderAPIFootball: 18, ProviderFootballDataOrg: 10},
		},
		{
			name:          "hybrid degrades to enabled provider",
			opts:          PlanOptions{ProviderMode: ProviderHybrid, EnableRealFootballDataOrg: true},
			wantProviders: map[string]int{ProviderFootballDataOrg: 10},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.opts.ProviderInterval = 4 * time.Hour
			test.opts.PredictionInterval = 15 * time.Minute
			planner := mustPlanner(t, test.opts)
			counts := map[string]int{}
			for _, scheduled := range planner.Plan(time.Date(2026, time.July, 14, 13, 37, 0, 0, time.UTC)) {
				if scheduled.Provider != "" {
					counts[scheduled.Provider]++
				}
			}
			if !equalCounts(counts, test.wantProviders) {
				t.Fatalf("provider counts = %v, want %v", counts, test.wantProviders)
			}
		})
	}
}

func TestPlannerUsesBothUpcomingFeedsInHybridMode(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:              ProviderHybrid,
		EnableRealAPIFootball:     true,
		EnableRealFootballDataOrg: true,
		ProviderInterval:          4 * time.Hour,
		PredictionInterval:        15 * time.Minute,
	})

	found := false
	for _, scheduled := range planner.Plan(time.Date(2026, time.July, 14, 13, 37, 0, 0, time.UTC)) {
		if scheduled.Provider == ProviderFootballDataOrg && scheduled.Job.JobKey == ingestion.FootballDataMatchesJobKey() {
			found = true
		}
	}
	if !found {
		t.Fatal("hybrid plan did not schedule the quota-free upcoming feed")
	}
}

func TestPlannerLimitsMockAPIFootballToSupportedCountriesReference(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderAPIFootball,
		EnableMockAPIFootball: true,
		ProviderInterval:      4 * time.Hour,
		PredictionInterval:    15 * time.Minute,
	})

	var providerJobs []ScheduledJob
	for _, scheduled := range planner.Plan(time.Date(2026, time.July, 14, 13, 37, 0, 0, time.UTC)) {
		if scheduled.Provider != "" {
			providerJobs = append(providerJobs, scheduled)
		}
	}
	// The mock serves exactly two routes: /countries and /fixtures?live=all.
	// Those are the only two jobs mock mode may schedule. Anything else --
	// leagues, odds, fixtures by date, backfill -- would 404 against it, and
	// the boundary is the mock's coverage, not a general timidity about mock
	// mode. Widen this list only when the mock actually serves the route.
	scheduled := map[string]bool{}
	for _, job := range providerJobs {
		scheduled[job.Job.JobKey] = true
	}
	want := map[string]bool{
		ingestion.CountriesJobKey():    true,
		ingestion.LiveFixturesJobKey(): true,
	}
	for key := range want {
		if !scheduled[key] {
			t.Fatalf("mock mode did not schedule %q; scheduled %v", key, scheduled)
		}
	}
	for key := range scheduled {
		if !want[key] {
			t.Fatalf("mock mode scheduled %q, which the mock cannot serve", key)
		}
	}
}

func TestPlannerDelaysOddsUntilFixtureMappingsCanLand(t *testing.T) {
	now := time.Date(2026, time.July, 14, 13, 37, 0, 0, time.UTC)
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderAPIFootball,
		EnableRealAPIFootball: true,
		ProviderInterval:      4 * time.Hour,
		PredictionInterval:    15 * time.Minute,
		OddsDelay:             time.Minute,
	})

	for _, scheduled := range planner.Plan(now) {
		switch scheduled.Job.JobKey {
		case ingestion.FixturesJobKey():
			if !scheduled.Job.RunAfter.Equal(now) {
				t.Fatalf("fixture RunAfter = %s, want immediate %s", scheduled.Job.RunAfter, now)
			}
		case ingestion.OddsJobKey():
			if want := now.Add(time.Minute); !scheduled.Job.RunAfter.Equal(want) {
				t.Fatalf("odds RunAfter = %s, want %s", scheduled.Job.RunAfter, want)
			}
		}
	}
}

func TestPlannerUsesCadenceBucketsForIdempotency(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderAPIFootball,
		EnableRealAPIFootball: true,
		ProviderInterval:      4 * time.Hour,
		PredictionInterval:    15 * time.Minute,
	})
	now := time.Date(2026, time.July, 14, 13, 37, 0, 0, time.UTC)

	initial := planner.Plan(now)
	withinPredictionBucket := planner.Plan(now.Add(7 * time.Minute))
	nextPredictionBucket := planner.Plan(now.Add(9 * time.Minute))
	nextProviderBucket := planner.Plan(now.Add(3*time.Hour + 30*time.Minute))
	nextDay := planner.Plan(now.Add(24 * time.Hour))

	assertSameKey(t, initial, withinPredictionBucket, predictions.GenerateUpcomingJobKey, "")
	assertDifferentKey(t, initial, nextPredictionBucket, predictions.GenerateUpcomingJobKey, "")
	assertDifferentKey(t, initial, nextProviderBucket, ingestion.FixturesJobKey(), "2026-07-14")
	assertDifferentKey(t, initial, nextDay, predictions.SnapshotAccuracyJobKey, "")
	assertSameKey(t, initial, withinPredictionBucket, billingevents.SubscriptionReminderJobKey, "")
	assertDifferentKey(t, initial, nextDay, billingevents.SubscriptionReminderJobKey, "")
	for _, scheduled := range initial {
		if !strings.HasPrefix(scheduled.Job.IdempotencyKey, "schedule:") {
			t.Fatalf("idempotency key %q is not schedule-bucketed", scheduled.Job.IdempotencyKey)
		}
		if !scheduled.Job.DeduplicateCompleted {
			t.Fatalf("scheduled job %q does not deduplicate completed schedule slots", scheduled.Job.IdempotencyKey)
		}
	}
}

func TestPlannerSweepsUpcomingFixturesInBoundedFrequentBatches(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode: ProviderAPIFootball, ProviderInterval: 4 * time.Hour, PredictionInterval: time.Minute,
	})
	for _, scheduled := range planner.Plan(time.Date(2026, time.August, 21, 17, 30, 0, 0, time.UTC)) {
		if scheduled.Job.JobKey == predictions.GenerateUpcomingJobKey {
			if got, want := string(scheduled.Job.Payload), `{"scope":"upcoming","batch_size":250}`; got != want {
				t.Fatalf("generation payload = %s, want %s", got, want)
			}
			if got, want := scheduled.Job.IdempotencyKey, "schedule:predictions:generate:20260821T173000Z"; got != want {
				t.Fatalf("generation idempotency key = %s, want %s", got, want)
			}
			if got, want := scheduled.Job.Priority, 4; got != want {
				t.Fatalf("generation priority = %d, want %d", got, want)
			}
			return
		}
	}
	t.Fatal("upcoming prediction generation job was not scheduled")
}

func TestPlannerSchedulesHourlyDBOnlySubscriptionReminderJob(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode: ProviderAPIFootball, ProviderInterval: 4 * time.Hour, PredictionInterval: 15 * time.Minute,
	})
	now := time.Date(2026, time.August, 5, 13, 37, 0, 0, time.UTC)
	for _, scheduled := range planner.Plan(now) {
		if scheduled.Job.JobKey != billingevents.SubscriptionReminderJobKey {
			continue
		}
		if scheduled.Job.Queue != billingevents.BillingQueue || scheduled.Schedule != "subscription-reminders" {
			t.Fatalf("subscription reminder schedule=%+v", scheduled)
		}
		if scheduled.Job.IdempotencyKey != "schedule:billing:subscription-reminders:20260805T130000Z" || !scheduled.Job.DeduplicateCompleted {
			t.Fatalf("subscription reminder idempotency=%+v", scheduled.Job)
		}
		return
	}
	t.Fatal("subscription reminder job was not scheduled")
}

func TestPlannerSchedulesDailyBoundedRetentionJob(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{ProviderMode: ProviderAPIFootball, ProviderInterval: 4 * time.Hour, PredictionInterval: 15 * time.Minute})
	now := time.Date(2026, time.August, 5, 13, 37, 0, 0, time.UTC)
	for _, scheduled := range planner.Plan(now) {
		if scheduled.Job.JobKey != maintenance.PruneJobKey {
			continue
		}
		if scheduled.Job.Queue != maintenance.Queue || scheduled.Job.IdempotencyKey != "schedule:maintenance:retention:2026-08-05" || !scheduled.Job.DeduplicateCompleted {
			t.Fatalf("retention schedule=%+v", scheduled)
		}
		return
	}
	t.Fatal("retention job was not scheduled")
}

func TestPlannerSchedulesHourlyRawPredictionDrain(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{ProviderMode: ProviderAPIFootball, ProviderInterval: 4 * time.Hour, PredictionInterval: 15 * time.Minute})
	now := time.Date(2026, time.August, 5, 13, 37, 0, 0, time.UTC)
	for _, scheduled := range planner.Plan(now) {
		if scheduled.Job.JobKey != maintenance.RawPredictionPruneJobKey {
			continue
		}
		if scheduled.Job.Queue != maintenance.Queue || scheduled.Schedule != "raw-predictions" ||
			scheduled.Job.IdempotencyKey != "schedule:maintenance:raw-predictions:20260805T130000Z" ||
			!scheduled.Job.DeduplicateCompleted {
			t.Fatalf("raw prediction drain schedule=%+v", scheduled)
		}
		return
	}
	t.Fatal("raw prediction drain job was not scheduled")
}

func TestPlannerSchedulesDailyProviderPredictionsBatchWhenRealEnabled(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderAPIFootball,
		EnableRealAPIFootball: true,
		ProviderInterval:      4 * time.Hour,
		PredictionInterval:    15 * time.Minute,
	})
	now := time.Date(2026, time.August, 5, 13, 37, 0, 0, time.UTC)
	found := false
	for _, scheduled := range planner.Plan(now) {
		if scheduled.Job.JobKey != ingestion.ProviderPredictionsJobKey() {
			continue
		}
		found = true
		if scheduled.Job.Queue != "provider-fixtures" || scheduled.Job.Priority != 33 {
			t.Fatalf("provider predictions schedule=%+v", scheduled)
		}
		if scheduled.Job.IdempotencyKey != "schedule:provider:api-football:predictions:2026-08-05" || !scheduled.Job.DeduplicateCompleted {
			t.Fatalf("provider predictions idempotency=%+v", scheduled.Job)
		}
		if string(scheduled.Job.Payload) != "{}" {
			t.Fatalf("provider predictions payload=%s, want batch-mode {}", scheduled.Job.Payload)
		}
	}
	if !found {
		t.Fatal("provider predictions batch job was not scheduled when real API-Football is enabled")
	}
}

func TestPlannerSkipsProviderPredictionsBatchInMockOnlyMode(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderAPIFootball,
		EnableMockAPIFootball: true,
		ProviderInterval:      4 * time.Hour,
		PredictionInterval:    15 * time.Minute,
	})
	for _, scheduled := range planner.Plan(time.Date(2026, time.August, 5, 13, 37, 0, 0, time.UTC)) {
		if scheduled.Job.JobKey == ingestion.ProviderPredictionsJobKey() {
			t.Fatal("provider predictions batch scheduled in mock-only mode")
		}
	}
}

func TestPlannerSchedulesDailyReferenceCatalogueSync(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderAPIFootball,
		EnableRealAPIFootball: true,
		ProviderInterval:      4 * time.Hour,
		PredictionInterval:    15 * time.Minute,
	})
	now := time.Date(2026, time.August, 5, 13, 37, 0, 0, time.UTC)
	for _, scheduled := range planner.Plan(now) {
		if scheduled.Job.JobKey != ingestion.ReferenceSyncJobKey() {
			continue
		}
		if scheduled.Job.Queue != ingestion.DefaultProviderQueue || scheduled.Job.Priority != 22 {
			t.Fatalf("reference sync schedule=%+v", scheduled)
		}
		if scheduled.Job.IdempotencyKey != "schedule:provider:api-football:reference-sync:2026-08-05" || !scheduled.Job.DeduplicateCompleted {
			t.Fatalf("reference sync idempotency=%+v", scheduled.Job)
		}
		return
	}
	t.Fatal("reference-catalogue sync job was not scheduled when real API-Football is enabled")
}

func TestPlannerSkipsReferenceCatalogueSyncInMockOnlyMode(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderAPIFootball,
		EnableMockAPIFootball: true,
		ProviderInterval:      4 * time.Hour,
		PredictionInterval:    15 * time.Minute,
	})
	for _, scheduled := range planner.Plan(time.Date(2026, time.August, 5, 13, 37, 0, 0, time.UTC)) {
		if scheduled.Job.JobKey == ingestion.ReferenceSyncJobKey() {
			t.Fatal("reference-catalogue sync scheduled in mock-only mode")
		}
	}
}

func TestPlannerSchedulesDailyTheSportsDBReferenceSyncWhenEnabled(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderHybrid,
		EnableRealTheSportsDB: true,
		ProviderInterval:      4 * time.Hour,
		PredictionInterval:    15 * time.Minute,
	})
	now := time.Date(2026, time.August, 5, 13, 37, 0, 0, time.UTC)
	for _, scheduled := range planner.Plan(now) {
		if scheduled.Job.JobKey != ingestion.TheSportsDBReferenceSyncJobKey() {
			continue
		}
		if scheduled.Provider != ProviderTheSportsDB || scheduled.CallMode != "real" {
			t.Fatalf("thesportsdb schedule provider/call mode = %+v", scheduled)
		}
		if scheduled.Job.Queue != ingestion.DefaultProviderQueue || scheduled.Job.Priority != 24 {
			t.Fatalf("thesportsdb reference sync schedule=%+v", scheduled)
		}
		if scheduled.Job.IdempotencyKey != "schedule:provider:thesportsdb:reference-sync:2026-08-05" || !scheduled.Job.DeduplicateCompleted {
			t.Fatalf("thesportsdb reference sync idempotency=%+v", scheduled.Job)
		}
		if string(scheduled.Job.Payload) != "{}" {
			t.Fatalf("thesportsdb reference sync payload=%s, want {}", scheduled.Job.Payload)
		}
		return
	}
	t.Fatal("thesportsdb reference sync job was not scheduled when enabled")
}

func TestPlannerSkipsTheSportsDBReferenceSyncWhenDisabled(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderHybrid,
		EnableRealAPIFootball: true,
		ProviderInterval:      4 * time.Hour,
		PredictionInterval:    15 * time.Minute,
	})
	for _, scheduled := range planner.Plan(time.Date(2026, time.August, 5, 13, 37, 0, 0, time.UTC)) {
		if scheduled.Job.JobKey == ingestion.TheSportsDBReferenceSyncJobKey() {
			t.Fatal("thesportsdb reference sync scheduled while disabled")
		}
	}
}

func TestPlannerSchedulesDailyAggregateBeforeRawResultPruning(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{ProviderMode: ProviderAPIFootball, ProviderInterval: 4 * time.Hour, PredictionInterval: 15 * time.Minute})
	now := time.Date(2026, time.August, 5, 13, 37, 0, 0, time.UTC)
	for _, scheduled := range planner.Plan(now) {
		if scheduled.Job.JobKey != predictions.AggregatePruneJobKey {
			continue
		}
		if scheduled.Job.Queue != "predictions" || scheduled.Job.IdempotencyKey != "schedule:predictions:aggregate-prune:2026-08-05" || !strings.Contains(string(scheduled.Job.Payload), `"raw_retention_days":2`) {
			t.Fatalf("aggregate/prune schedule=%+v", scheduled)
		}
		return
	}
	t.Fatal("aggregate/prune job was not scheduled")
}

func TestSchedulerDoesNotReenqueueCompletedScheduleSlotWithinProcess(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderAPIFootball,
		EnableRealAPIFootball: true,
		ProviderInterval:      4 * time.Hour,
		PredictionInterval:    15 * time.Minute,
	})
	repo := &recordingEnqueuer{}
	s := New(planner, repo, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, time.July, 14, 13, 37, 0, 0, time.UTC)

	if err := s.Schedule(context.Background(), now); err != nil {
		t.Fatalf("first Schedule returned error: %v", err)
	}
	firstCount := len(repo.jobs)
	if firstCount == 0 {
		t.Fatal("first Schedule enqueued no jobs")
	}
	// Today's fixtures refresh on a 60s cadence, so use a 30s delta to stay
	// within the same slot while still exercising completed-slot dedup.
	if err := s.Schedule(context.Background(), now.Add(30*time.Second)); err != nil {
		t.Fatalf("second Schedule returned error: %v", err)
	}
	if len(repo.jobs) != firstCount {
		t.Fatalf("same schedule slot enqueued %d extra jobs", len(repo.jobs)-firstCount)
	}
	if err := s.Schedule(context.Background(), now.Add(9*time.Minute)); err != nil {
		t.Fatalf("next prediction slot Schedule returned error: %v", err)
	}
	// +9m crosses the prediction/recommendation bucket (4 prediction --
	// generate, settle, the full-time snapshot freeze and the pre-match tracker
	// refresh -- plus 1 recommendation), the today-fixtures 60s bucket (1), the
	// match-state reaper's 2-minute bucket (1), and the volatile SEO sweep's
	// 15-minute bucket (1) -- 13:37 truncates to 13:30 and 13:46 to 13:45 = 8
	// new jobs.
	//
	// The stable SEO sweep buckets on the calendar day and does not cross, so
	// it contributes nothing here. That asymmetry is the point of giving the
	// two bands separate keys: a fixture's indexability changes with the clock
	// and a team's does not.
	if got := len(repo.jobs) - firstCount; got != 8 {
		t.Fatalf("next prediction/recommendation/today-fixtures/reaper/seo slots enqueued %d jobs, want 8", got)
	}
}

func TestSchedulerPrunesExpiredInMemoryScheduleBuckets(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderAPIFootball,
		EnableMockAPIFootball: true,
		ProviderInterval:      4 * time.Hour,
		PredictionInterval:    15 * time.Minute,
	})
	s := New(planner, &recordingEnqueuer{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, time.July, 14, 13, 37, 0, 0, time.UTC)
	for day := 0; day < 100; day++ {
		if err := s.Schedule(context.Background(), now.AddDate(0, 0, day)); err != nil {
			t.Fatalf("Schedule day %d returned error: %v", day, err)
		}
	}
	if got := len(s.seen); got > len(planner.Plan(now.AddDate(0, 0, 99))) {
		t.Fatalf("seen schedule buckets = %d, want bounded to current plan", got)
	}
}

func TestNewPlannerRejectsInvalidCadencesAndProviderMode(t *testing.T) {
	valid := PlanOptions{ProviderMode: ProviderAPIFootball, ProviderInterval: 4 * time.Hour, PredictionInterval: 15 * time.Minute}
	tests := []PlanOptions{
		{ProviderMode: "unknown", ProviderInterval: valid.ProviderInterval, PredictionInterval: valid.PredictionInterval},
		{ProviderMode: valid.ProviderMode, PredictionInterval: valid.PredictionInterval},
		{ProviderMode: valid.ProviderMode, ProviderInterval: valid.ProviderInterval},
	}
	for _, opts := range tests {
		if _, err := NewPlanner(opts); err == nil {
			t.Fatalf("NewPlanner(%+v) returned nil error", opts)
		}
	}
}

type recordingEnqueuer struct {
	jobs []jobs.NewJob
}

func (r *recordingEnqueuer) Enqueue(_ context.Context, job jobs.NewJob) (int64, error) {
	r.jobs = append(r.jobs, job)
	return int64(len(r.jobs)), nil
}

func mustPlanner(t *testing.T, opts PlanOptions) Planner {
	t.Helper()
	planner, err := NewPlanner(opts)
	if err != nil {
		t.Fatalf("NewPlanner returned error: %v", err)
	}
	return planner
}

func payloadDates(plan []ScheduledJob, jobKey string) []string {
	dates := []string{}
	for _, scheduled := range plan {
		if scheduled.Job.JobKey != jobKey {
			continue
		}
		payload := string(scheduled.Job.Payload)
		marker := `"date":"`
		start := strings.Index(payload, marker)
		if start < 0 {
			continue
		}
		start += len(marker)
		end := strings.Index(payload[start:], `"`)
		if end >= 0 {
			dates = append(dates, payload[start:start+end])
		}
	}
	sort.Strings(dates)
	return dates
}

func scheduledKey(plan []ScheduledJob, jobKey, date string) string {
	for _, scheduled := range plan {
		if scheduled.Job.JobKey == jobKey && (date == "" || strings.Contains(string(scheduled.Job.Payload), date)) {
			return scheduled.Job.IdempotencyKey
		}
	}
	return ""
}

func assertSameKey(t *testing.T, a, b []ScheduledJob, jobKey, date string) {
	t.Helper()
	aKey, bKey := scheduledKey(a, jobKey, date), scheduledKey(b, jobKey, date)
	if aKey == "" || bKey == "" || aKey != bKey {
		t.Fatalf("keys for %s = %q and %q, want same non-empty key", jobKey, aKey, bKey)
	}
}

func assertDifferentKey(t *testing.T, a, b []ScheduledJob, jobKey, date string) {
	t.Helper()
	aKey, bKey := scheduledKey(a, jobKey, date), scheduledKey(b, jobKey, date)
	if aKey == "" || bKey == "" || aKey == bKey {
		t.Fatalf("keys for %s = %q and %q, want different non-empty keys", jobKey, aKey, bKey)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// The quota reconciliation must be scheduled even when provider ingestion is
// switched off entirely.
//
// It is the job that discovers our idea of the remaining quota is wrong, and
// the state it corrects is exactly the state that stops ingestion running. A
// reconciliation gated on the thing it unblocks would be silent precisely when
// it is needed -- which is how a stale `remaining` of 33 survived a quota
// reset and froze live scores for fifteen hours on 2026-09-22.
func TestPlannerAlwaysSchedulesQuotaReconciliation(t *testing.T) {
	// The most switched-off configuration there is.
	planner := mustPlanner(t, PlanOptions{ProviderMode: ProviderAPIFootball, ProviderInterval: 4 * time.Hour, PredictionInterval: 15 * time.Minute})
	at := time.Date(2026, 9, 22, 6, 0, 0, 0, time.UTC)

	var found *ScheduledJob
	for _, scheduled := range planner.Plan(at) {
		if scheduled.Job.JobKey == ingestion.QuotaSyncJobKey() {
			found = &scheduled
			break
		}
	}
	if found == nil {
		t.Fatal("quota reconciliation was not scheduled with provider ingestion disabled")
	}
	if found.Job.Queue == "provider-fixtures" {
		t.Fatal("quota reconciliation shares the queue it is meant to unblock")
	}
}

// Hourly, so a wrong belief has a bounded lifetime even if in-process probing
// never fires -- for instance across a restart, which resets it.
func TestQuotaReconciliationRunsHourly(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{ProviderMode: ProviderAPIFootball, ProviderInterval: 4 * time.Hour, PredictionInterval: 15 * time.Minute})
	base := time.Date(2026, 9, 22, 6, 0, 0, 0, time.UTC)

	bucketAt := func(at time.Time) string {
		for _, scheduled := range planner.Plan(at) {
			if scheduled.Job.JobKey == ingestion.QuotaSyncJobKey() {
				return scheduled.Bucket
			}
		}
		t.Fatalf("no quota reconciliation scheduled at %s", at)
		return ""
	}

	if bucketAt(base) != bucketAt(base.Add(30*time.Minute)) {
		t.Fatal("two runs inside the same hour produced different buckets")
	}
	if bucketAt(base) == bucketAt(base.Add(time.Hour)) {
		t.Fatal("the next hour reused the same bucket, so it would be deduplicated away")
	}
}
