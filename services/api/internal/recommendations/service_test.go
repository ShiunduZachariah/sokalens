package recommendations

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestRankIsDeterministicDeduplicatedDiverseAndDismissalAware(t *testing.T) {
	repo := NewMemoryRepository()
	repo.Weights = map[string]float64{"favorite_team": 70, "high_confidence": 20, "exploration": 5}
	service := NewService(repo)
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	confidence := 80.0
	candidates := []Candidate{
		{ID: 1, ItemType: "match", ItemID: "a", BaseScore: 10, ReasonKey: "favorite_team", DiversityKeys: []string{"league:epl"}},
		{ID: 2, ItemType: "match", ItemID: "a", BaseScore: 20, ReasonKey: "favorite_team", DiversityKeys: []string{"league:epl"}},
		{ID: 3, ItemType: "match", ItemID: "b", BaseScore: 15, PredictionConfidence: &confidence, DiversityKeys: []string{"league:epl"}},
		{ID: 4, ItemType: "match", ItemID: "c", BaseScore: 14, DiversityKeys: []string{"league:epl"}},
		{ID: 5, ItemType: "match", ItemID: "d", BaseScore: 13, DiversityKeys: []string{"league:laliga"}},
	}
	if err := service.Dismiss(context.Background(), 42, "match", "b", "not interested", nil); err != nil {
		t.Fatal(err)
	}
	request := RankRequest{UserID: 42, Candidates: candidates, Limit: 4, MaxPerGroup: 2, Now: now}
	first, err := service.Rank(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Rank(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("rank is nondeterministic:\n%+v\n%+v", first, second)
	}
	ids := itemIDs(first)
	if !reflect.DeepEqual(ids, []string{"a", "c", "d"}) {
		t.Fatalf("unexpected ranked ids: %v", ids)
	}
	if first[0].ID != 2 {
		t.Fatalf("dedupe did not retain strongest generation: %+v", first[0])
	}
}

func TestColdStartIgnoresPersonalSignals(t *testing.T) {
	repo := NewMemoryRepository()
	repo.Weights = map[string]float64{"favorite_team": 100, "exploration": 5}
	service := NewService(repo)
	request := RankRequest{UserID: 1, ColdStart: true, Limit: 5, Candidates: []Candidate{
		{ItemType: "match", ItemID: "personal", BaseScore: 1, ReasonKey: "favorite_team"},
		{ItemType: "match", ItemID: "popular", BaseScore: 10},
	}}
	items, err := service.Rank(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(items); !reflect.DeepEqual(got, []string{"popular", "personal"}) {
		t.Fatalf("cold-start order = %v", got)
	}
}

func TestEventIngestionValidatesAndDeduplicatesBatch(t *testing.T) {
	repo := NewMemoryRepository()
	service := NewService(repo)
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	userID := int64(7)
	event := Event{UserID: &userID, EventType: " VIEW ", EntityType: " MATCH ", EntityID: "82", Source: "web", OccurredAt: now}
	if err := service.IngestEvents(context.Background(), []Event{event, event}); err != nil {
		t.Fatal(err)
	}
	if len(repo.Events) != 1 || repo.Events[0].EventType != "view" || repo.Events[0].EntityType != "match" {
		t.Fatalf("events = %+v", repo.Events)
	}
	guestID := int64(8)
	invalid := Event{UserID: &userID, GuestSessionID: &guestID, EventType: "click"}
	if err := service.IngestEvents(context.Background(), []Event{invalid}); err == nil {
		t.Fatal("expected ambiguous subject to be rejected")
	}
	if err := service.IngestEvents(context.Background(), []Event{{UserID: &userID, EventType: "view", OccurredAt: now.Add(6 * time.Minute)}}); err == nil {
		t.Fatal("expected future event to be rejected")
	}
}

func TestEventIngestionAcceptsAnonymousShareTracking(t *testing.T) {
	repo := NewMemoryRepository()
	service := NewService(repo)
	guestID := int64(8)
	event := Event{GuestSessionID: &guestID, EventType: "content_shared", EntityType: "builder", EntityID: "ref-42", Source: "web"}
	if err := service.IngestEvents(context.Background(), []Event{event}); err != nil {
		t.Fatal(err)
	}
	if len(repo.Events) != 1 || repo.Events[0].EventType != "content_shared" || repo.Events[0].GuestSessionID == nil {
		t.Fatalf("events = %+v", repo.Events)
	}
}

func TestDismissRequiresValidItem(t *testing.T) {
	service := NewService(NewMemoryRepository())
	if err := service.Dismiss(context.Background(), 0, "match", "1", "", nil); err == nil {
		t.Fatal("expected invalid dismissal")
	}
}

func itemIDs(candidates []Candidate) []string {
	ids := make([]string, len(candidates))
	for i, candidate := range candidates {
		ids[i] = candidate.ItemID
	}
	return ids
}
