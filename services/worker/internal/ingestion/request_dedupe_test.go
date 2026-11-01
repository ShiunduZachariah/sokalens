package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRequestFingerprintIsStableAndDistinct(t *testing.T) {
	paramsA := url.Values{}
	paramsA.Set("date", "2026-05-01")
	paramsA.Set("timezone", "UTC")
	paramsB := url.Values{}
	paramsB.Set("date", "2026-05-01")
	paramsB.Set("timezone", "Europe/London")
	fpA1 := RequestFingerprint("api-football", "fixtures", paramsA)
	fpA2 := RequestFingerprint("api-football", "fixtures", paramsA)
	fpB := RequestFingerprint("api-football", "fixtures", paramsB)
	if fpA1 != fpA2 {
		t.Fatalf("fingerprint not stable: %q vs %q", fpA1, fpA2)
	}
	if fpA1 == fpB {
		t.Fatalf("fingerprints collide for distinct params: %q", fpA1)
	}
	if !strings.Contains(fpA1, "api-football:fixtures:") {
		t.Fatalf("fingerprint %q missing provider:endpoint prefix", fpA1)
	}
}

func TestRequestDedupeClaimAndRelease(t *testing.T) {
	shim := &claimShimDB{}
	dedupe := NewRequestDedupe(shim, 2*time.Minute)

	// Claim succeeds when the row is inserted/owned.
	shim.owner = "worker-a"
	proceed, err := dedupe.Claim(context.Background(), "fp-1", "worker-a")
	if err != nil {
		t.Fatalf("Claim returned error: %v", err)
	}
	if !proceed {
		t.Fatal("Claim proceed = false, want true when row is owned")
	}
	if !strings.Contains(shim.claimQuery, "ON CONFLICT (fingerprint) DO UPDATE") ||
		!strings.Contains(shim.claimQuery, "expires_at < now()") {
		t.Fatalf("claim query = %s, want lease upsert with expiry takeover", shim.claimQuery)
	}

	// Another worker holds an active lease → defer.
	shim.owner = ""
	proceed, err = dedupe.Claim(context.Background(), "fp-1", "worker-b")
	if err != nil {
		t.Fatalf("Claim returned error: %v", err)
	}
	if proceed {
		t.Fatal("Claim proceed = true, want false when another worker holds the lease")
	}

	// Release frees the lease.
	if err := dedupe.Release(context.Background(), "fp-1", "worker-a"); err != nil {
		t.Fatalf("Release returned error: %v", err)
	}
	if !strings.Contains(shim.releaseQuery, "DELETE FROM provider_request_dedupe") ||
		!strings.Contains(shim.releaseQuery, "worker_id = $2") {
		t.Fatalf("release query = %s, want owned deletion", shim.releaseQuery)
	}
}

func TestRequestDedupeClaimDBFailureIsAnError(t *testing.T) {
	shim := &claimShimDB{err: errors.New("connection refused")}
	dedupe := NewRequestDedupe(shim, time.Minute)
	if _, err := dedupe.Claim(context.Background(), "fp-2", "worker-a"); err == nil {
		t.Fatal("Claim error = nil, want DB failure surfaced")
	}
}

func TestRequestDedupeNilStoreIsNoop(t *testing.T) {
	dedupe := NewRequestDedupe(nil, time.Minute)
	proceed, err := dedupe.Claim(context.Background(), "fp-3", "worker-a")
	if err != nil || !proceed {
		t.Fatalf("nil store Claim = (%v, %v), want (true, nil)", proceed, err)
	}
	if err := dedupe.Release(context.Background(), "fp-3", "worker-a"); err != nil {
		t.Fatalf("nil store Release returned error: %v", err)
	}
}

// claimShimDB implements the two methods RequestDedupe needs.
type claimShimDB struct {
	owner        string
	err          error
	claimQuery   string
	releaseQuery string
}

func (s *claimShimDB) QueryRowContext(_ context.Context, query string, _ ...any) dedupeRowScanner {
	s.claimQuery = query
	if s.err != nil {
		return errRow{err: s.err}
	}
	if s.owner == "" {
		return errRow{err: sql.ErrNoRows}
	}
	return valueRow{value: s.owner}
}

func (s *claimShimDB) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	s.releaseQuery = query
	if s.err != nil {
		return nil, s.err
	}
	return noopResult{}, nil
}

type valueRow struct{ value string }

func (r valueRow) Scan(dest ...any) error {
	*(dest[0].(*string)) = r.value
	return nil
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

type noopResult struct{}

func (noopResult) LastInsertId() (int64, error) { return 0, nil }
func (noopResult) RowsAffected() (int64, error) { return 0, nil }
