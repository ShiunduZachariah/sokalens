package acca

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type fakeOrigin struct {
	calls  int
	result Result
}

func (f *fakeOrigin) List(context.Context, Query) (Result, error) { f.calls++; return f.result, nil }

type fakeCache struct {
	values     map[string][]byte
	gets, sets int
}

func (f *fakeCache) Get(_ context.Context, key string) ([]byte, error) {
	f.gets++
	v, ok := f.values[key]
	if !ok {
		return nil, errors.New("miss")
	}
	return v, nil
}
func (f *fakeCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	f.sets++
	if ttl <= 0 {
		panic("missing ttl")
	}
	f.values[key] = value
	return nil
}

func TestCachedSourceHitAvoidsOrigin(t *testing.T) {
	origin := &fakeOrigin{result: Result{Limit: 3}}
	cache := &fakeCache{values: map[string][]byte{}}
	source := NewCachedSource(origin, cache, "qa:acca", time.Minute)
	if _, err := source.List(context.Background(), Query{Limit: 3, Seed: "visitor"}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.List(context.Background(), Query{Limit: 3, Seed: "visitor"}); err != nil {
		t.Fatal(err)
	}
	if origin.calls != 1 {
		t.Fatalf("origin calls=%d, want 1", origin.calls)
	}
	if cache.sets != 1 || cache.gets != 2 {
		t.Fatalf("cache gets/sets=%d/%d", cache.gets, cache.sets)
	}
}

func TestCachedSourceUsesNamespacedKeys(t *testing.T) {
	origin := &fakeOrigin{}
	cache := &fakeCache{values: map[string][]byte{}}
	qa := NewCachedSource(origin, cache, "qa:acca", time.Second)
	prod := NewCachedSource(origin, cache, "acca", time.Second)
	if _, err := qa.List(context.Background(), Query{}); err != nil {
		t.Fatal(err)
	}
	if _, err := prod.List(context.Background(), Query{}); err != nil {
		t.Fatal(err)
	}
	if len(cache.values) != 2 {
		t.Fatalf("keys=%d, want 2 isolated keys", len(cache.values))
	}
}

func TestCachedSourceDropsInvalidCachedItems(t *testing.T) {
	origin := &fakeOrigin{}
	invalid := validTestSlip()
	invalid.CombinedFairOdds = 0
	payload, _ := json.Marshal(Result{Items: []Slip{invalid}, Limit: 1})
	cache := &fakeCache{values: map[string][]byte{}}
	source := NewCachedSource(origin, cache, "qa:acca", time.Minute)
	// Seed the exact key through the public path, keeping key construction private.
	cache.values[source.key(Query{})] = payload
	result, err := source.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("invalid cached items=%d, want 0", len(result.Items))
	}
	if origin.calls != 0 {
		t.Fatalf("cache hit called origin %d times", origin.calls)
	}
}
