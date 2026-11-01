package ingestion

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

type ProviderClient interface {
	Get(context.Context, string, url.Values) (provider.Response, error)
}

type requestLogger interface {
	Insert(context.Context, RequestLog) error
}

type entityMapWriter interface {
	Upsert(context.Context, ProviderEntityMap) error
}

type rawPayloadWriter interface {
	InsertMetadata(context.Context, RawPayloadMetadata) error
}

type rateLimitWriter interface {
	UpdateFromHeaders(context.Context, ProviderRateLimitUpdate) error
}

type CountriesJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	EntityMaps  entityMapWriter
	RawPayloads rawPayloadWriter
	RateLimits  rateLimitWriter
	Catalog     countryCatalogWriter
	Reference   countryFlagWriter
	Now         func() time.Time
}

type CountriesJob struct {
	opts CountriesJobOptions
}

type countryCatalogWriter interface {
	UpsertCountry(context.Context, CountryRecord) error
}

type countryFlagWriter interface {
	ApplyCountryFlag(context.Context, string, string) error
}

func NewCountriesJob(opts CountriesJobOptions) CountriesJob {
	return CountriesJob{opts: opts}
}

func (j CountriesJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("countries job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("countries job request log repository is required")
	}
	if j.opts.EntityMaps == nil {
		return errors.New("countries job entity map repository is required")
	}
	now := time.Now().UTC
	if j.opts.Now != nil {
		now = j.opts.Now
	}

	resp, err := j.opts.Client.Get(ctx, "countries", url.Values{})
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, apifootball.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "countries"),
		Method:        "GET",
		StatusCode:    resp.StatusCode,
		QuotaCost:     providerQuotaCost(resp),
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		RateLimit:     resp.RateLimit,
		RequestedAt:   now(),
	}
	if err != nil {
		log.ErrorCategory = categorizeProviderError(resp, err)
		if logErr := j.opts.RequestLogs.Insert(ctx, log); logErr != nil {
			return logErr
		}
		return err
	}
	if err := j.opts.RequestLogs.Insert(ctx, log); err != nil {
		return err
	}
	if j.opts.RateLimits != nil {
		if err := j.opts.RateLimits.UpdateFromHeaders(ctx, ProviderRateLimitUpdate{
			Provider:        apifootball.ProviderName,
			DailyLimit:      resp.RateLimit.DailyLimit,
			DailyRemaining:  resp.RateLimit.DailyRemaining,
			MinuteLimit:     resp.RateLimit.MinuteLimit,
			MinuteRemaining: resp.RateLimit.MinuteRemaining,
			UpdatedAt:       now(),
		}); err != nil {
			return err
		}
	}
	if j.opts.RawPayloads != nil {
		if err := j.opts.RawPayloads.InsertMetadata(ctx, RawPayloadMetadata{
			Provider:      apifootball.ProviderName,
			EndpointKey:   "countries",
			StatusCode:    resp.StatusCode,
			ResponseHash:  ResponseHash(resp.Body),
			ResponseBytes: resp.ResponseBytes,
			Headers:       resp.RateLimit.Raw,
			StoragePolicy: "metadata_only",
			FetchedAt:     now(),
		}); err != nil {
			return err
		}
	}

	countries, err := apifootball.ParseCountries(resp.Body)
	if err != nil {
		return err
	}
	for _, country := range countries {
		canonicalCountry, catalogable := canonicalCountryRecord(country)
		if j.opts.Catalog != nil && catalogable {
			if err := j.opts.Catalog.UpsertCountry(ctx, canonicalCountry); err != nil {
				return err
			}
		}
		if j.opts.Reference != nil && catalogable {
			if err := j.opts.Reference.ApplyCountryFlag(ctx, canonicalCountry.Code, country.Flag); err != nil {
				return err
			}
		}
		mapping := ProviderEntityMap{
			Provider:          apifootball.ProviderName,
			EntityType:        "country",
			ProviderEntityID:  countryProviderID(country),
			ProviderEntityKey: country.Name,
			MatchStatus:       "unresolved",
			Metadata: map[string]any{
				"name":           country.Name,
				"code":           country.Code,
				"flag":           country.Flag,
				"canonical_code": canonicalCountry.Code,
			},
			SeenAt: now(),
		}
		if err := j.opts.EntityMaps.Upsert(ctx, mapping); err != nil {
			return err
		}
	}
	slog.Info("provider normalization completed",
		"provider", apifootball.ProviderName,
		"endpoint", "countries",
		"job_id", job.ID,
		"records_fetched", len(countries),
		"records_normalized", len(countries),
	)
	return nil
}

func canonicalCountryRecord(country apifootball.Country) (CountryRecord, bool) {
	code := strings.ToUpper(strings.TrimSpace(country.Code))
	name := strings.TrimSpace(country.Name)
	if sovereignCode, ok := subdivisionCountryCode(code, name); ok {
		return CountryRecord{Code: sovereignCode, Name: "United Kingdom"}, true
	}
	if !isISO2Code(code) || name == "" {
		return CountryRecord{}, false
	}
	return CountryRecord{Code: code, Name: name}, true
}

func subdivisionCountryCode(code, name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "england", "scotland", "wales", "northern ireland", "northern-ireland":
		return "GB", true
	}
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "ENG", "SCO", "WAL", "NIR":
		return "GB", true
	default:
		return "", false
	}
}

func isISO2Code(code string) bool {
	if len(code) != 2 {
		return false
	}
	return code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z'
}

func countryProviderID(country apifootball.Country) string {
	if country.Code != "" {
		return country.Code
	}
	return "name:" + slug(country.Name)
}

func slug(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func categorizeProviderError(resp provider.Response, err error) string {
	var providerErr *apifootball.ProviderError
	if errors.As(err, &providerErr) && strings.TrimSpace(providerErr.Category) != "" {
		return strings.ToLower(strings.TrimSpace(providerErr.Category))
	}
	if errors.Is(err, apifootball.ErrDailyQuotaReserved) || errors.Is(err, apifootball.ErrRateLimitDeferred) {
		return "rate_limit"
	}
	if resp.StatusCode >= 400 {
		return "provider_status"
	}
	if err != nil {
		return "provider_transport"
	}
	return ""
}

func providerQuotaCost(resp provider.Response) int {
	if resp.StatusCode <= 0 {
		return 0
	}
	return 1
}

func nonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func CountriesJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "countries")
}
