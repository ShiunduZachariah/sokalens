package ingestion

import (
	"errors"
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

func TestGovernorAllowsRequestAboveReserve(t *testing.T) {
	governor := NewGovernor(GovernorConfig{DailyReserve: 20, MinuteReserve: 2})

	err := governor.CanSpend(1, provider.RateLimitHeaders{
		DailyRemaining:  22,
		MinuteRemaining: 4,
	})
	if err != nil {
		t.Fatalf("CanSpend returned error: %v", err)
	}
}

func TestGovernorBlocksDailyReserve(t *testing.T) {
	governor := NewGovernor(GovernorConfig{DailyReserve: 20, MinuteReserve: 2})

	err := governor.CanSpend(2, provider.RateLimitHeaders{
		DailyRemaining:  21,
		MinuteRemaining: 10,
	})
	if !errors.Is(err, ErrDailyQuotaReserved) {
		t.Fatalf("error = %v, want ErrDailyQuotaReserved", err)
	}
}

func TestGovernorBlocksMinuteReserve(t *testing.T) {
	governor := NewGovernor(GovernorConfig{DailyReserve: 20, MinuteReserve: 2})

	err := governor.CanSpend(2, provider.RateLimitHeaders{
		DailyRemaining:  80,
		MinuteRemaining: 3,
	})
	if !errors.Is(err, ErrMinuteQuotaReserved) {
		t.Fatalf("error = %v, want ErrMinuteQuotaReserved", err)
	}
}

func TestGovernorRejectsInvalidCost(t *testing.T) {
	governor := NewGovernor(GovernorConfig{DailyReserve: 20, MinuteReserve: 2})

	if err := governor.CanSpend(0, provider.RateLimitHeaders{}); !errors.Is(err, ErrInvalidQuotaCost) {
		t.Fatalf("error = %v, want ErrInvalidQuotaCost", err)
	}
}

func TestGovernorBlocksMissingOrExhaustedHeaders(t *testing.T) {
	governor := NewGovernor(GovernorConfig{DailyReserve: 20, MinuteReserve: 2})

	if err := governor.CanSpend(1, provider.RateLimitHeaders{}); !errors.Is(err, ErrDailyQuotaReserved) {
		t.Fatalf("error = %v, want ErrDailyQuotaReserved", err)
	}
}
