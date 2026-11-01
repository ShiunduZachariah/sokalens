package ingestion

import (
	"errors"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

var (
	ErrInvalidQuotaCost    = errors.New("quota cost must be positive")
	ErrDailyQuotaReserved  = errors.New("daily quota reserve reached")
	ErrMinuteQuotaReserved = errors.New("minute quota reserve reached")
)

type GovernorConfig struct {
	DailyReserve  int
	MinuteReserve int
}

type Governor struct {
	cfg GovernorConfig
}

func NewGovernor(cfg GovernorConfig) Governor {
	return Governor{cfg: cfg}
}

func (g Governor) CanSpend(cost int, headers provider.RateLimitHeaders) error {
	if cost <= 0 {
		return ErrInvalidQuotaCost
	}
	if headers.DailyRemaining-cost < g.cfg.DailyReserve {
		return ErrDailyQuotaReserved
	}
	if headers.MinuteRemaining-cost < g.cfg.MinuteReserve {
		return ErrMinuteQuotaReserved
	}
	return nil
}
