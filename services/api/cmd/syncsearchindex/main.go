package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/search"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	typesenseURL := os.Getenv("TYPESENSE_URL")
	typesenseKey := os.Getenv("TYPESENSE_API_KEY")
	if databaseURL == "" || typesenseURL == "" || typesenseKey == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL, TYPESENSE_URL, and TYPESENSE_API_KEY are required")
		os.Exit(2)
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client := search.NewTypesenseClient(typesenseURL, typesenseKey)
	if err := client.EnsureCollection(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	type document struct {
		id        string
		typ       string
		entityID  int64
		name      string
		subtitle  string
		slug      string
		kickoffAt int64
		status    string
		logoURL   string
	}
	var documents []document
	appendRows := func(rows *sql.Rows, typ string) error {
		for rows.Next() {
			var d document
			d.typ = typ
			var slug, subtitle, code sql.NullString
			var logoURL sql.NullString
			var entityID int64
			if typ == "country" {
				if err := rows.Scan(&code, &d.name, &logoURL); err != nil {
					return err
				}
				d.id = "country-" + code.String
				d.subtitle = "Leagues · Teams · Matches"
			} else {
				if err := rows.Scan(&entityID, &d.name, &slug, &subtitle, &logoURL); err != nil {
					return err
				}
				d.entityID = entityID
				d.slug = slug.String
				d.subtitle = subtitle.String
				d.id = fmt.Sprintf("%s-%d", typ, entityID)
			}
			d.logoURL = logoURL.String
			documents = append(documents, d)
		}
		return rows.Err()
	}

	teamRows, err := db.QueryContext(ctx, `SELECT t.id, t.name, t.slug, COALESCE(c.name, t.country_code, ''), COALESCE(t.logo_url, '') FROM teams t LEFT JOIN countries c ON c.code = t.country_code`)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := appendRows(teamRows, "team"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	teamRows.Close()

	leagueRows, err := db.QueryContext(ctx, `SELECT l.id, l.name, l.slug, COALESCE(c.name, l.country_code, ''), COALESCE(l.logo_url, '') FROM leagues l LEFT JOIN countries c ON c.code = l.country_code`)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := appendRows(leagueRows, "league"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	leagueRows.Close()

	countryRows, err := db.QueryContext(ctx, `SELECT code, name, COALESCE(flag_url, '') FROM countries`)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := appendRows(countryRows, "country"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	countryRows.Close()

	matchRows, err := db.QueryContext(ctx, `
SELECT f.id, home.name || ' vs ' || away.name, COALESCE(c.name || ' · ', '') || COALESCE(l.name, 'Competition'), f.kickoff_at, COALESCE(f.status, 'scheduled')
FROM fixtures f
JOIN teams home ON home.id = f.home_team_id
JOIN teams away ON away.id = f.away_team_id
LEFT JOIN leagues l ON l.id = f.league_id
LEFT JOIN countries c ON c.code = l.country_code
WHERE f.kickoff_at > now() - interval '30 days'`)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for matchRows.Next() {
		var d document
		d.typ = "match"
		var kickoff time.Time
		var subtitle sql.NullString
		if err := matchRows.Scan(&d.entityID, &d.name, &subtitle, &kickoff, &d.status); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		d.id = fmt.Sprintf("match-%d", d.entityID)
		d.subtitle = subtitle.String
		d.kickoffAt = kickoff.Unix()
		documents = append(documents, d)
	}
	matchRows.Close()

	playerRows, err := db.QueryContext(ctx, `SELECT id, name, 'Player', COALESCE(photo_url, '') FROM players`)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for playerRows.Next() {
		var d document
		d.typ = "player"
		var subtitle sql.NullString
		var logoURL sql.NullString
		if err := playerRows.Scan(&d.entityID, &d.name, &subtitle, &logoURL); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		d.id = fmt.Sprintf("player-%d", d.entityID)
		d.subtitle = subtitle.String
		d.logoURL = logoURL.String
		documents = append(documents, d)
	}
	playerRows.Close()

	batch := make([]search.TypesenseDocument, 0, 1000)
	indexed := 0
	for _, d := range documents {
		batch = append(batch, search.TypesenseDocument{
			ID: d.id, Type: d.typ, EntityID: d.entityID, Name: d.name,
			Subtitle: d.subtitle, Slug: d.slug, KickoffAt: d.kickoffAt, Status: d.status,
			LogoURL: d.logoURL,
		})
		if len(batch) >= 1000 {
			if err := client.Index(ctx, batch); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			indexed += len(batch)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := client.Index(ctx, batch); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		indexed += len(batch)
	}
	fmt.Printf("typesense indexed=%d (teams+leagues+players+countries+matches)\n", indexed)
}
