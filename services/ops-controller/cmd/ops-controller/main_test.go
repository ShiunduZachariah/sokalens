package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/config"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestOpenIncidentSinkStaysDisabledWithoutExplicitEnablement(t *testing.T) {
	sink, db, err := openIncidentSink(context.Background(), config.Config{
		DatabaseURL: "not-even-a-database-url",
	})
	if err != nil {
		t.Fatal(err)
	}
	if db != nil {
		t.Fatal("disabled incident persistence opened a database")
	}
	if err := sink.Accept(context.Background(), alerts.Accepted{}); !errors.Is(err, alerts.ErrSinkDisabled) {
		t.Fatalf("disabled sink error = %v, want ErrSinkDisabled", err)
	}
}

func TestConfigureDatabasePoolAppliesEveryBound(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://unused")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.Config{
		DBMaxOpenConns:    17,
		DBMaxIdleConns:    3,
		DBConnMaxLifetime: 11 * time.Minute,
		DBConnMaxIdleTime: 2 * time.Minute,
	}
	configureDatabasePool(db, cfg)
	if got := db.Stats().MaxOpenConnections; got != 17 {
		t.Fatalf("MaxOpenConnections = %d, want 17", got)
	}
}

func TestOpenIncidentSinkDoesNotExposeDatabaseSecret(t *testing.T) {
	const password = "do-not-log-this-password"
	_, db, err := openIncidentSink(context.Background(), config.Config{
		IncidentStoreEnabled:      true,
		DatabaseURL:               "postgres://ops:" + password + "@%zz.invalid/sokalens",
		DBMaxOpenConns:            1,
		DBMaxIdleConns:            1,
		DBConnMaxLifetime:         time.Minute,
		DBConnMaxIdleTime:         time.Minute,
		DBPingTimeout:             10 * time.Millisecond,
		IncidentCorrelationWindow: time.Minute,
	})
	if db != nil {
		db.Close()
	}
	if err == nil {
		t.Fatal("unsafe database URL unexpectedly opened")
	}
	if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("database error exposed connection material: %v", err)
	}
}

func TestPingWithTimeoutIsBounded(t *testing.T) {
	p := blockingPinger{started: make(chan struct{})}
	started := time.Now()
	err := pingWithTimeout(context.Background(), p, 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("ping took %s, expected bounded return", elapsed)
	}
}

type blockingPinger struct{ started chan struct{} }

func (p blockingPinger) PingContext(ctx context.Context) error {
	close(p.started)
	<-ctx.Done()
	return ctx.Err()
}
