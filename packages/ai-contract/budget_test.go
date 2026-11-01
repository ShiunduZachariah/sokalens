package aicontract

import (
	"errors"
	"sync"
	"testing"
)

func policy() BudgetPolicy {
	return BudgetPolicy{
		MaxTokensPerIncident:     1_000,
		MaxCostMicrosPerIncident: 10_000,
		MaxAttemptsPerIncident:   3,
		MaxParallelPerIncident:   2,
		GlobalParallelLimit:      3,
		ReservedCriticalSlots:    1,
	}
}

func TestReservationSettlesActualUsageExactlyOnceConcurrently(t *testing.T) {
	manager, err := NewBudgetManager(policy())
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := manager.Reserve("inc-settle", PriorityCritical, 3, 900, 9_000)
	if err != nil {
		t.Fatal(err)
	}
	actual := ProviderUsage{Attempts: 2, InputTokens: 200, OutputTokens: 100, TotalTokens: 300, CostMicros: 2_500, Complete: true}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := reservation.Settle(actual); err != nil {
				t.Errorf("settle: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := manager.Usage("inc-settle"); got != (Usage{Attempts: 2, Tokens: 300, CostMicros: 2_500}) {
		t.Fatalf("actual usage not reconciled exactly once: %+v", got)
	}
}

func TestReservationMissingOrExcessUsageFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		actual ProviderUsage
		want   Usage
	}{
		{name: "missing", actual: ProviderUsage{}, want: Usage{Attempts: 3, Tokens: 900, CostMicros: 9_000}},
		{name: "too many attempts", actual: ProviderUsage{Attempts: 4, Complete: true}, want: Usage{Attempts: 4, Tokens: 900, CostMicros: 9_000}},
		{name: "token overflow", actual: ProviderUsage{Attempts: 1, InputTokens: 901, TotalTokens: 901, Complete: true}, want: Usage{Attempts: 3, Tokens: 901, CostMicros: 9_000}},
		{name: "cost overflow", actual: ProviderUsage{Attempts: 1, CostMicros: 9_001, Complete: true}, want: Usage{Attempts: 3, Tokens: 900, CostMicros: 9_001}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, err := NewBudgetManager(policy())
			if err != nil {
				t.Fatal(err)
			}
			reservation, err := manager.Reserve("inc", PriorityCritical, 3, 900, 9_000)
			if err != nil {
				t.Fatal(err)
			}
			if err := reservation.Settle(test.actual); !errors.Is(err, ErrUsageAccounting) {
				t.Fatalf("settle error = %v", err)
			}
			if got := manager.Usage("inc"); got != test.want {
				t.Fatalf("unsafe settlement refunded reservation: %+v", got)
			}
			if _, err := manager.Reserve("inc", PriorityCritical, 1, 1, 0); !errors.Is(err, ErrBudgetExhausted) {
				t.Fatalf("overrun did not exhaust future work: %v", err)
			}
		})
	}
}

func TestBudgetPolicyValidation(t *testing.T) {
	valid := policy()
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.ReservedCriticalSlots = valid.GlobalParallelLimit
	if err := valid.Validate(); err == nil {
		t.Fatal("accepted a reservation that consumes every slot")
	}
}

func TestBudgetManagerChargesAttemptsTokensAndCost(t *testing.T) {
	manager, err := NewBudgetManager(policy())
	if err != nil {
		t.Fatal(err)
	}
	release, err := manager.Acquire("inc-1", PriorityNormal, 400, 4_000)
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
	usage := manager.Usage("inc-1")
	if usage.Attempts != 1 || usage.Tokens != 400 || usage.CostMicros != 4_000 || usage.Active != 0 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if _, err := manager.Acquire("inc-1", PriorityNormal, 700, 1); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("expected token exhaustion, got %v", err)
	}
}

func TestCriticalReservationCannotBeConsumedByNormalWork(t *testing.T) {
	manager, err := NewBudgetManager(policy())
	if err != nil {
		t.Fatal(err)
	}
	release1, err := manager.Acquire("normal-1", PriorityNormal, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer release1()
	release2, err := manager.Acquire("normal-2", PriorityNormal, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer release2()
	if _, err := manager.Acquire("normal-3", PriorityNormal, 100, 1); !errors.Is(err, ErrConcurrencyFull) {
		t.Fatalf("normal work consumed critical reservation: %v", err)
	}
	criticalRelease, err := manager.Acquire("critical", PriorityCritical, 100, 1)
	if err != nil {
		t.Fatalf("critical work could not use reserved capacity: %v", err)
	}
	criticalRelease()
}

func TestPerIncidentParallelLimitAndAttemptLimit(t *testing.T) {
	manager, err := NewBudgetManager(policy())
	if err != nil {
		t.Fatal(err)
	}
	release1, err := manager.Acquire("inc-1", PriorityCritical, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	release2, err := manager.Acquire("inc-1", PriorityCritical, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Acquire("inc-1", PriorityCritical, 100, 1); !errors.Is(err, ErrConcurrencyFull) {
		t.Fatalf("expected per-incident concurrency rejection, got %v", err)
	}
	release1()
	release2()
	release3, err := manager.Acquire("inc-1", PriorityCritical, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	release3()
	if _, err := manager.Acquire("inc-1", PriorityCritical, 100, 1); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("expected attempt exhaustion, got %v", err)
	}
}
