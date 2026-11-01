package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/acca"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/recommendations"
)

func TestGenerationFollowUpsGateAccaAfterSuccessfulPredictionRun(t *testing.T) {
	source := jobs.Job{ID: 42}
	now := time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC)
	disabled := generationFollowUps(source, now, false)
	if len(disabled) != 1 || disabled[0].JobKey != recommendations.GenerateCandidatesJobKey {
		t.Fatalf("disabled ACCA follow-ups = %+v", disabled)
	}
	enabled := generationFollowUps(source, now, true)
	if len(enabled) != 2 || enabled[0].JobKey != recommendations.GenerateCandidatesJobKey || enabled[1].JobKey != acca.GenerateInventoryJobKey {
		t.Fatalf("enabled ACCA follow-ups = %+v", enabled)
	}
}

func TestWorkerEntrypointUsesPythonPredictionEngineOnly(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(body)
	if strings.Contains(source, "/internal/predictionengine\"") {
		t.Fatalf("worker entrypoint must not import the old Go prediction engine")
	}
	if !strings.Contains(source, "/internal/predictionenginepython\"") {
		t.Fatalf("worker entrypoint must keep the Python prediction engine runner wired")
	}
}

func TestWorkerEntrypointWiresRecurringSchedulerAndHeartbeat(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(body)
	for _, required := range []string{
		`/internal/scheduler"`,
		"scheduler.NewPlanner(",
		"scheduler.Run(",
		"scheduler.WriteHeartbeat(",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("worker entrypoint missing recurring runtime wiring %q", required)
		}
	}
}

func TestWorkerEntrypointRestoresAndEnforcesDailyProviderSpend(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(body)
	for _, required := range []string{
		"loadDailyProviderSpend(",
		"DailyLimit:",
		"cfg.ProviderDailyLimit",
		"InitialDailySpent:",
		"dailyProviderSpend",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("worker entrypoint missing durable daily quota wiring %q", required)
		}
	}
}

func TestWorkerDockerRuntimeHasHealthcheckRestartAndInterpolatedDatabaseURLs(t *testing.T) {
	compose, err := os.ReadFile("../../../../infra/docker/docker-compose.yml")
	if err != nil {
		t.Fatalf("read compose file: %v", err)
	}
	composeSource := string(compose)
	databaseURL := `DATABASE_URL: ${DATABASE_URL_DOCKER:-postgres://sports:sports@postgres:5432/sports_prediction?sslmode=disable}`
	if strings.Count(composeSource, databaseURL) != 2 {
		t.Fatalf("compose explicit Docker DATABASE_URL count = %d, want 2", strings.Count(composeSource, databaseURL))
	}
	if strings.Contains(composeSource, `DATABASE_URL: postgres://${POSTGRES_USER`) {
		t.Fatal("compose embeds raw PostgreSQL credentials in a URI")
	}
	for _, required := range []string{"restart: unless-stopped", `["CMD", "worker", "healthcheck"]`} {
		if !strings.Contains(composeSource, required) {
			t.Fatalf("compose missing worker runtime setting %q", required)
		}
	}

	dockerfile, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read worker Dockerfile: %v", err)
	}
	if !strings.Contains(string(dockerfile), "HEALTHCHECK") {
		t.Fatal("worker Dockerfile missing HEALTHCHECK")
	}
}

func TestDockerBuildContextExcludesEnvironmentSecretsAndPythonBytecode(t *testing.T) {
	body, err := os.ReadFile("../../../../.dockerignore")
	if err != nil {
		t.Fatalf("read .dockerignore: %v", err)
	}
	source := string(body)
	for _, required := range []string{".env", ".env.*", "!.env.example", "**/__pycache__", "**/*.py[cod]"} {
		if !strings.Contains(source, required) {
			t.Fatalf(".dockerignore missing %q", required)
		}
	}
}

func TestCriticalDockerServicesRestartAndAPIReportsReadiness(t *testing.T) {
	compose, err := os.ReadFile("../../../../infra/docker/docker-compose.yml")
	if err != nil {
		t.Fatalf("read compose file: %v", err)
	}
	source := string(compose)
	if got := strings.Count(source, "restart: unless-stopped"); got < 4 {
		t.Fatalf("critical service restart policies = %d, want at least 4", got)
	}
	if !strings.Contains(source, `wget --quiet --output-document=- http://127.0.0.1:8080/readyz`) {
		t.Fatal("API service missing /readyz healthcheck")
	}
	for _, binding := range []string{
		`${POSTGRES_BIND_ADDRESS:-127.0.0.1}:${POSTGRES_LOCAL_PORT:-5433}:5432`,
		`${REDIS_BIND_ADDRESS:-127.0.0.1}:${REDIS_PORT:-6379}:6379`,
		`${API_BIND_ADDRESS:-127.0.0.1}:${API_PORT:-8080}:8080`,
	} {
		if !strings.Contains(source, binding) {
			t.Fatalf("compose missing loopback-safe binding %q", binding)
		}
	}
}

func TestAuxiliaryDockerServicesDefaultToLoopbackBindings(t *testing.T) {
	compose, err := os.ReadFile("../../../../infra/docker/docker-compose.yml")
	if err != nil {
		t.Fatalf("read compose file: %v", err)
	}
	source := string(compose)
	for _, binding := range []string{
		`${TYPESENSE_BIND_ADDRESS:-127.0.0.1}:${TYPESENSE_PORT:-8108}:8108`,
		`${MAILHOG_BIND_ADDRESS:-127.0.0.1}:${MAILHOG_SMTP_PORT:-1025}:1025`,
		`${MAILHOG_BIND_ADDRESS:-127.0.0.1}:${MAILHOG_UI_PORT:-8025}:8025`,
		`${WEB_BIND_ADDRESS:-127.0.0.1}:${WEB_PORT:-3000}:3000`,
		`${ADMIN_BIND_ADDRESS:-127.0.0.1}:${ADMIN_PORT:-3001}:3001`,
		`${UPTIME_BIND_ADDRESS:-127.0.0.1}:${UPTIME_PORT:-3002}:3001`,
	} {
		if !strings.Contains(source, binding) {
			t.Fatalf("compose missing loopback-safe auxiliary binding %q", binding)
		}
	}
}

func TestRunWorkerTasksContinuesAfterOneQueueFails(t *testing.T) {
	firstErr := errors.New("reference queue unavailable")
	secondRan := false
	err := runWorkerTasks(context.Background(), 2,
		func(context.Context) error { return firstErr },
		func(context.Context) error {
			secondRan = true
			return nil
		},
	)
	if !secondRan {
		t.Fatal("later queue did not run after an earlier queue failed")
	}
	if !errors.Is(err, firstErr) {
		t.Fatalf("runWorkerTasks error = %v, want wrapped first error", err)
	}
}

func TestRunWorkerTasksRunsQueuesConcurrently(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	task := func(context.Context) error {
		started <- struct{}{}
		<-release
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- runWorkerTasks(context.Background(), 2, task, task)
	}()

	for count := 0; count < 2; count++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("queue tasks did not overlap")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("runWorkerTasks returned error: %v", err)
	}
}

func TestRunWorkerBurstUsesConfiguredConcurrency(t *testing.T) {
	var calls atomic.Int32
	if err := runWorkerBurst(context.Background(), 4, func(context.Context) error {
		calls.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("runWorkerBurst returned error: %v", err)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("burst calls = %d, want 4", got)
	}
}

func TestRunWorkerTasksHonorsConcurrencyLimit(t *testing.T) {
	var active atomic.Int32
	var peak atomic.Int32
	task := func(context.Context) error {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := peak.Load()
			if current <= observed || peak.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		return nil
	}
	tasks := make([]func(context.Context) error, 8)
	for index := range tasks {
		tasks[index] = task
	}
	if err := runWorkerTasks(context.Background(), 3, tasks...); err != nil {
		t.Fatalf("runWorkerTasks returned error: %v", err)
	}
	if got := peak.Load(); got < 2 || got > 3 {
		t.Fatalf("peak concurrency = %d, want 2..3", got)
	}
}

func TestRunWorkerTasksDoesNotStartAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	if err := runWorkerTasks(ctx, 4, func(context.Context) error {
		calls.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("runWorkerTasks returned error during shutdown: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("tasks started after cancellation = %d, want 0", got)
	}
}

func TestConfigureDatabasePoolAppliesWorkerLimits(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://unused")
	if err != nil {
		t.Fatalf("sql.Open returned error: %v", err)
	}
	defer db.Close()
	configureDatabasePool(db, config.Config{DBMaxOpenConns: 9, DBMaxIdleConns: 3})
	if got := db.Stats().MaxOpenConnections; got != 9 {
		t.Fatalf("MaxOpenConnections = %d, want 9", got)
	}
}
