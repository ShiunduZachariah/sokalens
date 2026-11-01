package emaillinks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type linkCases struct {
	Slugs []struct {
		Input    string `json:"input"`
		Expected string `json:"expected"`
	} `json:"slugs"`
	Fixtures []struct {
		FixtureID  int64  `json:"fixtureId"`
		LeagueName string `json:"leagueName"`
		HomeTeam   string `json:"homeTeam"`
		AwayTeam   string `json:"awayTeam"`
		Expected   string `json:"expected"`
	} `json:"fixtures"`
	Leagues []struct {
		LeagueName string `json:"leagueName"`
		Expected   string `json:"expected"`
	} `json:"leagues"`
	RetiredPrefixes []string `json:"retiredPrefixes"`
}

// loadCases reads the table shared with apps/web. The same file is asserted
// from JavaScript, so a change to either implementation that is not a change
// to both fails here or there.
func loadCases(t *testing.T) linkCases {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "packages", "contracts", "seo", "email-link-cases.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shared link cases: %v", err)
	}
	var cases linkCases
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("decode shared link cases: %v", err)
	}
	if len(cases.Slugs) == 0 || len(cases.Fixtures) == 0 {
		t.Fatal("shared link cases are empty")
	}
	return cases
}

func TestSlugMatchesTheWebImplementation(t *testing.T) {
	for _, testCase := range loadCases(t).Slugs {
		if got := Slug(testCase.Input); got != testCase.Expected {
			t.Errorf("Slug(%q) = %q, want %q", testCase.Input, got, testCase.Expected)
		}
	}
}

func TestFixturePathMatchesTheWebImplementation(t *testing.T) {
	for _, testCase := range loadCases(t).Fixtures {
		got := FixturePath(testCase.FixtureID, testCase.LeagueName, testCase.HomeTeam, testCase.AwayTeam)
		if got != testCase.Expected {
			t.Errorf("FixturePath(%d, %q, %q, %q) = %q, want %q", testCase.FixtureID, testCase.LeagueName, testCase.HomeTeam, testCase.AwayTeam, got, testCase.Expected)
		}
	}
}

func TestLeaguePathUsesTheCanonicalTree(t *testing.T) {
	for _, testCase := range loadCases(t).Leagues {
		if got := LeaguePath(testCase.LeagueName); got != testCase.Expected {
			t.Errorf("LeaguePath(%q) = %q, want %q", testCase.LeagueName, got, testCase.Expected)
		}
	}
}

// No builder can produce a URL on a retired tree. `/matches/{id}` and
// `/leagues/{slug}` answer with permanent redirects; an emailed link to either
// costs a hop and returns no index value, and there is no builder here that
// could accidentally emit one.
func TestNoBuilderEmitsARetiredURLTree(t *testing.T) {
	cases := loadCases(t)
	produced := []string{
		FixturePath(1234567, "Premier League", "Arsenal", "Chelsea"),
		FixturePath(0, "", "", ""),
		LeaguePath("Premier League"),
		LeaguePath(""),
		MarketHubPath("btts"),
		MarketHubPath(""),
	}
	for _, path := range produced {
		for _, prefix := range cases.RetiredPrefixes {
			if strings.HasPrefix(path, prefix) {
				t.Errorf("%q is on the retired tree %q", path, prefix)
			}
		}
	}
}

// Account pages are noindex and excluded from every sitemap, so they are the
// one family of email links that is allowed off the canonical prediction tree.
func TestAccountPathsEscapeTokensAndRejectUnknownPages(t *testing.T) {
	if got := AccountPath("password_reset", "a b&c"); got != "/reset-password?token=a+b%26c" {
		t.Errorf("AccountPath password_reset = %q", got)
	}
	if got := AccountPath("verify_email", ""); got != "/verify-email" {
		t.Errorf("AccountPath verify_email = %q", got)
	}
	if got := AccountPath("invented_page", "token"); got != TodayHub {
		t.Errorf("AccountPath for an unknown page = %q, want the today hub", got)
	}
}

// Campaign parameters would create a crawlable second URL for every canonical
// page, which is the duplicate-tree defect the search recovery just finished
// paying off.
func TestAbsoluteAddsNoTrackingParameters(t *testing.T) {
	builder := Builder{BaseURL: "https://sokalens.com/"}
	got := builder.Fixture(1234567, "Premier League", "Arsenal", "Chelsea")
	if got != "https://sokalens.com/predictions/premier-league/arsenal-vs-chelsea-1234567" {
		t.Fatalf("Fixture = %q", got)
	}
	if strings.ContainsAny(got, "?&") {
		t.Fatalf("email fixture URL carries query parameters: %q", got)
	}
}
