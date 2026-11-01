DROP INDEX IF EXISTS idx_provider_entity_maps_unresolved;
DROP INDEX IF EXISTS idx_provider_entity_maps_canonical;
DROP INDEX IF EXISTS idx_provider_entity_maps_active_external_id;
DROP TABLE IF EXISTS provider_entity_maps;

DROP INDEX IF EXISTS idx_provider_raw_payloads_recent;
DROP TABLE IF EXISTS provider_raw_payloads;

DROP TABLE IF EXISTS provider_feature_sources;

DROP INDEX IF EXISTS idx_provider_endpoint_specs_enabled;
DROP TABLE IF EXISTS provider_endpoint_specs;

DROP TABLE IF EXISTS providers;
