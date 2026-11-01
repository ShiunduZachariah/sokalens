package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

type Dialect string

const (
	DialectPostgres Dialect = "postgres"
	DialectSQLite   Dialect = "sqlite"
)

type SQLRepository struct {
	db      *sql.DB
	dialect Dialect
}

func NewSQLRepository(db *sql.DB, dialect Dialect) *SQLRepository {
	return &SQLRepository{db: db, dialect: dialect}
}

// ResolveUser prefers the prepared entitlement snapshot used by hot public
// requests. It falls back to normalized subscription/grant tables when a
// snapshot has not been generated yet.
func (r *SQLRepository) ResolveUser(ctx context.Context, userID int64, now time.Time) (Context, error) {
	tier, capabilities, ok, err := r.snapshot(ctx, userID, now)
	if err != nil {
		return Context{}, err
	}
	if !ok {
		tier, capabilities, err = r.liveEntitlements(ctx, userID, now)
		if err != nil {
			return Context{}, err
		}
	}
	roles, permissions, err := r.rolesAndPermissions(ctx, userID)
	if err != nil {
		return Context{}, err
	}
	for permission := range permissions {
		capabilities[permission] = json.RawMessage("true")
	}
	return NewContext(Principal{UserID: userID, Tier: tier, Roles: roles}, capabilities), nil
}

func (r *SQLRepository) snapshot(ctx context.Context, userID int64, now time.Time) (Tier, map[string]json.RawMessage, bool, error) {
	query := fmt.Sprintf(`SELECT tier, entitlements, ads_allowed FROM user_entitlement_snapshots WHERE user_id = %s AND (next_change_at IS NULL OR next_change_at > %s)`, r.p(1), r.p(2))
	var tier Tier
	var raw []byte
	var adsAllowed bool
	if err := r.db.QueryRowContext(ctx, query, userID, now).Scan(&tier, &raw, &adsAllowed); errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	} else if err != nil {
		return "", nil, false, err
	}
	values := make(map[string]json.RawMessage)
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &values); err != nil {
			return "", nil, false, fmt.Errorf("decode entitlement snapshot: %w", err)
		}
	}
	if adsAllowed {
		values[CapabilityAdsHidden] = json.RawMessage("false")
	} else {
		values[CapabilityAdsHidden] = json.RawMessage("true")
	}
	return normalizeTier(tier), values, true, nil
}

func (r *SQLRepository) liveEntitlements(ctx context.Context, userID int64, now time.Time) (Tier, map[string]json.RawMessage, error) {
	planQuery := fmt.Sprintf(`SELECT p.plan_key, p.tier FROM subscriptions s JOIN subscription_plans p ON p.plan_key = s.plan_key WHERE s.user_id = %s AND s.status IN ('active','trialing','past_due','scheduled_to_cancel') AND (s.current_period_end IS NULL OR s.current_period_end > %s OR s.grace_ends_at > %s) ORDER BY CASE p.tier WHEN 'pro' THEN 0 ELSE 1 END, s.current_period_end DESC LIMIT 1`, r.p(1), r.p(2), r.p(3))
	planKey := "basic"
	tier := TierBasic
	if err := r.db.QueryRowContext(ctx, planQuery, userID, now, now).Scan(&planKey, &tier); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}
	tier = normalizeTier(tier)
	values := make(map[string]json.RawMessage)

	planEntitlements := fmt.Sprintf(`SELECT entitlement_key, value FROM plan_entitlements WHERE plan_key = %s`, r.p(1))
	if err := scanValues(ctx, r.db, planEntitlements, []any{planKey}, values); err != nil {
		return "", nil, err
	}
	grantQuery := fmt.Sprintf(`SELECT entitlement_key, value FROM user_entitlement_grants WHERE user_id = %s AND revoked_at IS NULL AND effective_at <= %s AND (expires_at IS NULL OR expires_at > %s) ORDER BY effective_at, id`, r.p(1), r.p(2), r.p(3))
	if err := scanValues(ctx, r.db, grantQuery, []any{userID, now, now}, values); err != nil {
		return "", nil, err
	}
	return tier, values, nil
}

func scanValues(ctx context.Context, db *sql.DB, query string, args []any, destination map[string]json.RawMessage) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw any
		if err := rows.Scan(&key, &raw); err != nil {
			return err
		}
		switch typed := raw.(type) {
		case []byte:
			destination[key] = append(json.RawMessage(nil), typed...)
		case string:
			destination[key] = json.RawMessage(typed)
		default:
			encoded, err := json.Marshal(typed)
			if err != nil {
				return err
			}
			destination[key] = encoded
		}
	}
	return rows.Err()
}

func (r *SQLRepository) rolesAndPermissions(ctx context.Context, userID int64) ([]string, map[string]struct{}, error) {
	query := fmt.Sprintf(`SELECT ur.role_key, rp.permission_key FROM user_roles ur LEFT JOIN role_permissions rp ON rp.role_key = ur.role_key WHERE ur.user_id = %s AND ur.revoked_at IS NULL ORDER BY ur.role_key, rp.permission_key`, r.p(1))
	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	roleSet := make(map[string]struct{})
	permissions := make(map[string]struct{})
	for rows.Next() {
		var role string
		var permission sql.NullString
		if err := rows.Scan(&role, &permission); err != nil {
			return nil, nil, err
		}
		roleSet[role] = struct{}{}
		if permission.Valid {
			permissions[permission.String] = struct{}{}
		}
	}
	roles := make([]string, 0, len(roleSet))
	for role := range roleSet {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles, permissions, rows.Err()
}

func normalizeTier(tier Tier) Tier {
	if tier == TierPro {
		return TierPro
	}
	return TierBasic
}

func (r *SQLRepository) p(position int) string {
	if r.dialect == DialectSQLite {
		return "?"
	}
	return fmt.Sprintf("$%d", position)
}
