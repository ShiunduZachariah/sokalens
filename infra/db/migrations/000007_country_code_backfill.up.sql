UPDATE leagues AS l
SET country_code = c.code
FROM provider_entity_maps AS pem
JOIN countries AS c
  ON c.code = NULLIF(UPPER(pem.metadata->>'code'), '')
WHERE pem.provider = 'api-football'
  AND pem.entity_type = 'league'
  AND pem.retired_at IS NULL
  AND pem.canonical_entity_type = 'league'
  AND pem.canonical_entity_id ~ '^[0-9]+$'
  AND l.id = pem.canonical_entity_id::bigint
  AND l.country_code IS NULL;

UPDATE leagues AS l
SET country_code = c.code
FROM provider_entity_maps AS pem
JOIN countries AS c
  ON lower(c.name) = lower(NULLIF(pem.metadata->>'country', ''))
WHERE pem.provider = 'api-football'
  AND pem.entity_type = 'league'
  AND pem.retired_at IS NULL
  AND pem.canonical_entity_type = 'league'
  AND pem.canonical_entity_id ~ '^[0-9]+$'
  AND l.id = pem.canonical_entity_id::bigint
  AND l.country_code IS NULL;

UPDATE teams AS t
SET country_code = c.code
FROM provider_entity_maps AS pem
JOIN countries AS c
  ON c.code = COALESCE(
    NULLIF(UPPER(pem.metadata->>'country_code'), ''),
    NULLIF(UPPER(pem.metadata->>'code'), '')
  )
WHERE pem.provider = 'api-football'
  AND pem.entity_type = 'team'
  AND pem.retired_at IS NULL
  AND pem.canonical_entity_type = 'team'
  AND pem.canonical_entity_id ~ '^[0-9]+$'
  AND t.id = pem.canonical_entity_id::bigint
  AND t.country_code IS NULL;

UPDATE teams AS t
SET country_code = c.code
FROM provider_entity_maps AS pem
JOIN countries AS c
  ON lower(c.name) = lower(COALESCE(NULLIF(pem.metadata->>'country', ''), NULLIF(pem.metadata->>'country_name', '')))
WHERE pem.provider = 'api-football'
  AND pem.entity_type = 'team'
  AND pem.retired_at IS NULL
  AND pem.canonical_entity_type = 'team'
  AND pem.canonical_entity_id ~ '^[0-9]+$'
  AND t.id = pem.canonical_entity_id::bigint
  AND t.country_code IS NULL;
