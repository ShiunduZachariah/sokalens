package durablebudget

import (
	"context"
	"sync"
	"time"

	aicontract "github.com/ShiunduZachariah/sports-prediction-platform/packages/ai-contract"
)

// Budget is a PostgreSQL-backed ai-contract.Budget. Its methods are context-free
// to match the ai-contract interface, so each operation runs against a bounded
// background context.
type Budget struct {
	repo    *Repository
	policy  aicontract.BudgetPolicy
	lease   time.Duration
	timeout time.Duration
}

func New(policy aicontract.BudgetPolicy, repo *Repository, lease time.Duration) (*Budget, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if repo == nil {
		return nil, ErrRepositoryUnavailable
	}
	if lease <= 0 {
		lease = time.Minute
	}
	return &Budget{repo: repo, policy: policy, lease: lease, timeout: 10 * time.Second}, nil
}

func (b *Budget) Reserve(incidentID string, priority aicontract.Priority, attempts int, tokens, costMicros int64) (aicontract.ReservationHandle, error) {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	id, err := b.repo.Reserve(ctx, b.policy, incidentID, priority, attempts, tokens, costMicros, b.lease)
	if err != nil {
		return nil, err
	}
	return &reservation{repo: b.repo, id: id, timeout: b.timeout}, nil
}

func (b *Budget) Usage(incidentID string) aicontract.Usage {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()
	usage, err := b.repo.Usage(ctx, incidentID)
	if err != nil {
		return aicontract.Usage{}
	}
	return usage
}

// ReclaimStale drains expired active reservations, returning the number
// reclaimed. Call it at startup and periodically to recover concurrency slots
// leaked by a previous controller that crashed mid-call.
func (b *Budget) ReclaimStale(ctx context.Context) (int, error) {
	reclaimed := 0
	for {
		more, err := b.repo.ReclaimExpired(ctx, b.lease)
		if err != nil {
			return reclaimed, err
		}
		if !more {
			return reclaimed, nil
		}
		reclaimed++
	}
}

type reservation struct {
	repo    *Repository
	id      string
	timeout time.Duration
	once    sync.Once
	err     error
}

func (r *reservation) Settle(actual aicontract.ProviderUsage) error {
	r.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
		defer cancel()
		r.err = r.repo.Settle(ctx, r.id, actual)
	})
	return r.err
}

func (r *reservation) Release() {
	r.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
		defer cancel()
		r.err = r.repo.Release(ctx, r.id)
	})
}
