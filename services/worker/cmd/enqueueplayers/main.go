package main

import (
  "context"
  "database/sql"
  "encoding/json"
  "flag"
  "fmt"
  "log/slog"
  "os"
  "time"

  "github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/config"
  "github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
  "github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
  _ "github.com/jackc/pgx/v5/stdlib"
)

type playersPayload struct {
  TeamID         int64  `json:"team_id"`
  ProviderTeamID string `json:"provider_team_id"`
  Season         int    `json:"season"`
}

type venuesPayload struct {
  Country string `json:"country"`
}

// countryNamesForVenues expands GB (which API-Football does not use) into its
// constituent football countries so the venues?country call succeeds.
func countryNamesForVenues(name string) []string {
  if name == "United Kingdom" || name == "GB" {
    return []string{"England", "Scotland", "Wales", "Northern-Ireland"}
  }
  return []string{name}
}

func main() {
  maxTeams := flag.Int("max-teams", 40, "max players jobs to queue")
  season := flag.Int("season", 2026, "season year for players fetch")
  flag.Parse()

  cfg, err := config.Load(nil)
  if err != nil { slog.Error("load config", "error", err); os.Exit(1) }
  db, err := sql.Open("pgx", cfg.DatabaseURL)
  if err != nil { slog.Error("open db", "error", err); os.Exit(1) }
  defer db.Close()

  ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
  defer cancel()
  repo := jobs.NewRepository(db)
  now := time.Now().UTC()

  venuesQueued := 0
  countryRows, err := db.QueryContext(ctx, "SELECT DISTINCT btrim(COALESCE(metadata->>'country','')) FROM provider_entity_maps WHERE provider = 'api-football' AND entity_type = 'team' AND btrim(COALESCE(metadata->>'country','')) <> '' ORDER BY 1")
  if err != nil { slog.Error("list countries", "error", err); os.Exit(1) }
  for countryRows.Next() {
    var country string
    if err := countryRows.Scan(&country); err != nil { slog.Error("scan country", "error", err); os.Exit(1) }
    payload, err := json.Marshal(venuesPayload{Country: country})
    if err != nil { slog.Error("marshal venues payload", "error", err); os.Exit(1) }
    job := jobs.NewJob{JobKey: ingestion.VenuesJobKey(), Queue: ingestion.DefaultProviderQueue, Priority: 20, Payload: payload, IdempotencyKey: fmt.Sprintf("enqueue:venues:country:%s:%s", country, now.Format("20060102")), DeduplicateCompleted: true, RunAfter: now}
    if _, err := repo.Enqueue(ctx, job); err != nil { slog.Error("enqueue venues", "error", err); os.Exit(1) }
    venuesQueued++
  }
  countryRows.Close()
  if err := countryRows.Err(); err != nil { slog.Error("iterate countries", "error", err); os.Exit(1) }

  rows, err := db.QueryContext(ctx, "SELECT t.id, pem.provider_entity_id FROM teams t JOIN provider_entity_maps pem ON pem.provider = 'api-football' AND pem.entity_type = 'team' AND pem.canonical_entity_type = 'team' AND pem.canonical_entity_id = t.id::text AND pem.retired_at IS NULL WHERE EXISTS (SELECT 1 FROM fixtures f WHERE (f.home_team_id = t.id OR f.away_team_id = t.id) AND f.kickoff_at >= now() - interval '30 days') ORDER BY (SELECT count(*) FROM fixtures f2 WHERE (f2.home_team_id = t.id OR f2.away_team_id = t.id) AND f2.kickoff_at >= now() - interval '30 days') DESC, t.id ASC LIMIT $1", *maxTeams)
  if err != nil { slog.Error("list teams", "error", err); os.Exit(1) }
  defer rows.Close()
  queued := 0
  for rows.Next() {
    var teamID int64
    var providerID string
    if err := rows.Scan(&teamID, &providerID); err != nil { slog.Error("scan team", "error", err); os.Exit(1) }
    payload, err := json.Marshal(playersPayload{TeamID: teamID, ProviderTeamID: providerID, Season: *season})
    if err != nil { slog.Error("marshal payload", "error", err); os.Exit(1) }
    job := jobs.NewJob{JobKey: ingestion.PlayersJobKey(), Queue: ingestion.DefaultProviderQueue, Priority: 20, Payload: payload, IdempotencyKey: fmt.Sprintf("enqueue:players:%d:%d", teamID, *season), DeduplicateCompleted: true, RunAfter: now}
    if _, err := repo.Enqueue(ctx, job); err != nil { slog.Error("enqueue players", "error", err); os.Exit(1) }
    queued++
  }
  fmt.Printf("queued venues=%d players=%d season=%d\n", venuesQueued, queued, *season)
}