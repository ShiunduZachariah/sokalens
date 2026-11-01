package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/accounts"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/admin"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	if err := loadEnvFile(".env"); err != nil {
		fail(fmt.Errorf("load .env: %w", err))
	}
	databaseURL := os.Getenv("DATABASE_URL")
	appSecret := os.Getenv("APP_SECRET")
	adminEmail := strings.TrimSpace(os.Getenv("SEED_ADMIN_EMAIL"))
	adminPassword := os.Getenv("SEED_ADMIN_PASSWORD")
	basicEmail := strings.TrimSpace(os.Getenv("SEED_BASIC_USER_EMAIL"))
	basicPassword := os.Getenv("SEED_BASIC_USER_PASSWORD")
	if databaseURL == "" || appSecret == "" || adminEmail == "" || adminPassword == "" || basicEmail == "" || basicPassword == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL, APP_SECRET, and all SEED_* account variables are required")
		os.Exit(2)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		fail(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := seedAdmin(ctx, db, appSecret, adminEmail, adminPassword); err != nil {
		fail(err)
	}
	if err := seedBasic(ctx, db, basicEmail, basicPassword); err != nil {
		fail(err)
	}
	fmt.Println("Development seed accounts are ready; existing passwords were not changed.")
}

func seedAdmin(ctx context.Context, db *sql.DB, appSecret, email, password string) error {
	var userID int64
	err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE email_normalized=lower($1)`, email).Scan(&userID)
	if err == nil {
		if _, err := db.ExecContext(ctx, `UPDATE users SET role=$2,email_verified_at=COALESCE(email_verified_at,now()),updated_at=now() WHERE id=$1`, userID, admin.RoleSuperAdmin); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO user_roles(user_id,role_key) VALUES($1,$2) ON CONFLICT(user_id,role_key) DO UPDATE SET revoked_at=NULL`, userID, admin.RoleSuperAdmin); err != nil {
			return err
		}
		var mfaEnabled bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_mfa_methods WHERE user_id=$1 AND method='totp' AND enabled_at IS NOT NULL)`, userID).Scan(&mfaEnabled); err != nil {
			return err
		}
		if !mfaEnabled {
			return errors.New("existing seeded administrator has no enabled TOTP method; provision MFA with adminctl")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	secret, err := admin.ProvisionTOTPAdmin(ctx, admin.NewSQLAuthStore(db, admin.DialectPostgres), appSecret, email, password, admin.RoleSuperAdmin)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO user_roles(user_id,role_key) SELECT id,$2 FROM users WHERE email_normalized=lower($1) ON CONFLICT(user_id,role_key) DO UPDATE SET revoked_at=NULL`, email, admin.RoleSuperAdmin); err != nil {
		return err
	}
	issuer := "SokaLens Admin"
	otpauth := "otpauth://totp/" + url.PathEscape(issuer+":"+email) + "?secret=" + url.QueryEscape(secret) + "&issuer=" + url.QueryEscape(issuer)
	path := filepath.Join(".local", "seed-admin-totp-uri.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(otpauth+"\n"), 0600); err != nil {
		return err
	}
	fmt.Printf("Administrator created. Enrol TOTP from the local ignored file %s.\n", path)
	return nil
}

func seedBasic(ctx context.Context, db *sql.DB, email, password string) error {
	repo := accounts.NewSQLRepository(db, accounts.DialectPostgres)
	if account, err := repo.FindAccountByEmail(ctx, strings.ToLower(email)); err == nil {
		if _, err := db.ExecContext(ctx, `UPDATE users SET email_verified_at=COALESCE(email_verified_at,now()),updated_at=now() WHERE id=$1`, account.ID); err != nil {
			return err
		}
		_, err = db.ExecContext(ctx, `INSERT INTO user_roles(user_id,role_key) VALUES($1,'basic') ON CONFLICT(user_id,role_key) DO UPDATE SET revoked_at=NULL`, account.ID)
		return err
	} else if !errors.Is(err, accounts.ErrNotFound) {
		return err
	}
	service := accounts.NewService(repo)
	account, err := service.Register(ctx, email, password)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE users SET email_verified_at=now(),updated_at=now() WHERE id=$1`, account.ID)
	return err
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }

func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(line, "export ")), "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}
