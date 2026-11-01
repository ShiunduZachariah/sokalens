DELETE FROM providers
WHERE provider = 'thesportsdb'
  AND NOT EXISTS (
    SELECT 1
    FROM provider_entity_maps
    WHERE provider_entity_maps.provider = providers.provider
  );
