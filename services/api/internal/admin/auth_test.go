package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

func TestAuthServiceLoginCreatesAndValidatesSession(t *testing.T) {
	db := openAdminAuthDB(t)
	insertAdminUser(t, db, "ADMIN@example.com", "local-admin-password", RoleSuperAdmin)
	service := NewAuthService(NewSQLAuthStore(db, DialectSQLite), time.Hour)

	result, err := service.Login(context.Background(), "admin@example.com", "local-admin-password")
	if err != nil {
		t.Fatalf("Login returned error: %v", err)
	}
	if result.Token == "" {
		t.Fatal("session token is empty")
	}
	if result.User.Role != RoleSuperAdmin {
		t.Fatalf("role = %q, want %q", result.User.Role, RoleSuperAdmin)
	}

	var storedHash string
	if err := db.QueryRowContext(context.Background(), "SELECT token_hash FROM user_sessions WHERE user_id = ?", result.User.ID).Scan(&storedHash); err != nil {
		t.Fatalf("query session: %v", err)
	}
	if storedHash == result.Token {
		t.Fatal("stored token hash equals raw token")
	}

	user, err := service.ValidateSession(context.Background(), result.Token)
	if err != nil {
		t.Fatalf("ValidateSession returned error: %v", err)
	}
	if user.ID != result.User.ID {
		t.Fatalf("validated user ID = %d, want %d", user.ID, result.User.ID)
	}
}

func TestAuthServiceRejectsWrongPassword(t *testing.T) {
	db := openAdminAuthDB(t)
	insertAdminUser(t, db, "admin@example.com", "local-admin-password", RoleAdmin)
	service := NewAuthService(NewSQLAuthStore(db, DialectSQLite), time.Hour)

	_, err := service.Login(context.Background(), "admin@example.com", "wrong-password")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login error = %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthServiceLogoutRevokesSession(t *testing.T) {
	db := openAdminAuthDB(t)
	insertAdminUser(t, db, "admin@example.com", "local-admin-password", RoleAdmin)
	service := NewAuthService(NewSQLAuthStore(db, DialectSQLite), time.Hour)

	result, err := service.Login(context.Background(), "admin@example.com", "local-admin-password")
	if err != nil {
		t.Fatalf("Login returned error: %v", err)
	}
	if err := service.Logout(context.Background(), result.Token); err != nil {
		t.Fatalf("Logout returned error: %v", err)
	}

	_, err = service.ValidateSession(context.Background(), result.Token)
	if !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("ValidateSession error = %v, want ErrInvalidSession", err)
	}
}

func TestAuthServiceRejectsSessionAfterAdminDemotion(t *testing.T) {
	db := openAdminAuthDB(t)
	insertAdminUser(t, db, "admin@example.com", "local-admin-password", RoleSuperAdmin)
	service := NewAuthService(NewSQLAuthStore(db, DialectSQLite), time.Hour)
	result, err := service.Login(context.Background(), "admin@example.com", "local-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE users SET role = 'user' WHERE id = ?", result.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ValidateSession(context.Background(), result.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("ValidateSession error = %v, want ErrInvalidSession", err)
	}
}

func TestAuthServiceRejectsSessionAfterAdminSuspension(t *testing.T) {
	db := openAdminAuthDB(t)
	insertAdminUser(t, db, "support@example.com", "local-admin-password", RoleSupportAdmin)
	service := NewAuthService(NewSQLAuthStore(db, DialectSQLite), time.Hour)
	result, err := service.Login(context.Background(), "support@example.com", "local-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE users SET account_status = 'suspended' WHERE id = ?", result.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ValidateSession(context.Background(), result.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("ValidateSession error = %v, want ErrInvalidSession", err)
	}
}

func openAdminAuthDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  email TEXT UNIQUE,
  password_hash TEXT,
  role TEXT NOT NULL,
  account_status TEXT NOT NULL DEFAULT 'active',
  deleted_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE user_sessions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id BIGINT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		t.Fatalf("create auth tables: %v", err)
	}
	return db
}

func insertAdminUser(t *testing.T, db *sql.DB, email, password string, role Role) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO users (email, password_hash, role) VALUES (?, ?, ?)", email, string(hash), string(role)); err != nil {
		t.Fatalf("insert admin user: %v", err)
	}
}
