package ingestion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

type recordedLiveEvent struct {
	fixtureID int64
	provider  string
	at        time.Time
	event     apifootball.FixtureEvent
}

type recordingEvents struct {
	appended   []recordedLiveEvent
	reconciled int
	err        error
}

func (r *recordingEvents) Append(_ context.Context, fixtureID int64, provider string, at time.Time, event apifootball.FixtureEvent) (bool, error) {
	if r.err != nil {
		return false, r.err
	}
	r.appended = append(r.appended, recordedLiveEvent{fixtureID: fixtureID, provider: provider, at: at, event: event})
	return true, nil
}

func (r *recordingEvents) ReconcileScoringEvents(_ context.Context, _ int64, _ string, _ time.Time, _ []apifootball.FixtureEvent, _, _ int64, _, _ *int) (int, error) {
	r.reconciled++
	return 0, r.err
}

func liveFixtureEventBody(events string) []byte {
	return []byte(`{"errors":[],"response":[{
		"fixture":{"id":1234567,"date":"2026-09-17T15:00:00+00:00","timestamp":1789657200,"status":{"short":"2H","long":"Second Half","elapsed":63}},
		"league":{"id":39,"name":"Premier League","country":"England","season":2026,"round":"Regular Season - 5"},
		"teams":{"home":{"id":42,"name":"Arsenal"},"away":{"id":49,"name":"Chelsea"}},
		"goals":{"home":1,"away":0},
		"score":{"halftime":{"home":0,"away":0},"fulltime":{"home":null,"away":null}},
		"events":` + events + `
	}]}`)
}

func eventJob(t *testing.T, body []byte, writer liveEventWriter) (FixturesJob, *fakeCatalog) {
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
		Events:      writer,
		Now: func() time.Time {
			return time.Date(2026, 9, 17, 19, 45, 0, 0, time.UTC)
		},
	})
	return job, catalog
}

func TestLivePollWritesEmbeddedEventAgainstTheCanonicalFixture(t *testing.T) {
	writer := &recordingEvents{}
	body := liveFixtureEventBody(`[{"time":{"elapsed":63,"extra":2},"team":{"id":42},"player":{"id":7,"name":"Scorer"},"assist":{"id":8,"name":"Assist"},"type":"Goal","detail":"Penalty","comments":null}]`)
	job, catalog := eventJob(t, body, writer)

	if err := job.Run(context.Background(), jobs.Job{ID: 99, Payload: []byte(`{"live":true}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(writer.appended) != 1 {
		t.Fatalf("appended %d events, want 1", len(writer.appended))
	}
	if writer.reconciled != 1 {
		t.Fatalf("reconciled %d event lists, want 1", writer.reconciled)
	}
	if len(catalog.inserted) != 1 {
		t.Fatalf("inserted %d matches, want 1", len(catalog.inserted))
	}
	got := writer.appended[0]
	if got.fixtureID != catalog.nextID {
		t.Fatalf("event fixture=%d, canonical match=%d", got.fixtureID, catalog.nextID)
	}
	if got.event.FixtureID != 1234567 {
		t.Fatalf("provider fixture identity changed to %d", got.event.FixtureID)
	}
	// The provider's team id reaches storage unchanged. The read path resolves
	// the side from it through provider_entity_maps; translating to a canonical
	// id here would break that join and drop the side from every event.
	if got.event.TeamID == nil || *got.event.TeamID != 42 {
		t.Fatalf("event team=%v, want the provider's 42", got.event.TeamID)
	}
	if got.event.PlayerID == nil || *got.event.PlayerID != 7 {
		t.Fatalf("player id=%v, want provider player 7", got.event.PlayerID)
	}
	if got.event.Kind != apifootball.EventKindPenaltyGoal || got.event.IdentityKey == "" {
		t.Fatalf("parsed event=%+v", got.event)
	}
	if got.event.ElapsedExtra == nil || *got.event.ElapsedExtra != 2 {
		t.Fatalf("added time lost: %+v", got.event.ElapsedExtra)
	}
	if got.provider != apifootball.ProviderName || got.at != time.Date(2026, 9, 17, 19, 45, 0, 0, time.UTC) {
		t.Fatalf("provider/time=%q %v", got.provider, got.at)
	}
}

func TestNonLiveFixtureJobDoesNotWriteEmbeddedEvents(t *testing.T) {
	writer := &recordingEvents{}
	body := liveFixtureEventBody(`[{"time":{"elapsed":63},"team":{"id":42},"player":{"id":7,"name":"Scorer"},"assist":{"id":null,"name":null},"type":"Goal","detail":"Normal Goal"}]`)
	job, _ := eventJob(t, body, writer)
	if err := job.Run(context.Background(), jobs.Job{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(writer.appended) != 0 {
		t.Fatalf("non-live job appended %d events", len(writer.appended))
	}
	if writer.reconciled != 0 {
		t.Fatal("non-live job reconciled scoring events")
	}
}

// The flag reaches this job as a nil writer. Without this, a switched-off
// feature still parses every event list on every poll.
func TestDisabledWriterParsesNothing(t *testing.T) {
	body := liveFixtureEventBody(`[{"time":{"elapsed":63},"team":{"id":42},"player":{"id":7,"name":"Scorer"},"assist":{"id":null,"name":null},"type":"Goal","detail":"Normal Goal"}]`)
	job, catalog := eventJob(t, body, nil)
	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"live":true}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(catalog.inserted) != 1 {
		t.Fatal("the live poll stopped updating matches when events were disabled")
	}
}

func TestLiveEventWriteFailureDoesNotFreezeTheScoreboard(t *testing.T) {
	writer := &recordingEvents{err: errors.New("fixture_events partition missing")}
	body := liveFixtureEventBody(`[{"time":{"elapsed":63},"team":{"id":42},"player":{"id":7,"name":"Scorer"},"assist":{"id":null,"name":null},"type":"Goal","detail":"Normal Goal"}]`)
	job, catalog := eventJob(t, body, writer)
	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"live":true}`)}); err != nil {
		t.Fatalf("event storage failure failed the live poll: %v", err)
	}
	if len(catalog.inserted) != 1 {
		t.Fatal("match update was lost with event history")
	}
	if writer.reconciled != 0 {
		t.Fatal("an incomplete write triggered scoring reconciliation")
	}
}

func TestMalformedEmbeddedEventsDoNotFreezeTheScoreboard(t *testing.T) {
	writer := &recordingEvents{}
	job, catalog := eventJob(t, liveFixtureEventBody(`"not-an-array"`), writer)
	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"live":true}`)}); err != nil {
		t.Fatalf("malformed embedded events failed the live poll: %v", err)
	}
	if len(catalog.inserted) != 1 {
		t.Fatal("match update was lost with malformed events")
	}
	if len(writer.appended) != 0 {
		t.Fatalf("malformed events produced %d writes", len(writer.appended))
	}
	if writer.reconciled != 0 {
		t.Fatal("malformed events triggered reconciliation")
	}
}
