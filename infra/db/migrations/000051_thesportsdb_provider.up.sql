INSERT INTO providers (
  provider,
  display_name,
  base_url
) VALUES (
  'thesportsdb',
  'TheSportsDB',
  'https://www.thesportsdb.com/api/v1/json'
) ON CONFLICT (provider) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  base_url = EXCLUDED.base_url,
  updated_at = now();
