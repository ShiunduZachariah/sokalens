package accounts

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrEmailExists          = errors.New("email already registered")
	ErrInvalidCredential    = errors.New("invalid credentials")
	ErrInvalidToken         = errors.New("invalid or expired token")
	ErrInvalidSession       = errors.New("invalid session")
	ErrNotFound             = errors.New("account not found")
	ErrEmailNotVerified     = errors.New("email verification required")
	ErrNotificationsDormant = errors.New("favorite notifications are not enabled")
)

type Account struct {
	ID              int64
	PublicID        string
	Email           string
	PasswordHash    string
	EmailVerifiedAt *time.Time
	AccountStatus   string
	CountryCode     string
	CreatedAt       time.Time
}

type Acquisition struct {
	VisitorID    string
	CountryCode  string
	ReferrerHost string
	UTMSource    string
	UTMMedium    string
	UTMCampaign  string
	UTMContent   string
	LandingPage  string
	SignupPage   string
	DeviceType   string
	Platform     string
}

type GrowthEvent struct {
	VisitorID   string
	EventType   string
	CountryCode string
	Platform    string
	Source      string
	Metadata    json.RawMessage
	OccurredAt  time.Time
}

type Registration struct {
	Email             string
	Password          string
	DisplayName       string
	CountryCode       string
	PreferredLanguage string
	LanguageSource    string
	MarketingConsent  bool
	LegalVersion      string
	DeviceName        string
	UserAgent         string
	Acquisition       Acquisition
}

type RegistrationResult struct {
	Account      Account
	Credential   SessionCredential
	Verification IssuedToken
}

type Profile struct {
	UserID                 int64
	DisplayName            string
	Timezone               string
	CountryCode            string
	OddsFormat             string
	OddsFormatSelectedAt   *time.Time
	Language               string
	PersonalizationEnabled bool
	MarketingConsent       bool
	OnboardingCompletedAt  *time.Time
	UpdatedAt              time.Time
}

// Consent represents an immutable legal-document decision. The latest event for
// a document is its effective state; decisions are never overwritten.
type Consent struct {
	DocumentKey     string
	DocumentVersion string
	Granted         bool
	Source          string
	Metadata        json.RawMessage
	OccurredAt      time.Time
}

type Session struct {
	ID         int64
	PublicID   string
	UserID     int64
	DeviceName string
	UserAgent  string
	ExpiresAt  time.Time
	LastSeenAt time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

type SessionCredential struct {
	Token   string
	Session Session
}

type Favorite struct {
	ID                   int64
	EntityType           string
	EntityID             string
	DisplayName          string
	NotificationsEnabled bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type Interest struct {
	Type      string
	Key       string
	Weight    float64
	Source    string
	Disabled  bool
	UpdatedAt time.Time
}

type TokenPurpose string

const (
	PurposeEmailVerification TokenPurpose = "email_verification"
	PurposePasswordReset     TokenPurpose = "password_reset"
)

type IssuedToken struct {
	UserID    int64
	Token     string
	ExpiresAt time.Time
}
