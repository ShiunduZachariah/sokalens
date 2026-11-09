package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/opshealth"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	accadomain "github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/acca"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/access"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/accounts"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/admin"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/ads"
	analysisdomain "github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/analysis"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/billing"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/ops"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/recommendations"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/usernotifications"
)

type ReadinessChecker interface {
	Name() string
	Check() error
}

type AdminAuthenticator interface {
	Login(context.Context, string, string) (admin.LoginResult, error)
	ValidateSession(context.Context, string) (admin.User, error)
	Logout(context.Context, string) error
}

type AdminMFAAuthenticator interface {
	LoginWithMFA(context.Context, string, string, string) (admin.LoginResult, error)
	ValidateSession(context.Context, string) (admin.User, error)
	Logout(context.Context, string) error
}

type ProviderStatusSource interface {
	ListProviderStatuses(context.Context) ([]ProviderStatus, error)
}
type AccaSource interface {
	List(context.Context, accadomain.Query) (accadomain.Result, error)
}

// AccaSlipSource serves one slip by id.
//
// A SECOND, SMALLER INTERFACE rather than a method on AccaSource, because the
// two reads want different plumbing. List is served through a Redis-cached
// wrapper; a by-id read must not be, because its leg statuses change while the
// matches are being played and a cached "pending" would outlive the result it
// describes. Keeping them apart means the cache wrapper needs no Get at all,
// and nothing can accidentally start caching this.
type AccaSlipSource interface {
	Get(ctx context.Context, id string) (accadomain.Slip, error)
}

type PredictionSource interface {
	ListTodayPredictions(context.Context, PredictionFilter) ([]PredictionSummary, error)
	CountTodayPredictions(context.Context, PredictionFilter) (int, error)
	ListTodayProviderPredictions(context.Context, PredictionFilter) ([]ProviderPredictionSummary, error)
	CountTodayProviderPredictions(context.Context, PredictionFilter) (int, error)
	GetPrediction(context.Context, int64) (PredictionDetail, error)
	GetHitRateReport(context.Context) (PredictionHitRateReport, error)
}

type PredictionResultsSource interface {
	ListPredictionResults(context.Context, PredictionFilter) ([]PredictionResultSummary, error)
	CountPredictionResults(context.Context, PredictionFilter) (int, error)
}

// ProviderPredictionSource exposes API-Football native predictions as a
// separately-marked fallback for fixtures without our own prediction.
type ProviderPredictionSource interface {
	ListProviderPredictions(context.Context, int) ([]ProviderPredictionSummary, error)
}

// SearchSource exposes all-rounded entity search.
type SearchSource interface {
	Search(context.Context, string, int) ([]SearchResult, error)
}

// RecommendationSearchSource is opt-in so existing clients keep entity-only
// results during backend-first releases and independent frontend rollbacks.
type RecommendationSearchSource interface {
	SearchWithRecommendations(context.Context, string, int) ([]SearchResult, error)
}

// CoverageSource exposes canonical catalogue + search index coverage for the
// admin control plane (reference entities, media coverage, live pipeline).
type CoverageSource interface {
	Coverage(context.Context) (CatalogueCoverage, error)
}

// CatalogueCoverage describes canonical catalogue + search index coverage for
// the admin control plane: how many reference entities exist, how many carry
// display media (flags/logos), and when the live pipeline last produced work.
type CatalogueCoverage struct {
	Countries      int
	CountriesFlags int
	Teams          int
	TeamLogos      int
	Leagues        int
	LeagueLogos    int
	SearchIndexed  int
	LiveLastSeen   time.Time
	WorkerLastSeen time.Time
}

type SearchResult struct {
	Type    string
	ID      int64
	MatchID int64
	// Key carries non-numeric identifiers (for example an ACCA UUID).
	Key       string
	Code      string
	Name      string
	Slug      string
	Subtitle  string
	KickoffAt time.Time
	Status    string
	LogoURL   string
	MarketKey string
	Selection string
}

// CatalogSource exposes league and team read models for discovery pages.
type CatalogSource interface {
	ListLeagues(context.Context, int) ([]LeagueSummary, error)
	GetLeague(context.Context, string) (LeagueDetail, error)
	GetTeam(context.Context, string) (TeamDetail, error)
}

// FavoriteCatalogSource adds the bounded country fixture view used to enrich
// saved entities without expanding the public catalog contract for test fakes.
type FavoriteCatalogSource interface {
	CatalogSource
	GetTeamNextMatch(context.Context, string) (*MatchSummary, error)
	ListLeagueMatches(context.Context, string, int) ([]MatchSummary, error)
	ListCountryMatches(context.Context, string, int) ([]MatchSummary, error)
}

// OpsSource exposes read-only operational views for the admin control plane.
type OpsSource interface {
	ListJobs(context.Context, int) ([]ops.JobSummary, error)
	ListProviderRequests(context.Context, int) ([]ops.ProviderRequest, error)
	ListSettings(context.Context) ([]ops.Setting, error)
	ListNotifications(context.Context, int) ([]ops.Notification, error)
	ListNotificationOutbox(context.Context, int) ([]ops.NotificationOutbox, error)
}

// AdsSource exposes advertising and policy-control read models.
type AdsSource interface {
	ResolvePlacement(context.Context, string, string) (ads.PlacementDecision, error)
	ActiveRestrictions(context.Context, string) ([]ads.RestrictionRule, error)
	ListCampaigns(context.Context, int) ([]ads.Campaign, error)
	ListRestrictionRules(context.Context, int) ([]ads.RestrictionRule, error)
	ListFeatureFlags(context.Context) ([]ads.FeatureFlag, error)
}

// AdRoutingService owns provider assignment and ephemeral session/circuit
// state. Policy and direct-campaign reads remain in AdsSource so a Redis
// outage can degrade routing without weakening server-side restrictions.
type AdRoutingService interface {
	Resolve(context.Context, ads.ResolveRequest) (ads.ProviderRoute, error)
	Report(context.Context, string, ads.OutcomeReport) (ads.ProviderRoute, error)
	ListProviders(context.Context) ([]ads.Provider, error)
}

type LeagueSummary struct {
	ID            int64
	Slug          string
	Name          string
	Country       string
	Sport         string
	PriorityScore int
	LogoURL       string
	HasContent    bool
}

type LeagueDetail struct {
	LeagueSummary
	Upcoming []MatchSummary
	Recent   []MatchSummary
	Teams    []TeamSummary
}

type TeamSummary struct {
	ID      int64
	Slug    string
	Name    string
	Country string
	LogoURL string
	FlagURL string
}

type TeamDetail struct {
	TeamSummary
	LeagueName string
	// The slug for LeagueName. Without it a team page can name its competition
	// but cannot link to it, because league URLs are slug-addressed.
	LeagueSlug string
	NextMatch  *MatchSummary
	Recent     []MatchSummary
}

type MatchSource interface {
	ListMatches(context.Context, MatchQuery) ([]MatchSummary, error)
	CountMatches(context.Context, MatchQuery) (int, error)
	GetMatch(context.Context, int64) (MatchSummary, error)
}

type AdminManagement interface {
	ListUsers(context.Context, string, string, int) ([]admin.UserSummary, error)
	UserGrowth(context.Context) (admin.UserGrowthSummary, error)
	UserActivity(context.Context, int) ([]admin.UserActivity, error)
	Suspend(context.Context, admin.SuspensionCommand) error
	Reactivate(context.Context, int64, string, admin.User) error
	RevokeSessions(context.Context, int64, string, admin.User) error
	AuditLogs(context.Context, int) ([]admin.AuditLog, error)
	ListSubscriptions(context.Context, int) ([]admin.SubscriptionSummary, error)
	ListPlanConfigurations(context.Context) ([]admin.PlanConfiguration, error)
	UpdatePlanConfiguration(context.Context, admin.PlanConfiguration, admin.User) error
}

type PredictionFilter struct {
	MarketKey         string
	MinimumConfidence *float64
	BestBetsOnly      bool
	Date              string
	Day               string
	// How many calendar days the window covers, starting at Date/Day. 0 and 1
	// both mean a single day, which is every caller that does not ask.
	Days     int
	Timezone string
	Limit    int
	Page     int
}

type PredictionSummary struct {
	ID               int64
	MatchID          int64
	MatchTitle       string
	HomeTeam         string
	AwayTeam         string
	HomeTeamLogo     string
	AwayTeamLogo     string
	LeagueName       string
	Country          string
	KickoffAt        time.Time
	FinishedAt       *time.Time
	MatchStatus      string
	MarketKey        string
	Selection        string
	Confidence       float64
	Probability      float64
	IsBestBet        bool
	PublishedVersion int
	PublishedAt      time.Time
	UpdatedAt        time.Time
	LastEvaluatedAt  time.Time
	// SelectionOdds is the latest decimal odds for this prediction's
	// selection when the odds table has a matching 1x2 price; nil otherwise.
	SelectionOdds *float64
	Markets       []PredictionMarketOption
}

type PredictionMarketOption struct {
	MarketKey   string
	Selection   string
	Confidence  float64
	Probability float64
	IsBestBet   bool
}

type PredictionDetail struct {
	PredictionSummary
	Probabilities     map[string]float64
	InputSnapshotHash string
	Outcome           string
}

type PredictionHitRateReport struct {
	Settled     int
	Won         int
	Lost        int
	Void        int
	HitRate     float64
	Denominator int
	Markets     []PredictionMarketHitRate
}

type PredictionResultSummary struct {
	PredictionID int64
	MatchID      int64
	MatchTitle   string
	// The two sides, beside the pre-joined MatchTitle rather than instead of
	// it. A results row needs a crest per team (review 2.1, section 3), and a
	// crest needs a name to hash its fallback shield from -- splitting
	// "Home vs Away" back apart would be recovering structure from a string
	// that had already lost it.
	HomeTeam     string
	AwayTeam     string
	HomeTeamLogo string
	AwayTeamLogo string
	LeagueName   string
	Country      string
	KickoffAt    time.Time
	MatchStatus  string
	MarketKey    string
	Selection    string
	Actual       string
	Outcome      string
	Confidence   float64
}

type PredictionMarketHitRate struct {
	MarketKey   string
	Settled     int
	Won         int
	Lost        int
	Void        int
	HitRate     float64
	Denominator int
}

// ProviderPredictionSummary is an API-Football native prediction used only as a
// fallback for a fixture we have not predicted ourselves. It is always returned
// separately so the UI can mark it distinctly. The canonical primitives
// (market_key, selection, confidence, published_version, published_at) mirror
// PredictionSummary so the UI can render provider and engine items alike.
type ProviderPredictionSummary struct {
	MatchID    int64
	MatchTitle string
	HomeTeam   string
	AwayTeam   string
	// HomeTeamLogo/AwayTeamLogo are the crest URLs from the teams the provider
	// row already joins, flat beside the names they belong to -- the same shape
	// acca.Leg uses. Empty rather than absent when a team has no crest on
	// record: the clients key on the empty string to draw an initials shield,
	// and a missing team logo must never drop the row.
	HomeTeamLogo     string
	AwayTeamLogo     string
	LeagueName       string
	Country          string
	KickoffAt        time.Time
	MatchStatus      string
	PredictedWinner  string
	WinOrDraw        bool
	UnderOver        string
	PercentHome      float64
	PercentDraw      float64
	PercentAway      float64
	Advice           string
	MarketKey        string
	Selection        string
	Confidence       float64
	PublishedVersion int
	PublishedAt      time.Time
}

// TeamDiscipline is one team's card record over its recent matches, as
// totals. MatchCards counts both sides' cards in those matches.
type TeamDiscipline struct {
	Matches     int
	YellowCards int
	RedCards    int
	MatchCards  int
}

// MatchDiscipline is both teams' card records from before a fixture.
type MatchDiscipline struct {
	Home TeamDiscipline
	Away TeamDiscipline
}

// TeamMatchStats is one side's in-match statistics as the provider reported
// them.
//
// Every field is a pointer because zero and absent are different answers and
// the page renders them differently: nil is "the provider did not report this",
// 0 is "none happened". Collapsing them would print a confident 0% possession
// for a match whose feed carried no possession at all.
type TeamMatchStats struct {
	PossessionPct *float64
	ShotsTotal    *int
	ShotsOnTarget *int
	Corners       *int
	Fouls         *int
	Offsides      *int
	YellowCards   *int
	RedCards      *int
}

// MatchStatistics is both sides' in-match statistics.
//
// The eight fields are exactly the rows the design's Stats tab draws, in its
// order. Deliberately not everything fixture_statistics holds -- xG, passing
// and saves are stored and are not on this surface, and serving them because
// they exist would widen every match payload for a tab that does not show them.
type MatchStatistics struct {
	Home TeamMatchStats
	Away TeamMatchStats
}

type MatchSummary struct {
	ID                    int64
	LeagueName            string
	LeagueSlug            string
	Country               string
	HomeTeam              string
	AwayTeam              string
	HomeTeamLogo          string
	AwayTeamLogo          string
	HomeTeamSlug          string
	AwayTeamSlug          string
	KickoffAt             time.Time
	FinishedAt            *time.Time
	Status                string
	RawStatus             string
	RawStatusLabel        string
	StatusElapsed         *int
	LiveStateAt           *time.Time
	VenueName             string
	VenueCity             string
	HomeScore             *int
	AwayScore             *int
	HalfTimeHomeScore     *int
	HalfTimeAwayScore     *int
	Odds                  []OddsPrice
	Prediction            *MatchPredictionSummary
	Predictions           []MatchPredictionSummary
	PredictionState       string
	PredictionStateReason string
	HomeHistoryCount      int
	AwayHistoryCount      int
	HomeForm              string
	AwayForm              string
	Timeline              []MatchEvent
	Statistics            *MatchStatistics
	Discipline            *MatchDiscipline
}

// MatchEvent is one entry on a match's live timeline.
//
// Kind is the derived meaning and is what callers should switch on; Type and
// Detail are the provider's raw pair, carried so an unrecognised event can
// still be read by a human rather than disappearing behind kind="unknown".
//
// Retracted marks an event a later VAR decision withdrew -- a cancelled goal.
// Such events stay on the timeline: removing one leaves a reader unable to
// explain why the score does not match the goal they watched being celebrated,
// and the retraction is itself the information they want. A retracted goal
// does not count towards the score, which is served separately and comes from
// the provider's own tally.
type MatchEvent struct {
	Minute int
	// Nil outside added time rather than 0, so 90' and 90+0' stay distinct.
	Extra      *int
	Clock      string
	Kind       string
	Type       string
	Detail     string
	Side       string
	PlayerName string
	AssistName string
	Comments   string
	Retracted  bool
}

type MatchQuery struct {
	Date               string
	Day                string
	Timezone           string
	Statuses           []string
	Limit              int
	Page               int
	IncludeUnavailable bool
	ResultDetailWindow time.Duration
}

type OddsPrice struct {
	Label string
	Value string
	Trend string
}

type MatchPredictionSummary struct {
	ID         int64
	Source     string
	MarketKey  string
	Selection  string
	Confidence float64
	// Probability is the engine's probability for this selection, from 0 to 1, and
	// the only input a fair price may use. Confidence is a composite score, not a
	// chance. Zero means none: provider rows have no such figure.
	Probability      float64
	PublishedVersion int
	PublishedAt      time.Time
	UpdatedAt        time.Time
	LastEvaluatedAt  time.Time
	Outcome          string
	Actual           string
	// VoidReason is set only when Outcome is "void" and the void was not a
	// market rule -- it says no trustworthy result ever existed. Empty for a
	// graded void, so the page can tell a drawn draw-no-bet apart from a
	// fixture whose result never arrived.
	VoidReason string
	// TrackerState is how the pick is doing RIGHT NOW: on_track, off_track, won,
	// lost, void or unknown. Computed by the engine and stored, never derived
	// here, so the web app and the Flutter app cannot judge the same pick
	// differently.
	//
	// Distinct from Outcome, which is the settled grade and empty until a fixture
	// is settled. A pick can be `won` on the tracker before Outcome is populated,
	// because a decided market is decided the moment the evidence lands -- full
	// time over 1.5 with two goals at minute 10 is won, not pending.
	//
	// Empty when no tracker state has been computed, which is the honest reading
	// for a fixture the in-play pass has never covered. A client renders no
	// tracker rather than guessing, the same as for `unknown`.
	TrackerState string
}

type ProviderStatus struct {
	Provider      string
	DisplayName   string
	Enabled       bool
	Status        string
	ThrottleUntil *time.Time
	Quota         ProviderQuota
}

type ProviderQuota struct {
	DailyLimit      int
	DailyRemaining  int
	DailyReserve    int
	MinuteLimit     int
	MinuteRemaining int
	MinuteReserve   int
	UpdatedAt       time.Time
}

type ServerOptions struct {
	Config          config.Config
	ReadinessChecks []ReadinessChecker
	// HealthFreshness supplies the operational ages in /healthz. Optional: when
	// nil the endpoint still reports liveness and dependency reachability, so a
	// deployment that has not wired it degrades to the previous behaviour
	// rather than failing.
	HealthFreshness opshealth.Source
	OpenAPIPath     string
	AdminAuth       AdminAuthenticator
	AdminMFAAuth    AdminMFAAuthenticator
	ProviderStatus  ProviderStatusSource
	Accas           AccaSource
	// AccaSlips serves GET /v1/accas/{id}. Deliberately the UNCACHED source;
	// see AccaSlipSource.
	AccaSlips           AccaSlipSource
	Predictions         PredictionSource
	ProviderPredictions ProviderPredictionSource
	Search              SearchSource
	Coverage            CoverageSource
	Catalog             CatalogSource
	Ops                 OpsSource
	Ads                 AdsSource
	AdRouting           AdRoutingService
	Matches             MatchSource
	InPlay              InPlaySource
	// InPlayPublicRead publishes in-play predictions without a user session.
	// It applies only to this read surface; account and admin permissions do
	// not change. False retains the legacy gated policy.
	InPlayPublicRead bool
	// InPlayStream serves the combined first load, the resumable stream and the
	// polling fallback. Separate from InPlay because they load different things,
	// and optional so an environment without it still serves the board.
	InPlayStream InPlayStreamSource
	// InPlayStreamPacing bounds a stream connection. Zero values take the
	// defaults, which suit a real client but make a one-shot probe look like a
	// hang.
	InPlayStreamPacing InPlayStreamPacing
	// SEOURLs serves the crawlable URL set to the sitemap builders.
	SEOURLs         SEOURLSource
	AdminManagement AdminManagement
	EmailProvider   EmailProviderSettings
	Accounts        *accounts.Service
	Access          *access.SQLRepository
	GuestAccess     *access.GuestService
	Recommendations *recommendations.Service
	Billing         billing.Service
	Notifications   usernotifications.Service
	Analysis        analysisdomain.Service
}

func New(opts ServerOptions) http.Handler {
	if opts.OpenAPIPath == "" {
		opts.OpenAPIPath = opts.Config.OpenAPIPath
	}
	accountDeps := accountDependencies{accounts: opts.Accounts, access: opts.Access, guest: opts.GuestAccess, recommendations: opts.Recommendations, billing: opts.Billing, notifications: opts.Notifications, analysis: opts.Analysis, predictions: opts.Predictions, matches: opts.Matches, catalog: opts.Catalog, ads: opts.Ads, adRouting: opts.AdRouting, adsEnabled: opts.Config.AdsEnabled, publicWebURL: opts.Config.PublicWebURL, secureCookies: opts.Config.Environment == "production", guestLimit: opts.Config.GuestAnalysisViewLimit, authLimiter: newFixedWindowLimiter(10, time.Minute), verificationLimiter: newFixedWindowLimiter(2, 10*time.Minute), passwordResetLimiter: newFixedWindowLimiter(2, 10*time.Minute), shareLimiter: newFixedWindowLimiter(30, time.Minute), turnstileSecret: opts.Config.TurnstileSecretKey, turnstileRequired: opts.Config.TurnstileRequired, turnstileHostname: publicHostname(opts.Config.PublicWebURL)}
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health", "/healthz":
			healthHandler(opts.Config, opts.HealthFreshness)(w, r)
		case "/ready", "/readyz":
			readyHandler(opts.ReadinessChecks)(w, r)
		case "/v1/mobile/config":
			mobileConfigHandler()(w, r)
		case "/v1/matches/today":
			todayMatchesHandler(opts.Matches)(w, r)
		case "/v1/matches/history":
			historicalMatchesHandler(opts.Matches, publicResultDetailWindow(opts.Config))(w, r)
		case "/v1/predictions/today":
			todayPredictionsHandler(opts.Predictions)(w, r)
		case "/v1/predictions/hit-rate":
			predictionHitRateHandler(opts.Predictions)(w, r)
		case "/v1/predictions/results":
			predictionResultsHandler(opts.Predictions)(w, r)
		case "/v1/predictions/provider":
			providerPredictionsHandler(opts.ProviderPredictions)(w, r)
		case "/v1/search", "/v1/search/suggest":
			searchHandler(opts.Search)(w, r)
		case "/v1/leagues":
			leaguesHandler(opts.Catalog)(w, r)
		case "/v1/best-bets":
			accountDeps.bestBetsHandler()(w, r)
		case "/v1/accas", "/v1/accas/featured":
			accaHandler(opts.Accas, r.URL.Path == "/v1/accas/featured")(w, r)
		case "/openapi.yaml":
			openAPIHandler(opts.OpenAPIPath)(w, r)
		case "/docs":
			docsHandler()(w, r)
		case "/v1/providers/status":
			providerStatusHandler(opts.ProviderStatus)(w, r)
		case "/v1/access", "/v1/access/context":
			accountDeps.accessHandler()(w, r)
		case "/v1/context":
			accountDeps.contextHandler()(w, r)
		case "/v1/locales":
			accountDeps.localesHandler()(w, r)
		case "/v1/growth/events":
			accountDeps.growthEventsHandler()(w, r)
		case "/v1/auth/register":
			accountDeps.registerHandler()(w, r)
		case "/v1/auth/login":
			accountDeps.loginHandler()(w, r)
		case "/v1/auth/logout":
			accountDeps.logoutHandler()(w, r)
		case "/v1/auth/email-verification/request":
			accountDeps.verificationRequestHandler()(w, r)
		case "/v1/auth/email-verification/confirm":
			accountDeps.verificationConfirmHandler()(w, r)
		case "/v1/auth/password-reset/request":
			accountDeps.passwordResetRequestHandler()(w, r)
		case "/v1/auth/password-reset/confirm":
			accountDeps.passwordResetConfirmHandler()(w, r)
		case "/v1/me":
			accountDeps.meHandler()(w, r)
		case "/v1/me/profile":
			accountDeps.profileHandler()(w, r)
		case "/v1/me/capabilities":
			accountDeps.capabilitiesHandler()(w, r)
		case "/v1/me/password":
			accountDeps.passwordChangeHandler()(w, r)
		case "/v1/me/sessions":
			accountDeps.sessionsHandler()(w, r)
		case "/v1/me/favorites":
			accountDeps.favoritesHandler()(w, r)
		case "/v1/share-events":
			accountDeps.shareEventsHandler()(w, r)
		case "/v1/me/interests":
			accountDeps.interestsHandler()(w, r)
		case "/v1/me/notifications":
			accountDeps.notificationsHandler()(w, r)
		case "/v1/me/subscription":
			accountDeps.subscriptionHandler()(w, r)
		case "/v1/me/billing-portal":
			accountDeps.billingPortalHandler()(w, r)
		case "/v1/plans":
			accountDeps.plansHandler()(w, r)
		case "/v1/checkout", "/v1/checkout-sessions":
			accountDeps.checkoutHandler()(w, r)
		case "/v1/recommendations":
			accountDeps.recommendationsHandler()(w, r)
		case "/v1/recommendations/events":
			accountDeps.recommendationEventsHandler()(w, r)
		case "/v1/billing/webhooks":
			accountDeps.billingWebhookHandler()(w, r)
		case "/v1/admin/auth/login":
			if !accountDeps.allowAuth(r, "admin-login") {
				writeJSON(w, http.StatusTooManyRequests, apiError("rate_limited", "Please wait before trying again."))
				return
			}
			adminLoginHandler(opts.AdminAuth, opts.AdminMFAAuth)(w, r)
		case "/v1/admin/auth/session":
			adminSessionHandler(opts.AdminAuth)(w, r)
		case "/v1/admin/auth/logout":
			adminLogoutHandler(opts.AdminAuth)(w, r)
		case "/v1/admin/providers/status":
			adminProviderStatusHandler(opts.AdminAuth, opts.ProviderStatus)(w, r)
		case "/v1/admin/predictions/hit-rate":
			adminPredictionHitRateHandler(opts.AdminAuth, opts.Predictions)(w, r)
		case "/v1/admin/predictions/results":
			adminPredictionResultsHandler(opts.AdminAuth, opts.Predictions)(w, r)
		case "/v1/admin/users":
			adminUsersHandler(opts.AdminAuth, opts.AdminManagement)(w, r)
		case "/v1/admin/user-growth":
			adminUserGrowthHandler(opts.AdminAuth, opts.AdminManagement)(w, r)
		case "/v1/admin/user-activity":
			adminUserActivityHandler(opts.AdminAuth, opts.AdminManagement)(w, r)
		case "/v1/admin/audit-logs":
			adminAuditLogsHandler(opts.AdminAuth, opts.AdminManagement)(w, r)
		case "/v1/admin/subscriptions":
			adminSubscriptionsHandler(opts.AdminAuth, opts.AdminManagement)(w, r)
		case "/v1/admin/plans":
			adminPlansHandler(opts.AdminAuth, opts.AdminManagement)(w, r)
		case "/v1/admin/observability/metrics":
			observabilityMetricsHandler(opts.AdminAuth, defaultRequestMetrics)(w, r)
		case "/v1/admin/observability/diagnostics":
			observabilityDiagnosticsHandler(opts.AdminAuth, defaultRequestMetrics)(w, r)
		case "/v1/admin/jobs":
			adminJobsHandler(opts.AdminAuth, opts.Ops)(w, r)
		case "/v1/admin/provider-requests":
			adminProviderRequestsHandler(opts.AdminAuth, opts.Ops)(w, r)
		case "/v1/admin/ads":
			if !opts.Config.AdsEnabled {
				adsHidden(w)
				return
			}
			adminAdsHandler(opts.AdminAuth, opts.Ads, opts.AdRouting)(w, r)
		case "/v1/ads/outcomes":
			if !opts.Config.AdsEnabled {
				adsHidden(w)
				return
			}
			accountDeps.adsOutcomeHandler()(w, r)
		case "/v1/admin/policy/rules":
			adminPolicyRulesHandler(opts.AdminAuth, opts.Ads)(w, r)
		case "/v1/admin/settings":
			adminSettingsHandler(opts.AdminAuth, opts.Ops)(w, r)
		case "/v1/admin/email/provider":
			adminEmailProviderHandler(opts.AdminAuth, opts.EmailProvider)(w, r)
		case "/v1/admin/matches":
			adminMatchesHandler(opts.AdminAuth, opts.Matches)(w, r)
		case "/v1/admin/leagues/priority":
			adminLeaguesPriorityHandler(opts.AdminAuth, opts.Catalog)(w, r)
		case "/v1/admin/notifications":
			adminNotificationsHandler(opts.AdminAuth, opts.Ops)(w, r)
		case "/v1/seo/urls":
			seoURLsHandler(opts.SEOURLs)(w, r)
		case "/v1/admin/catalogue/coverage":
			adminCatalogueCoverageHandler(opts.AdminAuth, opts.Coverage)(w, r)
		default:
			// The whole in-play surface is one prefix behind one handler, so
			// the entitlement gate cannot be routed around. See
			// InPlayPathPrefix.
			if strings.HasPrefix(r.URL.Path, InPlayPathPrefix) {
				accountDeps.inPlayHandlerWithPolicy(opts.InPlay, opts.InPlayStream, opts.InPlayStreamPacing, opts.InPlayPublicRead)(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v1/analysis/") {
				accountDeps.analysisHandler()(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v1/me/sessions/") {
				accountDeps.sessionActionHandler()(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v1/me/notifications/") {
				accountDeps.notificationActionHandler()(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v1/admin/users/") {
				adminUserActionHandler(opts.AdminAuth, opts.AdminManagement)(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v1/admin/plans/") {
				adminPlanActionHandler(opts.AdminAuth, opts.AdminManagement)(w, r)
				return
			}
			// Exact /v1/accas and /v1/accas/featured are matched above, so
			// this prefix only ever sees an id.
			if strings.HasPrefix(r.URL.Path, "/v1/accas/") {
				accaDetailHandler(opts.AccaSlips)(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v1/matches/") {
				matchDetailHandler(opts.Matches)(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v1/leagues/") {
				leagueDetailHandler(opts.Catalog)(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v1/teams/") {
				teamDetailHandler(opts.Catalog)(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v1/ads/placements/") {
				if !opts.Config.AdsEnabled {
					adsHidden(w)
					return
				}
				accountDeps.adsPlacementHandler()(w, r)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/v1/predictions/") {
				predictionDetailHandler(opts.Predictions)(w, r)
				return
			}
			writeJSON(w, http.StatusNotFound, apiError("not_found", "Endpoint not found."))
		}
	})
	var handler http.Handler = router
	if opts.Config.APIRequestTimeout > 0 {
		message := "{\"error\":\"request_timeout\",\"code\":\"request_timeout\",\"message\":\"The request timed out.\"}\n"
		// Kept before the timeout wrappers so a streaming route can be served
		// without one. See streamingRoute.
		unbuffered := handler
		timeoutHandler := http.TimeoutHandler(handler, opts.Config.APIRequestTimeout, message)
		authTimeoutHandler := timeoutHandler
		if opts.Config.AuthRequestTimeout > opts.Config.APIRequestTimeout {
			authTimeoutHandler = http.TimeoutHandler(handler, opts.Config.AuthRequestTimeout, message)
		}
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Server-sent events cannot pass through http.TimeoutHandler.
			//
			// It swaps the ResponseWriter for an unexported timeoutWriter that
			// buffers the whole body and implements neither Flush nor Unwrap, so
			// the stream handler's http.NewResponseController finds no flusher in
			// the chain and answers 501 streaming_unsupported -- which is what
			// production returned on every /v1/inplay/stream request. Even if it
			// could flush, a wrapper that buffers the entire response is the
			// opposite of streaming.
			//
			// This does not make the request unbounded: the stream handler runs
			// its own lifetime timer and closes itself, which is the right bound
			// for a connection meant to stay open.
			if streamingRoute(r.URL.Path) {
				unbuffered.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if passwordHashingRoute(r.URL.Path) {
				authTimeoutHandler.ServeHTTP(w, r)
				return
			}
			timeoutHandler.ServeHTTP(w, r)
		})
	}
	return observabilityMiddleware(defaultRequestMetrics, requestMiddleware(handler))
}

// streamingRoute reports whether a path serves a long-lived response that must
// not be wrapped in http.TimeoutHandler.
//
// An exact-match list rather than a prefix, deliberately: everything else under
// /v1/inplay/ is an ordinary bounded JSON read and should keep its timeout.
func streamingRoute(path string) bool {
	switch path {
	case "/v1/inplay/stream":
		return true
	default:
		return false
	}
}

func passwordHashingRoute(path string) bool {
	switch path {
	case "/v1/auth/register", "/v1/auth/login", "/v1/auth/password-reset/confirm", "/v1/me/password":
		return true
	default:
		return false
	}
}

func todayMatchesHandler(source MatchSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("match_source_unavailable", "Match data is temporarily unavailable."))
			return
		}
		query, err := matchQueryFromRequest(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_match_filter", "Match filter is invalid."))
			return
		}
		// The live/today feed shows matches in play or not yet started; ended
		// matches belong to the results/history surfaces.
		if len(query.Statuses) == 0 {
			query.Statuses = []string{"live", "upcoming"}
		}
		total, err := source.CountMatches(r.Context(), query)
		if err != nil {
			slog.Error("today matches count unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("match_source_unavailable", "Match data is temporarily unavailable."))
			return
		}
		matches, err := source.ListMatches(r.Context(), query)
		if err != nil {
			slog.Error("today matches unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("match_source_unavailable", "Match data is temporarily unavailable."))
			return
		}
		items := make([]map[string]any, 0, len(matches))
		for _, match := range matches {
			items = append(items, matchSummaryResponse(match))
		}
		writeCanonicalJSON(w, r, map[string]any{
			"matches": items,
			"page":    matchPage(query.Page),
			"limit":   matchLimit(query.Limit),
			"total":   total,
		})
	}
}

func historicalMatchesHandler(source MatchSource, resultDetailWindow time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("match_source_unavailable", "Match data is temporarily unavailable."))
			return
		}
		query, err := matchQueryFromRequest(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_match_filter", "Match filter is invalid."))
			return
		}
		if len(query.Statuses) == 0 {
			query.Statuses = []string{"finished"}
		}
		query.ResultDetailWindow = resultDetailWindow
		total, err := source.CountMatches(r.Context(), query)
		if err != nil {
			slog.Error("historical matches count unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("match_source_unavailable", "Match data is temporarily unavailable."))
			return
		}
		matches, err := source.ListMatches(r.Context(), query)
		if err != nil {
			slog.Error("historical matches unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("match_source_unavailable", "Match data is temporarily unavailable."))
			return
		}
		items := make([]map[string]any, 0, len(matches))
		for _, match := range matches {
			items = append(items, matchSummaryResponse(match))
		}
		writeCanonicalJSON(w, r, map[string]any{
			"matches": items,
			"page":    matchPage(query.Page),
			"limit":   matchLimit(query.Limit),
			"total":   total,
		})
	}
}

func matchPage(page int) int {
	if page <= 0 {
		return 1
	}
	return page
}

func matchLimit(limit int) int {
	if limit <= 0 || limit > 200 {
		return 200
	}
	return limit
}

func publicResultDetailWindow(cfg config.Config) time.Duration {
	if cfg.PublicResultDetailWindow > 0 {
		return cfg.PublicResultDetailWindow
	}
	return 24 * time.Hour
}

func matchDetailHandler(source MatchSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("match_source_unavailable", "Match data is temporarily unavailable."))
			return
		}
		idText := strings.TrimPrefix(r.URL.Path, "/v1/matches/")
		matchID, err := strconv.ParseInt(idText, 10, 64)
		if err != nil || matchID <= 0 {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_match_id", "Match identifier is invalid."))
			return
		}
		match, err := source.GetMatch(r.Context(), matchID)
		if err != nil {
			if errors.Is(err, ErrMatchNotFound) {
				writeJSON(w, http.StatusNotFound, apiError("match_not_found", "Match not found."))
				return
			}
			slog.Error("match detail unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("match_source_unavailable", "Match data is temporarily unavailable."))
			return
		}
		writeCanonicalJSON(w, r, matchSummaryResponse(match))
	}
}

func matchQueryFromRequest(r *http.Request) (MatchQuery, error) {
	query := MatchQuery{
		Date:     strings.TrimSpace(r.URL.Query().Get("date")),
		Day:      strings.TrimSpace(r.URL.Query().Get("day")),
		Timezone: strings.TrimSpace(r.URL.Query().Get("timezone")),
		Statuses: statusFilters(r.URL.Query()["status"]),
	}
	if query.Timezone == "" {
		query.Timezone = "Africa/Nairobi"
	}
	if _, err := loadTimezone(query.Timezone); err != nil {
		return MatchQuery{}, err
	}
	if query.Date != "" {
		if _, err := time.Parse("2006-01-02", query.Date); err != nil {
			return MatchQuery{}, err
		}
	}
	if query.Day != "" {
		switch query.Day {
		case "yesterday", "today", "tomorrow":
		default:
			return MatchQuery{}, fmt.Errorf("unsupported day %q", query.Day)
		}
	}
	if query.Date == "" && query.Day == "" {
		query.Day = "today"
	}
	if limitText := strings.TrimSpace(r.URL.Query().Get("limit")); limitText != "" {
		limit, err := strconv.Atoi(limitText)
		if err != nil || limit <= 0 || limit > 200 {
			return MatchQuery{}, fmt.Errorf("invalid limit")
		}
		query.Limit = limit
	}
	if pageText := strings.TrimSpace(r.URL.Query().Get("page")); pageText != "" {
		page, err := strconv.Atoi(pageText)
		if err != nil || page < 1 || page > 1000 {
			return MatchQuery{}, fmt.Errorf("invalid page")
		}
		query.Page = page
	}
	if includeText := strings.TrimSpace(r.URL.Query().Get("include_unavailable")); includeText != "" {
		includeUnavailable, err := strconv.ParseBool(includeText)
		if err != nil {
			return MatchQuery{}, fmt.Errorf("invalid include_unavailable")
		}
		query.IncludeUnavailable = includeUnavailable
	}
	return query, nil
}

func statusFilters(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	filters := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			filters = append(filters, trimmed)
		}
	}
	return filters
}

func loadTimezone(value string) (*time.Location, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "Africa/Nairobi" {
		return time.FixedZone("EAT", 3*60*60), nil
	}
	return time.LoadLocation(trimmed)
}

type errDependencyUnavailable string

func (e errDependencyUnavailable) Error() string { return string(e) }

var ErrPredictionNotFound = errors.New("prediction not found")
var ErrMatchNotFound = errors.New("match not found")
var ErrLeagueNotFound = errors.New("league not found")
var ErrTeamNotFound = errors.New("team not found")

// healthHandler serves the terse, unauthenticated operational status document.
//
// It exists to close the gap named in the in-play architecture (16.4): when
// the VPS is down, the thing that alerts you is down too. ops-controller runs
// on the host it watches, so a host failure takes out the alerting with the
// service. An external watchdog closes exactly that -- a cron pinging a stable
// endpoint and escalating after N consecutive failures -- and it becomes a
// configuration change rather than a project if the endpoint already exists.
//
// IT ALWAYS RETURNS 200 WHILE THE PROCESS IS ALIVE. That is deliberate and it
// is what separates this from /readyz. /readyz answers "should traffic come
// here", fails closed on a dependency, and is what the container healthcheck
// and the deploy gate read -- making this fail closed too would duplicate it,
// and would mean a database blip could start restarting containers. This one
// answers "is the service process up, and what does it know", so a watchdog
// pinging it distinguishes a dead host from a degraded one. Degradation is
// reported in the body.
//
// The document carries booleans and ages only: no counts, versions, hostnames,
// connection strings or error text. It is unauthenticated, so everything in it
// has to be something an observer could infer from the outside anyway.
func healthHandler(cfg config.Config, freshness opshealth.Source) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}

		body := map[string]any{
			"service":     "sports-prediction-api",
			"status":      "ok",
			"environment": cfg.Environment,
		}

		// One cached read covers both the reachability probes and the freshness
		// ages. The probes are the expensive part -- a database ping and a
		// socket dial -- and running them per request on an unauthenticated
		// endpoint would make it the free database lever the cache exists to
		// prevent.
		if freshness != nil {
			snapshot := freshness.Snapshot(r.Context())
			degraded := false
			for _, reachable := range snapshot.Dependencies {
				if !reachable {
					degraded = true
					break
				}
			}
			if degraded {
				body["status"] = "degraded"
			}
			if snapshot.Dependencies != nil {
				body["dependencies"] = snapshot.Dependencies
			}
			if snapshot.LastLivePoll.At != nil || snapshot.LastWorkerJob.At != nil {
				body["last_live_poll"] = snapshot.LastLivePoll
				body["last_worker_job"] = snapshot.LastWorkerJob
			}
		}

		// Never cached at the edge: a health document served from a cache is a
		// statement about the past presented as one about the present, which is
		// the same defect as a stale match clock.
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, body)
	}
}

func readyHandler(checks []ReadinessChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}

		dependencies := map[string]string{}
		ready := true
		for _, check := range checks {
			status := "ready"
			if err := check.Check(); err != nil {
				status = "unavailable"
				ready = false
			}
			dependencies[check.Name()] = status
		}

		statusCode := http.StatusOK
		status := "ready"
		if !ready {
			statusCode = http.StatusServiceUnavailable
			status = "not_ready"
		}

		writeJSON(w, statusCode, map[string]any{
			"service":      "sports-prediction-api",
			"status":       status,
			"dependencies": dependencies,
		})
	}
}

func mobileConfigHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"features": map[string]bool{
				"public_content":  true,
				"predictions":     true,
				"odds_display":    true,
				"partner_ctas":    false,
				"push_promotions": false,
			},
			"terminology": map[string]string{
				"match_analysis":      "Match Analysis",
				"match_insights":      "Match Insights",
				"analysis_watchlist":  "Analysis Watchlist",
				"analysis_alerts":     "Analysis Alerts",
				"multi_match_builder": "Multi-Match Builder",
				"price_comparison":    "Price Comparison",
				"confidence":          "Confidence",
				"form_guide":          "Form Guide",
				"match_outlook":       "Match Outlook",
				"model_signal":        "Model Signal",
			},
			"multi_match_builder": map[string]any{
				"label":       "Multi-Match Builder",
				"min_matches": 2,
				"max_matches": 6,
				"filters": []string{
					"league",
					"competition",
					"team",
					"match_start_time",
					"match_end_time",
					"min_price",
					"max_price",
					"minimum_confidence_score",
					"market_type",
					"risk_level",
				},
			},
		})
	}
}

func todayPredictionsHandler(source PredictionSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("prediction_source_unavailable", "Prediction data is temporarily unavailable."))
			return
		}
		filter, err := predictionFilterFromRequest(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_prediction_filter", "Prediction filter is invalid."))
			return
		}
		total, err := source.CountTodayPredictions(r.Context(), filter)
		if err != nil {
			slog.Error("today predictions count unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("prediction_source_unavailable", "Prediction data is temporarily unavailable."))
			return
		}
		predictions, err := source.ListTodayPredictions(r.Context(), filter)
		if err != nil {
			slog.Error("today predictions unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("prediction_source_unavailable", "Prediction data is temporarily unavailable."))
			return
		}
		items := make([]map[string]any, 0, len(predictions))
		for _, prediction := range predictions {
			response := predictionSummaryResponse(prediction)
			response["source"] = "engine"
			items = append(items, response)
		}
		providerItems := make([]map[string]any, 0)
		providerTotal := 0
		providerPredictions, err := source.ListTodayProviderPredictions(r.Context(), filter)
		if err != nil {
			slog.Error("today provider predictions unavailable", "error", err)
		} else {
			for _, prediction := range providerPredictions {
				response := providerPredictionSummaryResponse(prediction)
				response["source"] = "provider"
				providerItems = append(providerItems, response)
			}
		}
		if count, countErr := source.CountTodayProviderPredictions(r.Context(), filter); countErr != nil {
			slog.Error("today provider predictions count unavailable", "error", countErr)
		} else {
			providerTotal = count
		}
		writeCanonicalJSON(w, r, map[string]any{
			"predictions":          items,
			"provider_predictions": providerItems,
			"page":                 predictionPage(filter.Page),
			"limit":                predictionLimit(filter.Limit),
			"engine_total":         total,
			"provider_total":       providerTotal,
			"total":                total + providerTotal,
		})
	}
}

func predictionPage(page int) int {
	if page <= 0 {
		return 1
	}
	return page
}

func predictionLimit(limit int) int {
	if limit <= 0 || limit > 100 {
		return 100
	}
	return limit
}

func providerPredictionsHandler(source ProviderPredictionSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("provider_prediction_source_unavailable", "Provider prediction data is temporarily unavailable."))
			return
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_limit", "Limit is invalid."))
				return
			}
			limit = parsed
		}
		predictions, err := source.ListProviderPredictions(r.Context(), limit)
		if err != nil {
			slog.Error("provider predictions unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("provider_prediction_source_unavailable", "Provider prediction data is temporarily unavailable."))
			return
		}
		items := make([]map[string]any, 0, len(predictions))
		for _, prediction := range predictions {
			items = append(items, providerPredictionSummaryResponse(prediction))
		}
		writeCanonicalJSON(w, r, map[string]any{"provider_predictions": items, "source": "provider", "marked": "provider_fallback"})
	}
}

func providerPredictionSummaryResponse(p ProviderPredictionSummary) map[string]any {
	return map[string]any{
		"match_id":          p.MatchID,
		"match_title":       p.MatchTitle,
		"home_team":         p.HomeTeam,
		"away_team":         p.AwayTeam,
		"home_team_logo":    p.HomeTeamLogo,
		"away_team_logo":    p.AwayTeamLogo,
		"league_name":       p.LeagueName,
		"country":           p.Country,
		"kickoff_at":        p.KickoffAt,
		"match_status":      p.MatchStatus,
		"predicted_winner":  p.PredictedWinner,
		"win_or_draw":       p.WinOrDraw,
		"under_over":        p.UnderOver,
		"percent_home":      p.PercentHome,
		"percent_draw":      p.PercentDraw,
		"percent_away":      p.PercentAway,
		"advice":            p.Advice,
		"market_key":        p.MarketKey,
		"selection":         p.Selection,
		"confidence":        p.Confidence,
		"published_version": p.PublishedVersion,
		"published_at":      p.PublishedAt.Format(time.RFC3339),
	}
}

func searchHandler(source SearchSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("search_source_unavailable", "Search is temporarily unavailable."))
			return
		}
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if len(query) < 3 {
			writeCanonicalJSON(w, r, map[string]any{"results": []any{}})
			return
		}
		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 50 {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_limit", "limit must be between 1 and 50."))
				return
			}
			limit = parsed
		}
		search := source.Search
		if r.URL.Query().Get("include_recommendations") == "true" {
			if enriched, ok := source.(RecommendationSearchSource); ok {
				search = enriched.SearchWithRecommendations
			}
		}
		results, err := search(r.Context(), query, limit)
		if err != nil {
			slog.Error("search unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("search_source_unavailable", "Search is temporarily unavailable."))
			return
		}
		items := make([]map[string]any, 0, len(results))
		for _, result := range results {
			item := map[string]any{
				"type":     result.Type,
				"id":       result.ID,
				"name":     result.Name,
				"subtitle": result.Subtitle,
			}
			if result.Key != "" {
				item["key"] = result.Key
			}
			if result.MatchID != 0 {
				item["match_id"] = result.MatchID
			}
			if result.MarketKey != "" {
				item["market_key"] = result.MarketKey
			}
			if result.Selection != "" {
				item["selection"] = result.Selection
			}
			if result.Code != "" {
				item["code"] = result.Code
			}
			if result.Slug != "" {
				item["slug"] = result.Slug
			}
			if result.LogoURL != "" {
				item["logo_url"] = result.LogoURL
			}
			if !result.KickoffAt.IsZero() {
				item["kickoff_at"] = result.KickoffAt
			}
			if result.Status != "" {
				item["status"] = result.Status
			}
			items = append(items, item)
		}
		writeCanonicalJSON(w, r, map[string]any{"results": items})
	}
}

func leaguesHandler(source CatalogSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("catalog_source_unavailable", "Catalog data is temporarily unavailable."))
			return
		}
		// The complete catalogue, not a page of it. The directory is the only
		// place a reader can reach a league that is not playing today, and the
		// sitemap already declares every one of them: capping here left the
		// difference crawlable but unlinked.
		leagues, err := source.ListLeagues(r.Context(), 0)
		if err != nil {
			slog.Error("leagues unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("catalog_source_unavailable", "Catalog data is temporarily unavailable."))
			return
		}
		items := make([]map[string]any, 0, len(leagues))
		for _, league := range leagues {
			items = append(items, leagueSummaryResponse(league))
		}
		writeCanonicalJSON(w, r, map[string]any{"leagues": items, "total": len(items)})
	}
}

func leagueDetailHandler(source CatalogSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("catalog_source_unavailable", "Catalog data is temporarily unavailable."))
			return
		}
		slug := strings.TrimPrefix(r.URL.Path, "/v1/leagues/")
		if strings.TrimSpace(slug) == "" {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_league_slug", "League slug is invalid."))
			return
		}
		detail, err := source.GetLeague(r.Context(), slug)
		if err != nil {
			if errors.Is(err, ErrLeagueNotFound) {
				writeJSON(w, http.StatusNotFound, apiError("league_not_found", "League not found."))
				return
			}
			slog.Error("league detail unavailable", "error", err, "slug", slug)
			writeJSON(w, http.StatusServiceUnavailable, apiError("catalog_source_unavailable", "Catalog data is temporarily unavailable."))
			return
		}
		response := leagueSummaryResponse(detail.LeagueSummary)
		matches := make([]map[string]any, 0, len(detail.Upcoming))
		for _, match := range detail.Upcoming {
			matches = append(matches, matchSummaryResponse(match))
		}
		response["upcoming_matches"] = matches
		recent := make([]map[string]any, 0, len(detail.Recent))
		for _, match := range detail.Recent {
			recent = append(recent, matchSummaryResponse(match))
		}
		response["recent_matches"] = recent
		teams := make([]map[string]any, 0, len(detail.Teams))
		for _, team := range detail.Teams {
			item := map[string]any{
				"id":   team.ID,
				"slug": team.Slug,
				"name": team.Name,
			}
			if team.Country != "" {
				item["country"] = team.Country
			}
			if team.LogoURL != "" {
				item["logo_url"] = team.LogoURL
			}
			teams = append(teams, item)
		}
		response["teams"] = teams
		writeCanonicalJSON(w, r, response)
	}
}

func teamDetailHandler(source CatalogSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("catalog_source_unavailable", "Catalog data is temporarily unavailable."))
			return
		}
		slug := strings.TrimPrefix(r.URL.Path, "/v1/teams/")
		if strings.TrimSpace(slug) == "" {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_team_slug", "Team slug is invalid."))
			return
		}
		detail, err := source.GetTeam(r.Context(), slug)
		if err != nil {
			if errors.Is(err, ErrTeamNotFound) {
				writeJSON(w, http.StatusNotFound, apiError("team_not_found", "Team not found."))
				return
			}
			slog.Error("team detail unavailable", "error", err, "slug", slug)
			writeJSON(w, http.StatusServiceUnavailable, apiError("catalog_source_unavailable", "Catalog data is temporarily unavailable."))
			return
		}
		response := map[string]any{
			"id":          detail.ID,
			"slug":        detail.Slug,
			"name":        detail.Name,
			"country":     detail.Country,
			"league_name": detail.LeagueName,
		}
		// Omitted rather than sent empty, the same way a match omits it: a team
		// whose league could not be resolved has no league page to link to, and
		// an empty string would be built into a URL pointing at the directory.
		if detail.LeagueSlug != "" {
			response["league_slug"] = detail.LeagueSlug
		}
		if detail.LogoURL != "" {
			response["logo_url"] = detail.LogoURL
		}
		if detail.FlagURL != "" {
			response["flag_url"] = detail.FlagURL
		}
		if detail.NextMatch != nil {
			response["next_match"] = matchSummaryResponse(*detail.NextMatch)
		}
		recent := make([]map[string]any, 0, len(detail.Recent))
		for _, match := range detail.Recent {
			recent = append(recent, matchSummaryResponse(match))
		}
		response["recent_matches"] = recent
		writeCanonicalJSON(w, r, response)
	}
}

func leagueSummaryResponse(league LeagueSummary) map[string]any {
	response := map[string]any{
		"id":             league.ID,
		"slug":           league.Slug,
		"name":           league.Name,
		"country":        league.Country,
		"sport":          league.Sport,
		"priority_score": league.PriorityScore,
	}
	if league.LogoURL != "" {
		response["logo_url"] = league.LogoURL
	}
	response["has_content"] = league.HasContent
	return response
}

func predictionDetailHandler(source PredictionSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("prediction_source_unavailable", "Prediction data is temporarily unavailable."))
			return
		}
		idText := strings.TrimPrefix(r.URL.Path, "/v1/predictions/")
		predictionID, err := strconv.ParseInt(idText, 10, 64)
		if err != nil || predictionID <= 0 {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_prediction_id", "Prediction identifier is invalid."))
			return
		}
		prediction, err := source.GetPrediction(r.Context(), predictionID)
		if err != nil {
			if errors.Is(err, ErrPredictionNotFound) {
				writeJSON(w, http.StatusNotFound, apiError("prediction_not_found", "Prediction not found."))
				return
			}
			slog.Error("prediction detail unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("prediction_source_unavailable", "Prediction data is temporarily unavailable."))
			return
		}
		writeCanonicalJSON(w, r, predictionDetailResponse(prediction))
	}
}

// The ceiling on `?days=`. A week is what the fixture calendar meaningfully
// holds and what the Builder already works to.
const maxPredictionHorizonDays = 7

func predictionFilterFromRequest(r *http.Request) (PredictionFilter, error) {
	query := r.URL.Query()
	filter := PredictionFilter{
		MarketKey: strings.TrimSpace(query.Get("market")),
		Date:      strings.TrimSpace(query.Get("date")),
		Day:       strings.TrimSpace(query.Get("day")),
		Timezone:  strings.TrimSpace(query.Get("timezone")),
		Limit:     100,
	}
	if filter.Timezone == "" {
		filter.Timezone = "Africa/Nairobi"
	}
	if _, err := loadTimezone(filter.Timezone); err != nil {
		return PredictionFilter{}, err
	}
	if filter.Date != "" {
		if _, err := time.Parse("2006-01-02", filter.Date); err != nil {
			return PredictionFilter{}, fmt.Errorf("date %q is invalid", filter.Date)
		}
	}
	// A market hub is a standing page about a market, not about a date, so it
	// asks for the week rather than today. Bounded at 7: the window is a
	// kickoff range on an indexed column, but an unbounded one invites a
	// request for a year of fixtures, and nothing reads more than a week.
	if raw := strings.TrimSpace(query.Get("days")); raw != "" {
		days, err := strconv.Atoi(raw)
		if err != nil || days < 1 || days > maxPredictionHorizonDays {
			return PredictionFilter{}, fmt.Errorf("days %q is invalid", raw)
		}
		filter.Days = days
	}
	if filter.Day != "" {
		switch filter.Day {
		case "yesterday", "today", "tomorrow":
		default:
			return PredictionFilter{}, fmt.Errorf("unsupported day %q", filter.Day)
		}
	}
	if filter.Date == "" && filter.Day == "" {
		filter.Day = "today"
	}
	if limitText := strings.TrimSpace(query.Get("limit")); limitText != "" {
		limit, err := strconv.Atoi(limitText)
		if err != nil || limit <= 0 || limit > 100 {
			return PredictionFilter{}, fmt.Errorf("limit %q is invalid", limitText)
		}
		filter.Limit = limit
	}
	if pageText := strings.TrimSpace(query.Get("page")); pageText != "" {
		page, err := strconv.Atoi(pageText)
		if err != nil || page < 1 || page > 1000 {
			return PredictionFilter{}, fmt.Errorf("page %q is invalid", pageText)
		}
		filter.Page = page
	}
	if bestBets := strings.TrimSpace(query.Get("best_bets")); bestBets == "1" || bestBets == "true" {
		filter.BestBetsOnly = true
	}
	minimumConfidence := strings.TrimSpace(query.Get("minimum_confidence"))
	if minimumConfidence == "" {
		return filter, nil
	}
	value, err := strconv.ParseFloat(minimumConfidence, 64)
	if err != nil || value < 0 || value > 100 {
		return PredictionFilter{}, fmt.Errorf("minimum_confidence %q is invalid", minimumConfidence)
	}
	filter.MinimumConfidence = &value
	return filter, nil
}

func openAPIHandler(openAPIPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}

		body, err := readOpenAPI(openAPIPath)
		if err != nil {
			slog.Error("read openapi contract", "path", openAPIPath, "error", err)
			writeJSON(w, http.StatusInternalServerError, apiError("openapi_unavailable", "OpenAPI contract is unavailable."))
			return
		}

		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

func docsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, scalarHTML)
	}
}

func readOpenAPI(openAPIPath string) ([]byte, error) {
	candidates := []string{openAPIPath}
	for _, fallback := range []string{
		"packages/contracts/openapi.yaml",
		"../../packages/contracts/openapi.yaml",
		"/contracts/openapi.yaml",
	} {
		if fallback != openAPIPath {
			candidates = append(candidates, fallback)
		}
	}
	var lastErr error
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		body, err := os.ReadFile(candidate)
		if err == nil {
			return body, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func adminLoginHandler(auth AdminAuthenticator, mfaAuth AdminMFAAuthenticator) http.HandlerFunc {
	type loginRequest struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		MFACode  string `json:"mfa_code"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if auth == nil && mfaAuth == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("admin_auth_unavailable", "Admin authentication is temporarily unavailable."))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
		var payload loginRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_json", "Request body is not valid JSON."))
			return
		}
		var result admin.LoginResult
		var err error
		if mfaAuth != nil {
			result, err = mfaAuth.LoginWithMFA(r.Context(), payload.Email, payload.Password, payload.MFACode)
		} else {
			result, err = auth.Login(r.Context(), payload.Email, payload.Password)
		}
		if err != nil {
			if errors.Is(err, admin.ErrInvalidCredentials) || errors.Is(err, admin.ErrInvalidMFACode) || errors.Is(err, admin.ErrMFARequired) {
				writeJSON(w, http.StatusUnauthorized, apiError("invalid_admin_credentials", "Admin credentials are invalid."))
				return
			}
			slog.Error("admin login failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, apiError("admin_login_failed", "Admin login failed."))
			return
		}
		writeJSON(w, http.StatusOK, adminSessionResponse(result))
	}
}

func adminSessionHandler(auth AdminAuthenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if auth == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("admin_auth_unavailable", "Admin authentication is temporarily unavailable."))
			return
		}
		token, ok := bearerToken(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, apiError("missing_bearer_token", "A bearer token is required."))
			return
		}
		user, err := auth.ValidateSession(r.Context(), token)
		if err != nil {
			if errors.Is(err, admin.ErrInvalidSession) {
				writeJSON(w, http.StatusUnauthorized, apiError("invalid_admin_session", "Admin session is invalid or expired."))
				return
			}
			slog.Error("admin session validation failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, apiError("admin_session_failed", "Admin session validation failed."))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"user": adminUserResponse(user)})
	}
}

func adminLogoutHandler(auth AdminAuthenticator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if auth == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("admin_auth_unavailable", "Admin authentication is temporarily unavailable."))
			return
		}
		token, ok := bearerToken(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, apiError("missing_bearer_token", "A bearer token is required."))
			return
		}
		if err := auth.Logout(r.Context(), token); err != nil {
			if errors.Is(err, admin.ErrInvalidSession) {
				writeJSON(w, http.StatusUnauthorized, apiError("invalid_admin_session", "Admin session is invalid or expired."))
				return
			}
			slog.Error("admin logout failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, apiError("admin_logout_failed", "Admin logout failed."))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func adminProviderStatusHandler(auth AdminAuthenticator, providerStatus ProviderStatusSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if auth == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("admin_auth_unavailable", "Admin authentication is temporarily unavailable."))
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAnalytics); !ok {
			return
		}
		if providerStatus == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("provider_status_unavailable", "Provider status data is temporarily unavailable."))
			return
		}
		statuses, err := providerStatus.ListProviderStatuses(r.Context())
		if err != nil {
			slog.Error("admin provider status unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("provider_status_unavailable", "Provider status data is temporarily unavailable."))
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"providers": providerStatusesResponse(statuses)})
	}
}

func providerStatusHandler(providerStatus ProviderStatusSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if providerStatus == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("provider_status_unavailable", "Provider status data is temporarily unavailable."))
			return
		}
		statuses, err := providerStatus.ListProviderStatuses(r.Context())
		if err != nil {
			slog.Error("provider status unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("provider_status_unavailable", "Provider status data is temporarily unavailable."))
			return
		}
		public := make([]map[string]any, 0, len(statuses))
		for _, status := range statuses {
			public = append(public, map[string]any{
				"provider":     status.Provider,
				"display_name": status.DisplayName,
				"status":       status.Status,
			})
		}
		w.Header().Set("Cache-Control", publicLiveCacheControl)
		writeJSON(w, http.StatusOK, map[string]any{"providers": public})
	}
}

func adminPredictionHitRateHandler(auth AdminAuthenticator, predictions PredictionSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if auth == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("admin_auth_unavailable", "Admin authentication is temporarily unavailable."))
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAnalytics); !ok {
			return
		}
		if predictions == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("prediction_source_unavailable", "Prediction data is temporarily unavailable."))
			return
		}
		report, err := predictions.GetHitRateReport(r.Context())
		if err != nil {
			slog.Error("admin prediction hit-rate unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("prediction_source_unavailable", "Prediction data is temporarily unavailable."))
			return
		}
		writeJSON(w, http.StatusOK, predictionHitRateResponse(report))
	}
}

func predictionHitRateHandler(predictions PredictionSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if predictions == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("prediction_source_unavailable", "Prediction data is temporarily unavailable."))
			return
		}
		report, err := predictions.GetHitRateReport(r.Context())
		if err != nil {
			slog.Error("prediction hit-rate unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("prediction_source_unavailable", "Prediction data is temporarily unavailable."))
			return
		}
		w.Header().Set("Cache-Control", publicReportCacheControl)
		writeJSON(w, http.StatusOK, predictionHitRateResponse(report))
	}
}

func predictionResultsHandler(predictions PredictionSource) http.HandlerFunc {
	return predictionResultsHTTPHandler(nil, predictions, false)
}

func adminPredictionResultsHandler(auth AdminAuthenticator, predictions PredictionSource) http.HandlerFunc {
	return predictionResultsHTTPHandler(auth, predictions, true)
}

func predictionResultsHTTPHandler(auth AdminAuthenticator, predictions PredictionSource, requireAuth bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if requireAuth {
			if auth == nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("admin_auth_unavailable", "Admin authentication is temporarily unavailable."))
				return
			}
			if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAnalytics); !ok {
				return
			}
		}
		source, ok := predictions.(PredictionResultsSource)
		if !ok {
			writeJSON(w, http.StatusServiceUnavailable, apiError("prediction_results_unavailable", "Prediction results are temporarily unavailable."))
			return
		}
		filter, err := predictionFilterFromRequest(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_prediction_filter", "Prediction filter is invalid."))
			return
		}
		rows, err := source.ListPredictionResults(r.Context(), filter)
		if err != nil {
			slog.Error("prediction results unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("prediction_results_unavailable", "Prediction results are temporarily unavailable."))
			return
		}
		total, err := source.CountPredictionResults(r.Context(), filter)
		if err != nil {
			slog.Error("prediction results count unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("prediction_results_unavailable", "Prediction results are temporarily unavailable."))
			return
		}
		items := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			items = append(items, predictionResultSummaryResponse(row))
		}
		if !requireAuth {
			w.Header().Set("Cache-Control", publicLiveCacheControl)
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": items, "total": total, "page": predictionPage(filter.Page), "limit": predictionLimit(filter.Limit)})
	}
}

const (
	publicLiveCacheControl   = "public, max-age=15, s-maxage=30, stale-while-revalidate=60"
	publicReportCacheControl = "public, max-age=300, s-maxage=600, stale-while-revalidate=1800"
)

func adminSessionResponse(result admin.LoginResult) map[string]any {
	return map[string]any{
		"token":      result.Token,
		"expires_at": result.ExpiresAt.Format(time.RFC3339),
		"user":       adminUserResponse(result.User),
	}
}

func adminUserResponse(user admin.User) map[string]any {
	return map[string]any{
		"id":    user.ID,
		"email": user.Email,
		"role":  user.Role,
	}
}

func predictionSummaryResponse(prediction PredictionSummary) map[string]any {
	updatedAt := prediction.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = prediction.PublishedAt
	}
	lastEvaluatedAt := prediction.LastEvaluatedAt
	if lastEvaluatedAt.IsZero() {
		lastEvaluatedAt = updatedAt
	}
	response := map[string]any{
		"id":                prediction.ID,
		"match_id":          prediction.MatchID,
		"match_title":       prediction.MatchTitle,
		"home_team":         prediction.HomeTeam,
		"home_team_logo":    prediction.HomeTeamLogo,
		"away_team_logo":    prediction.AwayTeamLogo,
		"away_team":         prediction.AwayTeam,
		"league_name":       prediction.LeagueName,
		"country":           prediction.Country,
		"kickoff_at":        prediction.KickoffAt.Format(time.RFC3339),
		"match_status":      prediction.MatchStatus,
		"market_key":        prediction.MarketKey,
		"selection":         prediction.Selection,
		"confidence":        prediction.Confidence,
		"probability":       prediction.Probability,
		"is_best_bet":       prediction.IsBestBet,
		"published_version": prediction.PublishedVersion,
		"published_at":      prediction.PublishedAt.Format(time.RFC3339),
		"updated_at":        updatedAt.Format(time.RFC3339),
		"last_evaluated_at": lastEvaluatedAt.Format(time.RFC3339),
		"markets":           predictionMarketOptionsResponse(prediction),
	}
	if prediction.SelectionOdds != nil {
		response["selection_odds"] = strconv.FormatFloat(*prediction.SelectionOdds, 'f', 2, 64)
	}
	return response
}

func predictionMarketOptionsResponse(prediction PredictionSummary) []map[string]any {
	markets := prediction.Markets
	if len(markets) == 0 {
		markets = []PredictionMarketOption{{
			MarketKey: prediction.MarketKey, Selection: prediction.Selection, Confidence: prediction.Confidence, Probability: prediction.Probability, IsBestBet: prediction.IsBestBet,
		}}
	}
	response := make([]map[string]any, 0, len(markets))
	for _, market := range markets {
		response = append(response, map[string]any{
			"market_key": market.MarketKey, "selection": market.Selection, "confidence": market.Confidence,
			"probability": market.Probability, "is_best_bet": market.IsBestBet,
		})
	}
	return response
}

// teamMatchStatsResponse renders one side's statistics, omitting what the
// provider did not report rather than sending null.
//
// An absent key is unambiguous to the client; a null has to be distinguished
// from 0 by every consumer separately, and one of them will eventually get it
// wrong and render "0 shots" for a match nobody counted shots in.
func teamMatchStatsResponse(stats TeamMatchStats) map[string]any {
	out := map[string]any{}
	if stats.PossessionPct != nil {
		out["possession_pct"] = *stats.PossessionPct
	}
	for key, value := range map[string]*int{
		"shots_total":     stats.ShotsTotal,
		"shots_on_target": stats.ShotsOnTarget,
		"corners":         stats.Corners,
		"fouls":           stats.Fouls,
		"offsides":        stats.Offsides,
		"yellow_cards":    stats.YellowCards,
		"red_cards":       stats.RedCards,
	} {
		if value != nil {
			out[key] = *value
		}
	}
	return out
}

func teamDisciplineResponse(team TeamDiscipline) map[string]any {
	return map[string]any{
		"matches":      team.Matches,
		"yellow_cards": team.YellowCards,
		"red_cards":    team.RedCards,
		"match_cards":  team.MatchCards,
	}
}

func matchSummaryResponse(match MatchSummary) map[string]any {
	response := map[string]any{
		"id":                 match.ID,
		"league_name":        match.LeagueName,
		"country":            match.Country,
		"home_team":          match.HomeTeam,
		"away_team":          match.AwayTeam,
		"kickoff_at":         match.KickoffAt.UTC().Format(time.RFC3339),
		"home_history_count": match.HomeHistoryCount,
		"away_history_count": match.AwayHistoryCount,
		"home_form":          match.HomeForm,
		"away_form":          match.AwayForm,
		"status":             match.Status,
	}
	if match.RawStatus != "" {
		response["raw_status"] = match.RawStatus
	}
	if match.RawStatusLabel != "" {
		response["raw_status_label"] = match.RawStatusLabel
	}
	if match.StatusElapsed != nil {
		response["status_elapsed"] = *match.StatusElapsed
	}
	// live_state_at is when a provider last confirmed this match's live state,
	// served as a raw fact rather than as a derived "is stale" flag. A flag is
	// computed once and then ages in the client's hands -- a payload cached for
	// two minutes would keep asserting freshness it no longer has. A timestamp
	// stays true however long it is held. Absent means never confirmed.
	if match.LiveStateAt != nil {
		response["live_state_at"] = match.LiveStateAt.UTC().Format(time.RFC3339)
	}
	if match.VenueName != "" {
		response["venue_name"] = match.VenueName
	}
	if match.VenueCity != "" {
		response["venue_city"] = match.VenueCity
	}
	if match.HomeTeamLogo != "" {
		response["home_team_logo"] = match.HomeTeamLogo
	}
	if match.AwayTeamLogo != "" {
		response["away_team_logo"] = match.AwayTeamLogo
	}
	if match.LeagueSlug != "" {
		response["league_slug"] = match.LeagueSlug
	}
	if match.HomeTeamSlug != "" {
		response["home_team_slug"] = match.HomeTeamSlug
	}
	if match.AwayTeamSlug != "" {
		response["away_team_slug"] = match.AwayTeamSlug
	}
	if match.HomeScore != nil {
		response["home_score"] = *match.HomeScore
	}
	if match.AwayScore != nil {
		response["away_score"] = *match.AwayScore
	}
	if match.HalfTimeHomeScore != nil {
		response["half_time_home_score"] = *match.HalfTimeHomeScore
	}
	if match.HalfTimeAwayScore != nil {
		response["half_time_away_score"] = *match.HalfTimeAwayScore
	}
	if state := publicPredictionState(match.PredictionState); state != "" {
		response["prediction_state"] = state
	}
	if match.PredictionStateReason == "backfill_pending" || match.PredictionStateReason == "PROVIDER_CONFIRMED_NO_HISTORY" || match.PredictionStateReason == "earlier_fixture_pending" {
		response["prediction_state_reason"] = match.PredictionStateReason
	}
	// Omitted rather than sent empty when there is nothing to show: an absent
	// timeline says "no events recorded", which is the truth for a fixture that
	// has not kicked off, and keeps the payload of every upcoming match the
	// size it was before this field existed.
	if len(match.Timeline) > 0 {
		response["timeline"] = matchTimelineResponse(match.Timeline)
	}
	// Detail responses only; absent when neither team has a card record.
	if match.Discipline != nil {
		response["discipline"] = map[string]any{
			"home": teamDisciplineResponse(match.Discipline.Home),
			"away": teamDisciplineResponse(match.Discipline.Away),
		}
	}
	// Detail responses only, and only once a provider has reported something.
	// An upcoming match has no statistics and should not carry eight nulls.
	if match.Statistics != nil {
		response["statistics"] = map[string]any{
			"home": teamMatchStatsResponse(match.Statistics.Home),
			"away": teamMatchStatsResponse(match.Statistics.Away),
		}
	}
	response["odds"] = oddsPricesResponse(match.Odds)
	if match.Prediction != nil {
		updatedAt := match.Prediction.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = match.Prediction.PublishedAt
		}
		lastEvaluatedAt := match.Prediction.LastEvaluatedAt
		if lastEvaluatedAt.IsZero() {
			lastEvaluatedAt = updatedAt
		}
		response["prediction"] = map[string]any{
			"id":                match.Prediction.ID,
			"source":            matchPredictionSource(*match.Prediction),
			"market_key":        match.Prediction.MarketKey,
			"selection":         match.Prediction.Selection,
			"confidence":        match.Prediction.Confidence,
			"published_version": match.Prediction.PublishedVersion,
			"published_at":      match.Prediction.PublishedAt.UTC().Format(time.RFC3339),
			"updated_at":        updatedAt.UTC().Format(time.RFC3339),
			"last_evaluated_at": lastEvaluatedAt.UTC().Format(time.RFC3339),
			"tracker_state":     trackerStateValue(match.Prediction.TrackerState),
		}
		if match.Prediction.Probability > 0 {
			response["prediction"].(map[string]any)["probability"] = match.Prediction.Probability
		}
	}
	if len(match.Predictions) > 0 {
		predictions := make([]map[string]any, 0, len(match.Predictions))
		for _, prediction := range match.Predictions {
			predictions = append(predictions, matchPredictionSummaryResponse(prediction))
		}
		response["predictions"] = predictions
	}
	return response
}

func matchPredictionSummaryResponse(prediction MatchPredictionSummary) map[string]any {
	response := map[string]any{
		"id": prediction.ID, "market_key": prediction.MarketKey, "selection": prediction.Selection,
		"source":     matchPredictionSource(prediction),
		"confidence": prediction.Confidence, "published_version": prediction.PublishedVersion,
	}
	if prediction.Probability > 0 {
		response["probability"] = prediction.Probability
	}
	if !prediction.PublishedAt.IsZero() {
		response["published_at"] = prediction.PublishedAt.UTC().Format(time.RFC3339)
	}
	if !prediction.UpdatedAt.IsZero() {
		response["updated_at"] = prediction.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if !prediction.LastEvaluatedAt.IsZero() {
		response["last_evaluated_at"] = prediction.LastEvaluatedAt.UTC().Format(time.RFC3339)
	}
	if prediction.Outcome != "" {
		response["outcome"] = prediction.Outcome
	}
	if prediction.Actual != "" {
		response["actual"] = prediction.Actual
	}
	if prediction.VoidReason != "" {
		response["void_reason"] = prediction.VoidReason
	}
	response["tracker_state"] = trackerStateValue(prediction.TrackerState)
	return response
}

func matchPredictionSource(prediction MatchPredictionSummary) string {
	if prediction.Source == "provider" {
		return "provider"
	}
	return "engine"
}

// trackerStateValue is how a tracker verdict reaches the wire: the word, or
// null when the engine has not judged this pick yet.
//
// ALWAYS PRESENT, and null rather than omitted. Three reasons, in order of
// how much they cost to get wrong:
//
//   - null and "unknown" are different answers and a client must be able to
//     tell them apart. null is "nothing has judged this pick" -- a fixture the
//     in-play pass has never covered, or one that has not kicked off.
//     "unknown" is a verdict the engine reached: it looked, and the match
//     state cannot decide the pick either way. Showing a placeholder for the
//     first and a verdict for the second is only possible if both arrive.
//   - An omitted key cannot be told apart from an API too old to have the
//     field. Present-and-null says "we have a tracker and it has nothing to
//     say yet", which is the true statement.
//   - One convention for one field name. This value appears on the headline
//     `prediction` object and on every entry of `predictions`, and a field
//     that is sometimes absent and sometimes null is a field every client
//     handles twice.
func trackerStateValue(state string) any {
	if state == "" {
		return nil
	}
	return state
}

func publicPredictionState(state string) string {
	switch state {
	case "published", "pending":
		return state
	default:
		return ""
	}
}

func oddsPricesResponse(prices []OddsPrice) []map[string]any {
	response := make([]map[string]any, 0, len(prices))
	for _, price := range prices {
		response = append(response, map[string]any{
			"label": price.Label,
			"value": price.Value,
			"trend": price.Trend,
		})
	}
	return response
}

func matchTimelineResponse(events []MatchEvent) []map[string]any {
	response := make([]map[string]any, 0, len(events))
	for _, event := range events {
		entry := map[string]any{
			"minute":     event.Minute,
			"clock":      event.Clock,
			"event_kind": event.Kind,
			"type":       event.Type,
			"detail":     event.Detail,
			"retracted":  event.Retracted,
		}
		// Every field below is omitted when empty rather than sent as null.
		// The provider leaves all of them out routinely -- an unassisted goal,
		// a substitution with no comment -- and a payload of nulls makes an
		// ordinary event look like a partial one.
		if event.Extra != nil {
			entry["extra"] = *event.Extra
		}
		// An unresolved side is absent, not guessed. The provider's team id is
		// mapped through provider_entity_maps; a team we have never mapped
		// yields no side, and the client renders the event without claiming it
		// belongs to either team.
		if event.Side != "" {
			entry["side"] = event.Side
		}
		if event.PlayerName != "" {
			entry["player"] = event.PlayerName
		}
		if event.AssistName != "" {
			entry["assist"] = event.AssistName
		}
		if event.Comments != "" {
			entry["comments"] = event.Comments
		}
		response = append(response, entry)
	}
	return response
}

func predictionDetailResponse(prediction PredictionDetail) map[string]any {
	response := predictionSummaryResponse(prediction.PredictionSummary)
	response["probabilities"] = prediction.Probabilities
	response["input_snapshot_hash"] = prediction.InputSnapshotHash
	response["outcome"] = prediction.Outcome
	return response
}

func predictionHitRateResponse(report PredictionHitRateReport) map[string]any {
	markets := make([]map[string]any, 0, len(report.Markets))
	for _, market := range report.Markets {
		markets = append(markets, map[string]any{
			"market_key":  market.MarketKey,
			"settled":     market.Settled,
			"won":         market.Won,
			"lost":        market.Lost,
			"void":        market.Void,
			"hit_rate":    market.HitRate,
			"denominator": market.Denominator,
		})
	}
	return map[string]any{
		"settled":     report.Settled,
		"won":         report.Won,
		"lost":        report.Lost,
		"void":        report.Void,
		"hit_rate":    report.HitRate,
		"denominator": report.Denominator,
		"markets":     markets,
	}
}

func predictionResultSummaryResponse(result PredictionResultSummary) map[string]any {
	return map[string]any{
		"prediction_id":  result.PredictionID,
		"match_id":       result.MatchID,
		"match_title":    result.MatchTitle,
		"home_team":      result.HomeTeam,
		"away_team":      result.AwayTeam,
		"home_team_logo": result.HomeTeamLogo,
		"away_team_logo": result.AwayTeamLogo,
		"league_name":    result.LeagueName,
		"country":        result.Country,
		"kickoff_at":     result.KickoffAt.UTC().Format(time.RFC3339),
		"match_status":   result.MatchStatus,
		"market_key":     result.MarketKey,
		"selection":      result.Selection,
		"actual":         result.Actual,
		"outcome":        result.Outcome,
		"confidence":     result.Confidence,
	}
}

func providerStatusesResponse(statuses []ProviderStatus) []map[string]any {
	response := make([]map[string]any, 0, len(statuses))
	for _, status := range statuses {
		item := map[string]any{
			"provider":     status.Provider,
			"display_name": status.DisplayName,
			"enabled":      status.Enabled,
			"status":       status.Status,
			"quota":        providerQuotaResponse(status),
		}
		if status.ThrottleUntil != nil {
			item["throttle_until"] = status.ThrottleUntil.Format(time.RFC3339)
		}
		response = append(response, item)
	}
	return response
}

func providerQuotaResponse(status ProviderStatus) map[string]any {
	return map[string]any{
		"daily_limit":      status.Quota.DailyLimit,
		"daily_remaining":  status.Quota.DailyRemaining,
		"daily_reserve":    status.Quota.DailyReserve,
		"minute_limit":     status.Quota.MinuteLimit,
		"minute_remaining": status.Quota.MinuteRemaining,
		"minute_reserve":   status.Quota.MinuteReserve,
		"updated_at":       status.Quota.UpdatedAt.Format(time.RFC3339),
	}
}

func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return token, token != ""
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		_, _ = fmt.Fprintf(w, `{"error":"encoding_failed","code":"encoding_failed","message":"Response encoding failed."}`)
	}
}

const scalarHTML = `<!doctype html>
<html lang="en">
  <head>
    <title>Sports Prediction Platform API</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <div id="app"></div>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
    <script>
      Scalar.createApiReference('#app', {
        url: '/openapi.yaml',
        theme: 'default',
        layout: 'modern'
      })
    </script>
  </body>
</html>`

func requestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = newRequestID()
		}
		statusWriter := &statusResponseWriter{ResponseWriter: w}
		statusWriter.Header().Set("X-Request-ID", requestID)
		r = r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, requestID))
		next.ServeHTTP(statusWriter, r)
		statusCode := statusWriter.statusCode
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		slog.Info("request completed", "method", r.Method, "path", r.URL.Path, "status", statusCode, "request_id", requestID, "duration_ms", time.Since(started).Milliseconds())
	})
}

type requestIDContextKey struct{}

func requestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey{}).(string)
	return value
}

type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *statusResponseWriter) WriteHeader(statusCode int) {
	if w.statusCode != 0 {
		return
	}
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *statusResponseWriter) Write(body []byte) (int, error) {
	if w.statusCode == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func newRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return "req_" + hex.EncodeToString(buf)
}
