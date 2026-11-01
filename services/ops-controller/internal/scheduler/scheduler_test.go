package scheduler

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type handlerFunc func(context.Context, Job) error

func (f handlerFunc) Handle(ctx context.Context, job Job) error { return f(ctx, job) }

func testConfig(concurrency, p0, p1, admission int) Config {
	return Config{
		Concurrency: concurrency, P0Reserved: p0, P1Reserved: p1,
		AdmissionCapacity: admission, PollInterval: time.Millisecond,
		AgingInterval: time.Second, Weights: [3]int{4, 2, 1}, Now: time.Now,
	}
}

func startCoordinator(t *testing.T, c *Coordinator) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runDone:
			if err != nil {
				t.Errorf("Run() error = %v", err)
			}
		case <-time.After(time.Second):
			t.Error("coordinator did not stop")
		}
	})
	return cancel
}

func submit(t *testing.T, c *Coordinator, job Job) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Submit(ctx, job); err != nil {
		t.Fatalf("Submit(%s) error = %v", job.ID, err)
	}
}

func receive(t *testing.T, ch <-chan Job) Job {
	t.Helper()
	select {
	case job := <-ch:
		return job
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job start")
		return Job{}
	}
}

func TestCoordinatorEnforcesGlobalCapAndPriorityReservations(t *testing.T) {
	started := make(chan Job, 16)
	release := make(chan struct{})
	c, err := New(nil, handlerFunc(func(_ context.Context, job Job) error {
		started <- job
		<-release
		return nil
	}), testConfig(4, 1, 1, 16))
	if err != nil {
		t.Fatal(err)
	}
	startCoordinator(t, c)

	for i := 0; i < 6; i++ {
		submit(t, c, Job{ID: fmt.Sprintf("normal-%d", i), Service: fmt.Sprintf("n-%d", i), Priority: P2})
	}
	first, second := receive(t, started), receive(t, started)
	if first.Priority != P2 || second.Priority != P2 {
		t.Fatalf("initial priorities = %v, %v", first.Priority, second.Priority)
	}
	select {
	case job := <-started:
		t.Fatalf("normal work consumed reserved capacity: %s", job.ID)
	case <-time.After(30 * time.Millisecond):
	}

	submit(t, c, Job{ID: "urgent-p1", Service: "p1", Priority: P1})
	if job := receive(t, started); job.ID != "urgent-p1" {
		t.Fatalf("started %s, want urgent-p1", job.ID)
	}
	submit(t, c, Job{ID: "urgent-p0", Service: "p0", Priority: P0})
	if job := receive(t, started); job.ID != "urgent-p0" {
		t.Fatalf("started %s, want urgent-p0", job.ID)
	}
	select {
	case job := <-started:
		t.Fatalf("exceeded global cap with %s", job.ID)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
}

func TestCoordinatorRunsServicesInParallelButExcludesSameService(t *testing.T) {
	var mu sync.Mutex
	active := map[string]int{}
	maxByService := map[string]int{}
	var global, maxGlobal int
	started := make(chan Job, 8)
	release := make(chan struct{})
	c, err := New(nil, handlerFunc(func(_ context.Context, job Job) error {
		mu.Lock()
		active[job.Service]++
		global++
		if active[job.Service] > maxByService[job.Service] {
			maxByService[job.Service] = active[job.Service]
		}
		if global > maxGlobal {
			maxGlobal = global
		}
		mu.Unlock()
		started <- job
		<-release
		mu.Lock()
		active[job.Service]--
		global--
		mu.Unlock()
		return nil
	}), testConfig(3, 0, 0, 8))
	if err != nil {
		t.Fatal(err)
	}
	startCoordinator(t, c)

	submit(t, c, Job{ID: "a1", Service: "a", Priority: P0})
	submit(t, c, Job{ID: "a2", Service: "a", Priority: P0})
	submit(t, c, Job{ID: "b1", Service: "b", Priority: P0})
	one, two := receive(t, started), receive(t, started)
	if one.Service == two.Service {
		t.Fatalf("same service ran concurrently: %s and %s", one.ID, two.ID)
	}
	select {
	case job := <-started:
		t.Fatalf("second same-service job started early: %s", job.ID)
	case <-time.After(30 * time.Millisecond):
	}
	mu.Lock()
	if maxGlobal != 2 || maxByService["a"] > 1 || maxByService["b"] > 1 {
		t.Fatalf("max global=%d, max by service=%v", maxGlobal, maxByService)
	}
	mu.Unlock()
	close(release)
}

func TestAgingPreventsNormalPriorityStarvation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	started := make(chan Job, 16)
	releases := make(chan struct{}, 16)
	config := testConfig(1, 0, 0, 16)
	config.Now = func() time.Time { return now }
	config.AgingInterval = time.Second
	c, err := New(nil, handlerFunc(func(_ context.Context, job Job) error {
		started <- job
		<-releases
		return nil
	}), config)
	if err != nil {
		t.Fatal(err)
	}
	startCoordinator(t, c)

	submit(t, c, Job{ID: "blocker", Service: "blocker", Priority: P0})
	receive(t, started)
	submit(t, c, Job{ID: "aged-normal", Service: "normal", Priority: P2, EnqueuedAt: now.Add(-2 * time.Second)})
	for i := 0; i < 8; i++ {
		submit(t, c, Job{ID: fmt.Sprintf("p0-%d", i), Service: fmt.Sprintf("p0-%d", i), Priority: P0, EnqueuedAt: now})
	}
	releases <- struct{}{}
	if job := receive(t, started); job.ID != "aged-normal" {
		t.Fatalf("first waiting job = %s, want aged-normal", job.ID)
	}
	for i := 0; i < 9; i++ {
		releases <- struct{}{}
	}
}

func TestAgingCanUseOtherwiseIdleReservedCapacity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	started := make(chan Job, 1)
	release := make(chan struct{})
	config := testConfig(2, 1, 1, 2)
	config.Now = func() time.Time { return now }
	config.AgingInterval = time.Second
	c, err := New(nil, handlerFunc(func(_ context.Context, job Job) error {
		started <- job
		<-release
		return nil
	}), config)
	if err != nil {
		t.Fatal(err)
	}
	startCoordinator(t, c)
	submit(t, c, Job{ID: "aged-normal", Service: "normal", Priority: P2, EnqueuedAt: now.Add(-2 * time.Second)})
	if job := receive(t, started); job.ID != "aged-normal" {
		t.Fatalf("started %s, want aged-normal", job.ID)
	}
	close(release)
}

func TestWeightedSelectionAllowsNormalWorkUnderContinuousUrgentBacklog(t *testing.T) {
	started := make(chan Job, 16)
	releases := make(chan struct{}, 16)
	config := testConfig(1, 0, 0, 16)
	config.AgingInterval = time.Hour
	c, err := New(nil, handlerFunc(func(_ context.Context, job Job) error {
		started <- job
		<-releases
		return nil
	}), config)
	if err != nil {
		t.Fatal(err)
	}
	startCoordinator(t, c)

	submit(t, c, Job{ID: "blocker", Service: "blocker", Priority: P0})
	receive(t, started)
	for i := 0; i < 8; i++ {
		submit(t, c, Job{ID: fmt.Sprintf("urgent-%d", i), Service: fmt.Sprintf("u-%d", i), Priority: P0})
	}
	submit(t, c, Job{ID: "normal", Service: "normal", Priority: P2})
	releases <- struct{}{}
	foundAt := -1
	for i := 0; i < 7; i++ {
		job := receive(t, started)
		if job.ID == "normal" {
			foundAt = i
			releases <- struct{}{}
			break
		}
		releases <- struct{}{}
	}
	if foundAt < 0 {
		t.Fatal("weighted selection starved normal work")
	}
	for i := 0; i < 9; i++ {
		select {
		case releases <- struct{}{}:
		default:
		}
	}
}

func TestAdmissionIsBoundedAndAppliesBackpressure(t *testing.T) {
	started := make(chan Job, 1)
	release := make(chan struct{})
	c, err := New(nil, handlerFunc(func(_ context.Context, job Job) error { started <- job; <-release; return nil }), testConfig(1, 0, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	startCoordinator(t, c)
	submit(t, c, Job{ID: "one", Service: "one", Priority: P0})
	receive(t, started)
	submit(t, c, Job{ID: "two", Service: "two", Priority: P0})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = c.Submit(ctx, Job{ID: "three", Service: "three", Priority: P0})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Submit() error = %v, want deadline", err)
	}
	close(release)
}

func TestCoordinatorLoadDrainsWithoutBreakingConcurrencyOrServiceCaps(t *testing.T) {
	const (
		jobCount     = 500
		serviceCount = 17
		globalCap    = 8
	)
	var activeGlobal atomic.Int32
	var maxGlobal atomic.Int32
	var activeService [serviceCount]atomic.Int32
	var maxService [serviceCount]atomic.Int32
	var completed sync.WaitGroup
	completed.Add(jobCount)
	updateMax := func(target *atomic.Int32, value int32) {
		for current := target.Load(); value > current && !target.CompareAndSwap(current, value); current = target.Load() {
		}
	}
	c, err := New(nil, handlerFunc(func(_ context.Context, job Job) error {
		service := job.Value.(int)
		global := activeGlobal.Add(1)
		perService := activeService[service].Add(1)
		updateMax(&maxGlobal, global)
		updateMax(&maxService[service], perService)
		time.Sleep(100 * time.Microsecond)
		activeService[service].Add(-1)
		activeGlobal.Add(-1)
		completed.Done()
		return nil
	}), testConfig(globalCap, 2, 2, 32))
	if err != nil {
		t.Fatal(err)
	}
	startCoordinator(t, c)
	for i := 0; i < jobCount; i++ {
		service := i % serviceCount
		submit(t, c, Job{
			ID:       fmt.Sprintf("load-%03d", i),
			Service:  fmt.Sprintf("service-%02d", service),
			Priority: Priority(i % int(priorityCount)),
			Value:    service,
		})
	}
	drained := make(chan struct{})
	go func() { completed.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("load did not drain")
	}
	if got := maxGlobal.Load(); got > globalCap || got < 2 {
		t.Fatalf("max global concurrency = %d, want 2..%d", got, globalCap)
	}
	for service := range serviceCount {
		if got := maxService[service].Load(); got > 1 {
			t.Fatalf("service %d max concurrency = %d, want <= 1", service, got)
		}
	}
}

func TestShutdownCancelsHandlersAndDoesNotLeakWorkers(t *testing.T) {
	baseline := runtime.NumGoroutine()
	started := make(chan struct{})
	var active atomic.Int32
	c, err := New(nil, handlerFunc(func(ctx context.Context, _ Job) error {
		active.Add(1)
		defer active.Add(-1)
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}), testConfig(2, 0, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- c.Run(context.Background()) }()
	submit(t, c, Job{ID: "running", Service: "svc", Priority: P0})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := <-runDone; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("active handlers = %d", got)
	}
	if err := c.Submit(context.Background(), Job{ID: "late", Service: "svc", Priority: P0}); !errors.Is(err, ErrClosed) {
		t.Fatalf("late Submit() error = %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > baseline+2 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := runtime.NumGoroutine(); got > baseline+2 {
		t.Fatalf("goroutines grew from %d to %d", baseline, got)
	}
}

func TestConfigAndJobValidation(t *testing.T) {
	config := testConfig(2, 2, 1, 2)
	if _, err := New(nil, handlerFunc(func(context.Context, Job) error { return nil }), config); err == nil {
		t.Fatal("New() accepted over-reservation")
	}
	config = testConfig(1, 0, 0, 1)
	c, err := New(nil, handlerFunc(func(context.Context, Job) error { return nil }), config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Submit(ctx, Job{Service: "svc", Priority: P0}); err == nil {
		t.Fatal("Submit() accepted missing ID")
	}
}
