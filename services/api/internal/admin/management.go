package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrAdminForbidden = errors.New("admin action is forbidden")
	ErrUserNotFound   = errors.New("user not found")
)

type UserSummary struct {
	ID            int64      `json:"id"`
	Email         string     `json:"email"`
	Role          Role       `json:"role"`
	AccountStatus string     `json:"account_status"`
	Tier          string     `json:"tier"`
	EmailVerified bool       `json:"email_verified"`
	CreatedAt     time.Time  `json:"created_at"`
	LastLoginAt   *time.Time `json:"last_login_at,omitempty"`
}

type GrowthBreakdown struct {
	Key   string `json:"key"`
	Count int64  `json:"count"`
}

type UserGrowthSummary struct {
	TotalUsers      int64             `json:"total_users"`
	VerifiedUsers   int64             `json:"verified_users"`
	UnverifiedUsers int64             `json:"unverified_users"`
	NewToday        int64             `json:"new_today"`
	New7Days        int64             `json:"new_7_days"`
	New30Days       int64             `json:"new_30_days"`
	Active7Days     int64             `json:"active_7_days"`
	Active30Days    int64             `json:"active_30_days"`
	ActivatedUsers  int64             `json:"activated_users"`
	ByCountry       []GrowthBreakdown `json:"by_country"`
	BySource        []GrowthBreakdown `json:"by_source"`
}

type UserActivity struct {
	ID          int64           `json:"id"`
	UserID      *int64          `json:"user_id,omitempty"`
	Email       string          `json:"email"`
	EventType   string          `json:"event_type"`
	CountryCode string          `json:"country_code"`
	Platform    string          `json:"platform"`
	Source      string          `json:"source"`
	OccurredAt  time.Time       `json:"occurred_at"`
	Metadata    json.RawMessage `json:"metadata"`
}

type SuspensionCommand struct {
	UserID   int64
	Reason   string
	Evidence map[string]any
	Actor    User
}

type AuditLog struct {
	ID         int64           `json:"id"`
	ActorID    *int64          `json:"actor_user_id,omitempty"`
	Action     string          `json:"action"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

type SubscriptionSummary struct {
	ID                     int64      `json:"id"`
	Email                  string     `json:"email"`
	PlanKey                string     `json:"plan_key"`
	Status                 string     `json:"status"`
	Provider               string     `json:"provider"`
	ProviderSubscriptionID string     `json:"provider_subscription_id"`
	CurrentPeriodEnd       *time.Time `json:"current_period_end,omitempty"`
	CancelAtPeriodEnd      bool       `json:"cancel_at_period_end"`
	LatestWebhookStatus    string     `json:"latest_webhook_status"`
	OpenDisputes           int        `json:"open_disputes"`
}

type PlanConfiguration struct {
	PlanKey         string   `json:"plan_key"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	BillingInterval string   `json:"billing_interval"`
	Currency        string   `json:"currency"`
	AmountMinor     int64    `json:"amount_minor"`
	Enabled         bool     `json:"enabled"`
	SavingsLabel    string   `json:"savings_label"`
	Features        []string `json:"features"`
}

type ManagementService struct {
	db          *sql.DB
	provider    string
	environment string
}

func NewManagementService(db *sql.DB) ManagementService {
	return ManagementService{db: db, provider: "sandbox", environment: "local"}
}

func (s ManagementService) ConfigureBilling(provider, environment string) ManagementService {
	if strings.TrimSpace(provider) != "" {
		s.provider = strings.TrimSpace(provider)
	}
	if strings.TrimSpace(environment) != "" {
		s.environment = strings.TrimSpace(environment)
	}
	return s
}

func (s ManagementService) ListUsers(ctx context.Context, search, status string, limit int) ([]UserSummary, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT u.id, COALESCE(u.email, ''), u.role, u.account_status,
       COALESCE(es.tier, 'basic'), u.email_verified_at IS NOT NULL,
       u.created_at, u.last_login_at
FROM users u
LEFT JOIN user_entitlement_snapshots es ON es.user_id = u.id
WHERE ($1 = '' OR u.email_normalized LIKE '%' || lower($1) || '%')
  AND ($2 = '' OR u.account_status = $2)
ORDER BY u.created_at DESC, u.id DESC
LIMIT $3`, strings.TrimSpace(search), strings.TrimSpace(status), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []UserSummary{}
	for rows.Next() {
		var item UserSummary
		var lastLogin sql.NullTime
		if err := rows.Scan(&item.ID, &item.Email, &item.Role, &item.AccountStatus, &item.Tier, &item.EmailVerified, &item.CreatedAt, &lastLogin); err != nil {
			return nil, err
		}
		if lastLogin.Valid {
			value := lastLogin.Time.UTC()
			item.LastLoginAt = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s ManagementService) UserGrowth(ctx context.Context) (UserGrowthSummary, error) {
	var summary UserGrowthSummary
	err := s.db.QueryRowContext(ctx, `
SELECT
  COUNT(*),
  COUNT(*) FILTER (WHERE email_verified_at IS NOT NULL),
  COUNT(*) FILTER (WHERE email_verified_at IS NULL),
  COUNT(*) FILTER (WHERE created_at >= date_trunc('day', now())),
  COUNT(*) FILTER (WHERE created_at >= now() - interval '7 days'),
  COUNT(*) FILTER (WHERE created_at >= now() - interval '30 days'),
  COUNT(*) FILTER (WHERE last_login_at >= now() - interval '7 days'),
  COUNT(*) FILTER (WHERE last_login_at >= now() - interval '30 days'),
  COUNT(*) FILTER (WHERE email_verified_at IS NOT NULL AND EXISTS (
    SELECT 1 FROM user_growth_events e
    WHERE e.user_id = users.id AND e.event_type = 'FIRST_FAVORITE_ADDED'
  ))
FROM users
WHERE deleted_at IS NULL AND role = 'user'`).Scan(
		&summary.TotalUsers,
		&summary.VerifiedUsers,
		&summary.UnverifiedUsers,
		&summary.NewToday,
		&summary.New7Days,
		&summary.New30Days,
		&summary.Active7Days,
		&summary.Active30Days,
		&summary.ActivatedUsers,
	)
	if err != nil {
		return UserGrowthSummary{}, err
	}
	var breakdownErr error
	summary.ByCountry, breakdownErr = s.growthBreakdown(ctx, `
SELECT COALESCE(NULLIF(signup_country_code, ''), 'unknown'), COUNT(*)
FROM user_acquisition GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 20`)
	if breakdownErr != nil {
		return UserGrowthSummary{}, breakdownErr
	}
	summary.BySource, breakdownErr = s.growthBreakdown(ctx, `
SELECT COALESCE(NULLIF(first_utm_source, ''), 'direct'), COUNT(*)
FROM user_acquisition GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 20`)
	if breakdownErr != nil {
		return UserGrowthSummary{}, breakdownErr
	}
	return summary, nil
}

func (s ManagementService) UserActivity(ctx context.Context, limit int) ([]UserActivity, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT e.id, e.user_id, COALESCE(u.email, ''), e.event_type,
       COALESCE(e.country_code, ''), COALESCE(e.platform, ''), COALESCE(e.source, ''),
       e.occurred_at, COALESCE(e.metadata, '{}'::jsonb)
FROM user_growth_events e
LEFT JOIN users u ON u.id = e.user_id
ORDER BY e.occurred_at DESC, e.id DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UserActivity, 0)
	for rows.Next() {
		var item UserActivity
		var userID sql.NullInt64
		if err := rows.Scan(&item.ID, &userID, &item.Email, &item.EventType, &item.CountryCode, &item.Platform, &item.Source, &item.OccurredAt, &item.Metadata); err != nil {
			return nil, err
		}
		if userID.Valid {
			value := userID.Int64
			item.UserID = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s ManagementService) growthBreakdown(ctx context.Context, query string) ([]GrowthBreakdown, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]GrowthBreakdown, 0)
	for rows.Next() {
		var item GrowthBreakdown
		if err := rows.Scan(&item.Key, &item.Count); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s ManagementService) Suspend(ctx context.Context, command SuspensionCommand) error {
	if !Can(command.Actor.Role, PermissionManageUsers) || command.Actor.ID == command.UserID || strings.TrimSpace(command.Reason) == "" {
		return ErrAdminForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous string
	var targetRole Role
	if err := tx.QueryRowContext(ctx, `SELECT account_status, role FROM users WHERE id = $1 FOR UPDATE`, command.UserID).Scan(&previous, &targetRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		return err
	}
	if isAdminRole(targetRole) && command.Actor.Role != RoleSuperAdmin {
		return ErrAdminForbidden
	}
	evidence, err := json.Marshal(command.Evidence)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET account_status = 'suspended', updated_at = now() WHERE id = $1`, command.UserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE user_sessions SET revoked_at = now(), revocation_reason = 'account_suspended'
WHERE user_id = $1 AND revoked_at IS NULL`, command.UserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO account_suspensions
  (user_id, status, reason, evidence, actor_user_id, previous_account_status, new_account_status)
VALUES ($1, 'active', $2, $3::jsonb, $4, $5, 'suspended')`, command.UserID, command.Reason, string(evidence), command.Actor.ID, previous); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_logs (actor_user_id, action, entity_type, entity_id, reason, before_value, after_value)
VALUES ($1, 'user.suspended', 'user', $2, $3,
        jsonb_build_object('account_status', $4::text), jsonb_build_object('account_status', 'suspended'))`,
		command.Actor.ID, fmt.Sprint(command.UserID), command.Reason, previous); err != nil {
		return err
	}
	return tx.Commit()
}

func (s ManagementService) Reactivate(ctx context.Context, userID int64, reason string, actor User) error {
	if !Can(actor.Role, PermissionManageUsers) || actor.ID == userID || strings.TrimSpace(reason) == "" {
		return ErrAdminForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous string
	var targetRole Role
	if err := tx.QueryRowContext(ctx, `SELECT account_status, role FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&previous, &targetRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		return err
	}
	if isAdminRole(targetRole) && actor.Role != RoleSuperAdmin {
		return ErrAdminForbidden
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET account_status = 'active', updated_at = now() WHERE id = $1`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE account_suspensions SET status = 'lifted', lifted_at = now(), lifted_by = $2
WHERE id = (SELECT id FROM account_suspensions WHERE user_id = $1 AND status = 'active' ORDER BY created_at DESC LIMIT 1)`, userID, actor.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_logs (actor_user_id, action, entity_type, entity_id, reason, before_value, after_value)
VALUES ($1, 'user.reactivated', 'user', $2, $3,
        jsonb_build_object('account_status', $4::text), jsonb_build_object('account_status', 'active'))`, actor.ID, fmt.Sprint(userID), reason, previous); err != nil {
		return err
	}
	return tx.Commit()
}

func (s ManagementService) RevokeSessions(ctx context.Context, userID int64, reason string, actor User) error {
	if !Can(actor.Role, PermissionManageUsers) || strings.TrimSpace(reason) == "" {
		return ErrAdminForbidden
	}
	result, err := s.db.ExecContext(ctx, `
WITH revoked AS (
  UPDATE user_sessions SET revoked_at = now(), revocation_reason = $2
  WHERE user_id = $1 AND revoked_at IS NULL RETURNING id
)
INSERT INTO audit_logs (actor_user_id, action, entity_type, entity_id, reason, after_value)
VALUES ($3, 'user.sessions_revoked', 'user', $4, $2,
        jsonb_build_object('revoked_count', (SELECT count(*) FROM revoked)))`, userID, reason, actor.ID, fmt.Sprint(userID))
	return resultError(result, err)
}

func (s ManagementService) AuditLogs(ctx context.Context, limit int) ([]AuditLog, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, actor_user_id, action, entity_type, COALESCE(entity_id, ''), COALESCE(reason, ''),
       COALESCE(before_value, 'null'::jsonb), COALESCE(after_value, 'null'::jsonb), created_at
FROM audit_logs ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AuditLog{}
	for rows.Next() {
		var item AuditLog
		var actor sql.NullInt64
		if err := rows.Scan(&item.ID, &actor, &item.Action, &item.EntityType, &item.EntityID, &item.Reason, &item.Before, &item.After, &item.CreatedAt); err != nil {
			return nil, err
		}
		if actor.Valid {
			value := actor.Int64
			item.ActorID = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s ManagementService) ListSubscriptions(ctx context.Context, limit int) ([]SubscriptionSummary, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT s.id,COALESCE(u.email,''),s.plan_key,s.status,s.provider,s.provider_subscription_id,s.current_period_end,s.cancel_at_period_end,COALESCE((SELECT bwe.status FROM billing_webhook_events bwe WHERE bwe.provider=s.provider AND bwe.environment=s.environment ORDER BY bwe.received_at DESC,bwe.id DESC LIMIT 1),'none'),(SELECT count(*) FROM payment_disputes pd WHERE pd.provider=s.provider AND pd.environment=s.environment AND pd.metadata->>'subscription_id'=s.id::text AND pd.status NOT IN ('won','lost','closed','resolved')) FROM subscriptions s JOIN users u ON u.id=s.user_id ORDER BY s.updated_at DESC,s.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []SubscriptionSummary{}
	for rows.Next() {
		var item SubscriptionSummary
		var end sql.NullTime
		if err := rows.Scan(&item.ID, &item.Email, &item.PlanKey, &item.Status, &item.Provider, &item.ProviderSubscriptionID, &end, &item.CancelAtPeriodEnd, &item.LatestWebhookStatus, &item.OpenDisputes); err != nil {
			return nil, err
		}
		if end.Valid {
			value := end.Time.UTC()
			item.CurrentPeriodEnd = &value
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s ManagementService) ListPlanConfigurations(ctx context.Context) ([]PlanConfiguration, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT p.plan_key,p.name,COALESCE(p.metadata->>'summary',''),p.billing_interval,
       COALESCE(pp.currency,'USD'),COALESCE(pp.amount_minor,0),p.active AND COALESCE(pp.active,false),
       COALESCE(p.metadata->>'savings_label',''),
       COALESCE((SELECT json_agg(pe.entitlement_key ORDER BY pe.entitlement_key) FROM plan_entitlements pe WHERE pe.plan_key=p.plan_key),'[]'::json)
FROM subscription_plans p
LEFT JOIN plan_prices pp ON pp.plan_key=p.plan_key AND pp.provider=$1 AND pp.environment=$2
WHERE p.tier='pro'
ORDER BY CASE p.billing_interval WHEN 'month' THEN 1 ELSE 2 END`, s.provider, s.environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []PlanConfiguration{}
	for rows.Next() {
		var item PlanConfiguration
		var features []byte
		if err := rows.Scan(&item.PlanKey, &item.Name, &item.Description, &item.BillingInterval, &item.Currency, &item.AmountMinor, &item.Enabled, &item.SavingsLabel, &features); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(features, &item.Features); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s ManagementService) UpdatePlanConfiguration(ctx context.Context, item PlanConfiguration, actor User) error {
	if !Can(actor.Role, PermissionManageBilling) || item.PlanKey == "" || strings.TrimSpace(item.Name) == "" || len(strings.TrimSpace(item.Currency)) != 3 || item.AmountMinor < 0 {
		return ErrAdminForbidden
	}
	currency := strings.ToUpper(strings.TrimSpace(item.Currency))
	metadata, err := json.Marshal(map[string]any{"summary": strings.TrimSpace(item.Description), "savings_label": strings.TrimSpace(item.SavingsLabel), "features": item.Features, "recommended": item.BillingInterval == "year"})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var before []byte
	if err := tx.QueryRowContext(ctx, `SELECT jsonb_build_object('name',name,'active',active,'metadata',metadata) FROM subscription_plans WHERE plan_key=$1 FOR UPDATE`, item.PlanKey).Scan(&before); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE subscription_plans SET name=$2,active=$3,metadata=$4::jsonb,updated_at=now() WHERE plan_key=$1 AND tier='pro'`, item.PlanKey, item.Name, item.Enabled, string(metadata)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE plan_prices SET active=false,updated_at=now() WHERE plan_key=$1 AND provider=$2 AND environment=$3`, item.PlanKey, s.provider, s.environment); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO plan_prices(plan_key,provider,environment,currency,amount_minor,active) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(plan_key,provider,environment,currency) DO UPDATE SET amount_minor=excluded.amount_minor,active=excluded.active,updated_at=now()`, item.PlanKey, s.provider, s.environment, currency, item.AmountMinor, item.Enabled); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plan_entitlements WHERE plan_key=$1`, item.PlanKey); err != nil {
		return err
	}
	features := make(map[string]struct{}, len(item.Features))
	for _, rawFeature := range item.Features {
		feature := strings.TrimSpace(rawFeature)
		if feature == "" {
			continue
		}
		if _, exists := features[feature]; exists {
			continue
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO plan_entitlements(plan_key,entitlement_key) SELECT $1,$2 WHERE EXISTS(SELECT 1 FROM entitlements WHERE entitlement_key=$2)`, item.PlanKey, feature)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return ErrAdminForbidden
		}
		features[feature] = struct{}{}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(actor_user_id,action,entity_type,entity_id,reason,before_value,after_value) VALUES($1,'billing.plan_updated','subscription_plan',$2,'Administrator updated public pricing',$3::jsonb,jsonb_build_object('name',$4::text,'active',$5::boolean,'currency',$6::text,'amount_minor',$7::bigint))`, actor.ID, item.PlanKey, string(before), item.Name, item.Enabled, currency, item.AmountMinor); err != nil {
		return err
	}
	return tx.Commit()
}

func isAdminRole(role Role) bool {
	switch role {
	case RoleSuperAdmin, RoleAdmin, RoleViewer, RoleSupportAdmin, RoleContentAdmin, RoleBillingAdmin, RoleAnalyst:
		return true
	default:
		return false
	}
}

func resultError(_ sql.Result, err error) error { return err }
