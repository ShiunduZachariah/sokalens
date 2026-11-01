CREATE TABLE IF NOT EXISTS fixtures (
  id BIGINT PRIMARY KEY REFERENCES matches(id) ON DELETE CASCADE,
  league_id BIGINT NOT NULL REFERENCES leagues(id),
  season_id INTEGER NOT NULL CHECK (season_id > 0),
  home_team_id BIGINT NOT NULL REFERENCES teams(id),
  away_team_id BIGINT NOT NULL REFERENCES teams(id),
  kickoff_at TIMESTAMPTZ NOT NULL,
  status TEXT NOT NULL,
  venue_name TEXT,
  referee TEXT,
  round TEXT,
  home_goals_ft INTEGER CHECK (home_goals_ft IS NULL OR home_goals_ft >= 0),
  away_goals_ft INTEGER CHECK (away_goals_ft IS NULL OR away_goals_ft >= 0),
  home_goals_ht INTEGER CHECK (home_goals_ht IS NULL OR home_goals_ht >= 0),
  away_goals_ht INTEGER CHECK (away_goals_ht IS NULL OR away_goals_ht >= 0),
  source_match_id BIGINT REFERENCES matches(id) ON DELETE SET NULL,
  source TEXT NOT NULL DEFAULT 'matches_backfill',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_fixtures_league_season_kickoff
  ON fixtures (league_id, season_id, kickoff_at);

CREATE INDEX IF NOT EXISTS idx_fixtures_home_team
  ON fixtures (home_team_id, kickoff_at DESC);

CREATE INDEX IF NOT EXISTS idx_fixtures_away_team
  ON fixtures (away_team_id, kickoff_at DESC);

CREATE INDEX IF NOT EXISTS idx_fixtures_status_kickoff
  ON fixtures (status, kickoff_at);

CREATE TABLE IF NOT EXISTS fixture_statistics (
  fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,
  team_id BIGINT NOT NULL REFERENCES teams(id),
  shots_on_target INTEGER CHECK (shots_on_target IS NULL OR shots_on_target >= 0),
  shots_total INTEGER CHECK (shots_total IS NULL OR shots_total >= 0),
  shots_blocked INTEGER CHECK (shots_blocked IS NULL OR shots_blocked >= 0),
  shots_inside_box INTEGER CHECK (shots_inside_box IS NULL OR shots_inside_box >= 0),
  shots_outside_box INTEGER CHECK (shots_outside_box IS NULL OR shots_outside_box >= 0),
  possession_pct NUMERIC(5,2) CHECK (possession_pct IS NULL OR (possession_pct >= 0 AND possession_pct <= 100)),
  corners INTEGER CHECK (corners IS NULL OR corners >= 0),
  fouls INTEGER CHECK (fouls IS NULL OR fouls >= 0),
  offsides INTEGER CHECK (offsides IS NULL OR offsides >= 0),
  yellow_cards INTEGER CHECK (yellow_cards IS NULL OR yellow_cards >= 0),
  red_cards INTEGER CHECK (red_cards IS NULL OR red_cards >= 0),
  goalkeeper_saves INTEGER CHECK (goalkeeper_saves IS NULL OR goalkeeper_saves >= 0),
  passes_total INTEGER CHECK (passes_total IS NULL OR passes_total >= 0),
  passes_accurate INTEGER CHECK (passes_accurate IS NULL OR passes_accurate >= 0),
  pass_accuracy NUMERIC(5,2) CHECK (pass_accuracy IS NULL OR (pass_accuracy >= 0 AND pass_accuracy <= 100)),
  xg NUMERIC(6,3) CHECK (xg IS NULL OR xg >= 0),
  card_points INTEGER CHECK (card_points IS NULL OR card_points >= 0),
  source TEXT NOT NULL DEFAULT 'matches_backfill',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (fixture_id, team_id)
);

CREATE INDEX IF NOT EXISTS idx_fixture_statistics_team
  ON fixture_statistics (team_id, fixture_id);

CREATE TABLE IF NOT EXISTS team_season_stats (
  team_id BIGINT NOT NULL REFERENCES teams(id),
  league_id BIGINT NOT NULL REFERENCES leagues(id),
  season_id INTEGER NOT NULL CHECK (season_id > 0),
  played_home INTEGER NOT NULL DEFAULT 0 CHECK (played_home >= 0),
  wins_home INTEGER NOT NULL DEFAULT 0 CHECK (wins_home >= 0),
  draws_home INTEGER NOT NULL DEFAULT 0 CHECK (draws_home >= 0),
  losses_home INTEGER NOT NULL DEFAULT 0 CHECK (losses_home >= 0),
  goals_for_home INTEGER NOT NULL DEFAULT 0 CHECK (goals_for_home >= 0),
  goals_against_home INTEGER NOT NULL DEFAULT 0 CHECK (goals_against_home >= 0),
  clean_sheets_home INTEGER NOT NULL DEFAULT 0 CHECK (clean_sheets_home >= 0),
  failed_to_score_home INTEGER NOT NULL DEFAULT 0 CHECK (failed_to_score_home >= 0),
  played_away INTEGER NOT NULL DEFAULT 0 CHECK (played_away >= 0),
  wins_away INTEGER NOT NULL DEFAULT 0 CHECK (wins_away >= 0),
  draws_away INTEGER NOT NULL DEFAULT 0 CHECK (draws_away >= 0),
  losses_away INTEGER NOT NULL DEFAULT 0 CHECK (losses_away >= 0),
  goals_for_away INTEGER NOT NULL DEFAULT 0 CHECK (goals_for_away >= 0),
  goals_against_away INTEGER NOT NULL DEFAULT 0 CHECK (goals_against_away >= 0),
  clean_sheets_away INTEGER NOT NULL DEFAULT 0 CHECK (clean_sheets_away >= 0),
  failed_to_score_away INTEGER NOT NULL DEFAULT 0 CHECK (failed_to_score_away >= 0),
  source TEXT NOT NULL DEFAULT 'matches_backfill',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (team_id, league_id, season_id)
);

CREATE INDEX IF NOT EXISTS idx_team_season_stats_league
  ON team_season_stats (league_id, season_id);

CREATE TABLE IF NOT EXISTS head_to_head (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  home_team_id BIGINT NOT NULL REFERENCES teams(id),
  away_team_id BIGINT NOT NULL REFERENCES teams(id),
  fixture_id BIGINT NOT NULL UNIQUE REFERENCES fixtures(id) ON DELETE CASCADE,
  played_at TIMESTAMPTZ NOT NULL,
  home_goals INTEGER NOT NULL CHECK (home_goals >= 0),
  away_goals INTEGER NOT NULL CHECK (away_goals >= 0),
  venue TEXT,
  source TEXT NOT NULL DEFAULT 'matches_backfill',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_head_to_head_pair_played
  ON head_to_head (home_team_id, away_team_id, played_at DESC);

CREATE TABLE IF NOT EXISTS standings (
  league_id BIGINT NOT NULL REFERENCES leagues(id),
  season_id INTEGER NOT NULL CHECK (season_id > 0),
  team_id BIGINT NOT NULL REFERENCES teams(id),
  rank INTEGER NOT NULL CHECK (rank > 0),
  points INTEGER NOT NULL DEFAULT 0,
  played INTEGER NOT NULL DEFAULT 0 CHECK (played >= 0),
  wins INTEGER NOT NULL DEFAULT 0 CHECK (wins >= 0),
  draws INTEGER NOT NULL DEFAULT 0 CHECK (draws >= 0),
  losses INTEGER NOT NULL DEFAULT 0 CHECK (losses >= 0),
  goals_for INTEGER NOT NULL DEFAULT 0 CHECK (goals_for >= 0),
  goals_against INTEGER NOT NULL DEFAULT 0 CHECK (goals_against >= 0),
  goal_diff INTEGER NOT NULL DEFAULT 0,
  form TEXT,
  source TEXT NOT NULL DEFAULT 'matches_backfill',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (league_id, season_id, team_id)
);

CREATE INDEX IF NOT EXISTS idx_standings_team
  ON standings (team_id, league_id, season_id);

CREATE TABLE IF NOT EXISTS player_availability (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,
  team_id BIGINT NOT NULL REFERENCES teams(id),
  player_name TEXT NOT NULL,
  reason TEXT NOT NULL,
  status TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT 'provider_ingestion',
  captured_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (fixture_id, team_id, player_name, reason)
);

CREATE INDEX IF NOT EXISTS idx_player_availability_fixture_team
  ON player_availability (fixture_id, team_id);

CREATE TABLE IF NOT EXISTS odds (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,
  bookmaker TEXT NOT NULL,
  market TEXT NOT NULL,
  outcome TEXT NOT NULL,
  value NUMERIC(12,4) NOT NULL CHECK (value > 0),
  implied_prob NUMERIC(9,6) CHECK (implied_prob IS NULL OR (implied_prob > 0 AND implied_prob < 1)),
  fetched_at TIMESTAMPTZ NOT NULL,
  source_snapshot_id BIGINT REFERENCES odds_snapshots(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (source_snapshot_id, outcome)
);

CREATE INDEX IF NOT EXISTS idx_odds_fixture_market
  ON odds (fixture_id, market, fetched_at DESC);

CREATE INDEX IF NOT EXISTS idx_odds_bookmaker
  ON odds (bookmaker, fetched_at DESC);

CREATE TABLE IF NOT EXISTS referee_stats (
  name TEXT PRIMARY KEY,
  matches_officiated INTEGER NOT NULL DEFAULT 0 CHECK (matches_officiated >= 0),
  avg_yellow_cards NUMERIC(6,3) CHECK (avg_yellow_cards IS NULL OR avg_yellow_cards >= 0),
  avg_red_cards NUMERIC(6,3) CHECK (avg_red_cards IS NULL OR avg_red_cards >= 0),
  avg_fouls NUMERIC(6,3) CHECK (avg_fouls IS NULL OR avg_fouls >= 0),
  source TEXT NOT NULL DEFAULT 'provider_ingestion',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS league_coverage (
  league_id BIGINT NOT NULL REFERENCES leagues(id),
  season_id INTEGER NOT NULL CHECK (season_id > 0),
  has_statistics BOOLEAN NOT NULL DEFAULT false,
  has_xg BOOLEAN NOT NULL DEFAULT false,
  has_injuries BOOLEAN NOT NULL DEFAULT false,
  has_odds BOOLEAN NOT NULL DEFAULT false,
  has_lineups BOOLEAN NOT NULL DEFAULT false,
  source TEXT NOT NULL DEFAULT 'derived_backfill',
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (league_id, season_id)
);

CREATE TABLE IF NOT EXISTS api_predictions (
  fixture_id BIGINT PRIMARY KEY REFERENCES fixtures(id) ON DELETE CASCADE,
  predicted_winner TEXT,
  win_or_draw BOOLEAN,
  under_over TEXT,
  percent_home NUMERIC(6,3) CHECK (percent_home IS NULL OR (percent_home >= 0 AND percent_home <= 100)),
  percent_draw NUMERIC(6,3) CHECK (percent_draw IS NULL OR (percent_draw >= 0 AND percent_draw <= 100)),
  percent_away NUMERIC(6,3) CHECK (percent_away IS NULL OR (percent_away >= 0 AND percent_away <= 100)),
  advice TEXT,
  source_benchmark_id BIGINT REFERENCES provider_prediction_benchmarks(id) ON DELETE SET NULL,
  captured_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO fixtures (
  id, league_id, season_id, home_team_id, away_team_id, kickoff_at, status,
  venue_name, referee, round, home_goals_ft, away_goals_ft, home_goals_ht, away_goals_ht,
  source_match_id, updated_at
)
SELECT
  m.id,
  m.league_id,
  EXTRACT(YEAR FROM m.kickoff_at AT TIME ZONE 'UTC')::integer,
  m.home_team_id,
  m.away_team_id,
  m.kickoff_at,
  m.status,
  NULLIF(BTRIM(m.venue_name), ''),
  NULL::text,
  NULL::text,
  m.home_score,
  m.away_score,
  m.half_time_home_score,
  m.half_time_away_score,
  m.id,
  now()
FROM matches AS m
ON CONFLICT (id) DO UPDATE SET
  league_id = EXCLUDED.league_id,
  season_id = EXCLUDED.season_id,
  home_team_id = EXCLUDED.home_team_id,
  away_team_id = EXCLUDED.away_team_id,
  kickoff_at = EXCLUDED.kickoff_at,
  status = EXCLUDED.status,
  venue_name = EXCLUDED.venue_name,
  home_goals_ft = EXCLUDED.home_goals_ft,
  away_goals_ft = EXCLUDED.away_goals_ft,
  home_goals_ht = EXCLUDED.home_goals_ht,
  away_goals_ht = EXCLUDED.away_goals_ht,
  source_match_id = EXCLUDED.source_match_id,
  updated_at = now();

INSERT INTO fixture_statistics (
  fixture_id, team_id, corners, fouls, card_points, source, updated_at
)
SELECT m.id, m.home_team_id, m.home_corners, m.home_fouls, m.home_cards, 'matches_backfill', now()
FROM matches AS m
UNION ALL
SELECT m.id, m.away_team_id, m.away_corners, m.away_fouls, m.away_cards, 'matches_backfill', now()
FROM matches AS m
ON CONFLICT (fixture_id, team_id) DO UPDATE SET
  corners = EXCLUDED.corners,
  fouls = EXCLUDED.fouls,
  card_points = EXCLUDED.card_points,
  updated_at = now();

WITH fixture_teams AS (
  SELECT DISTINCT home_team_id AS team_id, league_id, EXTRACT(YEAR FROM kickoff_at AT TIME ZONE 'UTC')::integer AS season_id
  FROM matches
  UNION
  SELECT DISTINCT away_team_id AS team_id, league_id, EXTRACT(YEAR FROM kickoff_at AT TIME ZONE 'UTC')::integer AS season_id
  FROM matches
),
home_stats AS (
  SELECT
    home_team_id AS team_id,
    league_id,
    EXTRACT(YEAR FROM kickoff_at AT TIME ZONE 'UTC')::integer AS season_id,
    COUNT(*) FILTER (WHERE home_score IS NOT NULL AND away_score IS NOT NULL)::integer AS played_home,
    COUNT(*) FILTER (WHERE home_score > away_score)::integer AS wins_home,
    COUNT(*) FILTER (WHERE home_score = away_score AND home_score IS NOT NULL)::integer AS draws_home,
    COUNT(*) FILTER (WHERE home_score < away_score)::integer AS losses_home,
    COALESCE(SUM(home_score) FILTER (WHERE home_score IS NOT NULL), 0)::integer AS goals_for_home,
    COALESCE(SUM(away_score) FILTER (WHERE away_score IS NOT NULL), 0)::integer AS goals_against_home,
    COUNT(*) FILTER (WHERE away_score = 0 AND home_score IS NOT NULL)::integer AS clean_sheets_home,
    COUNT(*) FILTER (WHERE home_score = 0 AND away_score IS NOT NULL)::integer AS failed_to_score_home
  FROM matches
  GROUP BY home_team_id, league_id, EXTRACT(YEAR FROM kickoff_at AT TIME ZONE 'UTC')::integer
),
away_stats AS (
  SELECT
    away_team_id AS team_id,
    league_id,
    EXTRACT(YEAR FROM kickoff_at AT TIME ZONE 'UTC')::integer AS season_id,
    COUNT(*) FILTER (WHERE home_score IS NOT NULL AND away_score IS NOT NULL)::integer AS played_away,
    COUNT(*) FILTER (WHERE away_score > home_score)::integer AS wins_away,
    COUNT(*) FILTER (WHERE away_score = home_score AND away_score IS NOT NULL)::integer AS draws_away,
    COUNT(*) FILTER (WHERE away_score < home_score)::integer AS losses_away,
    COALESCE(SUM(away_score) FILTER (WHERE away_score IS NOT NULL), 0)::integer AS goals_for_away,
    COALESCE(SUM(home_score) FILTER (WHERE home_score IS NOT NULL), 0)::integer AS goals_against_away,
    COUNT(*) FILTER (WHERE home_score = 0 AND away_score IS NOT NULL)::integer AS clean_sheets_away,
    COUNT(*) FILTER (WHERE away_score = 0 AND home_score IS NOT NULL)::integer AS failed_to_score_away
  FROM matches
  GROUP BY away_team_id, league_id, EXTRACT(YEAR FROM kickoff_at AT TIME ZONE 'UTC')::integer
)
INSERT INTO team_season_stats (
  team_id, league_id, season_id,
  played_home, wins_home, draws_home, losses_home, goals_for_home, goals_against_home, clean_sheets_home, failed_to_score_home,
  played_away, wins_away, draws_away, losses_away, goals_for_away, goals_against_away, clean_sheets_away, failed_to_score_away,
  updated_at
)
SELECT
  ft.team_id,
  ft.league_id,
  ft.season_id,
  COALESCE(hs.played_home, 0),
  COALESCE(hs.wins_home, 0),
  COALESCE(hs.draws_home, 0),
  COALESCE(hs.losses_home, 0),
  COALESCE(hs.goals_for_home, 0),
  COALESCE(hs.goals_against_home, 0),
  COALESCE(hs.clean_sheets_home, 0),
  COALESCE(hs.failed_to_score_home, 0),
  COALESCE(asx.played_away, 0),
  COALESCE(asx.wins_away, 0),
  COALESCE(asx.draws_away, 0),
  COALESCE(asx.losses_away, 0),
  COALESCE(asx.goals_for_away, 0),
  COALESCE(asx.goals_against_away, 0),
  COALESCE(asx.clean_sheets_away, 0),
  COALESCE(asx.failed_to_score_away, 0),
  now()
FROM fixture_teams AS ft
LEFT JOIN home_stats AS hs
  ON hs.team_id = ft.team_id AND hs.league_id = ft.league_id AND hs.season_id = ft.season_id
LEFT JOIN away_stats AS asx
  ON asx.team_id = ft.team_id AND asx.league_id = ft.league_id AND asx.season_id = ft.season_id
ON CONFLICT (team_id, league_id, season_id) DO UPDATE SET
  played_home = EXCLUDED.played_home,
  wins_home = EXCLUDED.wins_home,
  draws_home = EXCLUDED.draws_home,
  losses_home = EXCLUDED.losses_home,
  goals_for_home = EXCLUDED.goals_for_home,
  goals_against_home = EXCLUDED.goals_against_home,
  clean_sheets_home = EXCLUDED.clean_sheets_home,
  failed_to_score_home = EXCLUDED.failed_to_score_home,
  played_away = EXCLUDED.played_away,
  wins_away = EXCLUDED.wins_away,
  draws_away = EXCLUDED.draws_away,
  losses_away = EXCLUDED.losses_away,
  goals_for_away = EXCLUDED.goals_for_away,
  goals_against_away = EXCLUDED.goals_against_away,
  clean_sheets_away = EXCLUDED.clean_sheets_away,
  failed_to_score_away = EXCLUDED.failed_to_score_away,
  updated_at = now();

INSERT INTO head_to_head (
  home_team_id, away_team_id, fixture_id, played_at, home_goals, away_goals, venue
)
SELECT
  f.home_team_id,
  f.away_team_id,
  f.id,
  f.kickoff_at,
  f.home_goals_ft,
  f.away_goals_ft,
  f.venue_name
FROM fixtures AS f
WHERE f.home_goals_ft IS NOT NULL
  AND f.away_goals_ft IS NOT NULL
ON CONFLICT (fixture_id) DO UPDATE SET
  home_team_id = EXCLUDED.home_team_id,
  away_team_id = EXCLUDED.away_team_id,
  played_at = EXCLUDED.played_at,
  home_goals = EXCLUDED.home_goals,
  away_goals = EXCLUDED.away_goals,
  venue = EXCLUDED.venue;

WITH totals AS (
  SELECT
    team_id,
    league_id,
    season_id,
    played_home + played_away AS played,
    wins_home + wins_away AS wins,
    draws_home + draws_away AS draws,
    losses_home + losses_away AS losses,
    goals_for_home + goals_for_away AS goals_for,
    goals_against_home + goals_against_away AS goals_against
  FROM team_season_stats
),
ranked AS (
  SELECT
    league_id,
    season_id,
    team_id,
    RANK() OVER (
      PARTITION BY league_id, season_id
      ORDER BY ((wins * 3) + draws) DESC, (goals_for - goals_against) DESC, goals_for DESC, team_id
    )::integer AS rank,
    ((wins * 3) + draws)::integer AS points,
    played::integer AS played,
    wins::integer AS wins,
    draws::integer AS draws,
    losses::integer AS losses,
    goals_for::integer AS goals_for,
    goals_against::integer AS goals_against,
    (goals_for - goals_against)::integer AS goal_diff
  FROM totals
)
INSERT INTO standings (
  league_id, season_id, team_id, rank, points, played, wins, draws, losses,
  goals_for, goals_against, goal_diff, form, updated_at
)
SELECT
  league_id, season_id, team_id, rank, points, played, wins, draws, losses,
  goals_for, goals_against, goal_diff, NULL::text, now()
FROM ranked
ON CONFLICT (league_id, season_id, team_id) DO UPDATE SET
  rank = EXCLUDED.rank,
  points = EXCLUDED.points,
  played = EXCLUDED.played,
  wins = EXCLUDED.wins,
  draws = EXCLUDED.draws,
  losses = EXCLUDED.losses,
  goals_for = EXCLUDED.goals_for,
  goals_against = EXCLUDED.goals_against,
  goal_diff = EXCLUDED.goal_diff,
  form = EXCLUDED.form,
  updated_at = now();

WITH expanded AS (
  SELECT
    os.id AS source_snapshot_id,
    os.match_id AS fixture_id,
    COALESCE(NULLIF(os.payload->>'bookmaker', ''), os.provider) AS bookmaker,
    om.market_key AS market,
    price->>'label' AS outcome,
    price->>'value' AS raw_value,
    os.captured_at AS fetched_at
  FROM odds_snapshots AS os
  JOIN odds_markets AS om ON om.id = os.market_id
  CROSS JOIN LATERAL jsonb_array_elements(os.payload->'prices') AS price
  WHERE jsonb_typeof(os.payload->'prices') = 'array'
),
validated AS (
  SELECT
    source_snapshot_id,
    fixture_id,
    bookmaker,
    market,
    outcome,
    raw_value::numeric(12,4) AS value,
    fetched_at
  FROM expanded
  WHERE raw_value ~ '^[0-9]+(\.[0-9]+)?$'
)
INSERT INTO odds (
  fixture_id, bookmaker, market, outcome, value, implied_prob, fetched_at, source_snapshot_id
)
SELECT
  fixture_id,
  bookmaker,
  market,
  outcome,
  value,
  ROUND((1 / value)::numeric, 6),
  fetched_at,
  source_snapshot_id
FROM validated
ON CONFLICT (source_snapshot_id, outcome) DO UPDATE SET
  fixture_id = EXCLUDED.fixture_id,
  bookmaker = EXCLUDED.bookmaker,
  market = EXCLUDED.market,
  value = EXCLUDED.value,
  implied_prob = EXCLUDED.implied_prob,
  fetched_at = EXCLUDED.fetched_at;

WITH fixture_league_seasons AS (
  SELECT DISTINCT league_id, season_id
  FROM fixtures
)
INSERT INTO league_coverage (
  league_id, season_id, has_statistics, has_xg, has_injuries, has_odds, has_lineups, updated_at
)
SELECT
  fls.league_id,
  fls.season_id,
  EXISTS (
    SELECT 1
    FROM fixtures AS f
    JOIN fixture_statistics AS fs ON fs.fixture_id = f.id
    WHERE f.league_id = fls.league_id
      AND f.season_id = fls.season_id
      AND (
        fs.shots_on_target IS NOT NULL OR fs.shots_total IS NOT NULL OR fs.corners IS NOT NULL
        OR fs.fouls IS NOT NULL OR fs.offsides IS NOT NULL OR fs.possession_pct IS NOT NULL
      )
  ) AS has_statistics,
  EXISTS (
    SELECT 1
    FROM fixtures AS f
    JOIN fixture_statistics AS fs ON fs.fixture_id = f.id
    WHERE f.league_id = fls.league_id
      AND f.season_id = fls.season_id
      AND fs.xg IS NOT NULL
  ) AS has_xg,
  EXISTS (
    SELECT 1
    FROM fixtures AS f
    JOIN player_availability AS pa ON pa.fixture_id = f.id
    WHERE f.league_id = fls.league_id
      AND f.season_id = fls.season_id
  ) AS has_injuries,
  EXISTS (
    SELECT 1
    FROM fixtures AS f
    JOIN odds AS o ON o.fixture_id = f.id
    WHERE f.league_id = fls.league_id
      AND f.season_id = fls.season_id
  ) AS has_odds,
  false AS has_lineups,
  now()
FROM fixture_league_seasons AS fls
ON CONFLICT (league_id, season_id) DO UPDATE SET
  has_statistics = EXCLUDED.has_statistics,
  has_xg = EXCLUDED.has_xg,
  has_injuries = EXCLUDED.has_injuries,
  has_odds = EXCLUDED.has_odds,
  has_lineups = EXCLUDED.has_lineups,
  updated_at = now();
