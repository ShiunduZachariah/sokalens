package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The directory must serve the whole catalogue, not a page of it.
//
// Three independent caps at 100 used to agree with each other -- the SQL, this
// handler (which hardcoded 100 and ignored ?limit= entirely), and the web
// client's own Math.min. The sitemap meanwhile declared every league, so ~790
// competitions were crawlable with no internal link pointing at them. Nothing
// failed; the page just quietly stopped at 100.
type countingLeagueSource struct {
	requested int
	leagues   []LeagueSummary
}

func (s *countingLeagueSource) ListLeagues(_ context.Context, limit int) ([]LeagueSummary, error) {
	s.requested = limit
	return s.leagues, nil
}
func (s *countingLeagueSource) GetLeague(context.Context, string) (LeagueDetail, error) {
	return LeagueDetail{}, nil
}
func (s *countingLeagueSource) GetTeam(context.Context, string) (TeamDetail, error) {
	return TeamDetail{}, nil
}
func (s *countingLeagueSource) GetTeamNextMatch(context.Context, string) (*MatchSummary, error) {
	return nil, nil
}
func (s *countingLeagueSource) ListLeagueMatches(context.Context, string, int) ([]MatchSummary, error) {
	return nil, nil
}
func (s *countingLeagueSource) ListCountryMatches(context.Context, string, int) ([]MatchSummary, error) {
	return nil, nil
}

func TestLeagueDirectoryAsksForTheWholeCatalogue(t *testing.T) {
	source := &countingLeagueSource{leagues: []LeagueSummary{{ID: 1, Slug: "a", Name: "A"}}}

	recorder := httptest.NewRecorder()
	leaguesHandler(source).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/leagues", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	// 0 is the source's "no ceiling" sentinel. Any positive number here is a cap
	// by another name, and the next catalogue growth silently crosses it.
	if source.requested != 0 {
		t.Fatalf("handler requested limit %d; want 0, meaning the complete catalogue", source.requested)
	}
}

func TestLeagueDirectoryReportsItsTotal(t *testing.T) {
	source := &countingLeagueSource{leagues: []LeagueSummary{
		{ID: 1, Slug: "a", Name: "A"},
		{ID: 2, Slug: "b", Name: "B"},
		{ID: 3, Slug: "c", Name: "C"},
	}}

	recorder := httptest.NewRecorder()
	leaguesHandler(source).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/leagues", nil))

	var body struct {
		Leagues []map[string]any `json:"leagues"`
		Total   int              `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 3 || len(body.Leagues) != 3 {
		t.Fatalf("total = %d with %d leagues; want both 3", body.Total, len(body.Leagues))
	}
}
