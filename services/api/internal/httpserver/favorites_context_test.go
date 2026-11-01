package httpserver

import (
	"context"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/accounts"
)

type favoriteMatchSource struct {
	match MatchSummary
}

func (s favoriteMatchSource) ListMatches(context.Context, MatchQuery) ([]MatchSummary, error) {
	return []MatchSummary{s.match}, nil
}
func (s favoriteMatchSource) CountMatches(context.Context, MatchQuery) (int, error) { return 1, nil }
func (s favoriteMatchSource) GetMatch(context.Context, int64) (MatchSummary, error) {
	return s.match, nil
}

type favoriteCatalogSource struct {
	team    TeamDetail
	league  LeagueDetail
	country []MatchSummary
}

func (s favoriteCatalogSource) ListLeagues(context.Context, int) ([]LeagueSummary, error) {
	return nil, nil
}
func (s favoriteCatalogSource) GetLeague(context.Context, string) (LeagueDetail, error) {
	return s.league, nil
}
func (s favoriteCatalogSource) GetTeam(context.Context, string) (TeamDetail, error) {
	return s.team, nil
}
func (s favoriteCatalogSource) GetTeamNextMatch(context.Context, string) (*MatchSummary, error) {
	return s.team.NextMatch, nil
}
func (s favoriteCatalogSource) ListLeagueMatches(context.Context, string, int) ([]MatchSummary, error) {
	return s.league.Upcoming, nil
}
func (s favoriteCatalogSource) ListCountryMatches(context.Context, string, int) ([]MatchSummary, error) {
	return s.country, nil
}

func TestFavoriteContextShowsImmediateTeamAndCountryMatches(t *testing.T) {
	kickoff := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	next := MatchSummary{ID: 42, HomeTeam: "Arsenal", AwayTeam: "Leeds", LeagueName: "Premier League", KickoffAt: kickoff, Status: "scheduled"}
	deps := accountDependencies{catalog: favoriteCatalogSource{
		team:    TeamDetail{NextMatch: &next},
		country: []MatchSummary{next, {ID: 43, HomeTeam: "Chelsea", AwayTeam: "Fulham", KickoffAt: kickoff.Add(time.Hour), Status: "scheduled"}},
	}}

	team, expired := deps.favoriteContextResponse(context.Background(), accounts.Favorite{EntityType: "team", EntityID: "arsenal"}, kickoff.Add(-time.Hour))
	if expired || team["context_label"] != "Next match" || len(team["matches"].([]map[string]any)) != 1 {
		t.Fatalf("team context=%+v expired=%v", team, expired)
	}
	country, expired := deps.favoriteContextResponse(context.Background(), accounts.Favorite{EntityType: "country", EntityID: "GB"}, kickoff.Add(-time.Hour))
	if expired || country["context_label"] != "Next matches" || len(country["matches"].([]map[string]any)) != 2 {
		t.Fatalf("country context=%+v expired=%v", country, expired)
	}
}

func TestSavedMatchResultExpiresSixHoursAfterConfirmedFinish(t *testing.T) {
	kickoff := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	finishedAt := kickoff.Add(2*time.Hour + 8*time.Minute)
	home, away := 2, 1
	match := MatchSummary{ID: 42, HomeTeam: "Arsenal", AwayTeam: "Leeds", KickoffAt: kickoff, FinishedAt: &finishedAt, Status: "finished", HomeScore: &home, AwayScore: &away}
	deps := accountDependencies{matches: favoriteMatchSource{match: match}}
	favorite := accounts.Favorite{EntityType: "match", EntityID: "42"}

	recent, expired := deps.favoriteContextResponse(context.Background(), favorite, finishedAt.Add(5*time.Hour+59*time.Minute))
	if expired || recent["context_label"] != "Recent result" {
		t.Fatalf("recent context=%+v expired=%v", recent, expired)
	}
	_, expired = deps.favoriteContextResponse(context.Background(), favorite, finishedAt.Add(6*time.Hour))
	if !expired {
		t.Fatal("finished saved match did not expire at the retention boundary")
	}
}

func TestLegacySavedMatchUsesConservativeCompletionFallback(t *testing.T) {
	kickoff := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	match := MatchSummary{ID: 42, HomeTeam: "Arsenal", AwayTeam: "Leeds", KickoffAt: kickoff, Status: "finished"}
	deps := accountDependencies{matches: favoriteMatchSource{match: match}}
	favorite := accounts.Favorite{EntityType: "match", EntityID: "42"}

	_, expired := deps.favoriteContextResponse(context.Background(), favorite, kickoff.Add(7*time.Hour+59*time.Minute))
	if expired {
		t.Fatal("legacy saved match expired before conservative fallback boundary")
	}
	_, expired = deps.favoriteContextResponse(context.Background(), favorite, kickoff.Add(8*time.Hour))
	if !expired {
		t.Fatal("legacy saved match did not expire at fallback boundary")
	}
}

func TestPostponedSavedMatchDoesNotExpireWithoutConfirmedFinish(t *testing.T) {
	kickoff := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	match := MatchSummary{ID: 42, HomeTeam: "Arsenal", AwayTeam: "Leeds", KickoffAt: kickoff, Status: "postponed"}
	deps := accountDependencies{matches: favoriteMatchSource{match: match}}
	favorite := accounts.Favorite{EntityType: "match", EntityID: "42"}

	_, expired := deps.favoriteContextResponse(context.Background(), favorite, kickoff.Add(48*time.Hour))
	if expired {
		t.Fatal("postponed saved match expired without a confirmed completion")
	}
}

func TestSavedPredictionExpiresSixHoursAfterConfirmedFinish(t *testing.T) {
	kickoff := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	finishedAt := kickoff.Add(2*time.Hour + 4*time.Minute)
	detail := PredictionDetail{PredictionSummary: PredictionSummary{
		ID: 77, MatchID: 42, MatchTitle: "Arsenal vs Leeds", KickoffAt: kickoff,
		FinishedAt: &finishedAt, MatchStatus: "finished", MarketKey: "1x2", Selection: "home",
	}}
	deps := accountDependencies{predictions: fakePredictionSource{detail: detail}}
	favorite := accounts.Favorite{EntityType: "prediction", EntityID: "77", DisplayName: "Arsenal win"}

	recent, expired := deps.favoriteContextResponse(context.Background(), favorite, finishedAt.Add(5*time.Hour+59*time.Minute))
	if expired || recent["context_label"] != "Recent prediction" {
		t.Fatalf("recent context=%+v expired=%v", recent, expired)
	}
	_, expired = deps.favoriteContextResponse(context.Background(), favorite, finishedAt.Add(6*time.Hour))
	if !expired {
		t.Fatal("saved prediction did not expire at the confirmed completion boundary")
	}
}

func TestPrunedSavedPredictionDisappearsFromFavorites(t *testing.T) {
	deps := accountDependencies{predictions: fakePredictionSource{err: ErrPredictionNotFound}}
	favorite := accounts.Favorite{EntityType: "prediction", EntityID: "77", DisplayName: "Arsenal win"}

	_, expired := deps.favoriteContextResponse(context.Background(), favorite, time.Now())
	if !expired {
		t.Fatal("saved prediction remained visible after its raw prediction was pruned")
	}
}
