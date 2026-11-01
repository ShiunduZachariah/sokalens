/**
 * Deterministic fixtures for the end-to-end stub API.
 *
 * Everything is derived from the match index, so two runs of the suite see the
 * same teams, the same prices and the same ACCA composition. A random fixture
 * generator produces tests that fail once a week for reasons nobody can
 * reproduce.
 *
 * Kick-offs are relative to the run, not absolute: the web app filters
 * predictions to a 48-hour freshness window and reads the rolling seven-day
 * horizon by date, so a fixed date would make every page empty the day after
 * the fixtures were written.
 */

const TEAMS = [
  ["Arsenal", "Chelsea"], ["Liverpool", "Everton"], ["Man City", "Man United"],
  ["Real Madrid", "Barcelona"], ["Inter", "Milan"], ["Bayern", "Dortmund"],
  ["PSG", "Marseille"], ["Ajax", "Feyenoord"], ["Benfica", "Porto"],
  ["Celtic", "Rangers"], ["Flamengo", "Palmeiras"], ["Boca Juniors", "River Plate"],
];

const LEAGUES = [
  { name: "Premier League", country: "GB" },
  { name: "La Liga", country: "ES" },
  { name: "Serie A", country: "IT" },
  { name: "Bundesliga", country: "DE" },
  { name: "Ligue 1", country: "FR" },
  { name: "Eredivisie", country: "NL" },
  { name: "Kenyan Premier League", country: "KE" },
];

/**
 * The market/selection pairs each fixture publishes.
 *
 * Deliberately a mix of markets that carry a line in the key
 * (`total_goals` + `over_2.5`) and markets that do not, because the label
 * helpers treat the two differently and a stub that only exercised one would
 * let a formatting bug reach a reader.
 */
const MARKETS = [
  { market_key: "1x2", selection: "home" },
  { market_key: "btts", selection: "yes" },
  { market_key: "total_goals", selection: "over_2.5" },
  { market_key: "total_corners", selection: "over_9.5" },
  { market_key: "total_cards", selection: "under_4.5" },
];

const pad = (value) => String(value).padStart(2, "0");

/** A stable pseudo-random number in [0,1) from two integers. */
function noise(a, b) {
  const value = Math.sin(a * 127.1 + b * 311.7) * 43758.5453;
  return value - Math.floor(value);
}

function isoAt(dayOffset, hour, minute = 0) {
  const date = new Date(Date.now() + dayOffset * 86_400_000);
  return `${date.getUTCFullYear()}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())}T${pad(hour)}:${pad(minute)}:00Z`;
}

/** The fixtures for one day offset from the run. Twelve, one per team pair. */
export function fixturesForDay(dayOffset) {
  return TEAMS.map(([home, away], index) => {
    const league = LEAGUES[index % LEAGUES.length];
    const matchId = 100_000 + dayOffset * 100 + index;
    return {
      matchId,
      home,
      away,
      league,
      // Spread across the day so the day-grouping and the "starting soon" rails
      // both have something to show.
      kickoffAt: isoAt(dayOffset, 12 + (index % 8), index % 2 === 0 ? 0 : 30),
    };
  });
}

/** One engine prediction row, in the API's own snake_case wire form. */
function predictionRow(fixture, marketIndex, dayOffset) {
  const market = MARKETS[marketIndex % MARKETS.length];
  const seed = noise(fixture.matchId, marketIndex);
  // 0.30 to 0.85, so every band the UI distinguishes is represented: short
  // prices, mid prices and long shots all appear in a single page.
  const probability = Number((0.3 + seed * 0.55).toFixed(4));
  return {
    id: fixture.matchId * 10 + marketIndex,
    match_id: fixture.matchId,
    match_title: `${fixture.home} vs ${fixture.away}`,
    home_team: fixture.home,
    away_team: fixture.away,
    home_team_logo: "",
    away_team_logo: "",
    league_name: fixture.league.name,
    country: fixture.league.country,
    kickoff_at: fixture.kickoffAt,
    match_status: "scheduled",
    market_key: market.market_key,
    selection: market.selection,
    confidence: Math.round(probability * 100),
    probability,
    is_best_bet: marketIndex === 0 && fixture.matchId % 3 === 0,
    published_version: 1,
    published_at: isoAt(dayOffset - 1, 6),
    selection_odds: null,
    markets: MARKETS.map((item, index) => {
      const marketSeed = noise(fixture.matchId, index + 50);
      const marketProbability = Number((0.3 + marketSeed * 0.55).toFixed(4));
      return {
        market_key: item.market_key,
        selection: item.selection,
        confidence: Math.round(marketProbability * 100),
        probability: marketProbability,
        is_best_bet: index === 0 && fixture.matchId % 3 === 0,
      };
    }),
  };
}

/**
 * The engine rows for one date.
 *
 * `market` narrows to one row per fixture, which is what the public feed asks
 * for; without it the Builder gets every market, which is what IT asks for.
 */
export function predictionsForDate(dayOffset, market) {
  // A market the stub does not publish falls back to the first rather than
  // throwing: the feed asks for `1x2` by default and a future caller asking
  // for something else should get a page, not a 500 that reads as an outage.
  const requested = MARKETS.findIndex((item) => item.market_key === market);
  return fixturesForDay(dayOffset).flatMap((fixture) =>
    market && market !== "all"
      ? [predictionRow(fixture, requested >= 0 ? requested : 0, dayOffset)]
      : MARKETS.map((_, index) => predictionRow(fixture, index, dayOffset)),
  );
}

/** How many days ahead of the run a date string is, or null if it is not one. */
export function dayOffsetFor(date) {
  if (!date) return 0;
  const target = Date.parse(`${date}T12:00:00Z`);
  if (!Number.isFinite(target)) return null;
  const today = Date.parse(`${isoAt(0, 12).slice(0, 10)}T12:00:00Z`);
  const offset = Math.round((target - today) / 86_400_000);
  return offset >= 0 && offset <= 7 ? offset : null;
}

/**
 * The ACCA catalogue.
 *
 * Four slips, each exercising one state the detail page draws:
 *
 * - `open`: every leg still to come.
 * - `live`: some legs graded, one match in progress.
 * - `won`: all graded, one void, none lost.
 * - `lost`: one lost leg, which settles the ticket whatever else happened.
 *
 * Fixed ids, so a test can navigate straight to the state it is about.
 */
const SLIP_SHAPES = [
  { id: "stub-open-0000-0000-0000-000000000001", statuses: [null, null, null], dayOffsets: [1, 1, 2] },
  // `liveLeg` kicked off an hour ago with no grade, which is the payload the
  // detail page infers "live" from. `aheadLeg` is pinned three hours out so the
  // banner reads "1 won · 1 live · 1 to play" whatever time the suite runs.
  { id: "stub-live-0000-0000-0000-000000000002", statuses: ["won", null, null], dayOffsets: [0, 0, 0], liveLeg: 1, aheadLeg: 2 },
  { id: "stub-won0-0000-0000-0000-000000000003", statuses: ["won", "void", "won"], dayOffsets: [-1, -1, -1] },
  { id: "stub-lost-0000-0000-0000-000000000004", statuses: ["won", "lost", "won"], dayOffsets: [-1, -1, -1] },
];

function slipLegs(shape, withStatus) {
  return shape.statuses.map((status, position) => {
    const dayOffset = shape.dayOffsets[position];
    const fixture = fixturesForDay(Math.max(dayOffset, 0))[position + 2];
    const market = MARKETS[position % MARKETS.length];
    const probability = Number((0.45 + noise(position, 7) * 0.3).toFixed(4));
    const relative = (offsetMs) => new Date(Date.now() + offsetMs).toISOString().replace(/\.\d+Z$/, "Z");
    const kickoffAt = shape.liveLeg === position
      ? relative(-3_600_000)
      : shape.aheadLeg === position
        ? relative(3 * 3_600_000)
        : dayOffset < 0
          ? isoAt(dayOffset, 15)
          : fixture.kickoffAt;
    return {
      position,
      source: "engine",
      prediction_id: `${fixture.matchId}-${position}`,
      match_id: fixture.matchId,
      market: market.market_key,
      selection: market.selection,
      fair_odds: Number((1 / probability).toFixed(2)),
      kickoff_at: kickoffAt,
      home_team: fixture.home,
      away_team: fixture.away,
      home_team_logo: "",
      away_team_logo: "",
      league: fixture.league.name,
      country: fixture.league.country,
      confidence: Math.round(probability * 100),
      probability,
      is_best_bet: position === 0,
      ...(withStatus ? { status: status ?? "pending" } : {}),
    };
  });
}

export function slip(shape, withStatus) {
  const legs = slipLegs(shape, withStatus);
  const combined = legs.reduce((total, leg) => total * leg.fair_odds, 1);
  // Sorted, because the envelope's own window must agree with the legs it
  // carries -- legs[0] is the first by position, not by time.
  const times = legs.map((leg) => leg.kickoff_at).sort();
  return {
    id: shape.id,
    market_tab: "all",
    period: "seven_days",
    leg_count: legs.length,
    combined_fair_odds: Number(combined.toFixed(2)),
    ranking_score: 1,
    odds_band: "mid",
    earliest_kickoff: times[0],
    latest_kickoff: times[times.length - 1],
    legs,
  };
}

export const slipShapes = SLIP_SHAPES;

/** The list endpoint serves only slips that have not kicked off. */
export function listSlips() {
  return SLIP_SHAPES.filter((shape) => shape.dayOffsets.every((offset) => offset >= 0)).map((shape) => slip(shape, false));
}

export function slipById(id) {
  const shape = SLIP_SHAPES.find((item) => item.id === id);
  return shape ? slip(shape, true) : null;
}

/**
 * A slip composed for the request, the way `generate=1` asks for one.
 *
 * The real generator composes to the requested leg count from the vetted pool;
 * returning a catalogue slip of a different size instead would make the
 * generator look broken for every leg count but the one the catalogue happens
 * to hold.
 *
 * Per-leg odds are solved backwards from a target combined price inside the
 * caller's range, and capped so a 20-leg slip does not land at 2^20. The web
 * layer rejects a slip whose combined odds exceed 1000 -- correctly, since
 * nothing the engine publishes is priced there -- and a fixture that tripped
 * that would read as the generator failing.
 */
export function generatedSlip(legCount, minOdds, maxOdds) {
  const legs = Math.max(2, Math.min(20, Math.floor(legCount) || 2));
  const ceiling = Math.min(Number.isFinite(maxOdds) && maxOdds > 1 ? maxOdds : 1000, 900);
  const floor = Number.isFinite(minOdds) && minOdds > 1 ? minOdds : 1.5;
  const target = Math.max(floor, Math.min(ceiling, Math.pow(1.8, legs)));
  const perLeg = Math.max(1.05, Number(Math.pow(target, 1 / legs).toFixed(2)));
  const pool = [...fixturesForDay(0), ...fixturesForDay(1), ...fixturesForDay(2)];

  const rows = Array.from({ length: legs }, (_, position) => {
    const fixture = pool[position % pool.length];
    const market = MARKETS[position % MARKETS.length];
    return {
      position,
      source: "engine",
      prediction_id: `gen-${fixture.matchId}-${position}`,
      match_id: fixture.matchId,
      market: market.market_key,
      selection: market.selection,
      fair_odds: perLeg,
      kickoff_at: fixture.kickoffAt,
      home_team: fixture.home,
      away_team: fixture.away,
      home_team_logo: "",
      away_team_logo: "",
      league: fixture.league.name,
      country: fixture.league.country,
      confidence: Math.round(100 / perLeg),
      probability: Number((1 / perLeg).toFixed(4)),
      is_best_bet: position === 0,
    };
  });
  const times = rows.map((row) => row.kickoff_at).sort();
  return {
    // The real API gives a composed slip a `generated-<hash>` id (see
    // `acca/generate.go`): it is not stored, but the web layer rejects a slip
    // with no id at all, and the catalogue card keys off `origin` rather than
    // the id to decide the slip has no page of its own.
    id: `generated-${legs}-${perLeg.toFixed(2).replace(".", "")}`,
    market_tab: "all",
    period: "seven_days",
    leg_count: rows.length,
    combined_fair_odds: Number(Math.pow(perLeg, rows.length).toFixed(2)),
    ranking_score: 1,
    odds_band: "mid",
    earliest_kickoff: times[0],
    latest_kickoff: times[times.length - 1],
    legs: rows,
    origin: "on_demand",
  };
}

/** Every market the stub publishes, for the catalogue's facets. */
export const marketKeys = MARKETS.map((item) => item.market_key);
