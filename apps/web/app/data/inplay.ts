import { accountApiRequest, asArray, asRecord, readNumber, readString } from "./accountApi";

/*
 * The in-play wire types, per `docs/integrations/in-play-data-contract.md`.
 *
 * Everything here is read, never computed. The contract is explicit that the
 * engine decides and the clients render, because web and Flutter deriving the
 * same judgement separately is how they end up disagreeing.
 */

/**
 * `ok` | `stale` | `suspended`, per contract §4 of the handoff's rules.
 *
 * `stale` means "this is what we last knew" and must never be presented as
 * current. `suspended` means show no probabilities at all. Both are rendered as
 * states rather than quietly dropped, because a board that silently hides a
 * suspended market looks like a board with fewer markets.
 */
export type ModelHealth = "ok" | "stale" | "suspended";

const MODEL_HEALTHS = new Set<string>(["ok", "stale", "suspended"]);

export type InPlayPrediction = {
  selection: string;
  /**
   * Integer basis points out of 10,000.
   *
   * The contract says to read this and not `probability`: the float is retained
   * only for the existing client, and basis points exist so web and Flutter
   * cannot round the same market differently. Apportioned by largest remainder,
   * so a market's selections sum to exactly 10,000.
   */
  probabilityBp: number;
  confidence: string;
  confidenceScore: number;
};

export type InPlayMarket = {
  key: string;
  label: string;
  /**
   * True when this viewer may not see the number.
   *
   * A locked market still arrives with its key and label -- the contract shapes
   * both viewers identically and differs only in `locked` and the presence of
   * `prediction` -- so the line is shown and the figure withheld. Dropping locked
   * markets would understate the coverage the subscription buys.
   */
  locked: boolean;
  prediction?: InPlayPrediction;
  /** When the model last had reason to revise this line. */
  dataCutoffAt?: string;
  matchMinute?: number;
};

export type InPlayScore = { home: number; away: number };

export type InPlayFixture = {
  fixtureId: number;
  status: string;
  minute: number;
  /**
   * Crest URLs for the two sides, empty when the team has no logo on record.
   *
   * Ungated: a crest is not model output, so it arrives for both viewer tiers
   * and never varies with `locked`. Decoration only -- the fixture is still
   * identified by `fixtureId`, and an empty string means the client draws the
   * initials shield rather than that the row should be dropped.
   */
  homeTeamLogo: string;
  awayTeamLogo: string;
  /** The fixture's own staleness, distinct from a snapshot's `model_health`. */
  stale: boolean;
  score: InPlayScore;
  markets: InPlayMarket[];
  lockedCount: number;
};

export type InPlaySnapshot = {
  fixtureId: number;
  sequence: number;
  trigger: string;
  modelHealth: ModelHealth;
  minute: number;
  period: string;
  score: InPlayScore;
  redCards: InPlayScore;
  locked: boolean;
  /**
   * The model's own output for this point, in basis points, keyed by market and
   * then by selection: `{ match_result_in_play: { home: 6800, draw: 2100,
   * away: 1100 } }`.
   *
   * This was parsed away. The engine has written the whole vector on every
   * snapshot since migration 000079 and the API has always sent it; the client
   * kept the score and the clock and dropped the probabilities, which is why
   * the probability graph "had no data" -- it had been arriving all along.
   *
   * Null when the reader is not entitled, which is distinct from an empty
   * object: one means withheld, the other means the model published nothing.
   */
  markets: Record<string, Record<string, number>> | null;
  /** The event that caused this point, when a goal or card triggered it. */
  eventKey: string | null;
};

export type InPlayEvent = {
  sequence: number;
  fixtureId: number;
  identityKey: string;
  kind: string;
  detail: string;
  minute: number;
  extra?: number;
  playerName?: string;
  /**
   * A goal the provider withdrew.
   *
   * Shown struck through, never removed: the contract requires corrections to be
   * visible, and the match timeline already establishes that treatment.
   */
  retracted: boolean;
};

/** What to offer a viewer who is seeing locked lines. */
export type InPlayUnlock = { reason: string; cta: string };

export type InPlayOverview = {
  fixtures: InPlayFixture[];
  snapshots: InPlaySnapshot[];
  events: InPlayEvent[];
  /**
   * Resume token. Taken from the payload, never computed from the lists, which
   * the server may have truncated.
   */
  cursor: number;
  entitled: boolean;
  lockedCount: number;
  unlock?: InPlayUnlock;
};

export type InPlayChangeSet = {
  snapshots: InPlaySnapshot[];
  events: InPlayEvent[];
  cursor: number;
  /** The limit truncated the set: poll again at once rather than falling behind. */
  more: boolean;
  unlock?: InPlayUnlock;
};

function toScore(value: unknown): InPlayScore {
  const record = asRecord(value);
  return { home: readNumber(record, "home") ?? 0, away: readNumber(record, "away") ?? 0 };
}

function toModelHealth(value: unknown): ModelHealth {
  // An unrecognised health is treated as `stale` rather than `ok`. Guessing
  // upward would present an unknown state as current, which is the one reading
  // the contract forbids; guessing downward only withholds.
  return typeof value === "string" && MODEL_HEALTHS.has(value) ? (value as ModelHealth) : "stale";
}

function toPrediction(value: unknown): InPlayPrediction | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const record = asRecord(value);
  const probabilityBp = readNumber(record, "probability_bp");
  const selection = readString(record, "selection");
  // No selection or no basis points means nothing can be drawn, and falling back
  // to the retained `probability` float would reintroduce exactly the rounding
  // divergence basis points exist to prevent.
  if (!selection || probabilityBp === undefined || !Number.isFinite(probabilityBp)) return undefined;
  return {
    selection,
    probabilityBp,
    confidence: readString(record, "confidence") ?? "",
    confidenceScore: readNumber(record, "confidence_score") ?? 0,
  };
}

function toMarket(value: unknown): InPlayMarket | null {
  const record = asRecord(value);
  const key = readString(record, "key");
  if (!key) return null;
  const prediction = toPrediction(record.prediction);
  const matchMinute = readNumber(record, "match_minute");
  return {
    key,
    label: readString(record, "label") ?? key,
    locked: record.locked === true,
    ...(prediction ? { prediction } : {}),
    ...(readString(record, "data_cutoff_at") ? { dataCutoffAt: readString(record, "data_cutoff_at") } : {}),
    ...(matchMinute !== undefined ? { matchMinute } : {}),
  };
}

function toFixture(value: unknown): InPlayFixture | null {
  const record = asRecord(value);
  const fixtureId = readNumber(record, "fixture_id");
  if (fixtureId === undefined || !Number.isFinite(fixtureId)) return null;
  return {
    fixtureId,
    status: readString(record, "status") ?? "",
    minute: readNumber(record, "minute") ?? 0,
    homeTeamLogo: readString(record, "home_team_logo") ?? "",
    awayTeamLogo: readString(record, "away_team_logo") ?? "",
    stale: record.stale === true,
    score: toScore(record.score),
    markets: asArray(record.markets).map(toMarket).filter((market): market is InPlayMarket => market !== null),
    lockedCount: readNumber(record, "locked_count") ?? 0,
  };
}

function toSnapshot(value: unknown): InPlaySnapshot | null {
  const record = asRecord(value);
  const sequence = readNumber(record, "sequence");
  const fixtureId = readNumber(record, "fixture_id");
  if (sequence === undefined || fixtureId === undefined) return null;
  return {
    fixtureId,
    sequence,
    trigger: readString(record, "trigger") ?? "",
    modelHealth: toModelHealth(record.model_health),
    minute: readNumber(record, "minute") ?? 0,
    period: readString(record, "period") ?? "",
    score: toScore(record.score),
    redCards: toScore(record.red_cards),
    locked: record.locked === true,
    markets: toMarketVectors(record.markets),
    eventKey: readString(record, "event_key") ?? null,
  };
}

/**
 * The per-point probability vectors, kept in basis points.
 *
 * Not converted to percentages here: 6800 is exact and 68 is not, and the line
 * between two points a basis point apart is the difference between a graph that
 * moves and one that looks flat. The chart divides when it draws.
 */
function toMarketVectors(value: unknown): Record<string, Record<string, number>> | null {
  const record = asRecord(value);
  if (!record) return null;
  const out: Record<string, Record<string, number>> = {};
  for (const [market, raw] of Object.entries(record)) {
    const selections = asRecord(raw);
    if (!selections) continue;
    const vector: Record<string, number> = {};
    for (const [selection, points] of Object.entries(selections)) {
      const parsed = Number(points);
      if (Number.isFinite(parsed)) vector[selection] = parsed;
    }
    if (Object.keys(vector).length > 0) out[market] = vector;
  }
  return Object.keys(out).length > 0 ? out : null;
}

function toEvent(value: unknown): InPlayEvent | null {
  const record = asRecord(value);
  const sequence = readNumber(record, "sequence");
  const fixtureId = readNumber(record, "fixture_id");
  if (sequence === undefined || fixtureId === undefined) return null;
  const extra = readNumber(record, "extra");
  return {
    sequence,
    fixtureId,
    identityKey: readString(record, "identity_key") ?? "",
    kind: readString(record, "kind") ?? "",
    detail: readString(record, "detail") ?? "",
    minute: readNumber(record, "minute") ?? 0,
    ...(extra !== undefined ? { extra } : {}),
    ...(readString(record, "player_name") ? { playerName: readString(record, "player_name") } : {}),
    retracted: record.retracted === true,
  };
}

function toUnlock(value: unknown): InPlayUnlock | undefined {
  const record = asRecord(value);
  const reason = readString(record, "reason");
  const cta = readString(record, "cta");
  return reason || cta ? { reason: reason ?? "", cta: cta ?? "" } : undefined;
}

export function parseOverview(payload: unknown): InPlayOverview {
  const root = asRecord(payload);
  const unlock = toUnlock(root.unlock);
  return {
    fixtures: asArray(root.fixtures).map(toFixture).filter((f): f is InPlayFixture => f !== null),
    snapshots: asArray(root.snapshots).map(toSnapshot).filter((s): s is InPlaySnapshot => s !== null),
    events: asArray(root.events).map(toEvent).filter((e): e is InPlayEvent => e !== null),
    cursor: readNumber(root, "cursor") ?? 0,
    entitled: root.entitled === true,
    lockedCount: readNumber(root, "locked_count") ?? 0,
    ...(unlock ? { unlock } : {}),
  };
}

export function parseChangeSet(payload: unknown): InPlayChangeSet {
  const root = asRecord(payload);
  const unlock = toUnlock(root.unlock);
  return {
    snapshots: asArray(root.snapshots).map(toSnapshot).filter((s): s is InPlaySnapshot => s !== null),
    events: asArray(root.events).map(toEvent).filter((e): e is InPlayEvent => e !== null),
    cursor: readNumber(root, "cursor") ?? 0,
    more: root.more === true,
    ...(unlock ? { unlock } : {}),
  };
}

/** The combined first load. One call; a cold client must not fan out. */
export async function fetchInPlayOverview() {
  const result = await accountApiRequest<unknown>("/v1/inplay/overview");
  return result.ok ? parseOverview(result.data) : null;
}

/** A bounded read of the current market publication for one open match page. */
export async function fetchInPlayFixture(fixtureId: number): Promise<InPlayFixture | null> {
  const result = await accountApiRequest<unknown>(`/v1/inplay/fixtures/${encodeURIComponent(String(fixtureId))}`);
  return result.ok ? toFixture(asRecord(result.data).fixture) : null;
}

/**
 * One point on a fixture's probability graph.
 *
 * `markets` is null when the reader is not entitled. The shape of the graph --
 * when it moved, and what the score was -- is served either way, so an
 * anonymous reader sees a real chart with the values withheld rather than an
 * empty panel that reads as a broken feature.
 */
/**
 * What happened at a marked minute: a goal or a red card, and to whom.
 *
 * Present only when the API could resolve the incident. A marker without one
 * still draws -- it simply says less -- because roughly half of recorded
 * fixtures have no event feed ingested against them.
 */
export type InPlaySeriesEvent = {
  type: string;
  detail: string | null;
  side: "home" | "away";
  player: string | null;
};

export type InPlaySeriesPoint = {
  sequence: number;
  minute: number;
  trigger: string;
  eventKey: string | null;
  score: InPlayScore;
  markets: Record<string, Record<string, number>> | null;
  locked: boolean;
  event: InPlaySeriesEvent | null;
};

function toSeriesEvent(value: unknown): InPlaySeriesEvent | null {
  const record = asRecord(value);
  const type = readString(record, "type");
  const side = readString(record, "side");
  // Side is what makes the label readable; without it there is nothing useful
  // to say, so the point renders as a bare marker instead.
  if (!type || (side !== "home" && side !== "away")) return null;
  return {
    type,
    detail: readString(record, "detail") ?? null,
    side,
    player: readString(record, "player") ?? null,
  };
}

/**
 * A fixture's whole graph, oldest point first.
 *
 * Separate from the change feed on purpose: that answers "what has happened
 * since cursor N", which is right for a live board and wrong for a chart that
 * has to start at minute zero.
 */
export async function fetchInPlaySeries(fixtureId: number): Promise<InPlaySeriesPoint[]> {
  const result = await accountApiRequest<unknown>(
    `/v1/inplay/fixtures/${encodeURIComponent(String(fixtureId))}/series`,
  );
  if (!result.ok) return [];
  return asArray(asRecord(result.data).series)
    .map(toSeriesPoint)
    .filter((point): point is InPlaySeriesPoint => point !== null);
}

function toSeriesPoint(value: unknown): InPlaySeriesPoint | null {
  const record = asRecord(value);
  const sequence = readNumber(record, "sequence");
  if (sequence === undefined) return null;
  return {
    sequence,
    minute: readNumber(record, "minute") ?? 0,
    trigger: readString(record, "trigger") ?? "",
    eventKey: readString(record, "event_key") ?? null,
    score: toScore(record.score),
    markets: toMarketVectors(record.markets),
    locked: record.locked === true,
    event: toSeriesEvent(record.event),
  };
}

/** Everything after `since`. Shares one server-side query with the stream. */
export async function fetchInPlayChanges(since: number) {
  const result = await accountApiRequest<unknown>(`/v1/inplay/changes?since=${encodeURIComponent(String(since))}`);
  return result.ok ? parseChangeSet(result.data) : null;
}

/**
 * Basis points as a percentage string.
 *
 * Rounded once, here, so every surface shows the same figure for the same
 * market. 10,000bp is 100%.
 */
export function formatProbabilityBp(probabilityBp: number) {
  return `${Math.round(probabilityBp / 100)}%`;
}

/**
 * The family a line belongs to.
 *
 * A line's market key carries the line -- `total_goals_match_9.5`, selection
 * `over` -- because keys supersede on `(fixture_id, market)` and lines sharing a
 * key would supersede each other. The contract says a client grouping a family
 * matches on the key prefix, which is what this strips.
 */
export function marketFamily(key: string) {
  return key.replace(/_\d+(?:\.\d+)?$/, "");
}

const MARKET_FAMILY_LABELS: Record<string, string> = {
  btts_in_play: "Both teams to score",
  match_result_in_play: "Match result",
  next_goal: "Next goal",
  corners_total: "Total corners",
  total_cards: "Total cards",
  total_goals_match: "Total goals in match",
  total_goals_remaining: "Goals remaining",
};

/** Display a market family, never a per-line wire key such as total_cards_1.5. */
export function marketFamilyLabel(family: string, providerLabel?: string) {
  if (MARKET_FAMILY_LABELS[family]) return MARKET_FAMILY_LABELS[family];
  const readable = providerLabel && !providerLabel.includes("_") ? providerLabel : family;
  return readable.replace(/_/g, " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}
