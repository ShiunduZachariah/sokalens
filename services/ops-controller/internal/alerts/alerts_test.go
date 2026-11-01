package alerts

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func validBody() []byte {
	return []byte(`{"version":"v1","alert_id":"alert-1","source":"prometheus","name":"api_latency","service":"api","environment":"staging","severity":"critical","starts_at":"2026-08-14T10:00:00Z","summary":"API latency is above its SLO."}`)
}

func validator() *Validator {
	v := NewValidator(testSecret, 5*time.Minute, 100)
	v.now = func() time.Time { return time.Date(2026, 8, 14, 10, 2, 0, 0, time.UTC) }
	return v
}

func TestValidateAcceptsSignedStrictAlert(t *testing.T) {
	v := validator()
	timestamp := "2026-08-14T10:01:00Z"
	got, err := v.Validate(timestamp, "delivery-1", Sign(testSecret, timestamp, validBody()), validBody())
	if err != nil {
		t.Fatal(err)
	}
	if got.Severity != SeverityCritical || got.Service != "api" || len(got.IdempotencyKey) != 64 {
		t.Fatalf("unexpected accepted alert: %+v", got)
	}

	v2 := validator()
	again, err := v2.Validate(timestamp, "delivery-other", Sign(testSecret, timestamp, validBody()), validBody())
	if err != nil || again.IdempotencyKey != got.IdempotencyKey {
		t.Fatalf("idempotency key is not deterministic: %v %+v", err, again)
	}
}

func TestValidateRejectsUnauthenticatedStaleReplayAndMalformedAlerts(t *testing.T) {
	timestamp := "2026-08-14T10:01:00Z"
	tests := []struct {
		name, ts, delivery, signature string
		body                          []byte
		want                          error
	}{
		{"bad signature", timestamp, "delivery-1", Sign("wrong-secret-wrong-secret-wrong-se", timestamp, validBody()), validBody(), ErrSignature},
		{"stale", "2026-08-14T09:00:00Z", "delivery-1", Sign(testSecret, "2026-08-14T09:00:00Z", validBody()), validBody(), ErrStale},
		{"unknown field", timestamp, "delivery-1", "", []byte(`{"version":"v1","extra":true}`), ErrInvalid},
		{"bad delivery", timestamp, "spaces forbidden", Sign(testSecret, timestamp, validBody()), validBody(), ErrInvalid},
		{"bad severity", timestamp, "delivery-1", "", []byte(`{"version":"v1","alert_id":"a","source":"s","name":"n","service":"api","environment":"prod","severity":"panic","starts_at":"2026-08-14T10:00:00Z","summary":"bad"}`), ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.signature == "" {
				tc.signature = Sign(testSecret, tc.ts, tc.body)
			}
			_, err := validator().Validate(tc.ts, tc.delivery, tc.signature, tc.body)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}

	v := validator()
	sig := Sign(testSecret, timestamp, validBody())
	if _, err := v.Validate(timestamp, "delivery-1", sig, validBody()); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(timestamp, "delivery-1", sig, validBody()); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay error = %v", err)
	}
	v.Release("delivery-1")
	if _, err := v.Validate(timestamp, "delivery-1", sig, validBody()); err != nil {
		t.Fatalf("released delivery was not retryable: %v", err)
	}
}

func TestReplayStoreFailsClosedAtBound(t *testing.T) {
	v := validator()
	v.limit = 1
	timestamp := "2026-08-14T10:01:00Z"
	sig := Sign(testSecret, timestamp, validBody())
	if _, err := v.Validate(timestamp, "delivery-1", sig, validBody()); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(timestamp, "delivery-2", sig, validBody()); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
}

func TestConcurrentReplayStormAcceptsExactlyOnce(t *testing.T) {
	v := validator()
	timestamp := "2026-08-14T10:01:00Z"
	sig := Sign(testSecret, timestamp, validBody())
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.Validate(timestamp, "storm-delivery", sig, validBody()); err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrReplay) {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted = %d, want 1", accepted.Load())
	}
}

func TestFutureDatedDeliveryRemainsReplayProtectedForEntireValidWindow(t *testing.T) {
	v := NewValidator(testSecret, 5*time.Minute, 100)
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	v.now = func() time.Time { return now }
	timestamp := "2026-08-14T10:05:00Z"
	sig := Sign(testSecret, timestamp, validBody())
	if _, err := v.Validate(timestamp, "future-delivery", sig, validBody()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(9*time.Minute + 59*time.Second)
	if _, err := v.Validate(timestamp, "future-delivery", sig, validBody()); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay near end of window = %v", err)
	}
}

func TestDisabledValidatorFailsClosed(t *testing.T) {
	_, err := NewValidator("", time.Minute, 100).Validate(time.Now().Format(time.RFC3339), "delivery-1", "v1=00", validBody())
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("error = %v", err)
	}
}
