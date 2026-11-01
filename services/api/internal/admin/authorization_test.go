package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestDatabaseAuthorizationDefaultsDenyAndHonorsAssignmentLifecycle(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE principal_role_assignments (
  principal_type TEXT NOT NULL, principal_id TEXT NOT NULL, role_key TEXT NOT NULL,
  scope_type TEXT NOT NULL, valid_from DATETIME NOT NULL, valid_until DATETIME, revoked_at DATETIME
);
CREATE TABLE role_permissions (role_key TEXT NOT NULL, permission_key TEXT NOT NULL);
INSERT INTO role_permissions(role_key, permission_key) VALUES ('support_agent', 'users.read'), ('support_agent', 'users.manage');
`); err != nil {
		t.Fatal(err)
	}
	store := NewSQLAuthStore(db, DialectSQLite)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

	if allowed, err := store.authorize(context.Background(), 42, PermissionReadUsers, now); err != nil || allowed {
		t.Fatalf("unassigned permission allowed=%v err=%v", allowed, err)
	}
	if _, err := db.Exec(`INSERT INTO principal_role_assignments
      (principal_type, principal_id, role_key, scope_type, valid_from)
      VALUES ('HUMAN_ADMIN', '42', 'support_agent', 'GLOBAL', ?)`, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if allowed, err := store.authorize(context.Background(), 42, PermissionManageUsers, now); err != nil || !allowed {
		t.Fatalf("active permission allowed=%v err=%v", allowed, err)
	}
	if allowed, err := store.authorize(context.Background(), 42, PermissionManageBilling, now); err != nil || allowed {
		t.Fatalf("ungranted permission allowed=%v err=%v", allowed, err)
	}
	if _, err := db.Exec(`UPDATE principal_role_assignments SET revoked_at=? WHERE principal_id='42'`, now); err != nil {
		t.Fatal(err)
	}
	if allowed, err := store.authorize(context.Background(), 42, PermissionReadUsers, now); err != nil || allowed {
		t.Fatalf("revoked permission allowed=%v err=%v", allowed, err)
	}
}

func TestDatabaseAuthorizationRejectsNonGlobalAssignmentsForGlobalEndpoint(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
CREATE TABLE principal_role_assignments (
  principal_type TEXT NOT NULL, principal_id TEXT NOT NULL, role_key TEXT NOT NULL,
  scope_type TEXT NOT NULL, valid_from DATETIME NOT NULL, valid_until DATETIME, revoked_at DATETIME
);
CREATE TABLE role_permissions (role_key TEXT NOT NULL, permission_key TEXT NOT NULL);
INSERT INTO role_permissions(role_key, permission_key) VALUES ('content_editor', 'content.manage');
INSERT INTO principal_role_assignments(principal_type, principal_id, role_key, scope_type, valid_from)
VALUES ('HUMAN_ADMIN', '7', 'content_editor', 'LEAGUE', '2026-08-01 00:00:00');
`); err != nil {
		t.Fatal(err)
	}
	allowed, err := NewSQLAuthStore(db, DialectSQLite).authorize(
		context.Background(), 7, PermissionManageContent, time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	)
	if err != nil || allowed {
		t.Fatalf("scoped assignment authorized global endpoint: allowed=%v err=%v", allowed, err)
	}
}

func TestLegacyAdminRolesMapToSeparatedAdministrativeRoles(t *testing.T) {
	cases := map[Role]string{
		RoleSuperAdmin:   "platform_owner",
		RoleAdmin:        "platform_admin",
		RoleSupportAdmin: "support_agent",
		RoleContentAdmin: "content_editor",
		RoleBillingAdmin: "billing_admin",
		RoleAnalyst:      "auditor",
		RoleViewer:       "auditor",
	}
	for role, want := range cases {
		if got := administrativeRoleKey(role); got != want {
			t.Fatalf("administrativeRoleKey(%q)=%q want %q", role, got, want)
		}
	}
	if got := administrativeRoleKey(Role("basic")); got != "" {
		t.Fatalf("consumer role mapped into admin RBAC: %q", got)
	}
}
