package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
	_ "time/tzdata"

	"github.com/redis/go-redis/v9"

	accadomain "github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/acca"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/access"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/accounts"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/admin"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/ads"
	analysisdomain "github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/analysis"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/billing"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/catalog"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/emailsettings"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/inplay"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/matches"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/ops"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/opshealth"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/entitlement"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/providers"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/readiness"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/recommendations"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/search"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/seo"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/usernotifications"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	cfg, err := config.Load(nil)
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	configureDatabasePool(db, cfg)

	redisAddress, err := readiness.RedisAddressFromURL(cfg.RedisURL)
	if err != nil {
		slog.Error("invalid redis URL", "error", err)
		os.Exit(1)
	}

	// Built once and shared by both surfaces: /readyz probes them per request
	// because it gates traffic and must fail closed on the current truth, while
	// /healthz reads them through a cache because it is unauthenticated.
	postgresChecker := readiness.SQLChecker{NameValue: opshealth.DatabaseDependency, DB: db, Timeout: 2 * time.Second}
	redisChecker := readiness.RedisChecker{Address: redisAddress, Password: readiness.RedisPasswordFromURL(cfg.RedisURL), Timeout: 2 * time.Second}
	checks := []httpserver.ReadinessChecker{postgresChecker, redisChecker}
	// /healthz is unauthenticated, so everything behind it is cached -- the
	// reachability probes as well as the freshness queries. Without that the
	// endpoint is a free database ping and socket dial for anyone who finds it.
	healthFreshness := &opshealth.Cached{
		Source: opshealth.NewRepository(db, postgresChecker, redisChecker),
		TTL:    10 * time.Second,
	}
	adminStore := admin.NewSQLAuthStore(db, admin.DialectPostgres)
	var adminAuth httpserver.AdminAuthenticator
	var adminMFAAuth httpserver.AdminMFAAuthenticator
	if cfg.AdminMFAEnabled {
		mfa := admin.NewMFAAuthService(adminStore, cfg.AdminSessionTTL, cfg.AppSecret)
		adminAuth = mfa
		adminMFAAuth = mfa
	} else {
		adminAuth = admin.NewAuthService(adminStore, cfg.AdminSessionTTL)
	}
	accountStore := accounts.NewSQLRepository(db, accounts.DialectPostgres)
	accountService := accounts.NewService(accountStore)
	accountService.ConfigureLifetimes(cfg.UserSessionTTL, cfg.AuthTokenTTL, cfg.AuthTokenTTL)
	accessStore := access.NewSQLRepository(db, access.DialectPostgres)
	guestStore := access.NewSQLGuestRepository(db, access.DialectPostgres)
	guestKey := sha256.Sum256([]byte("sokalens-guest:" + cfg.AppSecret))
	guestSigner, err := access.NewGuestSigner(guestKey[:])
	if err != nil {
		slog.Error("configure guest identity", "error", err)
		os.Exit(1)
	}
	guestService := access.NewGuestService(guestStore, guestSigner, cfg.GuestAnalysisViewLimit)
	guestService.ConfigureWindow(cfg.GuestAnalysisWindow, cfg.UserSessionTTL)
	recommendationService := recommendations.NewService(recommendations.NewSQLRepository(db, recommendations.DialectPostgres))
	var checkout billing.CheckoutProvider
	if cfg.BillingMode == "sandbox" {
		checkout = billing.SandboxCheckout{BaseURL: cfg.PublicWebURL}
	}
	billingService := billing.NewService(billing.Options{Repository: billing.NewSQLRepository(db), Checkout: checkout, Provider: cfg.BillingProvider, Environment: cfg.Environment, Mode: cfg.BillingMode, WebhookSecret: cfg.BillingWebhookSecret})
	notificationService := usernotifications.NewService(db, cfg.AppSecret)

	searchSource := search.NewSource(db).WithSchema(os.Getenv("ACCA_DB_SCHEMA"))
	if cfg.TypesenseURL != "" && cfg.TypesenseAPIKey != "" {
		searchSource = searchSource.WithTypesense(search.NewTypesenseClient(cfg.TypesenseURL, cfg.TypesenseAPIKey))
	}
	predictionSource := predictions.NewSource(db).ConfigurePublicResultDetailWindow(cfg.PublicResultDetailWindow)
	accaSource := accadomain.NewSource(db, os.Getenv("ACCA_DB_SCHEMA"))
	redisOptions, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		slog.Error("invalid redis URL", "error", err)
		os.Exit(1)
	}
	redisClient := redis.NewClient(redisOptions)
	defer redisClient.Close()
	adSignerKey := sha256.Sum256([]byte("sokalens-ads:" + cfg.AppSecret))
	adRouting := ads.NewService(
		db,
		ads.NewRedisStateStore(redisClient, os.Getenv("AD_ROUTING_REDIS_NAMESPACE")),
		adSignerKey[:],
		nil,
	)
	accaNamespace := os.Getenv("ACCA_REDIS_NAMESPACE")
	accaSourceCached := accadomain.NewCachedSource(accaSource, accadomain.RedisCache{Client: redisClient}, accaNamespace, accadomain.TTLFromEnv(os.Getenv("ACCA_REDIS_TTL_SECONDS"), 15*time.Second))
	// Read-through cache for the prediction endpoints. /v1/predictions/today
	// was recomputing an identical response up to 880 times a minute under
	// crawler load and failing 56% of them on the request deadline.
	predictionsCached := predictions.NewCachedSource(predictionSource, predictions.RedisCache{Client: redisClient}, os.Getenv("PREDICTIONS_REDIS_NAMESPACE"), predictions.CacheTTLFromEnv())
	matchSource := matches.NewSource(db).ConfigurePublicResultDetailWindow(cfg.PublicResultDetailWindow)
	// One reader shared by the point-in-time surface and the transport, so
	// "what is live" has a single definition rather than two that can disagree.
	inPlayReader := inplay.NewSource(db)
	handler := httpserver.New(httpserver.ServerOptions{
		Config:          cfg,
		ReadinessChecks: checks,
		HealthFreshness: healthFreshness,
		AdminAuth:       adminAuth,
		AdminMFAAuth:    adminMFAAuth,
		ProviderStatus:  providers.NewStatusSource(db),
		Accas:           accaSourceCached,
		// The UNCACHED source for the by-id read: its leg statuses change
		// while the matches are being played, and a cached "pending" would
		// outlive the result it describes.
		AccaSlips:           accaSource,
		Predictions:         predictionsCached,
		ProviderPredictions: predictionSource,
		Search:              searchSource,
		Coverage:            searchSource,
		Catalog:             catalog.NewSource(db),
		// Without this the in-play handler serves 503 to everyone. The worker
		// generates the predictions, the schema holds them and the entitlement
		// filter is written and tested -- this field being nil is the whole
		// reason none of it reached a reader.
		InPlay: inPlaySource{inPlayReader},
		// Public product policy: in-play reads require no account. This
		// changes only serialization access, not freshness or account rights.
		InPlayPublicRead: true,
		// The transport: the combined first load, the resumable stream and the
		// polling fallback. Nil here would serve 503 on all three -- the same
		// gap InPlay itself had, one layer out. It shares the reader above so
		// "what is live" has one definition.
		InPlayStream: inplay.NewStreamSource(db, inPlayReader),
		// The crawlable URL set, read from seo_urls rather than rebuilt from
		// reader-facing endpoints whose pagination caps had the sitemaps
		// advertising 4,462 URLs of the 21,377 that qualify.
		SEOURLs:         seo.NewSource(db),
		Ops:             ops.NewSource(db),
		Ads:             ads.NewSource(db),
		AdRouting:       adRouting,
		Matches:         matchSource,
		AdminManagement: admin.NewManagementService(db).ConfigureBilling(cfg.BillingProvider, cfg.Environment),
		EmailProvider:   emailsettings.NewRepository(db, "cloudflare"),
		Accounts:        accountService,
		Access:          accessStore,
		GuestAccess:     guestService,
		Recommendations: recommendationService,
		Billing:         billingService,
		Notifications:   notificationService,
		Analysis:        analysisdomain.NewService(db),
	})
	server := newHTTPServer(cfg, handler)

	slog.Info("starting api", "addr", server.Addr, "environment", cfg.Environment)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("api stopped", "error", err)
		os.Exit(1)
	}
}

func configureDatabasePool(db *sql.DB, cfg config.Config) {
	db.SetMaxOpenConns(cfg.DBMaxOpenConns)
	db.SetMaxIdleConns(cfg.DBMaxIdleConns)
	db.SetConnMaxLifetime(cfg.DBConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.DBConnMaxIdleTime)
}

func newHTTPServer(cfg config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		MaxHeaderBytes:    cfg.HTTPMaxHeaderBytes,
	}
}

func shutdown(ctx context.Context, server *http.Server) error {
	return server.Shutdown(ctx)
}

// inPlaySource adapts the reader's not-found sentinel to the transport's.
//
// The two packages each own an error for "this fixture is not in play" and
// neither should import the other for it: the reader must not depend on the
// HTTP layer, and the HTTP layer must not depend on a particular reader. So
// the translation lives here, at the one place that already knows about both.
//
// It matters that this is a translation and not a collapse into a generic
// error. A finished match is not an outage, and if it arrived at the handler
// as one the API would report a failure every time a game ended.
type inPlaySource struct {
	*inplay.Source
}

func (s inPlaySource) GetInPlayFixture(ctx context.Context, fixtureID int64) (entitlement.FixtureState, error) {
	state, err := s.Source.GetInPlayFixture(ctx, fixtureID)
	if errors.Is(err, inplay.ErrNotFound) {
		return state, httpserver.ErrInPlayFixtureNotFound
	}
	return state, err
}
