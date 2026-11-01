package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/admin"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	email := flag.String("email", "", "admin email")
	role := flag.String("role", string(admin.RoleSuperAdmin), "admin role")
	flag.Parse()
	databaseURL := os.Getenv("DATABASE_URL")
	password := os.Getenv("ADMIN_BOOTSTRAP_PASSWORD")
	secretKey := os.Getenv("APP_SECRET")
	if databaseURL == "" || password == "" || secretKey == "" || *email == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL, APP_SECRET, ADMIN_BOOTSTRAP_PASSWORD, and -email are required")
		os.Exit(2)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	secret, err := admin.ProvisionTOTPAdmin(ctx, admin.NewSQLAuthStore(db, admin.DialectPostgres), secretKey, *email, password, admin.Role(*role))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	issuer := "SokaLens Admin"
	otpauth := "otpauth://totp/" + url.PathEscape(issuer+":"+*email) + "?secret=" + url.QueryEscape(secret) + "&issuer=" + url.QueryEscape(issuer)
	fmt.Println("Admin provisioned. Add this TOTP URI to the administrator's authenticator now; it is shown once:")
	fmt.Println(otpauth)
}
