package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	apiKey := os.Getenv("API_FOOTBALL_KEY")
	baseURL := os.Getenv("API_FOOTBALL_BASE_URL")
	if baseURL == "" {
		baseURL = "https://v3.football.api-sports.io"
	}
	maxPredictions := 20
	if raw := os.Getenv("MAX_PREDICTIONS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 100 {
			maxPredictions = parsed
		}
	}
	if databaseURL == "" || apiKey == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL and API_FOOTBALL_KEY are required")
		os.Exit(2)
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rows, err := db.QueryContext(ctx, `
SELECT pem.provider_entity_id, f.id
FROM provider_entity_maps AS pem
JOIN fixtures AS f ON f.id = pem.canonical_entity_id::bigint
WHERE pem.provider = 'api-football'
  AND pem.entity_type = 'match'
  AND pem.retired_at IS NULL
  AND f.kickoff_at > now() - interval '2 hours'
  AND f.kickoff_at < now() + interval '7 days'
  AND NOT EXISTS (
    SELECT 1 FROM engine_predictions AS ep
    WHERE ep.fixture_id = f.id AND ep.superseded_at IS NULL
  )
ORDER BY f.kickoff_at ASC
LIMIT $1`, maxPredictions)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer rows.Close()

	client := apifootball.NewClient(apifootball.Config{
		BaseURL:   baseURL,
		APIKey:    apiKey,
		HTTPClient: &http.Client{Timeout: 20 * time.Second},
	})
	stored := 0
	fetched := 0
	for rows.Next() {
		var providerFixtureID string
		var canonicalFixtureID int64
		if err := rows.Scan(&providerFixtureID, &canonicalFixtureID); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		params := url.Values{}
		params.Set("fixture", providerFixtureID)
		resp, err := client.Get(ctx, "predictions", params)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fixture %s: %v\n", providerFixtureID, err)
			continue
		}
		fetched++
		items, err := apifootball.ParsePredictions(resp.Body)
		if err != nil || len(items) == 0 {
			continue
		}
		item := items[0]
		home, _ := apifootball.ParsePercent(item.Predictions.Percent.Home)
		draw, _ := apifootball.ParsePercent(item.Predictions.Percent.Draw)
		away, _ := apifootball.ParsePercent(item.Predictions.Percent.Away)
		if _, err := db.ExecContext(ctx, `
INSERT INTO api_predictions (
  fixture_id, predicted_winner, win_or_draw, under_over,
  percent_home, percent_draw, percent_away, advice
) VALUES ($1, $2, $3, NULLIF($4,''), $5, $6, $7, NULLIF($8,''))
ON CONFLICT (fixture_id) DO UPDATE SET
  predicted_winner = EXCLUDED.predicted_winner,
  win_or_draw = EXCLUDED.win_or_draw,
  under_over = EXCLUDED.under_over,
  percent_home = EXCLUDED.percent_home,
  percent_draw = EXCLUDED.percent_draw,
  percent_away = EXCLUDED.percent_away,
  advice = EXCLUDED.advice,
  captured_at = now()`,
			canonicalFixtureID, item.Predictions.Winner.Name, item.Predictions.WinOrDraw, item.Predictions.UnderOver,
			home, draw, away, item.Predictions.Advice); err != nil {
			fmt.Fprintf(os.Stderr, "store fixture %s: %v\n", providerFixtureID, err)
			continue
		}
		stored++
	}
	fmt.Printf("provider predictions fetched=%d stored=%d\n", fetched, stored)
}
