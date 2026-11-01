package recommendations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type Service struct {
	repo Repository
	now  func() time.Time
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) IngestEvents(ctx context.Context, events []Event) error {
	if len(events) > 100 {
		return errors.New("event batch exceeds 100 items")
	}
	now := s.now()
	seen := make(map[string]struct{}, len(events))
	clean := make([]Event, 0, len(events))
	allowedEvents := map[string]bool{
		"view": true, "click": true,
		"fixture_viewed": true, "prediction_viewed": true, "analysis_opened": true,
		"fixture_searched": true, "team_searched": true, "league_searched": true,
		"prediction_saved": true, "favorite_added": true, "favorite_removed": true,
		"recommendation_clicked": true, "recommendation_dismissed": true,
		"content_shared":      true,
		"notification_opened": true, "notification_ignored": true, "subscription_started": true,
	}
	for _, event := range events {
		event.EventType = strings.ToLower(strings.TrimSpace(event.EventType))
		event.EntityType = strings.ToLower(strings.TrimSpace(event.EntityType))
		event.EntityID = strings.TrimSpace(event.EntityID)
		event.Source = strings.TrimSpace(event.Source)
		if len(event.Metadata) > 16*1024 {
			return errors.New("event metadata exceeds 16 KiB")
		}
		if len(event.Metadata) != 0 && !json.Valid(event.Metadata) {
			return errors.New("event metadata must be valid JSON")
		}
		if (event.UserID == nil) == (event.GuestSessionID == nil) {
			return errors.New("event must identify exactly one user or guest session")
		}
		if !allowedEvents[event.EventType] {
			return errors.New("unsupported event type")
		}
		if event.OccurredAt.IsZero() {
			event.OccurredAt = now
		}
		if event.OccurredAt.After(now.Add(5 * time.Minute)) {
			return errors.New("event occurred_at is in the future")
		}
		fingerprint := eventFingerprint(event)
		if _, duplicate := seen[fingerprint]; duplicate {
			continue
		}
		seen[fingerprint] = struct{}{}
		clean = append(clean, event)
	}
	return s.repo.IngestEvents(ctx, clean)
}

func (s *Service) Feed(ctx context.Context, userID int64, limit int, coldStart bool) ([]Candidate, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	now := s.now()
	candidates, err := s.repo.LoadCandidates(ctx, userID, now, min(limit*5, 500))
	if err != nil {
		return nil, err
	}
	return s.Rank(ctx, RankRequest{UserID: userID, Candidates: candidates, Limit: limit, ColdStart: coldStart, MaxSponsored: 1, Now: now})
}

func (s *Service) Rank(ctx context.Context, request RankRequest) ([]Candidate, error) {
	if request.UserID < 1 {
		return nil, errors.New("user id is required")
	}
	if request.Limit < 1 || request.Limit > 100 {
		request.Limit = 20
	}
	if request.MaxPerGroup < 1 {
		request.MaxPerGroup = 2
	}
	if request.MaxSponsored < 0 {
		request.MaxSponsored = 0
	}
	if request.Now.IsZero() {
		request.Now = s.now()
	}
	weights, err := s.repo.LoadWeights(ctx)
	if err != nil {
		return nil, err
	}
	dismissed, err := s.repo.Dismissed(ctx, request.UserID, request.Now)
	if err != nil {
		return nil, err
	}

	deduped := make(map[string]Candidate, len(request.Candidates))
	for _, candidate := range request.Candidates {
		candidate.ItemType = strings.ToLower(strings.TrimSpace(candidate.ItemType))
		candidate.ItemID = strings.TrimSpace(candidate.ItemID)
		if candidate.ItemType == "" || candidate.ItemID == "" || (candidate.UserID != 0 && candidate.UserID != request.UserID) || math.IsNaN(candidate.BaseScore) || math.IsInf(candidate.BaseScore, 0) || (!candidate.ExpiresAt.IsZero() && !candidate.ExpiresAt.After(request.Now)) {
			continue
		}
		key := itemKey(candidate.ItemType, candidate.ItemID)
		if _, hidden := dismissed[key]; hidden {
			continue
		}
		candidate.Score = score(candidate, weights, request.ColdStart, request.Now)
		if existing, exists := deduped[key]; !exists || stronger(candidate, existing) {
			deduped[key] = candidate
		}
	}
	ordered := make([]Candidate, 0, len(deduped))
	for _, candidate := range deduped {
		ordered = append(ordered, candidate)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Score != ordered[j].Score {
			return ordered[i].Score > ordered[j].Score
		}
		left, right := itemKey(ordered[i].ItemType, ordered[i].ItemID), itemKey(ordered[j].ItemType, ordered[j].ItemID)
		if left != right {
			return left < right
		}
		return ordered[i].ID < ordered[j].ID
	})

	groupCounts := make(map[string]int)
	selected := make([]Candidate, 0, min(request.Limit, len(ordered)))
	sponsored := 0
	for _, candidate := range ordered {
		if candidate.Sponsored && sponsored >= request.MaxSponsored {
			continue
		}
		if exceedsDiversity(candidate, groupCounts, request.MaxPerGroup) {
			continue
		}
		selected = append(selected, candidate)
		for _, group := range normalizedGroups(candidate) {
			groupCounts[group]++
		}
		if candidate.Sponsored {
			sponsored++
		}
		if len(selected) == request.Limit {
			break
		}
	}
	return selected, nil
}

func (s *Service) Dismiss(ctx context.Context, userID int64, itemType, itemID, reason string, expiresAt *time.Time) error {
	itemType = strings.ToLower(strings.TrimSpace(itemType))
	itemID = strings.TrimSpace(itemID)
	if userID < 1 || itemType == "" || itemID == "" {
		return errors.New("user and item are required")
	}
	return s.repo.Dismiss(ctx, userID, itemType, itemID, strings.TrimSpace(reason), expiresAt, s.now())
}

func (s *Service) DismissCandidate(ctx context.Context, userID, candidateID int64, reason string, expiresAt *time.Time) (Candidate, error) {
	if userID < 1 || candidateID < 1 {
		return Candidate{}, errors.New("user and recommendation are required")
	}
	candidate, err := s.repo.CandidateByID(ctx, userID, candidateID)
	if err != nil {
		return Candidate{}, err
	}
	return candidate, s.Dismiss(ctx, userID, candidate.ItemType, candidate.ItemID, reason, expiresAt)
}

func score(candidate Candidate, weights map[string]float64, coldStart bool, now time.Time) float64 {
	result := candidate.BaseScore
	signals := make([]string, 0, len(candidate.Signals))
	for signal := range candidate.Signals {
		signals = append(signals, signal)
	}
	sort.Strings(signals)
	for _, signal := range signals {
		strength := candidate.Signals[signal]
		if coldStart && isPersonalSignal(signal) {
			continue
		}
		result += weights[signal] * strength
	}
	if candidate.ReasonKey != "" && !(coldStart && isPersonalSignal(candidate.ReasonKey)) {
		result += weights[candidate.ReasonKey]
	}
	if candidate.PredictionConfidence != nil && *candidate.PredictionConfidence >= 70 {
		result += weights["high_confidence"] * (*candidate.PredictionConfidence / 100)
	}
	if candidate.StartsAt != nil && candidate.StartsAt.After(now) && candidate.StartsAt.Before(now.Add(6*time.Hour)) {
		result += weights["starting_soon"]
	}
	if coldStart {
		result += weights["exploration"]
	}
	return result
}

func stronger(candidate, existing Candidate) bool {
	if candidate.Score != existing.Score {
		return candidate.Score > existing.Score
	}
	if candidate.GenerationKey != existing.GenerationKey {
		return candidate.GenerationKey > existing.GenerationKey
	}
	return candidate.ID > existing.ID
}

func isPersonalSignal(signal string) bool {
	return strings.HasPrefix(signal, "favorite_") || signal == "recent_search" || signal == "recent_engagement" || signal == "preferred_market"
}

func exceedsDiversity(candidate Candidate, counts map[string]int, max int) bool {
	for _, group := range normalizedGroups(candidate) {
		if counts[group] >= max {
			return true
		}
	}
	return false
}

func normalizedGroups(candidate Candidate) []string {
	seen := make(map[string]struct{})
	groups := make([]string, 0, len(candidate.DiversityKeys)+1)
	for _, group := range candidate.DiversityKeys {
		group = strings.ToLower(strings.TrimSpace(group))
		if group == "" {
			continue
		}
		if _, duplicate := seen[group]; !duplicate {
			seen[group] = struct{}{}
			groups = append(groups, group)
		}
	}
	if len(groups) == 0 && candidate.InterestKey != "" {
		groups = append(groups, "interest:"+strings.ToLower(candidate.InterestKey))
	}
	return groups
}

func eventFingerprint(event Event) string {
	subject := ""
	if event.UserID != nil {
		subject = fmt.Sprintf("u:%d", *event.UserID)
	} else if event.GuestSessionID != nil {
		subject = fmt.Sprintf("g:%d", *event.GuestSessionID)
	}
	recommendationID := int64(0)
	if event.RecommendationID != nil {
		recommendationID = *event.RecommendationID
	}
	return fmt.Sprintf("%s|%s|%s|%s|%d|%s|%d", subject, event.EventType, event.EntityType, event.EntityID, recommendationID, event.SessionPublicID, event.OccurredAt.UnixNano())
}

func itemKey(itemType, itemID string) string {
	return strings.ToLower(strings.TrimSpace(itemType)) + ":" + strings.TrimSpace(itemID)
}
