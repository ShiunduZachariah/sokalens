package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestContextUsesSnapshotAndRolePermissions(t *testing.T) {
	db := accessTestDB(t)
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO user_entitlement_snapshots(user_id,tier,entitlements,ads_allowed) VALUES (7,'pro',?,false)`, `{"analysis.advanced":true,"ads.hidden":true}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_roles(user_id,role_key,revoked_at) VALUES (7,'analyst',NULL); INSERT INTO role_permissions(role_key,permission_key) VALUES ('analyst','analytics.read')`); err != nil {
		t.Fatal(err)
	}

	accessContext, err := NewSQLRepository(db, DialectSQLite).ResolveUser(context.Background(), 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if accessContext.Principal.Tier != TierPro || !accessContext.Allows(CapabilityAnalysisAdvanced) || !accessContext.Allows("analytics.read") || accessContext.AdsAllowed {
		t.Fatalf("unexpected access context: %+v capabilities=%v", accessContext, accessContext.CapabilityNames())
	}
	if err := accessContext.Require("billing.manage"); !errors.Is(err, ErrCapabilityDenied) {
		t.Fatalf("require error = %v", err)
	}
}

func TestLiveSubscriptionAndGrantFallback(t *testing.T) {
	db := accessTestDB(t)
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	statements := []string{
		`INSERT INTO subscription_plans(plan_key,tier) VALUES ('basic','basic'),('pro_monthly','pro')`,
		`INSERT INTO subscriptions(user_id,plan_key,status,current_period_end) VALUES (9,'pro_monthly','active','2026-09-05T09:00:00Z')`,
		`INSERT INTO plan_entitlements(plan_key,entitlement_key,value) VALUES ('pro_monthly','analysis.advanced','true'),('pro_monthly','ads.hidden','true')`,
		`INSERT INTO user_entitlement_grants(id,user_id,entitlement_key,value,effective_at) VALUES (1,9,'ads.hidden','false','2026-08-01T09:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	accessContext, err := NewSQLRepository(db, DialectSQLite).ResolveUser(context.Background(), 9, now)
	if err != nil {
		t.Fatal(err)
	}
	if accessContext.Principal.Tier != TierPro || !accessContext.Allows(CapabilityAnalysisAdvanced) || accessContext.Allows(CapabilityAdsHidden) || !accessContext.AdsAllowed {
		t.Fatalf("live entitlements were not overlaid correctly: %+v %v", accessContext, accessContext.CapabilityNames())
	}
}

func TestProEntitlementEndsAtExactAuthoritativePeriodBoundary(t *testing.T) {
	db := accessTestDB(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, statement := range []string{
		`INSERT INTO subscription_plans(plan_key,tier) VALUES ('basic','basic'),('pro_yearly','pro')`,
		`INSERT INTO plan_entitlements(plan_key,entitlement_key,value) VALUES ('basic','analysis.standard','true'),('pro_yearly','analysis.advanced','true'),('pro_yearly','ads.hidden','true')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO subscriptions(user_id,plan_key,status,current_period_end) VALUES (10,'pro_yearly','scheduled_to_cancel',?)`, now); err != nil {
		t.Fatal(err)
	}
	ctx, err := NewSQLRepository(db, DialectSQLite).ResolveUser(context.Background(), 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Principal.Tier != TierBasic || ctx.Allows(CapabilityAnalysisAdvanced) || !ctx.AdsAllowed || !ctx.Allows(CapabilityAnalysisStandard) {
		t.Fatalf("access at exact expiry = tier %s capabilities %v ads=%v", ctx.Principal.Tier, ctx.CapabilityNames(), ctx.AdsAllowed)
	}
}

func TestGuestSignedSessionLimitDoesNotCountRefreshes(t *testing.T) {
	db := accessTestDB(t)
	signer, err := NewGuestSigner([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewGuestService(NewSQLGuestRepository(db, DialectSQLite), signer, 2)
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	token, session, err := service.CreateSession(context.Background(), "stable-browser-visitor")
	if err != nil || session.ID == 0 {
		t.Fatalf("create guest session: %+v %v", session, err)
	}
	first, err := service.ObserveAnalysis(context.Background(), token, 100, "match_analysis")
	if err != nil || !first.Counted || first.Count != 1 {
		t.Fatalf("first view = %+v, %v", first, err)
	}
	refresh, err := service.ObserveAnalysis(context.Background(), token, 100, "match_analysis")
	if err != nil || refresh.Counted || refresh.Count != 1 {
		t.Fatalf("refresh = %+v, %v", refresh, err)
	}
	second, err := service.ObserveAnalysis(context.Background(), token, 101, "match_analysis")
	if err != nil || !second.Counted || second.Count != 2 {
		t.Fatalf("second view = %+v, %v", second, err)
	}
	blocked, err := service.ObserveAnalysis(context.Background(), token, 102, "match_analysis")
	if !errors.Is(err, ErrViewLimitReached) || blocked.Allowed || blocked.Count != 2 {
		t.Fatalf("blocked view = %+v, %v", blocked, err)
	}
	if _, err := service.Resolve(context.Background(), token+"tampered"); !errors.Is(err, ErrInvalidGuestToken) {
		t.Fatalf("tampered token error = %v", err)
	}
}

func TestContextCapabilityValues(t *testing.T) {
	ctx := NewContext(Principal{Tier: TierBasic}, map[string]json.RawMessage{"limit": json.RawMessage(`5`), "off": json.RawMessage(`false`)})
	if !ctx.Allows("limit") || ctx.Allows("off") || !ctx.Allows(CapabilityPublicRead) {
		t.Fatalf("unexpected capability evaluation: %v", ctx.CapabilityNames())
	}
}

func accessTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	schema := `
CREATE TABLE user_entitlement_snapshots (user_id INTEGER PRIMARY KEY, tier TEXT NOT NULL, entitlements TEXT NOT NULL, ads_allowed BOOLEAN NOT NULL, version INTEGER DEFAULT 1, next_change_at DATETIME, updated_at DATETIME);
CREATE TABLE subscription_plans (plan_key TEXT PRIMARY KEY, tier TEXT NOT NULL);
CREATE TABLE subscriptions (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, plan_key TEXT NOT NULL, status TEXT NOT NULL, current_period_end DATETIME, grace_ends_at DATETIME);
CREATE TABLE plan_entitlements (plan_key TEXT NOT NULL, entitlement_key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(plan_key,entitlement_key));
CREATE TABLE user_entitlement_grants (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, entitlement_key TEXT NOT NULL, value TEXT NOT NULL, effective_at DATETIME NOT NULL, expires_at DATETIME, revoked_at DATETIME);
CREATE TABLE user_roles (user_id INTEGER NOT NULL, role_key TEXT NOT NULL, revoked_at DATETIME);
CREATE TABLE role_permissions (role_key TEXT NOT NULL, permission_key TEXT NOT NULL);
CREATE TABLE guest_sessions (id INTEGER PRIMARY KEY AUTOINCREMENT, visitor_hash TEXT NOT NULL UNIQUE, session_hash TEXT NOT NULL UNIQUE, first_seen_at DATETIME NOT NULL, last_seen_at DATETIME NOT NULL, expires_at DATETIME NOT NULL);
CREATE TABLE guest_analysis_views (id INTEGER PRIMARY KEY AUTOINCREMENT, guest_session_id INTEGER NOT NULL, match_id INTEGER NOT NULL, page_type TEXT NOT NULL, first_viewed_at DATETIME NOT NULL, last_viewed_at DATETIME NOT NULL, view_count INTEGER NOT NULL, UNIQUE(guest_session_id,match_id,page_type));`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return db
}
