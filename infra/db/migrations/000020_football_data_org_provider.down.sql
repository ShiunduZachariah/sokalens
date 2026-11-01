DELETE FROM provider_feature_sources
WHERE provider = 'football-data-org';

DELETE FROM provider_endpoint_specs
WHERE provider = 'football-data-org';

DELETE FROM provider_rate_limits
WHERE provider = 'football-data-org';

DELETE FROM providers
WHERE provider = 'football-data-org'
  AND NOT EXISTS (
    SELECT 1
    FROM provider_raw_payloads
    WHERE provider_raw_payloads.provider = providers.provider
  )
  AND NOT EXISTS (
    SELECT 1
    FROM provider_entity_maps
    WHERE provider_entity_maps.provider = providers.provider
  );
