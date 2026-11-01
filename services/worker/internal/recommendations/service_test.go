package recommendations

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeRepository struct {
	users   []int64
	inputs  []Input
	signals Signals
	weights map[string]float64
	saved   map[int64][]Candidate
	mu      sync.Mutex
}

func (f *fakeRepository) ListEligibleUsers(context.Context, int) ([]int64, error) {
	return f.users, nil
}
func (f *fakeRepository) LoadInputs(context.Context, time.Time, time.Time, int) ([]Input, error) {
	return f.inputs, nil
}
func (f *fakeRepository) LoadSignals(context.Context, int64, time.Time) (Signals, error) {
	return f.signals, nil
}
func (f *fakeRepository) LoadWeights(context.Context) (map[string]float64, error) {
	return f.weights, nil
}
func (f *fakeRepository) SaveCandidates(_ context.Context, userID int64, _ string, candidates []Candidate) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saved == nil {
		f.saved = map[int64][]Candidate{}
	}
	f.saved[userID] = candidates
	return nil
}

func TestServiceGeneratesCandidatesForStoredEligibleUsers(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	repository := &fakeRepository{
		users:   []int64{1, 2},
		inputs:  []Input{{ItemType: "prediction", ItemID: "p1", MatchID: 1, StartsAt: now.Add(time.Hour), Confidence: 0.8, FreshnessAt: now}},
		signals: Signals{Favorites: map[string]bool{}, Interests: map[string]float64{}, RecentSearches: map[string]bool{}, RecentEngagement: map[string]float64{}, Viewed: map[string]bool{}, Dismissed: map[string]bool{}},
		weights: map[string]float64{"high_confidence": 20, "starting_soon": 15, "exploration": 5},
	}
	service, err := NewService(repository, Config{MaximumConcurrentUsers: 2}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	count, err := service.GenerateBatch(context.Background(), 0, "generation-1")
	if err != nil {
		t.Fatalf("GenerateBatch returned error: %v", err)
	}
	if count != 2 || len(repository.saved[1]) != 1 || len(repository.saved[2]) != 1 {
		t.Fatalf("count=%d saved=%+v", count, repository.saved)
	}
}
