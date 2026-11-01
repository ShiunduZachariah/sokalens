package ingestion

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

type capturedStatSnapshot struct {
	fixtureID  int64
	teamID     int64
	capturedAt time.Time
	minute     *int
	stats      StatRecord
}

type fakeInPlayStatisticsWriter struct {
	appended []capturedStatSnapshot
	err      error
	// duplicate makes every write report "already present", the way a
	// re-observed cutoff does.
	duplicate bool
}

func (f *fakeInPlayStatisticsWriter) AppendSnapshot(
	_ context.Context,
	fixtureID int64,
	teamID int64,
	capturedAt time.Time,
	minute *int,
	stats StatRecord,
) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.appended = append(f.appended, capturedStatSnapshot{
		fixtureID:  fixtureID,
		teamID:     teamID,
		capturedAt: capturedAt,
		minute:     minute,
		stats:      stats,
	})
	return !f.duplicate, nil
}

type fakeLiveFixtureLister struct {
	ids   []int64
	limit int
	err   error
}

func (f *fakeLiveFixtureLister) LiveProviderFixtureIDs(_ context.Context, limit int) ([]int64, error) {
	f.limit = limit
	return f.ids, f.err
}

// The real fixtures?ids= capture, ten live fixtures, taken 2026-10-02. It
// replaced the synthetic payload these tests were written against, so the job
// is now exercised with the number of fixtures, the number of teams and the
// statistic types the provider actually sends.
func capturedEmbeddedBody(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("../provider/apifootball/testdata/fixtures-ids-embedded.captured.json")
	if err != nil {
		t.Fatalf("read captured embedded payload: %v", err)
	}
	return body
}

func inPlayStatsJob(t *testing.T, opts InPlayStatisticsJobOptions) (InPlayStatisticsJob, *fakeProviderClient, *fakeInPlayStatisticsWriter) {
	t.Helper()
	body := capturedEmbeddedBody(t)
	client := &fakeProviderClient{response: provider.Response{
		Provider:      "api-football",
		Endpoint:      "fixtures",
		StatusCode:    200,
		Body:          body,
		Latency:       40 * time.Millisecond,
		ResponseBytes: len(body),
	}}
	writer := &fakeInPlayStatisticsWriter{}

	opts.Client = client
	opts.RequestLogs = &fakeRequestLogger{}
	opts.Statistics = writer
	if opts.EntityMaps == nil {
		opts.EntityMaps = &fakeEntityMapResolver{lookup: map[string]string{
			"api-football:match:1493161": "1001",
			"api-football:match:1498856": "1002",
			"api-football:team:453":      "2453",
			"api-football:team:478":      "2478",
		}}
	}
	opts.Now = func() time.Time { return time.Date(2026, 9, 30, 15, 3, 0, 0, time.UTC) }
	return NewInPlayStatisticsJob(opts), client, writer
}

// The gate is the whole point of the flag: the embedded statistics shape is an
// assumption until a capture confirms it, and a wrong assumption decodes to
// zero teams without erroring. Off, the job must not spend a single request --
// and it must not fail either, because a scheduled job that errors retries,
// alerts, and looks like a broken poll rather than a disabled one.
func TestInPlayStatisticsJobDisabledSpendsNoRequestAndDoesNotFail(t *testing.T) {
	job, client, writer := inPlayStatsJob(t, InPlayStatisticsJobOptions{
		Enabled: false,
		Live:    &fakeLiveFixtureLister{ids: []int64{1208021}},
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 7}); err != nil {
		t.Fatalf("a disabled job returned an error: %v", err)
	}
	if client.calls != 0 {
		t.Fatalf("a disabled job made %d provider requests, want 0", client.calls)
	}
	if len(writer.appended) != 0 {
		t.Fatalf("a disabled job wrote %d snapshots, want 0", len(writer.appended))
	}
}

// The reason this job exists rather than the per-fixture statistics endpoint:
// coverage has to cost one request per twenty matches, not one per match. At
// 200 concurrent fixtures the per-fixture shape would be 200 requests a minute
// against a 75,000 daily budget.
func TestInPlayStatisticsJobBatchesTwentyFixturesPerRequest(t *testing.T) {
	ids := make([]int64, 0, 45)
	for id := int64(1208021); id < 1208021+45; id++ {
		ids = append(ids, id)
	}
	job, client, _ := inPlayStatsJob(t, InPlayStatisticsJobOptions{
		Enabled: true,
		Live:    &fakeLiveFixtureLister{ids: ids},
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 8}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	// 45 ids -> 20 + 20 + 5.
	if client.calls != 3 {
		t.Fatalf("45 live fixtures cost %d requests, want 3", client.calls)
	}
	if got := client.query.Get("ids"); strings.Count(got, "-") != 4 {
		t.Fatalf("last batch ids = %q, want 5 ids joined by '-'", got)
	}
	if client.endpoint != "fixtures" {
		t.Fatalf("endpoint = %q, want fixtures", client.endpoint)
	}
}

// What the writer receives, and what it does not.
//
// A fixture whose payload carried no statistics writes nothing: empty is "not
// observed", never zero. A team the provider reports but we have never mapped
// is skipped, not guessed -- rule 1 of the in-play contract, and the mistake
// that once split one team history across two ids.
func TestInPlayStatisticsJobWritesOnlyObservedAndMappedTeams(t *testing.T) {
	job, _, writer := inPlayStatsJob(t, InPlayStatisticsJobOptions{
		Enabled: true,
		Live:    &fakeLiveFixtureLister{ids: []int64{1493161, 1498856}},
		EntityMaps: &fakeEntityMapResolver{lookup: map[string]string{
			"api-football:match:1493161": "1001",
			"api-football:match:1498856": "1002",
			"api-football:team:453":      "2453",
			// Team 478, and both teams of the second fixture, deliberately
			// unmapped -- so the one write below is the mapped team, not the
			// first team the payload happened to list.
		}},
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 9}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(writer.appended) != 1 {
		t.Fatalf("wrote %d snapshots, want 1 (the one mapped team)", len(writer.appended))
	}
	got := writer.appended[0]
	if got.fixtureID != 1001 || got.teamID != 2453 {
		t.Fatalf("wrote fixture %d team %d, want canonical 1001/2453", got.fixtureID, got.teamID)
	}
	if got.minute == nil || *got.minute != 45 {
		t.Fatalf("match minute = %v, want 45 from the provider clock", got.minute)
	}
	if got.stats.Corners == nil || *got.stats.Corners != 1 {
		t.Fatalf("corners = %v, want 1", got.stats.Corners)
	}
	// Same units as fixture_statistics: cards shown, yellow + red. The column
	// was once yellow*10 + red*25 in one writer and yellow + red in another,
	// and the engine settled total_cards lines against it.
	if got.stats.CardPoints == nil || got.stats.YellowCards == nil || got.stats.RedCards == nil {
		t.Fatalf("card counts not captured: yellow=%v red=%v points=%v",
			got.stats.YellowCards, got.stats.RedCards, got.stats.CardPoints)
	}
	// This team was shown one yellow and one red, so card_points must be 2.
	// The synthetic payload had no red card, which made the old assertion
	// (points == yellow) pass whether or not reds were counted at all -- the
	// exact blind spot that let two card scales coexist until 2026-09-24.
	if want := *got.stats.YellowCards + *got.stats.RedCards; *got.stats.CardPoints != want {
		t.Fatalf("card_points = %d, want %d (yellow %d + red %d)", *got.stats.CardPoints, want,
			*got.stats.YellowCards, *got.stats.RedCards)
	}
	if *got.stats.CardPoints != 2 {
		t.Fatalf("card_points = %d, want 2", *got.stats.CardPoints)
	}
	if !got.capturedAt.Equal(time.Date(2026, 9, 30, 15, 3, 0, 0, time.UTC)) {
		t.Fatalf("captured_at = %s, want the batch's single capture time", got.capturedAt)
	}
}

// Both teams of one match must share a captured_at. Stamping each row as it is
// written would put the two sides of the same observation at different points
// in the series, and a reader comparing home and away corners at minute 63
// would be comparing two different moments.
func TestInPlayStatisticsJobGivesOneBatchASingleCaptureTime(t *testing.T) {
	job, _, writer := inPlayStatsJob(t, InPlayStatisticsJobOptions{
		Enabled: true,
		Live:    &fakeLiveFixtureLister{ids: []int64{1493161}},
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 10}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(writer.appended) != 2 {
		t.Fatalf("wrote %d snapshots, want 2 (both mapped teams of one fixture)", len(writer.appended))
	}
	if !writer.appended[0].capturedAt.Equal(writer.appended[1].capturedAt) {
		t.Fatalf("the two teams of one match got different capture times: %s and %s",
			writer.appended[0].capturedAt, writer.appended[1].capturedAt)
	}
}

// No live match means no request. The live set comes from our own database, so
// learning that nothing is in play is free -- and a poll that asked the
// provider to tell it would spend a request every minute through the night.
func TestInPlayStatisticsJobWithNoLiveFixturesSpendsNothing(t *testing.T) {
	job, client, _ := inPlayStatsJob(t, InPlayStatisticsJobOptions{
		Enabled: true,
		Live:    &fakeLiveFixtureLister{ids: nil},
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 11}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if client.calls != 0 {
		t.Fatalf("an empty live set cost %d requests, want 0", client.calls)
	}
}

// An explicit id list overrides the live lookup, for a targeted manual run.
func TestInPlayStatisticsJobAcceptsExplicitIDs(t *testing.T) {
	lister := &fakeLiveFixtureLister{ids: []int64{999999}}
	job, client, _ := inPlayStatsJob(t, InPlayStatisticsJobOptions{Enabled: true, Live: lister})
	if err := job.Run(context.Background(), jobs.Job{ID: 12, Payload: []byte(`{"ids":[1208021,1208022]}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	ids := client.query.Get("ids")
	if !strings.Contains(ids, strconv.Itoa(1208021)) || strings.Contains(ids, "999999") {
		t.Fatalf("ids = %q, want the payload's ids and not the live lookup's", ids)
	}
}

// One failing batch must not abandon the pass. The next tick is a minute away,
// so a lost batch costs one series point; an abandoned pass costs every match
// scheduled after the failure.
func TestInPlayStatisticsJobContinuesAfterABatchFails(t *testing.T) {
	ids := make([]int64, 0, 25)
	for id := int64(1493161); id < 1493161+25; id++ {
		ids = append(ids, id)
	}
	job, client, writer := inPlayStatsJob(t, InPlayStatisticsJobOptions{
		Enabled: true,
		Live:    &fakeLiveFixtureLister{ids: ids},
	})
	// The fake client fails only the first call.
	body := capturedEmbeddedBody(t)
	calls := 0
	failing := &failOnceClient{inner: client, body: body, calls: &calls}
	job.opts.Client = failing

	if err := job.Run(context.Background(), jobs.Job{ID: 13}); err != nil {
		t.Fatalf("a single failed batch failed the whole pass: %v", err)
	}
	if calls != 2 {
		t.Fatalf("made %d requests, want 2 (the failure and the batch after it)", calls)
	}
	if len(writer.appended) == 0 {
		t.Fatal("nothing was written: the pass gave up after the first failed batch")
	}
}

// failOnceClient fails its first request and serves the payload afterwards.
type failOnceClient struct {
	inner *fakeProviderClient
	body  []byte
	calls *int
}

func (f *failOnceClient) Get(ctx context.Context, endpoint string, query url.Values) (provider.Response, error) {
	*f.calls++
	if *f.calls == 1 {
		return provider.Response{Provider: "api-football", Endpoint: endpoint, StatusCode: 429}, errors.New("rate limited")
	}
	return f.inner.Get(ctx, endpoint, query)
}
