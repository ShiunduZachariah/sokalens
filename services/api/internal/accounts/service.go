package accounts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/locales"
)

type Service struct {
	repo         *SQLRepository
	argon        Argon2Config
	now          func() time.Time
	sessionTTL   time.Duration
	verifyTTL    time.Duration
	resetTTL     time.Duration
	randomSecret func() (string, error)
}

func NewService(repo *SQLRepository) *Service {
	return &Service{
		repo: repo, argon: DefaultArgon2Config(), now: func() time.Time { return time.Now().UTC() },
		sessionTTL: 30 * 24 * time.Hour, verifyTTL: 24 * time.Hour, resetTTL: time.Hour, randomSecret: newOpaqueToken,
	}
}

func (s *Service) ConfigureLifetimes(sessionTTL, verificationTTL, resetTTL time.Duration) {
	if sessionTTL > 0 {
		s.sessionTTL = sessionTTL
	}
	if verificationTTL > 0 {
		s.verifyTTL = verificationTTL
	}
	if resetTTL > 0 {
		s.resetTTL = resetTTL
	}
}

func (s *Service) Register(ctx context.Context, email, password string) (Account, error) {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return Account{}, err
	}
	hash, err := HashPassword(password, s.argon)
	if err != nil {
		return Account{}, err
	}
	account, err := s.repo.CreateAccount(ctx, normalized, hash, s.now())
	account.PasswordHash = ""
	return account, err
}

func (s *Service) RegisterPending(ctx context.Context, input Registration) (RegistrationResult, error) {
	normalized, err := NormalizeEmail(input.Email)
	if err != nil {
		return RegistrationResult{}, err
	}
	hash, err := HashPassword(input.Password, s.argon)
	if err != nil {
		return RegistrationResult{}, err
	}
	input.Email = normalized
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.CountryCode = strings.ToUpper(strings.TrimSpace(input.CountryCode))
	if input.PreferredLanguage == "" {
		input.PreferredLanguage = locales.Default
	} else if normalized, ok := locales.Normalize(input.PreferredLanguage); ok {
		input.PreferredLanguage = normalized
	} else {
		return RegistrationResult{}, errors.New("preferred language must be one of en, es, fr, pt, ar, de, or it")
	}
	input.LanguageSource = strings.ToLower(strings.TrimSpace(input.LanguageSource))
	switch input.LanguageSource {
	case "selected", "guest", "browser", "country", "fallback":
	default:
		input.LanguageSource = "fallback"
	}
	if len(input.CountryCode) != 2 {
		return RegistrationResult{}, errors.New("country code must contain two letters")
	}
	if strings.TrimSpace(input.LegalVersion) == "" {
		return RegistrationResult{}, errors.New("legal document version is required")
	}
	if strings.TrimSpace(input.Acquisition.Platform) == "" {
		input.Acquisition.Platform = "web"
	}
	if strings.TrimSpace(input.Acquisition.DeviceType) == "" {
		input.Acquisition.DeviceType = "unknown"
	}
	sessionToken, err := s.randomSecret()
	if err != nil {
		return RegistrationResult{}, err
	}
	verificationToken, err := s.randomSecret()
	if err != nil {
		return RegistrationResult{}, err
	}
	now := s.now()
	result, err := s.repo.CreatePendingRegistration(ctx, input, hash, hashToken(sessionToken), hashToken(verificationToken), now, now.Add(s.sessionTTL), now.Add(s.verifyTTL))
	if err != nil {
		return RegistrationResult{}, err
	}
	result.Account.PasswordHash = ""
	result.Credential.Token = sessionToken
	result.Verification.Token = verificationToken
	return result, nil
}

func (s *Service) RecordGrowthEvent(ctx context.Context, event GrowthEvent) error {
	event.EventType = strings.ToUpper(strings.TrimSpace(event.EventType))
	allowed := map[string]bool{
		"SIGNUP_VIEWED": true, "SIGNUP_STARTED": true,
		"TURNSTILE_SUCCEEDED": true, "TURNSTILE_FAILED": true,
		"REGISTRATION_SUCCEEDED": true, "REGISTRATION_FAILED": true,
	}
	if !allowed[event.EventType] || strings.TrimSpace(event.VisitorID) == "" {
		return errors.New("invalid growth event")
	}
	event.CountryCode = strings.ToUpper(strings.TrimSpace(event.CountryCode))
	if event.CountryCode != "" && len(event.CountryCode) != 2 {
		return errors.New("invalid growth event country")
	}
	event.Platform = "web"
	event.Source = strings.TrimSpace(event.Source)
	if len(event.Source) > 64 || len(event.Metadata) > 2048 || !json.Valid(event.Metadata) {
		return errors.New("invalid growth event metadata")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = s.now()
	}
	return s.repo.RecordGrowthEvent(ctx, event)
}

func (s *Service) Authenticate(ctx context.Context, email, password string) (Account, error) {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return Account{}, ErrInvalidCredential
	}
	account, err := s.repo.FindAccountByEmail(ctx, normalized)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Account{}, ErrInvalidCredential
		}
		return Account{}, err
	}
	valid, err := VerifyPassword(account.PasswordHash, password)
	if err != nil || !valid {
		return Account{}, ErrInvalidCredential
	}
	if err := s.repo.RecordLogin(ctx, account.ID, s.now()); err != nil {
		return Account{}, err
	}
	account.PasswordHash = ""
	return account, nil
}

func (s *Service) IssueEmailVerification(ctx context.Context, userID int64) (IssuedToken, error) {
	return s.issueToken(ctx, userID, PurposeEmailVerification, s.verifyTTL)
}

func (s *Service) IssueEmailVerificationForEmail(ctx context.Context, email string) (IssuedToken, Account, error) {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return IssuedToken{}, Account{}, ErrNotFound
	}
	account, err := s.repo.FindAccountByEmail(ctx, normalized)
	if err != nil {
		return IssuedToken{}, Account{}, err
	}
	if account.EmailVerifiedAt != nil {
		return IssuedToken{}, account, nil
	}
	token, err := s.issueToken(ctx, account.ID, PurposeEmailVerification, s.verifyTTL)
	return token, account, err
}

func (s *Service) VerifyEmail(ctx context.Context, token string) error {
	return s.repo.VerifyEmail(ctx, hashToken(strings.TrimSpace(token)), s.now())
}

func (s *Service) IssuePasswordReset(ctx context.Context, email string) (IssuedToken, Account, error) {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return IssuedToken{}, Account{}, ErrNotFound
	}
	account, err := s.repo.FindAccountByEmail(ctx, normalized)
	if err != nil {
		return IssuedToken{}, Account{}, err
	}
	token, err := s.issueToken(ctx, account.ID, PurposePasswordReset, s.resetTTL)
	return token, account, err
}

func (s *Service) ResetPassword(ctx context.Context, token, password string) error {
	hash, err := HashPassword(password, s.argon)
	if err != nil {
		return err
	}
	return s.repo.ResetPassword(ctx, hashToken(strings.TrimSpace(token)), hash, s.now())
}

func (s *Service) ChangePassword(ctx context.Context, userID int64, currentPassword, newPassword string) error {
	account, err := s.repo.FindAccountByID(ctx, userID)
	if err != nil {
		return ErrInvalidCredential
	}
	valid, err := VerifyPassword(account.PasswordHash, currentPassword)
	if err != nil || !valid {
		return ErrInvalidCredential
	}
	hash, err := HashPassword(newPassword, s.argon)
	if err != nil {
		return err
	}
	return s.repo.ChangePassword(ctx, userID, hash, s.now())
}

func (s *Service) CreateSession(ctx context.Context, userID int64, deviceName, userAgent string) (SessionCredential, error) {
	token, err := s.randomSecret()
	if err != nil {
		return SessionCredential{}, err
	}
	expiresAt := s.now().Add(s.sessionTTL)
	session, err := s.repo.CreateSession(ctx, userID, hashToken(token), strings.TrimSpace(deviceName), strings.TrimSpace(userAgent), expiresAt, s.now())
	if err != nil {
		return SessionCredential{}, err
	}
	return SessionCredential{Token: token, Session: session}, nil
}

func (s *Service) ValidateSession(ctx context.Context, token string) (Account, Session, error) {
	now := s.now()
	account, session, err := s.repo.FindAccountBySession(ctx, hashToken(strings.TrimSpace(token)), now)
	account.PasswordHash = ""
	return account, session, err
}

func (s *Service) ListSessions(ctx context.Context, userID int64) ([]Session, error) {
	return s.repo.ListSessions(ctx, userID)
}

func (s *Service) RevokeSession(ctx context.Context, userID, sessionID int64) error {
	return s.repo.RevokeSession(ctx, userID, sessionID, s.now())
}

func (s *Service) RevokeAllSessions(ctx context.Context, userID int64) error {
	return s.repo.RevokeAllSessions(ctx, userID, s.now())
}

func (s *Service) UpdateProfile(ctx context.Context, profile Profile) error {
	profile.DisplayName = strings.TrimSpace(profile.DisplayName)
	profile.Timezone = strings.TrimSpace(profile.Timezone)
	profile.CountryCode = strings.ToUpper(strings.TrimSpace(profile.CountryCode))
	profile.OddsFormat = strings.ToLower(strings.TrimSpace(profile.OddsFormat))
	profile.Language = strings.ToLower(strings.TrimSpace(profile.Language))
	if profile.Timezone == "" {
		profile.Timezone = "Africa/Nairobi"
	}
	if profile.OddsFormat == "" {
		profile.OddsFormat = "decimal"
	}
	if profile.OddsFormat != "decimal" && profile.OddsFormat != "fractional" && profile.OddsFormat != "american" {
		return errors.New("odds format must be decimal, fractional, or american")
	}
	if profile.Language == "" {
		profile.Language = locales.Default
	} else if normalized, ok := locales.Normalize(profile.Language); ok {
		profile.Language = normalized
	} else {
		return errors.New("preferred language must be one of en, es, fr, pt, ar, de, or it")
	}
	if len(profile.CountryCode) != 0 && len(profile.CountryCode) != 2 {
		return errors.New("country code must contain two letters")
	}
	profile.UpdatedAt = s.now()
	return s.repo.UpsertProfile(ctx, profile)
}

func (s *Service) GetProfile(ctx context.Context, userID int64) (Profile, error) {
	return s.repo.GetProfile(ctx, userID)
}

func (s *Service) SetConsent(ctx context.Context, userID int64, consent Consent) error {
	consent.DocumentKey = strings.ToLower(strings.TrimSpace(consent.DocumentKey))
	consent.DocumentVersion = strings.TrimSpace(consent.DocumentVersion)
	consent.Source = strings.TrimSpace(consent.Source)
	consent.OccurredAt = s.now()
	if consent.DocumentKey == "" || consent.DocumentVersion == "" || consent.Source == "" {
		return errors.New("consent document, version, and source are required")
	}
	return s.repo.RecordConsent(ctx, userID, consent)
}

func (s *Service) ListLatestConsents(ctx context.Context, userID int64) ([]Consent, error) {
	return s.repo.ListLatestConsents(ctx, userID)
}

func (s *Service) AddFavorite(ctx context.Context, userID int64, entityType, entityID, displayName string) error {
	entityType = strings.ToLower(strings.TrimSpace(entityType))
	entityID = strings.ToLower(strings.TrimSpace(entityID))
	displayName = strings.TrimSpace(displayName)
	allowed := map[string]bool{"team": true, "league": true, "competition": true, "country": true, "match": true, "prediction": true, "market": true}
	if !allowed[entityType] || entityID == "" || len(displayName) > 160 {
		return errors.New("invalid favorite")
	}
	return s.repo.AddFavorite(ctx, userID, Favorite{EntityType: entityType, EntityID: entityID, DisplayName: displayName, NotificationsEnabled: false, UpdatedAt: s.now()})
}

func (s *Service) UpdateFavoriteNotifications(ctx context.Context, userID int64, entityType, entityID string, enabled bool) error {
	if enabled {
		return ErrNotificationsDormant
	}
	return s.repo.UpdateFavoriteNotifications(ctx, userID, strings.ToLower(strings.TrimSpace(entityType)), strings.ToLower(strings.TrimSpace(entityID)), enabled, s.now())
}

func (s *Service) ListFavorites(ctx context.Context, userID int64) ([]Favorite, error) {
	return s.repo.ListFavorites(ctx, userID)
}

func (s *Service) RemoveFavorite(ctx context.Context, userID int64, entityType, entityID string) error {
	return s.repo.RemoveFavorite(ctx, userID, strings.ToLower(strings.TrimSpace(entityType)), strings.ToLower(strings.TrimSpace(entityID)))
}

func (s *Service) UpsertInterest(ctx context.Context, userID int64, interest Interest) error {
	interest.Type = strings.ToLower(strings.TrimSpace(interest.Type))
	interest.Key = strings.ToLower(strings.TrimSpace(interest.Key))
	interest.Source = strings.ToLower(strings.TrimSpace(interest.Source))
	if interest.Source == "" {
		interest.Source = "explicit"
	}
	if interest.Type == "" || interest.Key == "" || interest.Weight < -100 || interest.Weight > 100 {
		return errors.New("invalid interest")
	}
	interest.UpdatedAt = s.now()
	return s.repo.UpsertInterest(ctx, userID, interest)
}

func (s *Service) ListInterests(ctx context.Context, userID int64, includeDisabled bool) ([]Interest, error) {
	return s.repo.ListInterests(ctx, userID, includeDisabled)
}

func (s *Service) ReplaceInterests(ctx context.Context, userID int64, interests []Interest) error {
	if len(interests) > 100 {
		return errors.New("too many interests")
	}
	for index := range interests {
		interests[index].Type = strings.ToLower(strings.TrimSpace(interests[index].Type))
		interests[index].Key = strings.ToLower(strings.TrimSpace(interests[index].Key))
		if interests[index].Type == "" || interests[index].Key == "" || interests[index].Weight < -100 || interests[index].Weight > 100 {
			return errors.New("invalid interest")
		}
		interests[index].Source = "explicit"
		interests[index].UpdatedAt = s.now()
	}
	return s.repo.ReplaceInterests(ctx, userID, interests)
}

func (s *Service) ResetPersonalization(ctx context.Context, userID int64) error {
	return s.repo.ResetPersonalization(ctx, userID)
}

func (s *Service) RequestDeletion(ctx context.Context, userID int64) error {
	return s.repo.RequestDeletion(ctx, userID, s.now())
}

func (s *Service) issueToken(ctx context.Context, userID int64, purpose TokenPurpose, ttl time.Duration) (IssuedToken, error) {
	token, err := s.randomSecret()
	if err != nil {
		return IssuedToken{}, err
	}
	expiresAt := s.now().Add(ttl)
	if err := s.repo.IssueAccountToken(ctx, userID, purpose, hashToken(token), expiresAt, s.now()); err != nil {
		return IssuedToken{}, err
	}
	return IssuedToken{UserID: userID, Token: token, ExpiresAt: expiresAt}, nil
}

func NormalizeEmail(value string) (string, error) {
	parsed, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil || parsed.Address == "" || strings.ContainsAny(parsed.Address, "\r\n") {
		return "", errors.New("valid email is required")
	}
	return strings.ToLower(parsed.Address), nil
}

func newOpaqueToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
