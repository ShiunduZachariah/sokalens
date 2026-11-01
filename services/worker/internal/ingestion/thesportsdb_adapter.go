package ingestion

import (
	"database/sql"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/thesportsdb"
)

// NewTheSportsDBAdapter registers the TheSportsDB reference-sync logo backfill
// on the default provider queue. The optional maxPerCycle bounds how many
// canonical teams/leagues a single run touches; the zero value falls back to
// the default (20).
func NewTheSportsDBAdapter(db *sql.DB, client ProviderClient, jobRepo jobs.Repository, maxPerCycle ...int) ProviderAdapter {
	perCycle := 0
	if len(maxPerCycle) > 0 {
		perCycle = maxPerCycle[0]
	}
	return ProviderAdapter{
		Provider: thesportsdb.ProviderName,
		Queues: map[string]map[string]JobHandler{
			DefaultProviderQueue: {
				TheSportsDBReferenceSyncJobKey(): NewTheSportsDBReferenceSyncJob(TheSportsDBReferenceSyncJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					Reference:   NewReferenceSyncRepository(db),
					Selector:    NewTheSportsDBRepository(db),
					MaxPerCycle: perCycle,
				}),
			},
			"provider-fixtures": {
				TheSportsDBTeamHistoryJobKey(): NewTheSportsDBTeamHistoryJob(TheSportsDBTeamHistoryJobOptions{
					Client: client, RequestLogs: NewRequestLogRepository(db), Catalog: NewCatalogRepository(db),
					EntityMaps: NewProviderEntityMapRepository(db),
				}),
			},
		},
	}
}
