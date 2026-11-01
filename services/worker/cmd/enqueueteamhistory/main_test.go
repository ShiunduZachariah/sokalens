package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

func TestProviderTeamIDFromSlug(t *testing.T) {
	tests := []struct {
		slug string
		want int
		ok   bool
	}{
		{slug: "team-123-arsenal", want: 123, ok: true},
		{slug: "team-987", want: 987, ok: true},
		{slug: "club-123-arsenal", ok: false},
		{slug: "team-zero-arsenal", ok: false},
		{slug: "team-0-arsenal", ok: false},
	}

	for _, tt := range tests {
		got, ok := providerTeamIDFromSlug(tt.slug)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("providerTeamIDFromSlug(%q) = %d/%v, want %d/%v", tt.slug, got, ok, tt.want, tt.ok)
		}
	}
}

func TestTeamHistoryJobUsesStablePayloadAndIdempotencyKey(t *testing.T) {
	now := time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC)
	job := newTeamHistoryJob(123, 15, now)

	if job.JobKey != ingestion.FixturesJobKey() {
		t.Fatalf("job key = %q, want fixtures job key", job.JobKey)
	}
	if job.Queue != providerFixturesQueue {
		t.Fatalf("queue = %q, want %q", job.Queue, providerFixturesQueue)
	}
	if job.Priority != teamHistoryPriority {
		t.Fatalf("priority = %d, want %d", job.Priority, teamHistoryPriority)
	}
	if string(job.Payload) != `{"endpoint":"fixtures","team_id":123,"last":15}` {
		t.Fatalf("payload = %s", job.Payload)
	}
	wantKey := ingestion.ProviderIdempotencyKey(apifootball.ProviderName, "fixtures", `{"last":15,"team":123}`)
	if job.IdempotencyKey != wantKey {
		t.Fatalf("idempotency key = %q, want %q", job.IdempotencyKey, wantKey)
	}
	if !job.RunAfter.Equal(now) {
		t.Fatalf("run after = %s, want %s", job.RunAfter, now)
	}
}

func TestBuildTeamHistoryJobsDedupesSkipsInvalidSlugsAndCaps(t *testing.T) {
	now := time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC)
	candidates := []candidateTeam{
		{CanonicalTeamID: 1, Slug: "team-101-a"},
		{CanonicalTeamID: 2, Slug: "team-102-b"},
		{CanonicalTeamID: 3, Slug: "bad-103-c"},
		{CanonicalTeamID: 4, Slug: "team-101-a-duplicate"},
		{CanonicalTeamID: 5, Slug: "team-104-d"},
	}

	queued, skipped := buildTeamHistoryJobs(candidates, 15, 2, now)

	if len(queued) != 2 {
		t.Fatalf("queued jobs = %d, want 2", len(queued))
	}
	if skipped != 2 {
		t.Fatalf("skipped = %d, want 2", skipped)
	}
	payloads := string(queued[0].Payload) + "\n" + string(queued[1].Payload)
	if !strings.Contains(payloads, `"team_id":101`) || !strings.Contains(payloads, `"team_id":102`) {
		t.Fatalf("payloads = %s, want first two valid unique provider teams", payloads)
	}
}
