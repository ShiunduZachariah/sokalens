package ingestion

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/snapshots"
)

type recordingSnapshots struct {
	appended []snapshots.LiveSnapshot
	err      error
}

func (r *recordingSnapshots) Append(_ context.Context, snapshot snapshots.LiveSnapshot) (bool, error) {
	if r.err != nil {
		return false, r.err
	}
	r.appended = append(r.appended, snapshot)
	return true, nil
}

// liveFixtureBody is one in-play fixture as the bulk live endpoint returns it.
func liveFixtureBody(statusShort string, elapsed int) []byte {
	return []byte(`{"errors":[],"response":[{
		"fixture":{"id":1234567,"date":"2026-09-08T15:00:00+00:00","timestamp":1788000000,"status":{"short":"` + statusShort + `","long":"Second Half","elapsed":` + strconv.Itoa(elapsed) + `}},
		"league":{"id":39,"name":"Premier League","country":"England","season":2026,"round":"Regular Season - 5"},
		"teams":{"home":{"id":42,"name":"Arsenal"},"away":{"id":49,"name":"Chelsea"}},
		"goals":{"home":1,"away":0},
		"score":{"halftime":{"home":0,"away":0},"fulltime":{"home":null,"away":null}}
	}]}`)
}

func snapshotJob(t *testing.T, body []byte, writer liveSnapshotWriter) (FixturesJob, *fakeCatalog) {
	t.Helper()
	client := &fakeProviderClient{response: provider.Response{
		Provider: "api-football", Endpoint: "fixtures", StatusCode: 200,
		Body: body, Latency: 30 * time.Millisecond, ResponseBytes: len(body),
	}}
	catalog := &fakeCatalog{}
	job := NewFixturesJob(FixturesJobOptions{
		Client:      client,
		RequestLogs: &fakeRequestLogger{},
		EntityMaps:  &fakeEntityMapResolver{},
		Catalog:     catalog,
		Snapshots:   writer,
	})
	return job, catalog
}

func TestLivePollRecordsASnapshotOfAnInPlayFixture(t *testing.T) {
	writer := &recordingSnapshots{}
	job, _ := snapshotJob(t, liveFixtureBody("2H", 63), writer)

	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"live":true}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(writer.appended) != 1 {
		t.Fatalf("appended %d snapshots, want 1", len(writer.appended))
	}
	snapshot := writer.appended[0]
	if snapshot.Status != "2H" || snapshot.ElapsedMinute == nil || *snapshot.ElapsedMinute != 63 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.HomeGoals == nil || *snapshot.HomeGoals != 1 || snapshot.AwayGoals == nil || *snapshot.AwayGoals != 0 {
		t.Fatalf("score = %+v", snapshot)
	}
	// Red cards are not in the bulk live response. Nil says "not observed";
	// zero would claim there were none.
	if snapshot.HomeRedCards != nil || snapshot.AwayRedCards != nil {
		t.Fatalf("red cards were invented: %+v", snapshot)
	}
	if snapshot.ProviderTimestamp == nil {
		t.Fatal("provider timestamp was dropped")
	}
}

func TestAFinishedFixtureIsNotAPointOnATimeline(t *testing.T) {
	writer := &recordingSnapshots{}
	job, _ := snapshotJob(t, liveFixtureBody("FT", 90), writer)

	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"live":true}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(writer.appended) != 0 {
		t.Fatalf("appended %d snapshots for a finished fixture", len(writer.appended))
	}
}

// The default. Nil means the job does not construct a snapshot, does not hash
// it, and does not touch the database.
func TestSnapshotsDisabledCostsNothingAndBreaksNothing(t *testing.T) {
	job, catalog := snapshotJob(t, liveFixtureBody("2H", 63), nil)
	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"live":true}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(catalog.inserted) == 0 {
		t.Fatal("the match itself was not ingested")
	}
}

// The load-bearing decision. A snapshot is valuable precisely because it
// cannot be re-fetched, so the instinct is to treat a failed write as fatal.
// But the same job updates `matches`, which is what the live scoreboard reads:
// failing here would freeze live scores for every fixture in the batch, over
// and over, for as long as the cause persisted -- turning a lost dataset into
// the stuck clock this system just finished fixing.
func TestASnapshotFailureDoesNotFreezeTheScoreboard(t *testing.T) {
	writer := &recordingSnapshots{err: errors.New("relation \"fixture_live_snapshots_2027_01\" does not exist")}
	job, catalog := snapshotJob(t, liveFixtureBody("2H", 63), writer)

	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"live":true}`)}); err != nil {
		t.Fatalf("a snapshot failure failed the live poll: %v", err)
	}
	if len(catalog.inserted) == 0 {
		t.Fatal("the match update was lost with the snapshot")
	}
}
