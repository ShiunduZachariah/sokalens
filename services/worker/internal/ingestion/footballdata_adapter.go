package ingestion

import (
	"database/sql"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/footballdata"
)

func NewFootballDataAdapter(db *sql.DB, client ProviderClient, jobRepo jobs.Repository) ProviderAdapter {
	return ProviderAdapter{
		Provider: footballdata.ProviderName,
		Queues: map[string]map[string]JobHandler{
			DefaultProviderQueue: {
				FootballDataCompetitionsJobKey(): NewFootballDataCompetitionsJob(FootballDataCompetitionsJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Catalog:     NewCatalogRepository(db),
					Jobs:        jobRepo,
				}),
				FootballDataTeamsJobKey(): NewFootballDataTeamsJob(FootballDataTeamsJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Catalog:     NewCatalogRepository(db),
				}),
			},
			"provider-fixtures": {
				FootballDataMatchesJobKey(): NewFootballDataMatchesJob(FootballDataMatchesJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Catalog:     NewCatalogRepository(db),
					Jobs:        jobRepo,
				}),
			},
		},
	}
}
