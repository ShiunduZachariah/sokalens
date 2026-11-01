package predictions

import (
	"testing"
	"time"
)

func TestIdempotencyKeyUsesSharedUTCRefreshBucket(t *testing.T) {
	now := time.Date(2026, time.August, 5, 13, 37, 0, 0, time.FixedZone("EAT", 3*60*60))
	first := IdempotencyKey("generate", now, DefaultRefreshWindow)
	second := IdempotencyKey("generate", now.Add(7*time.Minute), DefaultRefreshWindow)
	if first != "schedule:predictions:generate:20260805T103000Z" || second != first {
		t.Fatalf("keys=%q and %q", first, second)
	}
	if next := IdempotencyKey("generate", now.Add(8*time.Minute), DefaultRefreshWindow); next == first {
		t.Fatalf("next bucket key=%q, want different from %q", next, first)
	}
}
