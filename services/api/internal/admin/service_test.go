package admin

import "testing"

func TestAdminServiceCreatesUserAndAuthenticatesPassword(t *testing.T) {
	repo := NewMemoryRepository()
	service := NewService(repo)

	user, err := service.CreateAdmin("ops@example.com", "correct horse battery staple", RoleSuperAdmin, 42)
	if err != nil {
		t.Fatalf("CreateAdmin returned error: %v", err)
	}
	if user.Email != "ops@example.com" {
		t.Fatalf("Email = %q, want ops@example.com", user.Email)
	}

	authenticated, err := service.Authenticate("ops@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatalf("Authenticate returned error: %v", err)
	}
	if authenticated.ID != user.ID {
		t.Fatalf("authenticated ID = %d, want %d", authenticated.ID, user.ID)
	}
}

func TestAdminServiceRejectsWrongPassword(t *testing.T) {
	repo := NewMemoryRepository()
	service := NewService(repo)
	if _, err := service.CreateAdmin("ops@example.com", "correct horse battery staple", RoleAdmin, 42); err != nil {
		t.Fatalf("CreateAdmin returned error: %v", err)
	}

	_, err := service.Authenticate("ops@example.com", "wrong")
	if err == nil {
		t.Fatal("Authenticate returned nil error for wrong password")
	}
}

func TestAdminServiceRejectsDuplicateEmail(t *testing.T) {
	repo := NewMemoryRepository()
	service := NewService(repo)
	if _, err := service.CreateAdmin("ops@example.com", "correct horse battery staple", RoleAdmin, 42); err != nil {
		t.Fatalf("CreateAdmin returned error: %v", err)
	}

	_, err := service.CreateAdmin("ops@example.com", "another correct horse battery staple", RoleAdmin, 42)
	if err == nil {
		t.Fatal("CreateAdmin returned nil error for duplicate email")
	}
}

func TestCanChecksRolePermissions(t *testing.T) {
	if !Can(RoleSuperAdmin, PermissionManagePolicy) {
		t.Fatal("super admin cannot manage policy")
	}
	if Can(RoleViewer, PermissionManagePolicy) {
		t.Fatal("viewer can manage policy")
	}
	if !Can(RoleAdmin, PermissionReadAuditLogs) {
		t.Fatal("admin cannot read audit logs")
	}
}

func TestCreateAdminWritesAuditEvent(t *testing.T) {
	repo := NewMemoryRepository()
	service := NewService(repo)

	if _, err := service.CreateAdmin("ops@example.com", "correct horse battery staple", RoleAdmin, 42); err != nil {
		t.Fatalf("CreateAdmin returned error: %v", err)
	}

	events := repo.AuditEvents()
	if len(events) != 1 {
		t.Fatalf("audit event count = %d, want 1", len(events))
	}
	if events[0].Action != "admin_user.created" {
		t.Fatalf("audit action = %q, want admin_user.created", events[0].Action)
	}
	if events[0].ActorID != 42 {
		t.Fatalf("actor = %d, want 42", events[0].ActorID)
	}
}
