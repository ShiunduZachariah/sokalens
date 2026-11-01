package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	accadomain "github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/acca"
)

// validCursor is the encoding the list source emits: raw-url-base64 of a
// 32-character md5 sort key.
const validCursor = "MDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDA"

type fakeAccaSource struct{ query accadomain.Query }

func (f *fakeAccaSource) List(_ context.Context, q accadomain.Query) (accadomain.Result, error) {
	f.query = q
	return accadomain.Result{Items: []accadomain.Slip{{ID: "s1", LegCount: 2, Legs: []accadomain.Leg{}}}, Limit: q.Limit}, nil
}
func TestAccaHandlerParsesReadFilters(t *testing.T) {
	f := &fakeAccaSource{}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/accas?tab=btts&period=weekend&legs=5&min_fair_odds=3&max_fair_odds=100&limit=20&seed=abc", nil)
	accaHandler(f, false)(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if f.query.Tab != "btts" || f.query.Period != "weekend" || *f.query.Legs != 5 || *f.query.MinFairOdds != 3 || f.query.Limit != 20 || f.query.Seed != "abc" {
		t.Fatalf("query=%+v", f.query)
	}
}
func TestAccaHandlerRejectsInvalidLegs(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/accas?legs=21", nil)
	accaHandler(&fakeAccaSource{}, false)(rr, req)
	if rr.Code != 400 {
		t.Fatalf("status=%d", rr.Code)
	}
}
func TestFeaturedCapsLimit(t *testing.T) {
	f := &fakeAccaSource{}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/accas/featured?limit=50", nil)
	accaHandler(f, true)(rr, req)
	if rr.Code != 200 || f.query.Limit != 3 {
		t.Fatalf("status=%d limit=%d", rr.Code, f.query.Limit)
	}
}

func TestAccaHandlerRejectsInvalidOddsAndCursorBeforeReading(t *testing.T) {
	for _, query := range []string{"min_fair_odds=NaN", "max_fair_odds=NaN", "min_fair_odds=%2BInf", "max_fair_odds=-Inf", "min_fair_odds=10&max_fair_odds=2", "cursor=!!!", "cursor=YQ"} {
		t.Run(query, func(t *testing.T) {
			f := &fakeAccaSource{}
			rr := httptest.NewRecorder()
			accaHandler(f, false)(rr, httptest.NewRequest(http.MethodGet, "/v1/accas?"+query, nil))
			if rr.Code != 400 || f.query.Limit != 0 {
				t.Fatalf("status=%d query=%+v", rr.Code, f.query)
			}
		})
	}
}

func TestAccaHandlerPassesExactSelection(t *testing.T) {
	f := &fakeAccaSource{}
	rr := httptest.NewRecorder()
	accaHandler(f, false)(rr, httptest.NewRequest(http.MethodGet, "/v1/accas?tab=total_goals&selection=over_2_5", nil))
	if rr.Code != 200 || f.query.Selection != "over_2_5" {
		t.Fatalf("status=%d query=%+v", rr.Code, f.query)
	}
}

// Request-time composition is opt-in. Browse and featured surfaces must keep
// serving stored inventory only, and a cursor page describes stored rows, so
// neither may quietly switch to a freshly composed slip.
func TestGenerateIsOptInAndNeverAppliesToFeaturedOrPaging(t *testing.T) {
	cases := []struct {
		name, query string
		featured    bool
		want        bool
	}{
		{name: "explicit generate", query: "generate=1&legs=20&min_fair_odds=10", want: true},
		{name: "default browse", query: "legs=20", want: false},
		{name: "featured strip", query: "generate=1", featured: true, want: false},
		{name: "cursor page", query: "generate=1&cursor=" + validCursor, want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := &fakeAccaSource{}
			rr := httptest.NewRecorder()
			accaHandler(f, testCase.featured)(rr, httptest.NewRequest(http.MethodGet, "/v1/accas?"+testCase.query, nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if f.query.Generate != testCase.want {
				t.Fatalf("generate=%v, want %v", f.query.Generate, testCase.want)
			}
		})
	}
}
