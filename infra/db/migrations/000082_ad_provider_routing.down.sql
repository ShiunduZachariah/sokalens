DROP INDEX IF EXISTS idx_ad_provider_outcomes_occurred_at;
DROP INDEX IF EXISTS idx_ad_provider_outcomes_placement_recent;
DROP INDEX IF EXISTS idx_ad_provider_outcomes_session_recent;
DROP INDEX IF EXISTS idx_ad_provider_outcomes_provider_failures;
DROP INDEX IF EXISTS idx_ad_provider_outcomes_provider_recent;
DROP TABLE IF EXISTS ad_provider_outcomes;

DROP INDEX IF EXISTS idx_ad_providers_routing;
DROP TABLE IF EXISTS ad_providers;
