package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/db/migrations"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/footballdata"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	mode := flag.String("mode", "up", "one of: up, down, seed, verify, verify-engine-contract, compact-predictions, import-football-data")
	migrationsDir := flag.String("migrations", "infra/db/migrations", "migration directory")
	seedPath := flag.String("seed", "infra/db/seeds/001_foundation_seed.sql", "seed SQL file")
	footballDataPath := flag.String("football-data-path", "Model-Training-Data", "Football-Data root, raw/main directory, or CSV file")
	footballDataSeason := flag.String("football-data-season", "", "optional Football-Data season filter such as 2025-2026")
	footballDataReplace := flag.Bool("football-data-replace", false, "replace existing Football-Data imported fixtures for the selected season")
	envFile := flag.String("env-file", ".env", "optional environment file loaded without overriding process variables")
	timeout := flag.Duration("timeout", 30*time.Second, "operation timeout")
	flag.Parse()
	if err := loadEnvFile(*envFile); err != nil {
		slog.Error("load environment file", "path", *envFile, "error", err)
		os.Exit(1)
	}

	cfg, err := config.Load(nil)
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	switch *mode {
	case "up":
		err = migrations.Runner{DB: db, Directory: *migrationsDir}.Up(ctx)
	case "down":
		err = migrations.Runner{DB: db, Directory: *migrationsDir}.Down(ctx)
	case "seed":
		err = migrations.Seed(ctx, db, *seedPath)
	case "verify":
		err = migrations.VerifyFoundationSeed(ctx, db)
	case "verify-engine-contract":
		err = migrations.VerifyPredictionEngineContract(ctx, db)
	case "compact-predictions":
		err = migrations.CompactPredictionStorage(ctx, db)
	case "import-football-data":
		var report footballdata.Report
		report, err = footballdata.Import(ctx, db, footballdata.Options{
			Path:            *footballDataPath,
			Season:          *footballDataSeason,
			ReplaceExisting: *footballDataReplace,
		})
		if err == nil {
			slog.Info(
				"football-data import complete",
				"parsed_files", report.ParsedFiles,
				"imported_matches", report.ImportedMatches,
				"skipped_rows", report.SkippedRows,
				"row_errors", report.RowErrors,
				"failed_files", len(report.FailedFiles),
			)
		}
	default:
		slog.Error("unsupported dbtool mode", "mode", *mode)
		os.Exit(1)
	}
	if err != nil {
		slog.Error("dbtool failed", "mode", *mode, "error", err)
		os.Exit(1)
	}
}

func loadEnvFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
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
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}
