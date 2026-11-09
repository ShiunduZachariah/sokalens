import { cache } from "react";
import { parseMatchDiscipline, type MatchDiscipline } from "../lib/discipline.ts";
import { parseMatchStatistics, type MatchStatistics } from "../lib/matchStatistics.ts";
import { HIDDEN_PUBLIC_MARKETS } from "../lib/markets.ts";
import { liveClockIsCurrent } from "../lib/liveClock.ts";
import { matchStatusLabel as sharedMatchStatusLabel } from "../lib/matchStatus.ts";
import { DEFAULT_VIEWER_TIMEZONE } from "../lib/viewerTimezone.ts";
import { toTimelineEvents, type MatchTimelineEvent } from "../lib/matchTimeline.ts";

// Re-exported so callers keep importing a match's shape from one module.
export type { MatchTimelineEvent };

export type LiveOddsPrice = {
  label: string;
  value: string;
  trend: "up" | "down" | "flat";
};

export type MatchPredictionState =
  | "published"
  | "pending"
  | "no_odds"
  | "insufficient_data"
  | "insufficient_history"
  | "failed_generation";

/**
 * The six values the engine sends, per the in-play data contract §8.
 *
 * `on_track` means "would win if the match ended now" -- a report on the pick
 * already made, not a second prediction. `off_track` includes a draw under a
 * home pick, because that would not win. A decided pick is terminal and never
 * reads `on_track`: full-time over 1.5 with two goals at minute 10 reads `won`.
 */
export type TrackerState = "on_track" | "off_track" | "won" | "lost" | "void" | "unknown";

const TRACKER_STATES = new Set<string>(["on_track", "off_track", "won", "lost", "void", "unknown"]);

/**
 * Reads `tracker_state` without inventing one.
 *
 * An unrecognised value is dropped rather than passed through: a state this
 * client cannot render is indistinguishable from no state for a reader, and
 * passing it on would put an unstyled string where a label belongs.
 */
function toTrackerState(value: unknown): TrackerState | undefined {
  return typeof value === "string" && TRACKER_STATES.has(value) ? (value as TrackerState) : undefined;
}

export type LiveMatchPrediction = {
  id: number;
  source: "engine" | "provider";
  marketKey: string;
  selection: string;
  confidence: number;
  /** The engine's probability, 0 to 1. Zero when the API sent none, as on provider rows. */
  probability: number;
  publishedVersion: number;
  publishedAt: string;
  outcome: string;
  actual: string;
  /**
   * Why a void was written, when it was not a market rule. Empty for every
   * other outcome and for a graded void such as a drawn draw-no-bet.
   */
  voidReason: string;
  /**
   * How the pick is doing against the live score, computed by the engine.
   *
   * `undefined` here when the API sent no verdict. As of the 2026-10-02 API
   * change the wire form of that is **`tracker_state: null`, always present** --
   * it was an absent key before, and this parser maps both to `undefined`, so the
   * change needed no code. The distinction the contract draws is not
   * null-vs-absent but **null-vs-`unknown`**: null means nothing has judged the
   * pick (the in-play pass has not covered the fixture, or it has not kicked
   * off), while `unknown` is a verdict the engine reached and could not decide.
   * Both render no tracker; they are kept apart in the data because when one of
   * them is wrong, which one it is tells you where to look.
   *
   * Never derived here. The contract computes it server-side specifically so
   * that web and Flutter cannot grade the same pick differently, and it grades
   * through the same code as settlement so it cannot contradict the eventual
   * outcome. It is also independent of `outcome`: a pick reads `won` on the
   * tracker while `outcome` is still `pending`, because settlement writes a
   * different record.
   */
  trackerState?: TrackerState;
};

export type LiveMatch = {
  id: number;
  leagueName: string;
  /**
   * Catalog slug of the competition, or "" when the API does not know it.
   * Link the league with this, never with a slug derived from `leagueName`:
   * names repeat across countries ("Second League" exists in five).
   */
  leagueSlug: string;
  country: string;
  homeTeam: string;
  awayTeam: string;
  homeTeamLogo: string;
  awayTeamLogo: string;
  homeTeamSlug: string;
  awayTeamSlug: string;
  homeHistoryCount: number;
  awayHistoryCount: number;
  homeForm: string;
  awayForm: string;
  kickoffAt: string;
  status: string;
  rawStatus: string;
  rawStatusLabel: string;
  statusElapsed: number | null;
  /** ISO timestamp of the last provider confirmation of the live state, or "" if never confirmed. */
  liveStateAt: string;
  venueName: string;
  venueCity: string;
  homeScore: number | null;
  awayScore: number | null;
  halfTimeHomeScore: number | null;
  halfTimeAwayScore: number | null;
  odds: LiveOddsPrice[];
  predictionState: MatchPredictionState;
  predictionStateReason: string;
  prediction: LiveMatchPrediction | null;
  predictions: LiveMatchPrediction[];
  /**
   * Live events in match-clock order. Empty when the API sent none, which is
   * every fixture that has not kicked off -- and, today, every match in a list
   * response: only the detail endpoint carries a timeline, because joining
   * events onto the today list is a change to the query that #165 had to fix
   * for timing out. The shape is shared so the day that changes is a server
   * change alone.
   */
  timeline: MatchTimelineEvent[];
  /** Card records from before kickoff; detail responses only, else null. */
  discipline: MatchDiscipline | null;
  statistics: MatchStatistics | null;
};

type TodayMatchesResponse = {
  matches?: Array<Record<string, unknown>>;
  total?: number;
  page?: number;
  limit?: number;
};

type FetchMatchesOptions = {
  date?: string;
  day?: "yesterday" | "today" | "tomorrow";
  includeUnavailable?: boolean;
  statuses?: string[];
  limit?: number;
  timezone?: string;
  page?: number;
};

const defaultTimezone = DEFAULT_VIEWER_TIMEZONE;

function toNullableNumber(value: unknown) {
  if (typeof value !== "number") {
    return null;
  }
  return Number.isFinite(value) ? value : null;
}

function toOdds(value: unknown): LiveOddsPrice[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => {
      if (typeof item !== "object" || item === null) {
        return null;
      }
      const candidate = item as Record<string, unknown>;
      const trend = String(candidate.trend ?? "flat");
      if (trend !== "up" && trend !== "down" && trend !== "flat") {
        return null;
      }
      return {
        label: String(candidate.label ?? ""),
        value: String(candidate.value ?? ""),
        trend,
      } as LiveOddsPrice;
    })
    .filter((item): item is LiveOddsPrice => item !== null && item.label.length > 0);
}

function toPrediction(value: unknown): LiveMatchPrediction | null {
  if (typeof value !== "object" || value === null) {
    return null;
  }
  const candidate = value as Record<string, unknown>;
  const prediction = {
    id: Number(candidate.id),
    source: candidate.source === "provider" ? "provider" as const : "engine" as const,
    marketKey: String(candidate.market_key ?? ""),
    selection: String(candidate.selection ?? ""),
    confidence: Number(candidate.confidence),
    probability: Number(candidate.probability ?? 0),
    publishedVersion: Number(candidate.published_version),
    publishedAt: String(candidate.published_at ?? ""),
    outcome: String(candidate.outcome ?? "pending"),
    actual: String(candidate.actual ?? ""),
    voidReason: String(candidate.void_reason ?? ""),
    // Spread rather than assigned, so an absent key stays absent on the object
    // instead of becoming an explicit `undefined` the renderer has to re-check.
    ...(toTrackerState(candidate.tracker_state) ? { trackerState: toTrackerState(candidate.tracker_state) } : {}),
  };
  if (
    !Number.isFinite(prediction.id) ||
    (prediction.source === "provider" ? prediction.id !== 0 : prediction.id <= 0) ||
    prediction.marketKey.length === 0 ||
    prediction.selection.length === 0 ||
    HIDDEN_PUBLIC_MARKETS.has(prediction.marketKey)
  ) {
    return null;
  }
  return prediction;
}

export function toPredictions(value: unknown): LiveMatchPrediction[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => {
      if (typeof item !== "object" || item === null) {
        return null;
      }
      const candidate = item as Record<string, unknown>;
      return toPrediction(candidate);
    })
    .filter((item): item is LiveMatchPrediction => item !== null);
}

function normalizeMatch(match: Record<string, unknown>): LiveMatch | null {
  const normalized = {
    id: Number(match.id),
    leagueName: String(match.league_name ?? ""),
    leagueSlug: String(match.league_slug ?? ""),
    country: String(match.country ?? "International"),
    homeTeam: String(match.home_team ?? ""),
    awayTeam: String(match.away_team ?? ""),
    homeTeamLogo: String(match.home_team_logo ?? ""),
    awayTeamLogo: String(match.away_team_logo ?? ""),
    homeTeamSlug: String(match.home_team_slug ?? ""),
    awayTeamSlug: String(match.away_team_slug ?? ""),
    homeHistoryCount: Number(match.home_history_count ?? 0),
    awayHistoryCount: Number(match.away_history_count ?? 0),
    homeForm: String(match.home_form ?? ""),
    awayForm: String(match.away_form ?? ""),
    kickoffAt: String(match.kickoff_at ?? ""),
    status: String(match.status ?? "upcoming"),
    rawStatus: String(match.raw_status ?? ""),
    rawStatusLabel: String(match.raw_status_label ?? ""),
    statusElapsed: toNullableNumber(match.status_elapsed),
    liveStateAt: String(match.live_state_at ?? ""),
    venueName: String(match.venue_name ?? ""),
    venueCity: String(match.venue_city ?? ""),
    homeScore: toNullableNumber(match.home_score),
    awayScore: toNullableNumber(match.away_score),
    halfTimeHomeScore: toNullableNumber(match.half_time_home_score),
    halfTimeAwayScore: toNullableNumber(match.half_time_away_score),
    odds: toOdds(match.odds),
    predictionState: String(match.prediction_state ?? "pending") as MatchPredictionState,
    predictionStateReason: String(match.prediction_state_reason ?? ""),
    prediction: toPrediction(match.prediction),
    predictions: toPredictions(match.predictions),
    timeline: toTimelineEvents(match.timeline),
    discipline: parseMatchDiscipline(match.discipline),
    statistics: parseMatchStatistics(match.statistics),
  };

  if (
    !Number.isFinite(normalized.id) ||
    normalized.id <= 0 ||
    normalized.homeTeam.length === 0 ||
    normalized.awayTeam.length === 0 ||
    normalized.kickoffAt.length === 0
  ) {
    return null;
  }
  return normalized;
}

export async function fetchMatches(options: FetchMatchesOptions = {}): Promise<LiveMatch[]> {
  const { fetchPredictionApi } = await import("./apiClient");
  const params = new URLSearchParams();
  if (options.date) {
    params.set("date", options.date);
  } else if (options.day) {
    params.set("day", options.day);
  } else {
    params.set("day", "today");
  }
  params.set("timezone", options.timezone ?? defaultTimezone);
  if (options.limit) {
    params.set("limit", String(Math.min(200, Math.max(1, Math.floor(options.limit)))));
  }
  if (options.page) {
    params.set("page", String(Math.max(1, Math.floor(options.page))));
  }
  if (options.includeUnavailable) {
    params.set("include_unavailable", "true");
  }
  for (const status of options.statuses ?? []) {
    params.append("status", status);
  }

  const response = await fetchPredictionApi(`/v1/matches/today?${params.toString()}`, {
    revalidate: 15,
    staleWhileRevalidate: 30,
  });
  if (!response.ok) {
    throw new Error(`Match feed request failed with status ${response.status}`);
  }
  const payload = (await response.json()) as TodayMatchesResponse;
  return (payload.matches ?? [])
    .map((match) => normalizeMatch(match))
    .filter((match): match is LiveMatch => match !== null);
}

export async function fetchAllMatches(options: FetchMatchesOptions = {}): Promise<LiveMatch[]> {
  const pageSize = 200;
  const fetchPage = async (page: number) => {
    const { fetchPredictionApi } = await import("./apiClient");
    const params = new URLSearchParams();
    if (options.date) params.set("date", options.date);
    else if (options.day) params.set("day", options.day);
    else params.set("day", "today");
    params.set("timezone", options.timezone ?? defaultTimezone);
    params.set("limit", String(pageSize));
    params.set("page", String(page));
    if (options.includeUnavailable) params.set("include_unavailable", "true");
    for (const status of options.statuses ?? []) params.append("status", status);
    const response = await fetchPredictionApi(`/v1/matches/today?${params.toString()}`, { revalidate: 15, staleWhileRevalidate: 30 });
    if (!response.ok) throw new Error(`Match feed request failed with status ${response.status}`);
    return (await response.json()) as TodayMatchesResponse;
  };

  const first = await fetchPage(1);
  const total = Math.max(0, Number(first.total) || 0);
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const pages = [first];
  for (let page = 2; page <= pageCount; page += 1) {
    pages.push(await fetchPage(page));
  }
  return pages
    .flatMap((page) => page.matches ?? [])
    .map((match) => normalizeMatch(match))
    .filter((match): match is LiveMatch => match !== null);
}

export async function fetchMatchCount(options: FetchMatchesOptions = {}): Promise<number> {
  const { fetchPredictionApi } = await import("./apiClient");
  const params = new URLSearchParams({ limit: "1", page: "1" });
  if (options.date) params.set("date", options.date);
  else params.set("day", options.day ?? "today");
  params.set("timezone", options.timezone ?? defaultTimezone);
  if (options.includeUnavailable) params.set("include_unavailable", "true");
  for (const status of options.statuses ?? []) params.append("status", status);
  const response = await fetchPredictionApi(`/v1/matches/today?${params}`, { revalidate: 15, staleWhileRevalidate: 30 });
  if (!response.ok) throw new Error(`Match count request failed with status ${response.status}`);
  const payload = (await response.json()) as TodayMatchesResponse;
  return Math.max(0, Number(payload.total) || 0);
}

export async function fetchTodayMatches(limit = 8): Promise<LiveMatch[]> {
  return fetchMatches({ day: "today", includeUnavailable: true, limit, timezone: defaultTimezone });
}

const fetchMatchByIdCached = cache(async (id: number): Promise<LiveMatch | null> => {
  if (!Number.isInteger(id) || id <= 0) {
    return null;
  }

  const { fetchPredictionApi } = await import("./apiClient");
  const response = await fetchPredictionApi(`/v1/matches/${id}`, {
    revalidate: 15,
    staleWhileRevalidate: 30,
  });
  if (response.status === 404) {
    return null;
  }
  if (!response.ok) {
    throw new Error(`Match detail request failed with status ${response.status}`);
  }

  const payload = (await response.json()) as Record<string, unknown>;
  return normalizeMatch(payload);
});

export function fetchMatchById(id: number): Promise<LiveMatch | null> {
  return fetchMatchByIdCached(id);
}

export function matchStatusLabel(match: LiveMatch) {
  return sharedMatchStatusLabel(match.status, match.rawStatus, match.rawStatusLabel);
}

/**
 * The score, or `null` when the match has not produced one yet.
 *
 * This used to fall back to a formatted kickoff clock, which is how a fixture
 * page came to show two different times for the same kickoff: the header ran
 * through `LocalTime` and said 3:00 PM while the scoreboard printed this
 * function's raw UTC "12:00". A string formatted on the server cannot be
 * corrected in the browser, because nothing marks it as a time.
 *
 * So the fallback is the caller's job now, and the only correct fallback is a
 * `LocalTime` element.
 */
export function matchScore(match: LiveMatch): string | null {
  if (match.homeScore !== null && match.awayScore !== null) {
    return `${match.homeScore}-${match.awayScore}`;
  }
  return null;
}

function addedTimeClock(baseMinute: number, elapsed: number) {
  if (elapsed > baseMinute) {
    return `${baseMinute} + ${elapsed - baseMinute}'`;
  }
  return `${elapsed}'`;
}

/**
 * Shown instead of a match minute once the stored minute is too old to assert.
 *
 * It says the feed is behind, not that the match has stopped -- we do not know
 * that, and a freeze is far more often a missed poll than an abandoned game.
 */
export const STALE_CLOCK_LABEL = "Awaiting update";

export function matchClockLabel(match: LiveMatch, now: number = Date.now()) {
  // A minute nobody has confirmed for two minutes is not the current minute.
  // Printing it anyway is how a match came to read "second half, 67'" hours
  // after kickoff: the number was true when written and simply never changed.
  if (!liveClockIsCurrent(match, now)) {
    return STALE_CLOCK_LABEL;
  }
  const rawStatus = match.rawStatus.trim().toUpperCase();
  switch (rawStatus) {
    case "FT":
    case "AET":
    case "PEN":
    case "P":
    case "HT":
    case "BT":
    case "AWD":
    case "WO":
      return sharedMatchStatusLabel("", rawStatus);
    case "1H":
      return match.statusElapsed !== null ? addedTimeClock(45, match.statusElapsed) : "1H";
    case "2H":
      return match.statusElapsed !== null ? addedTimeClock(90, match.statusElapsed) : "2H";
    case "ET":
      if (match.statusElapsed === null) {
        return "ET";
      }
      return match.statusElapsed > 105
        ? addedTimeClock(105, match.statusElapsed)
        : `${match.statusElapsed}'`;
    default:
      if (match.statusElapsed !== null) {
        return `${match.statusElapsed}'`;
      }
      return "Kickoff";
  }
}

export function predictionStateLabel(state: MatchPredictionState) {
  switch (state) {
    case "published":
      return "Prediction ready";
    case "no_odds":
      return "No odds yet";
    case "insufficient_data":
    case "insufficient_history":
      return "Insufficient data";
    case "failed_generation":
      return "Generation failed";
    default:
      return "Pending";
  }
}
