package scheduler

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/quota"
)

// The 2026-09-21 shape, in arithmetic. A restart left 4,076 backfill jobs
// queued; every per-cycle cap was respected and the day spent about 74,000
// requests. Against a 7,500 package that is the whole thing before lunch, so
// the bound has to be per day and it has to count what is already committed.
func TestBackfillDayCeilingCountsWhatIsAlreadyQueued(t *testing.T) {
	cases := []struct {
		name            string
		enqueuedToday   int
		maxPerDay       int
		wantSlotsAtMost int
	}{
		{"fresh day", 0, 1200, 600},
		{"half spent", 300, 1200, 300},
		{"ceiling reached", 600, 1200, 0},
		{"the incident: a restart left thousands queued", 4076, 1200, 0},
		{"ceiling disabled", 4076, 0, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := backfillSlotsFromCeiling(tc.enqueuedToday, tc.maxPerDay)
			if got != tc.wantSlotsAtMost {
				t.Fatalf("slots = %d, want %d", got, tc.wantSlotsAtMost)
			}
		})
	}
}

// With no database to count against, the ceiling cannot be evaluated at all --
// and backfill does not run either, because picking candidates needs the same
// database. Nothing to protect, nothing to protect it from.
func TestBackfillDayCeilingIsInapplicableWithoutADatabase(t *testing.T) {
	opts := QuotaOptions{BackfillMaxRequestsPerDay: 1200}
	if got := backfillSlotsLeftToday(context.Background(), opts, time.Now(), discardLogger()); got != -1 {
		t.Fatalf("slots = %d; with no DB there is no ceiling to apply", got)
	}
}

// A configured ceiling with a database that cannot answer must pause backfill,
// not proceed.
//
// This is the case worth testing and the easiest one to fake passing. A nil
// database also produces a plan with no backfill in it, for an unrelated
// reason -- candidate selection needs the same connection -- so a test that
// merely asserts "no backfill was scheduled" passes whether or not the ceiling
// exists. Hence a database that is present and fails: the only thing that can
// stop backfill here is the ceiling failing closed.
func TestBackfillDayCeilingFailsClosedWhenTheQueryFails(t *testing.T) {
	db := sql.OpenDB(failingConnector{})
	defer db.Close()

	opts := QuotaOptions{
		DB:                        db,
		BackfillMaxRequestsPerDay: 1200,
		MaxBackfillPerCycle:       25,
		CycleInterval:             30 * time.Second,
	}
	if got := backfillSlotsLeftToday(context.Background(), opts, time.Now(), discardLogger()); got != 0 {
		t.Fatalf("slots = %d; an unanswerable ceiling must pause backfill", got)
	}

	decision := decisionWith(quota.P0, quota.P1, quota.P2, quota.P3, quota.P4)
	decision.BallastPerMinute = 400
	decision.MinutesLeftToday = 600
	plan := ApplyQuotaPlan(context.Background(), nil, decision, opts, time.Now(), discardLogger())
	for _, scheduled := range plan {
		if scheduled.Schedule == "backfill" || scheduled.Schedule == "statistics-backfill" {
			t.Fatal("backfill was scheduled while the day ceiling could not be checked")
		}
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// failingConnector is a database/sql driver that refuses to connect, so every
// query through it returns an error.
type failingConnector struct{}

func (failingConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("database unavailable")
}

func (failingConnector) Driver() driver.Driver { return failingDriver{} }

type failingDriver struct{}

func (failingDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("database unavailable")
}
