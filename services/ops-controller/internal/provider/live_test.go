package provider

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestLiveReadsAgainstPostgres is an explicit, manual smoke test. The unit
// tests above cover the projection logic through the fake Queryer; this one
// exists to prove the SQL itself parses, the column order matches the scan
// order, and every driver type converts — none of which a fake can tell you.
//
// It is skipped unless OPS_PROVIDER_LIVE=1, and it takes its connection string
// from DATABASE_URL. No credential is written here: this file is committed, and
// a connection string in a tracked file is a leaked one.
//
// It reads only. Nothing in this package can write.
func TestLiveReadsAgainstPostgres(t *testing.T) {
	if os.Getenv("OPS_PROVIDER_LIVE") != "1" {
		t.Skip("set OPS_PROVIDER_LIVE=1 and DATABASE_URL for the live read smoke test")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Fatal("DATABASE_URL is required for the live read smoke test")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	queries, err := NewQueries(SQLQueryer{DB: db}, nil)
	if err != nil {
		t.Fatal(err)
	}

	usage, err := queries.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	for _, p := range usage.Providers {
		if p.Provider == "" {
			t.Fatalf("provider key is empty: %+v", p)
		}
		if p.DailyKnown && p.DailyUsed != p.DailyLimit-p.DailyRemaining && p.DailyRemaining <= p.DailyLimit {
			t.Fatalf("used must be limit-remaining: %+v", p)
		}
		if !p.DailyKnown && (p.DailyPercent != 0 || p.DailyUsed != 0) {
			t.Fatalf("an unknown ceiling must leave usage undefined: %+v", p)
		}
		t.Logf("%s daily=%d/%d known=%v minute=%d/%d throttled=%v age=%s",
			p.Provider, p.DailyUsed, p.DailyLimit, p.DailyKnown,
			p.MinuteUsed, p.MinuteLimit, p.Throttled, p.Age)
	}

	today, err := queries.Today(ctx)
	if err != nil {
		t.Fatalf("Today: %v", err)
	}
	for _, p := range today.Providers {
		if p.Requests < p.Errors {
			t.Fatalf("errors cannot exceed requests: %+v", p)
		}
		t.Logf("%s req=%d err=%d cost=%d categories=%v",
			p.Provider, p.Requests, p.Errors, p.QuotaCost, p.Categories)
	}
}
