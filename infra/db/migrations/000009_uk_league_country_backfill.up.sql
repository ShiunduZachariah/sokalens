UPDATE leagues AS l
SET country_code = 'GB'
FROM provider_entity_maps AS pem
WHERE pem.provider = 'api-football'
  AND pem.entity_type = 'league'
  AND pem.retired_at IS NULL
  AND pem.canonical_entity_type = 'league'
  AND pem.canonical_entity_id ~ '^[0-9]+$'
  AND lower(NULLIF(pem.metadata->>'country', '')) IN ('england', 'scotland', 'wales', 'northern-ireland')
  AND l.id = pem.canonical_entity_id::bigint
  AND l.country_code IS NULL;
