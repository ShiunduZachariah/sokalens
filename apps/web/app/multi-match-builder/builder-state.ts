// Client-only ticket store. Version 3 removes the artificial leg cap and
// preserves whether a selection was added manually or as part of an ACCA.

const STORAGE_KEY = "sokalens.builder.selection.v3";
const LEGACY_STORAGE_KEY = "sokalens.builder.selection.v2";
export const BUILDER_EVENT_NAME = "sokalens:builder-change";
export const MIN_LEGS = 2;

export type BuilderLeg = {
  id: string;
  predictionId: number;
  matchId: number;
  matchTitle: string;
  /**
   * The two sides and their crests, for the ticket leg's inline crest pair
   * (review 2.3, UI-04). Optional on purpose: a ticket persisted by an earlier
   * build carries only `matchTitle`, and a reader must not lose their legs
   * because the shape grew. A leg without them renders the joined title, which
   * is exactly what it rendered before.
   */
  homeTeam?: string;
  awayTeam?: string;
  homeTeamLogo?: string;
  awayTeamLogo?: string;
  marketKey: string;
  selection: string;
  odds: number | null;
  oddsType: "market" | "fair" | "unavailable";
  kickoffAt: string;
  matchStatus: string;
  source: "manual" | "acca";
  groupId?: string;
  groupLabel?: string;
};

export type SharedBuilderLeg = Pick<BuilderLeg, "predictionId" | "matchId" | "marketKey" | "selection">;
export type BuilderPruneResult = { active: BuilderLeg[]; expired: BuilderLeg[] };

const MAX_SHARED_LEGS = 40;
const MAX_SHARED_TOKEN_LENGTH = 8_192;

const NOT_STARTED_STATUSES = new Set([
  "", "scheduled", "upcoming", "not_started", "not started", "ns", "tbd",
]);

export function builderLegId(predictionId: number, matchId: number, marketKey: string, selection: string) {
  return `${predictionId}:${matchId}:${marketKey}:${selection}`;
}

export function hasBuilderLegStarted(leg: BuilderLeg, now = Date.now()): boolean {
  if (!NOT_STARTED_STATUSES.has((leg.matchStatus || "").trim().toLowerCase())) return true;
  const kickoff = Date.parse(leg.kickoffAt);
  return Number.isFinite(kickoff) && kickoff <= now;
}

function normalizeLeg(value: unknown): BuilderLeg | null {
  if (typeof value !== "object" || value === null) return null;
  const candidate = value as Record<string, unknown>;
  if (
    typeof candidate.matchId !== "number" ||
    typeof candidate.matchTitle !== "string" ||
    typeof candidate.marketKey !== "string" ||
    typeof candidate.selection !== "string" ||
    (candidate.odds !== null && typeof candidate.odds !== "number") ||
    typeof candidate.kickoffAt !== "string"
  ) return null;
  const predictionId = typeof candidate.predictionId === "number"
    ? candidate.predictionId
    : typeof candidate.id === "number" ? candidate.id : 0;
  return {
    id: typeof candidate.id === "string"
      ? candidate.id
      : builderLegId(predictionId, candidate.matchId, candidate.marketKey, candidate.selection),
    predictionId,
    matchId: candidate.matchId,
    matchTitle: candidate.matchTitle,
    /*
      Read back only when they are strings. A leg stored before these fields
      existed has them `undefined`, and the renderer falls back to the joined
      title rather than drawing half a pair.
    */
    homeTeam: typeof candidate.homeTeam === "string" ? candidate.homeTeam : undefined,
    awayTeam: typeof candidate.awayTeam === "string" ? candidate.awayTeam : undefined,
    homeTeamLogo: typeof candidate.homeTeamLogo === "string" ? candidate.homeTeamLogo : undefined,
    awayTeamLogo: typeof candidate.awayTeamLogo === "string" ? candidate.awayTeamLogo : undefined,
    marketKey: candidate.marketKey,
    selection: candidate.selection,
    odds: candidate.odds as number | null,
    oddsType: candidate.oddsType === "market" || candidate.oddsType === "fair"
      ? candidate.oddsType
      : candidate.odds == null ? "unavailable" : "market",
    kickoffAt: candidate.kickoffAt,
    matchStatus: typeof candidate.matchStatus === "string" ? candidate.matchStatus : "scheduled",
    source: candidate.source === "acca" ? "acca" : "manual",
    groupId: typeof candidate.groupId === "string" ? candidate.groupId : undefined,
    groupLabel: typeof candidate.groupLabel === "string" ? candidate.groupLabel : undefined,
  };
}

function base64URL(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

function fromBase64URL(value: string): Uint8Array {
  const padded = value.replaceAll("-", "+").replaceAll("_", "/").padEnd(Math.ceil(value.length / 4) * 4, "=");
  const binary = atob(padded);
  return Uint8Array.from(binary, (character) => character.charCodeAt(0));
}

export function encodeBuilderSelection(legs: BuilderLeg[], now = Date.now()): string {
  const references = pruneBuilderLegs(legs, now).active.slice(0, MAX_SHARED_LEGS).map((leg) => [leg.predictionId, leg.matchId, leg.marketKey, leg.selection]);
  return base64URL(new TextEncoder().encode(JSON.stringify(references)));
}

export function decodeBuilderSelection(token: string): SharedBuilderLeg[] {
  if (!token || token.length > MAX_SHARED_TOKEN_LENGTH || !/^[A-Za-z0-9_-]+$/.test(token)) return [];
  try {
    const parsed: unknown = JSON.parse(new TextDecoder().decode(fromBase64URL(token)));
    if (!Array.isArray(parsed) || parsed.length > MAX_SHARED_LEGS) return [];
    const unique = new Map<string, SharedBuilderLeg>();
    for (const value of parsed) {
      if (!Array.isArray(value) || value.length !== 4) return [];
      const [predictionId, matchId, marketKey, selection] = value;
      if (
        !Number.isSafeInteger(predictionId) || predictionId < 1 ||
        !Number.isSafeInteger(matchId) || matchId < 1 ||
        typeof marketKey !== "string" || marketKey.length < 1 || marketKey.length > 80 ||
        typeof selection !== "string" || selection.length < 1 || selection.length > 120
      ) return [];
      const reference = { predictionId, matchId, marketKey, selection };
      unique.set(builderLegId(predictionId, matchId, marketKey, selection), reference);
    }
    return [...unique.values()];
  } catch {
    return [];
  }
}

function parseStored(raw: string | null): BuilderLeg[] {
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.map(normalizeLeg).filter((leg): leg is BuilderLeg => leg !== null);
  } catch {
    return [];
  }
}

export function readBuilderSelection(): BuilderLeg[] {
  if (typeof window === "undefined") return [];
  try {
    const current = parseStored(window.sessionStorage.getItem(STORAGE_KEY));
    if (current.length > 0) return current;
    return parseStored(window.sessionStorage.getItem(LEGACY_STORAGE_KEY));
  } catch {
    return [];
  }
}

export function pruneBuilderLegs(legs: BuilderLeg[], now = Date.now()): BuilderPruneResult {
  const active: BuilderLeg[] = [];
  const expired: BuilderLeg[] = [];
  for (const leg of legs) (hasBuilderLegStarted(leg, now) ? expired : active).push(leg);
  return { active, expired };
}

export function pruneBuilderSelection(now = Date.now()): BuilderPruneResult {
  const current = readBuilderSelection();
  const result = pruneBuilderLegs(current, now);
  if (result.expired.length > 0) writeBuilderSelection(result.active);
  return result;
}

export function builderSelectionCount(): number {
  return pruneBuilderSelection().active.length;
}

/**
 * One selection per fixture, which is what the Builder already promises.
 *
 * The surface says "Tap a price to add it - one pick per match", but the ticket
 * was keyed on `leg.id`, so two markets on the same fixture were two distinct
 * ids and both survived. That is not only a broken invariant: `totalOdds`
 * multiplies every leg, and two markets on one match are correlated, so the
 * ticket overstated its own return. The rule is about arithmetic, not tidiness.
 *
 * Keyed on `matchId`, newest wins -- a later tap REPLACES the pick already held
 * for that fixture, which is the behaviour the design describes. To reject the
 * new pick instead and keep the existing one, reverse the merge order in
 * `addBuilderLegs`; nothing else needs to change.
 *
 * Safe for ACCA imports: across 16,890 published slips on production, no slip
 * contains two legs from the same fixture, so collapsing here cannot silently
 * drop a leg from an authored ACCA.
 */
export function writeBuilderSelection(legs: BuilderLeg[]): void {
  if (typeof window === "undefined") return;
  const unique = [...new Map(legs.map((leg) => [leg.matchId, leg])).values()];
  try {
    window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(unique));
    window.sessionStorage.removeItem(LEGACY_STORAGE_KEY);
  } catch {
    // Best effort: session storage can be disabled by the browser.
  }
  window.dispatchEvent(new CustomEvent(BUILDER_EVENT_NAME, { detail: { count: unique.length } }));
}

export function addBuilderLegs(legs: BuilderLeg[]): BuilderLeg[] {
  const now = Date.now();
  const next = pruneBuilderLegs([...readBuilderSelection(), ...legs], now).active;
  writeBuilderSelection(next);
  return readBuilderSelection();
}

export function totalOdds(legs: BuilderLeg[]): number | null {
  if (legs.length === 0 || !legs.every((leg) => typeof leg.odds === "number" && leg.odds > 0)) return null;
  return legs.reduce((acc, leg) => acc * (leg.odds as number), 1);
}

export { formatOdds } from "../lib/odds.ts";
export type { OddsFormat } from "../lib/odds.ts";
