package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/notifications"
)

func main() {
	if err := loadEnvFile(".env"); err != nil {
		slog.Error("load local environment", "error", err)
		os.Exit(1)
	}
	cfg, err := config.Load(nil)
	if err != nil {
		slog.Error("invalid email configuration", "error", err)
		os.Exit(1)
	}
	if cfg.EmailTestTo == "" {
		slog.Error("EMAIL_TEST_TO is required")
		os.Exit(1)
	}
	var transport notifications.Transport
	switch cfg.EmailProvider {
	case "mailtrap", "smtp":
		transport = notifications.SMTPTransport{Host: cfg.MailtrapHost, Port: cfg.MailtrapPort, From: cfg.EmailFromAddress, FromName: cfg.EmailFromName, Username: cfg.MailtrapUsername, Password: cfg.MailtrapPassword, Encryption: cfg.MailtrapEncryption}
	case "resend":
		from := cfg.ResendFromAddress
		if from == "" {
			from = cfg.EmailFromAddress
		}
		transport = notifications.ResendTransport{APIKey: cfg.ResendAPIKey, From: from, FromName: cfg.EmailFromName}
	case "cloudflare":
		transport = notifications.CloudflareTransport{AccountID: cfg.CloudflareAccountID, APIToken: cfg.CloudflareEmailAPIToken, From: cfg.EmailFromNoreply, FromName: cfg.EmailFromName}
	default:
		slog.Error("EMAIL_PROVIDER must select mailtrap, smtp, resend, or cloudflare for a live test")
		os.Exit(1)
	}
	payload, _ := json.Marshal(notifications.EmailPayload{To: cfg.EmailTestTo, Subject: "SokaLens transactional email test", Text: "Your SokaLens email configuration is working.", HTML: "<div style=\"font-family:Arial,sans-serif\"><h1>SokaLens</h1><p>Your transactional email configuration is working.</p></div>", RequestID: "email-test"})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	delivery, err := transport.Send(ctx, notifications.DeliveryRequest{Message: notifications.Message{ID: 1, Channel: "email", Category: "test", Payload: payload}, IdempotencyKey: fmt.Sprintf("email-test-%d", time.Now().UTC().Unix())})
	if err != nil {
		slog.Error("test email failed", "provider", cfg.EmailProvider, "error", err)
		os.Exit(1)
	}
	slog.Info("test email accepted", "provider", cfg.EmailProvider, "message_id", delivery.ProviderMessageID, "status", delivery.ResponseCode)
}

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
