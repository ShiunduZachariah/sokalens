INSERT INTO countries (code, name)
SELECT DISTINCT
  UPPER(NULLIF(pem.metadata->>'code', '')) AS code,
  NULLIF(pem.metadata->>'name', '') AS name
FROM provider_entity_maps AS pem
WHERE pem.provider = 'api-football'
  AND pem.entity_type = 'country'
  AND pem.retired_at IS NULL
  AND NULLIF(pem.metadata->>'code', '') IS NOT NULL
  AND length(NULLIF(pem.metadata->>'code', '')) = 2
  AND NULLIF(pem.metadata->>'name', '') IS NOT NULL
ON CONFLICT (code)
DO UPDATE SET name = EXCLUDED.name;

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

UPDATE leagues AS l
SET country_code = c.code
FROM provider_entity_maps AS pem
JOIN countries AS c
  ON c.code = 'GB'
WHERE pem.provider = 'api-football'
  AND pem.entity_type = 'league'
  AND pem.retired_at IS NULL
  AND pem.canonical_entity_type = 'league'
  AND pem.canonical_entity_id ~ '^[0-9]+$'
  AND lower(NULLIF(pem.metadata->>'country', '')) IN ('england', 'scotland', 'wales', 'northern-ireland')
  AND l.id = pem.canonical_entity_id::bigint
  AND l.country_code IS NULL;
