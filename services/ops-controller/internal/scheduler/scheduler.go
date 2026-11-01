// Package scheduler coordinates bounded, priority-aware execution of operations jobs.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Priority is ordered from the most urgent (P0) to normal background work (P2).
type Priority uint8

const (
	P0 Priority = iota
	P1
	P2
	priorityCount
)

// Job is the minimum scheduling projection of a durable job. ID, Service and
// Priority map directly onto the OPS-006 job claim; Value may carry that claim.
type Job struct {
	ID         string
	Service    string
	Priority   Priority
	EnqueuedAt time.Time
	Value      any
}

// Source supplies already-claimed jobs. Next must honor context cancellation.
// Returning ErrNoJob asks the coordinator to poll again; io.EOF stops polling.
type Source interface {
	Next(context.Context) (Job, error)
}

// Handler executes a job and must honor context cancellation.
type Handler interface {
	Handle(context.Context, Job) error
}

var (
	ErrNoJob      = errors.New("scheduler: no job available")
	ErrClosed     = errors.New("scheduler: coordinator closed")
	ErrNotStarted = errors.New("scheduler: coordinator not started")
)

// Config controls admission and execution. Reserved capacity protects urgent
// work without creating a global sequential queue.
type Config struct {
	Concurrency       int
	P0Reserved        int
	P1Reserved        int
	AdmissionCapacity int
	PollInterval      time.Duration
	AgingInterval     time.Duration
	Weights           [3]int
	Now               func() time.Time
}

func (c Config) validate() error {
	if c.Concurrency < 1 {
		return errors.New("scheduler: concurrency must be positive")
	}
	if c.P0Reserved < 0 || c.P1Reserved < 0 || c.P0Reserved+c.P1Reserved > c.Concurrency {
		return errors.New("scheduler: invalid reserved capacity")
	}
	if c.AdmissionCapacity < c.Concurrency {
		return errors.New("scheduler: admission capacity must be at least concurrency")
	}
	if c.PollInterval <= 0 || c.AgingInterval <= 0 {
		return errors.New("scheduler: poll and aging intervals must be positive")
	}
	for _, weight := range c.Weights {
		if weight < 1 {
			return errors.New("scheduler: all priority weights must be positive")
		}
	}
	if c.Now == nil {
		return errors.New("scheduler: clock is required")
	}
	return nil
}

// DefaultConfig is conservative and suitable for a single controller process.
func DefaultConfig() Config {
	return Config{
		Concurrency: 8, P0Reserved: 2, P1Reserved: 2,
		AdmissionCapacity: 64, PollInterval: 100 * time.Millisecond,
		AgingInterval: 30 * time.Second, Weights: [3]int{4, 2, 1}, Now: time.Now,
	}
}

type queuedJob struct {
	job Job
	seq uint64
}

// Coordinator is safe for concurrent Submit and Shutdown calls.
type Coordinator struct {
	source  Source
	handler Handler
	config  Config

	admission chan Job
	wake      chan struct{}
	done      chan struct{}
	cancel    context.CancelFunc
	started   atomic.Bool
	closed    atomic.Bool
	sequence  atomic.Uint64
	once      sync.Once
	lifecycle sync.Mutex

	mu             sync.Mutex
	queue          []queuedJob
	active         int
	activePriority [3]int
	activeService  map[string]struct{}
	credits        [3]int
	workers        sync.WaitGroup
	pumps          sync.WaitGroup
	errors         chan error
}

func New(source Source, handler Handler, config Config) (*Coordinator, error) {
	if handler == nil {
		return nil, errors.New("scheduler: handler is required")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Coordinator{
		source: source, handler: handler, config: config,
		admission: make(chan Job), wake: make(chan struct{}, 1),
		done: make(chan struct{}), activeService: make(map[string]struct{}),
		errors: make(chan error, config.AdmissionCapacity),
	}, nil
}

// Errors reports source and handler failures without stopping unrelated work.
func (c *Coordinator) Errors() <-chan error { return c.errors }

// Submit applies bounded backpressure until the job is admitted or ctx ends.
func (c *Coordinator) Submit(ctx context.Context, job Job) error {
	if c.closed.Load() {
		return ErrClosed
	}
	if err := validateJob(job); err != nil {
		return err
	}
	select {
	case c.admission <- job:
		return nil
	case <-c.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func validateJob(job Job) error {
	if job.ID == "" || job.Service == "" {
		return errors.New("scheduler: job ID and service are required")
	}
	if job.Priority >= priorityCount {
		return errors.New("scheduler: invalid priority")
	}
	return nil
}

// Run starts admission and dispatch and blocks until cancellation and all
// cooperative handlers have stopped. A Coordinator may only be run once.
func (c *Coordinator) Run(ctx context.Context) error {
	c.lifecycle.Lock()
	if !c.started.CompareAndSwap(false, true) {
		c.lifecycle.Unlock()
		return errors.New("scheduler: coordinator already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.lifecycle.Unlock()
	if c.source != nil {
		c.pumps.Add(1)
		go func() {
			defer c.pumps.Done()
			c.pollSource(runCtx)
		}()
	}
	c.dispatch(runCtx)
	c.pumps.Wait()
	c.workers.Wait()
	c.once.Do(func() { c.closed.Store(true); close(c.done); close(c.errors) })
	return nil
}

// Shutdown cancels source polling and active handlers, then waits for Run.
func (c *Coordinator) Shutdown(ctx context.Context) error {
	c.lifecycle.Lock()
	if !c.started.Load() {
		c.lifecycle.Unlock()
		return ErrNotStarted
	}
	c.cancel()
	c.lifecycle.Unlock()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Coordinator) pollSource(ctx context.Context) {
	for {
		job, err := c.source.Next(ctx)
		switch {
		case err == nil:
			if submitErr := c.Submit(ctx, job); submitErr != nil && !errors.Is(submitErr, context.Canceled) && !errors.Is(submitErr, ErrClosed) {
				c.report(fmt.Errorf("admit source job: %w", submitErr))
			}
		case errors.Is(err, ErrNoJob):
			timer := time.NewTimer(c.config.PollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		case errors.Is(err, io.EOF), errors.Is(err, context.Canceled):
			return
		default:
			c.report(fmt.Errorf("poll source: %w", err))
			timer := time.NewTimer(c.config.PollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func (c *Coordinator) dispatch(ctx context.Context) {
	for {
		c.startEligible(ctx)
		c.mu.Lock()
		outstanding := c.active + len(c.queue)
		c.mu.Unlock()
		var admission <-chan Job
		if outstanding < c.config.AdmissionCapacity {
			admission = c.admission
		}
		select {
		case <-ctx.Done():
			return
		case job := <-admission:
			if job.EnqueuedAt.IsZero() {
				job.EnqueuedAt = c.config.Now()
			}
			c.mu.Lock()
			c.queue = append(c.queue, queuedJob{job: job, seq: c.sequence.Add(1)})
			c.mu.Unlock()
		case <-c.wake:
		}
	}
}

func (c *Coordinator) startEligible(ctx context.Context) {
	for {
		c.mu.Lock()
		index := c.selectLocked(c.config.Now())
		if index < 0 {
			c.mu.Unlock()
			return
		}
		entry := c.queue[index]
		c.queue = append(c.queue[:index], c.queue[index+1:]...)
		c.active++
		c.activePriority[entry.job.Priority]++
		c.activeService[entry.job.Service] = struct{}{}
		c.workers.Add(1)
		c.mu.Unlock()
		go c.execute(ctx, entry.job)
	}
}

func (c *Coordinator) execute(ctx context.Context, job Job) {
	defer c.workers.Done()
	if err := c.handler.Handle(ctx, job); err != nil && !errors.Is(err, context.Canceled) {
		c.report(fmt.Errorf("handle job %s: %w", job.ID, err))
	}
	c.mu.Lock()
	c.active--
	c.activePriority[job.Priority]--
	delete(c.activeService, job.Service)
	c.mu.Unlock()
	c.signal()
}

func (c *Coordinator) selectLocked(now time.Time) int {
	if c.active >= c.config.Concurrency || len(c.queue) == 0 {
		return -1
	}
	var best [3]int
	for i := range best {
		best[i] = -1
	}
	for i, entry := range c.queue {
		if _, busy := c.activeService[entry.job.Service]; busy {
			continue
		}
		effective := entry.job.Priority
		ageSteps := int(now.Sub(entry.job.EnqueuedAt) / c.config.AgingInterval)
		if ageSteps < 0 {
			ageSteps = 0
		}
		if ageSteps > int(effective) {
			ageSteps = int(effective)
		}
		effective -= Priority(ageSteps)
		if !c.capacityAvailable(effective) {
			continue
		}
		current := best[effective]
		if current < 0 || entry.seq < c.queue[current].seq {
			best[effective] = i
		}
	}
	for round := 0; round < 2; round++ {
		for p := P0; p < priorityCount; p++ {
			if best[p] >= 0 && c.credits[p] > 0 {
				c.credits[p]--
				return best[p]
			}
		}
		c.credits = c.config.Weights
	}
	return -1
}

func (c *Coordinator) capacityAvailable(priority Priority) bool {
	freeFor := c.config.Concurrency
	if priority >= P1 {
		freeFor -= max(0, c.config.P0Reserved-c.activePriority[P0])
	}
	if priority >= P2 {
		freeFor -= max(0, c.config.P1Reserved-c.activePriority[P1])
	}
	return c.active < freeFor
}

func (c *Coordinator) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Coordinator) report(err error) {
	select {
	case c.errors <- err:
	default:
	}
}
