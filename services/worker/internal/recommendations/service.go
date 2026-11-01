package recommendations

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Repository interface {
	ListEligibleUsers(context.Context, int) ([]int64, error)
	LoadInputs(context.Context, time.Time, time.Time, int) ([]Input, error)
	LoadSignals(context.Context, int64, time.Time) (Signals, error)
	LoadWeights(context.Context) (map[string]float64, error)
	SaveCandidates(context.Context, int64, string, []Candidate) error
}

type Config struct {
	MaximumUsers            int
	MaximumInputs           int
	MaximumCandidates       int
	MaximumConcurrentUsers  int
	Horizon                 time.Duration
	CandidateTTL            time.Duration
	EventLookback           time.Duration
	HighConfidenceThreshold float64
	StartingSoonWindow      time.Duration
	DefaultWeights          map[string]float64
}

type Service struct {
	repository Repository
	config     Config
	now        func() time.Time
}

func NewService(repository Repository, config Config, now func() time.Time) (Service, error) {
	if repository == nil {
		return Service{}, errors.New("recommendation repository is required")
	}
	if config.MaximumUsers <= 0 {
		config.MaximumUsers = 100
	}
	if config.MaximumInputs <= 0 {
		config.MaximumInputs = 500
	}
	if config.MaximumCandidates <= 0 {
		config.MaximumCandidates = 50
	}
	if config.MaximumConcurrentUsers <= 0 {
		config.MaximumConcurrentUsers = 4
	}
	if config.Horizon <= 0 {
		config.Horizon = 72 * time.Hour
	}
	if config.CandidateTTL <= 0 {
		config.CandidateTTL = 6 * time.Hour
	}
	if config.EventLookback <= 0 {
		config.EventLookback = 30 * 24 * time.Hour
	}
	if now == nil {
		now = time.Now
	}
	return Service{repository: repository, config: config, now: now}, nil
}

func (s Service) GenerateBatch(ctx context.Context, userID int64, generationKey string) (int, error) {
	users := []int64{userID}
	if userID <= 0 {
		var err error
		users, err = s.repository.ListEligibleUsers(ctx, s.config.MaximumUsers)
		if err != nil {
			return 0, err
		}
	}
	if len(users) == 0 {
		return 0, nil
	}
	now := s.now().UTC()
	prepared, err := s.prepare(ctx, now, generationKey)
	if err != nil {
		return 0, err
	}

	semaphore := make(chan struct{}, s.config.MaximumConcurrentUsers)
	errorsByUser := make([]error, len(users))
	var completed int
	var completedMu sync.Mutex
	var workers sync.WaitGroup
	for index, currentUserID := range users {
		workers.Add(1)
		go func(index int, currentUserID int64) {
			defer workers.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				errorsByUser[index] = ctx.Err()
				return
			}
			count, err := s.generateUser(ctx, currentUserID, prepared)
			errorsByUser[index] = err
			completedMu.Lock()
			completed += count
			completedMu.Unlock()
		}(index, currentUserID)
	}
	workers.Wait()
	return completed, errors.Join(errorsByUser...)
}

func (s Service) GenerateUser(ctx context.Context, userID int64, generationKey string) (int, error) {
	if userID <= 0 {
		return 0, errors.New("recommendation user ID must be positive")
	}
	now := s.now().UTC()
	prepared, err := s.prepare(ctx, now, generationKey)
	if err != nil {
		return 0, err
	}
	return s.generateUser(ctx, userID, prepared)
}

type preparedGeneration struct {
	now           time.Time
	inputs        []Input
	weights       map[string]float64
	generationKey string
}

func (s Service) prepare(ctx context.Context, now time.Time, generationKey string) (preparedGeneration, error) {
	inputs, err := s.repository.LoadInputs(ctx, now, now.Add(s.config.Horizon), s.config.MaximumInputs)
	if err != nil {
		return preparedGeneration{}, fmt.Errorf("load recommendation inputs: %w", err)
	}
	weights := cloneWeights(s.config.DefaultWeights)
	configured, err := s.repository.LoadWeights(ctx)
	if err != nil {
		return preparedGeneration{}, fmt.Errorf("load recommendation weights: %w", err)
	}
	for key, value := range configured {
		weights[key] = value
	}
	if generationKey == "" {
		generationKey = "recommendations:" + now.Truncate(time.Hour).Format("20060102T150405Z")
	}
	return preparedGeneration{now: now, inputs: inputs, weights: weights, generationKey: generationKey}, nil
}

func (s Service) generateUser(ctx context.Context, userID int64, prepared preparedGeneration) (int, error) {
	signals, err := s.repository.LoadSignals(ctx, userID, prepared.now.Add(-s.config.EventLookback))
	if err != nil {
		return 0, fmt.Errorf("load recommendation signals: %w", err)
	}
	candidates := Rank(prepared.inputs, signals, RankOptions{
		Weights: prepared.weights, HighConfidenceThreshold: s.config.HighConfidenceThreshold,
		StartingSoonWindow: s.config.StartingSoonWindow, Maximum: s.config.MaximumCandidates,
		GenerationKey: prepared.generationKey, ExpiresAt: prepared.now.Add(s.config.CandidateTTL), Now: prepared.now,
	})
	if err := s.repository.SaveCandidates(ctx, userID, prepared.generationKey, candidates); err != nil {
		return 0, fmt.Errorf("save recommendation candidates: %w", err)
	}
	return len(candidates), nil
}

func cloneWeights(source map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
