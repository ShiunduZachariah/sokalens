package recommendations

import (
	"encoding/json"
	"time"
)

type Event struct {
	UserID           *int64
	GuestSessionID   *int64
	SessionPublicID  string
	EventType        string
	EntityType       string
	EntityID         string
	RecommendationID *int64
	Source           string
	Metadata         json.RawMessage
	OccurredAt       time.Time
}

type Candidate struct {
	ID                   int64
	UserID               int64
	ItemType             string
	ItemID               string
	BaseScore            float64
	Score                float64
	ReasonKey            string
	ReasonText           string
	InterestKey          string
	PredictionConfidence *float64
	StartsAt             *time.Time
	FreshnessAt          time.Time
	Sponsored            bool
	Editorial            bool
	GenerationKey        string
	ExpiresAt            time.Time
	CreatedAt            time.Time
	Title                string
	Subtitle             string
	Href                 string

	// Signals and DiversityKeys are populated by candidate generators when
	// ranking in-memory. They are deliberately not additional persistence fields.
	Signals       map[string]float64
	DiversityKeys []string
}

type RankRequest struct {
	UserID       int64
	Candidates   []Candidate
	Limit        int
	ColdStart    bool
	MaxPerGroup  int
	MaxSponsored int
	Now          time.Time
}
