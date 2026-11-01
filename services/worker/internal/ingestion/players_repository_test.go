package ingestion

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCatalogRepositoryUpsertsPlayers(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE teams (id INTEGER PRIMARY KEY AUTOINCREMENT, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL);",
		"CREATE TABLE players (id INTEGER PRIMARY KEY, team_id INTEGER NOT NULL, name TEXT NOT NULL, age INTEGER, number INTEGER, position TEXT, photo_url TEXT, nationality TEXT, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);",
		"INSERT INTO teams (id, slug, name) VALUES (33, 'manchester-united', 'Manchester United');",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	repo := NewCatalogRepository(db)
	playerID, err := repo.UpsertPlayer(context.Background(), PlayerRecord{
		ID:          882,
		TeamID:      33,
		Name:        "P. Cech",
		Age:         42,
		Number:      1,
		Position:    "Goalkeeper",
		PhotoURL:    "https://example.com/882.png",
		Nationality: "Czech Republic",
	})
	if err != nil {
		t.Fatalf("UpsertPlayer returned error: %v", err)
	}
	if playerID != 882 {
		t.Fatalf("player id = %d, want 882", playerID)
	}

	// Re-upserting the same provider id must update in place, not duplicate.
	updatedID, err := repo.UpsertPlayer(context.Background(), PlayerRecord{
		ID:       882,
		TeamID:   33,
		Name:     "Petr Cech",
		Age:      43,
		Position: "Goalkeeper",
	})
	if err != nil {
		t.Fatalf("UpsertPlayer update returned error: %v", err)
	}
	if updatedID != 882 {
		t.Fatalf("updated player id = %d, want 882", updatedID)
	}

	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM players`).Scan(&count); err != nil {
		t.Fatalf("count players: %v", err)
	}
	if count != 1 {
		t.Fatalf("player count = %d, want 1", count)
	}

	var name string
	var age int
	var position sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT name, age, position FROM players WHERE id = 882`).Scan(&name, &age, &position); err != nil {
		t.Fatalf("scan player: %v", err)
	}
	if name != "Petr Cech" || age != 43 {
		t.Fatalf("player = %q age %d, want Petr Cech 43", name, age)
	}
	if !position.Valid || position.String != "Goalkeeper" {
		t.Fatalf("player position = %q, want preserved Goalkeeper", position.String)
	}
}

func TestCatalogRepositoryUpsertsVenues(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE venues (id INTEGER PRIMARY KEY, name TEXT NOT NULL, address TEXT, city TEXT, country TEXT, capacity INTEGER, surface TEXT, image_url TEXT, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP);",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	repo := NewCatalogRepository(db)
	venueID, err := repo.UpsertVenue(context.Background(), VenueRecord{
		ID:       556,
		Name:     "Old Trafford",
		Address:  "Sir Matt Busby Way",
		City:     "Manchester",
		Country:  "England",
		Capacity: 76212,
		Surface:  "grass",
		ImageURL: "https://example.com/556.png",
	})
	if err != nil {
		t.Fatalf("UpsertVenue returned error: %v", err)
	}
	if venueID != 556 {
		t.Fatalf("venue id = %d, want 556", venueID)
	}

	updatedID, err := repo.UpsertVenue(context.Background(), VenueRecord{
		ID:       556,
		Name:     "Old Trafford",
		City:     "Manchester",
		Capacity: 76000,
	})
	if err != nil {
		t.Fatalf("UpsertVenue update returned error: %v", err)
	}
	if updatedID != 556 {
		t.Fatalf("updated venue id = %d, want 556", updatedID)
	}

	var count int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM venues`).Scan(&count); err != nil {
		t.Fatalf("count venues: %v", err)
	}
	if count != 1 {
		t.Fatalf("venue count = %d, want 1", count)
	}

	var capacity int
	var city sql.NullString
	if err := db.QueryRowContext(context.Background(), `SELECT capacity, city FROM venues WHERE id = 556`).Scan(&capacity, &city); err != nil {
		t.Fatalf("scan venue: %v", err)
	}
	if capacity != 76000 {
		t.Fatalf("venue capacity = %d, want 76000", capacity)
	}
	if !city.Valid || city.String != "Manchester" {
		t.Fatalf("venue city = %q, want preserved Manchester", city.String)
	}
}

func TestCatalogRepositoryPlayerVenueValidation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	repo := NewCatalogRepository(db)
	if _, err := repo.UpsertPlayer(context.Background(), PlayerRecord{ID: 0, TeamID: 1, Name: "X"}); err == nil {
		t.Fatal("UpsertPlayer with zero id returned nil error")
	}
	if _, err := repo.UpsertPlayer(context.Background(), PlayerRecord{ID: 1, TeamID: 0, Name: "X"}); err == nil {
		t.Fatal("UpsertPlayer with zero team id returned nil error")
	}
	if _, err := repo.UpsertPlayer(context.Background(), PlayerRecord{ID: 1, TeamID: 1, Name: " "}); err == nil {
		t.Fatal("UpsertPlayer with blank name returned nil error")
	}
	if _, err := repo.UpsertVenue(context.Background(), VenueRecord{ID: 0, Name: "X"}); err == nil {
		t.Fatal("UpsertVenue with zero id returned nil error")
	}
	if _, err := repo.UpsertVenue(context.Background(), VenueRecord{ID: 1, Name: " "}); err == nil {
		t.Fatal("UpsertVenue with blank name returned nil error")
	}
	if _, err := (CatalogRepository{}).UpsertPlayer(context.Background(), PlayerRecord{ID: 1, TeamID: 1, Name: "X"}); err == nil {
		t.Fatal("UpsertPlayer with nil db returned nil error")
	}
	if _, err := (CatalogRepository{}).UpsertVenue(context.Background(), VenueRecord{ID: 1, Name: "X"}); err == nil {
		t.Fatal("UpsertVenue with nil db returned nil error")
	}
}
