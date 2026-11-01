package notifications

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type SQLGuards struct {
	db                   *sql.DB
	requiredEntitlements map[string]string
	now                  func() time.Time
}

func NewSQLGuards(db *sql.DB, requiredEntitlements map[string]string) SQLGuards {
	return SQLGuards{db: db, requiredEntitlements: requiredEntitlements, now: time.Now}
}

func (g SQLGuards) CheckAccount(ctx context.Context, message Message) (GuardDecision, error) {
	if g.db == nil {
		return GuardDecision{}, errors.New("notification guard DB is nil")
	}
	var allowed bool
	err := g.db.QueryRowContext(ctx, `
SELECT deleted_at IS NULL
       AND (account_status = 'active'
            OR ($2 IN ('email_verification', 'password_reset') AND account_status = 'pending_verification'))
FROM users
WHERE id = $1`, message.UserID, message.Category).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return GuardDecision{Allowed: false, Reason: "account unavailable"}, nil
	}
	if err != nil {
		return GuardDecision{}, err
	}
	if !allowed {
		return GuardDecision{Allowed: false, Reason: "account is not eligible for this message"}, nil
	}
	return GuardDecision{Allowed: true}, nil
}

func (g SQLGuards) CheckPreference(ctx context.Context, message Message) (GuardDecision, error) {
	if g.db == nil {
		return GuardDecision{}, errors.New("notification guard DB is nil")
	}
	var allowed bool
	var timezone, quietStart, quietEnd, digestMode string
	err := g.db.QueryRowContext(ctx, `
SELECT COALESCE(channel_preference.enabled, true)
       AND COALESCE(legacy.opted_out_at IS NULL, true)
       AND CASE $2
             WHEN 'email' THEN COALESCE(legacy.email_enabled, true)
             WHEN 'web_push' THEN COALESCE(legacy.push_enabled, true)
             WHEN 'mobile_push' THEN COALESCE(legacy.push_enabled, true)
             ELSE true
           END
       AND COALESCE(channel_preference.digest_mode, 'immediate') <> 'off',
       account.timezone,
       COALESCE(channel_preference.quiet_start::text, ''),
       COALESCE(channel_preference.quiet_end::text, ''),
       COALESCE(channel_preference.digest_mode, 'immediate')
FROM users AS account
LEFT JOIN notification_channel_preferences AS channel_preference
  ON channel_preference.user_id = account.id
 AND channel_preference.channel = $2
 AND channel_preference.category = $3
LEFT JOIN notification_preferences AS legacy ON legacy.user_id = account.id
WHERE account.id = $1`, message.UserID, message.Channel, message.Category).Scan(&allowed, &timezone, &quietStart, &quietEnd, &digestMode)
	if errors.Is(err, sql.ErrNoRows) {
		return GuardDecision{Allowed: false, Reason: "notification preference account unavailable"}, nil
	}
	if err != nil {
		return GuardDecision{}, err
	}
	if !allowed {
		return GuardDecision{Allowed: false, Reason: "notification preference disabled"}, nil
	}
	if message.Channel == "in_app" || digestMode != "immediate" || quietStart == "" || quietEnd == "" {
		return GuardDecision{Allowed: true}, nil
	}
	if message.Channel != "email" && message.Channel != "web_push" && message.Channel != "mobile_push" {
		return GuardDecision{Allowed: true}, nil
	}
	start, err := parseClockTime(quietStart)
	if err != nil {
		return GuardDecision{}, fmt.Errorf("parse quiet start: %w", err)
	}
	end, err := parseClockTime(quietEnd)
	if err != nil {
		return GuardDecision{}, fmt.Errorf("parse quiet end: %w", err)
	}
	now := time.Now()
	if g.now != nil {
		now = g.now()
	}
	deferUntil, err := nextDeliveryAfter(now, timezone, start, end)
	if err != nil {
		return GuardDecision{}, err
	}
	if deferUntil.After(now) {
		return GuardDecision{Allowed: true, Reason: "deferred for user quiet hours", DeferUntil: deferUntil}, nil
	}
	return GuardDecision{Allowed: true}, nil
}

type clockTime struct {
	hour       int
	minute     int
	second     int
	nanosecond int
}

func parseClockTime(value string) (clockTime, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{"15:04:05.999999999", "15:04:05", "15:04"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return clockTime{hour: parsed.Hour(), minute: parsed.Minute(), second: parsed.Second(), nanosecond: parsed.Nanosecond()}, nil
		}
	}
	return clockTime{}, fmt.Errorf("invalid time %q", value)
}

func nextDeliveryAfter(now time.Time, timezone string, start, end clockTime) (time.Time, error) {
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return time.Time{}, fmt.Errorf("load notification timezone %q: %w", timezone, err)
	}
	localNow := now.In(location)
	startToday := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), start.hour, start.minute, start.second, start.nanosecond, location)
	endToday := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), end.hour, end.minute, end.second, end.nanosecond, location)

	// Equal endpoints are treated as an empty quiet window. This avoids turning
	// an ambiguous preference into an accidental 24-hour delivery pause.
	if startToday.Equal(endToday) {
		return now, nil
	}
	if startToday.Before(endToday) {
		if !localNow.Before(startToday) && localNow.Before(endToday) {
			return endToday.UTC(), nil
		}
		return now, nil
	}
	if localNow.Before(endToday) {
		return endToday.UTC(), nil
	}
	if !localNow.Before(startToday) {
		tomorrow := localNow.AddDate(0, 0, 1)
		return time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), end.hour, end.minute, end.second, end.nanosecond, location).UTC(), nil
	}
	return now, nil
}

func (g SQLGuards) CheckEntitlement(ctx context.Context, message Message) (GuardDecision, error) {
	required := g.requiredEntitlements[message.Category]
	if required == "" {
		return GuardDecision{Allowed: true}, nil
	}
	if g.db == nil {
		return GuardDecision{}, errors.New("notification guard DB is nil")
	}
	var allowed bool
	err := g.db.QueryRowContext(ctx, `
SELECT COALESCE(entitlements ? $2, false)
FROM user_entitlement_snapshots
WHERE user_id = $1`, message.UserID, required).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return GuardDecision{Allowed: false, Reason: "required notification entitlement unavailable"}, nil
	}
	if err != nil {
		return GuardDecision{}, err
	}
	if !allowed {
		return GuardDecision{Allowed: false, Reason: "required notification entitlement unavailable"}, nil
	}
	return GuardDecision{Allowed: true}, nil
}
