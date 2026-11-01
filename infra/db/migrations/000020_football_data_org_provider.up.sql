INSERT INTO providers (
  provider,
  display_name,
  base_url
) VALUES (
  'football-data-org',
  'football-data.org',
  'https://api.football-data.org/v4'
) ON CONFLICT (provider) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  base_url = EXCLUDED.base_url,
  updated_at = now();

INSERT INTO provider_rate_limits (
  provider,
  daily_limit,
  daily_remaining,
  minute_limit,
  minute_remaining,
  daily_reserve,
  minute_reserve
) VALUES (
  'football-data-org',
  0,
  0,
  10,
  10,
  0,
  1
) ON CONFLICT (provider) DO UPDATE SET
  minute_limit = EXCLUDED.minute_limit,
  minute_remaining = LEAST(provider_rate_limits.minute_remaining, EXCLUDED.minute_remaining),
  minute_reserve = EXCLUDED.minute_reserve,
  updated_at = now();

INSERT INTO provider_endpoint_specs (
  provider,
  endpoint_key,
  endpoint_group,
  path_template,
  params_schema,
  default_queue,
  default_priority,
  notes
) VALUES
  ('football-data-org', 'competitions', 'reference', 'competitions', '{"optional":["areas"]}', 'provider-reference', 20, 'Free plan returns the authenticated client competition catalog, currently 12 competitions.'),
  ('football-data-org', 'matches', 'fixtures', 'matches', '{"optional":["ids","date","dateFrom","dateTo","status"]}', 'provider-fixtures', 30, 'Daily match list for subscribed competitions. Scores may be delayed on the free plan.'),
  ('football-data-org', 'competition.matches', 'fixtures', 'competitions/{competition}/matches', '{"optional":["season","matchday","status","dateFrom","dateTo","stage","group"]}', 'provider-fixtures', 35, 'Competition-scoped fixture and historical match access where the free plan allows the competition/season.'),
  ('football-data-org', 'competition.teams', 'reference', 'competitions/{competition}/teams', '{"optional":["season"]}', 'provider-reference', 40, 'Competition team list for canonical team mapping.'),
  ('football-data-org', 'standings', 'performance', 'competitions/{competition}/standings', '{"optional":["season","matchday","date"]}', 'provider-performance', 50, 'League tables are available on the free plan; historical standings are limited by endpoint support.'),
  ('football-data-org', 'scorers', 'performance', 'competitions/{competition}/scorers', '{"optional":["season","matchday","limit"]}', 'provider-performance', 60, 'Scorers are deep-data/plan-dependent; treat 403 as unavailable and leave scorer/stat fields nullable.'),
  ('football-data-org', 'match.detail', 'fixtures', 'matches/{id}', '{}', 'provider-fixtures', 70, 'Detailed lineups, bookings, statistics, odds, and referees are plan/add-on dependent; free-plan gaps must not break normalization.')
ON CONFLICT (provider, endpoint_key) DO UPDATE SET
  endpoint_group = EXCLUDED.endpoint_group,
  path_template = EXCLUDED.path_template,
  params_schema = EXCLUDED.params_schema,
  default_queue = EXCLUDED.default_queue,
  default_priority = EXCLUDED.default_priority,
  notes = EXCLUDED.notes,
  updated_at = now();

INSERT INTO provider_feature_sources (
  provider,
  feature_key,
  endpoint_key,
  notes
) VALUES
  ('football-data-org', 'over_under_goals', 'matches', 'Supported from normalized full-time scores for finished matches; no free-plan odds lines.'),
  ('football-data-org', 'team_over_under_goals', 'matches', 'Supported from nullable home/away full-time scores where available.'),
  ('football-data-org', 'half_time_full_time', 'matches', 'Supported only when halftime and full-time score blocks are populated.'),
  ('football-data-org', 'corners', 'match.detail', 'Statistic add-on dependent; leave unavailable on the free plan and use API-Football later if needed.'),
  ('football-data-org', 'cards', 'match.detail', 'Deep-data/statistic add-on dependent; leave unavailable on the free plan and use API-Football later if needed.'),
  ('football-data-org', 'fouls', 'match.detail', 'Statistic add-on dependent; leave unavailable on the free plan and use API-Football later if needed.'),
  ('football-data-org', 'throw_ins', 'match.detail', 'Statistic add-on dependent; leave unavailable on the free plan and use API-Football later if needed.')
ON CONFLICT (provider, feature_key, endpoint_key) DO UPDATE SET
  notes = EXCLUDED.notes,
  updated_at = now();
