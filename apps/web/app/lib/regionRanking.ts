/**
 * Ranking for the home / top predictions surface.
 *
 * Before this existed the only ordering signal was kickoff time, so whatever
 * started soonest led the page for everybody. A viewer in São Paulo would open
 * the homepage to a Kenyan Premier League fixture.
 *
 * `leagues.priority_score` cannot fill the gap: exactly one league of 2524 has
 * a non-zero score (Kenyan Premier League at 90). It is a home-market boost,
 * not a quality tier, so the curated tier below carries that job instead.
 *
 * Applies to top predictions ONLY. ACCA ordering is deliberately untouched.
 */

export type RankableFeedItem = {
  confidence?: number | null;
  kickoffAt?: string | null;
  leagueCountry?: string | null;
  leagueName?: string | null;
};

/** Ordered bands, so a human can look at the page and explain the order. */
//
// ELITE sits ABOVE home country deliberately. A reader in Nairobi is more
// likely to want Arsenal v Chelsea than a mid-table FKF fixture, and the same
// holds for a World Cup match anywhere on earth. Home country still beats
// elite when both apply to the same league (Brazil's own Serie A scores
// 1000 + 600), so a domestic top flight still leads for its own audience.
const WEIGHT_ELITE = 1200;
const WEIGHT_HOME_COUNTRY = 1000;
const WEIGHT_TOP_LEAGUE = 600;
const WEIGHT_SAME_CONTINENT = 300;

// Senior men's football is the default surface, expressed as a SORT BAND
// rather than a weight. No penalty or cap works reliably here: a senior
// fixture can legitimately score near zero, so any non-senior score high
// enough to be "demoted but visible" still outranks it. Banding in
// rankPredictions is unambiguous -- every senior fixture precedes every
// non-senior one, and standing orders within each band.
const WEIGHT_CONFIDENCE_MAX = 100;
const WEIGHT_IMMINENCE_MAX = 50;

/** How far ahead a fixture still earns some imminence credit. */
const IMMINENCE_WINDOW_HOURS = 48;

/**
 * Curated global tier, keyed on `COUNTRY|NAME`.
 *
 * Keyed on the pair because league names are NOT unique in this dataset:
 * "Premier League" exists for AM, BB, BD, BH, BM and BT as well as GB,
 * "Bundesliga" for AT as well as DE, and "Ligue 1" for BF as well as FR.
 * Matching on name alone would promote Bhutan's top flight to the front page.
 */
const ELITE_LEAGUES = new Set([
  "GB|premier league",
  "ES|la liga",
  "IT|serie a",
  "DE|bundesliga",
  "FR|ligue 1",
]);

/**
 * Elite competitions with no country attached. Matched on the whole normalised
 * name, never as a substring: "uefa champions league women" and
 * "world cup - qualification asia" are different competitions from the two
 * they contain, and both must miss this set.
 */
const ELITE_COMPETITIONS = new Set([
  "world cup",
  "uefa champions league",
  "euro championship",
  "european championship",
  "copa america",
  "africa cup of nations",
]);

/** Well known, but a rung below. Qualifiers land here rather than in ELITE. */
const MAJOR_LEAGUES = new Set([
  "PT|primeira liga",
  "NL|eredivisie",
  "BR|serie a",
  "AR|liga profesional argentina",
  "US|major league soccer",
  "GB|championship",
]);

const MAJOR_COMPETITION_PATTERNS = [
  "uefa europa league",
  "uefa europa conference league",
  "copa libertadores",
  "caf champions league",
  "concacaf champions league",
  "afc champions league elite",
  "fifa club world cup",
  "world cup qualification",
];

/**
 * Women's and youth competitions, across the spellings that actually occur in
 * the catalogue: "NWSL Women", "Supercup der Frauen", "CONMEBOL U20 Femenino",
 * "1a Divisão - Women", "AFC Women's Champions League".
 */
const NON_SENIOR_MENS = /\b(women|womens|femenino|feminina|feminine|femminile|frauen|dames|ladies|girls)\b|\bwomen's\b|\(w\)|\bu-?(1[5-9]|2[0-3])\b|\b(youth|junior|juvenil|primavera)\b/i;

/** Minimal continent map: enough to answer "is this near me?". */
const CONTINENT_BY_COUNTRY: Record<string, string> = {
  KE: "AF", UG: "AF", TZ: "AF", NG: "AF", GH: "AF", ZA: "AF", EG: "AF", MA: "AF",
  SN: "AF", CI: "AF", CM: "AF", DZ: "AF", TN: "AF", ET: "AF", ZM: "AF", RW: "AF",
  GB: "EU", IE: "EU", ES: "EU", IT: "EU", DE: "EU", FR: "EU", PT: "EU", NL: "EU",
  BE: "EU", AT: "EU", CH: "EU", SE: "EU", NO: "EU", DK: "EU", FI: "EU", PL: "EU",
  GR: "EU", TR: "EU", RU: "EU", UA: "EU", CZ: "EU", RO: "EU", HR: "EU", RS: "EU",
  BR: "SA", AR: "SA", CL: "SA", CO: "SA", PE: "SA", UY: "SA", EC: "SA", PY: "SA",
  BO: "SA", VE: "SA",
  US: "NA", CA: "NA", MX: "NA", CR: "NA", JM: "NA", HN: "NA", PA: "NA",
  JP: "AS", KR: "AS", CN: "AS", IN: "AS", SA: "AS", AE: "AS", QA: "AS", IR: "AS",
  ID: "AS", TH: "AS", VN: "AS", MY: "AS", UZ: "AS",
  AU: "OC", NZ: "OC",
};

function normalizeCountry(value: string | null | undefined): string {
  const code = String(value ?? "").trim().toUpperCase();
  return /^[A-Z]{2}$/.test(code) ? code : "";
}

function normalizeName(value: string | null | undefined): string {
  return String(value ?? "").trim().toLowerCase();
}

/** Senior men's football is the default surface; everything else is demoted. */
export function isSeniorMens(leagueName: string | null | undefined): boolean {
  return !NON_SENIOR_MENS.test(String(leagueName ?? ""));
}

/**
 * Competition standing. Exact-match on the full normalised name for
 * country-less competitions, so "UEFA Champions League Women" and
 * "World Cup - Qualification Asia" cannot inherit the rank of the competition
 * whose name they contain.
 */
export function competitionTier(
  leagueName: string | null | undefined,
  leagueCountry: string | null | undefined,
): "elite" | "major" | "none" {
  // Strip the women's/youth marker before looking up standing. The women's
  // Champions League IS an elite women's competition; it should be tiered as
  // such and then demoted once, not denied a tier and demoted as well.
  const name = normalizeName(leagueName)
    .replace(NON_SENIOR_MENS, " ")
    .replace(/[-–—]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  if (!name) return "none";

  if (ELITE_COMPETITIONS.has(name)) return "elite";
  if (MAJOR_COMPETITION_PATTERNS.some((pattern) => name.startsWith(pattern))) return "major";

  const country = normalizeCountry(leagueCountry);
  if (!country) return "none";
  if (ELITE_LEAGUES.has(`${country}|${name}`)) return "elite";
  return MAJOR_LEAGUES.has(`${country}|${name}`) ? "major" : "none";
}

/** Kept for callers that only need "is this notable at all". */
export function isTopLeague(
  leagueName: string | null | undefined,
  leagueCountry: string | null | undefined,
): boolean {
  return competitionTier(leagueName, leagueCountry) !== "none";
}

export function continentOf(country: string | null | undefined): string {
  return CONTINENT_BY_COUNTRY[normalizeCountry(country)] ?? "";
}

/**
 * Imminence decays linearly across IMMINENCE_WINDOW_HOURS. A fixture already
 * under way, or beyond the window, earns nothing. Deliberately the smallest
 * weight: it used to be the only one.
 */
function imminenceScore(kickoffAt: string | null | undefined, now: Date): number {
  const kickoff = Date.parse(String(kickoffAt ?? ""));
  if (!Number.isFinite(kickoff)) return 0;
  const hoursAway = (kickoff - now.getTime()) / 3_600_000;
  if (hoursAway <= 0 || hoursAway >= IMMINENCE_WINDOW_HOURS) return 0;
  return WEIGHT_IMMINENCE_MAX * (1 - hoursAway / IMMINENCE_WINDOW_HOURS);
}

export function scorePrediction(
  item: RankableFeedItem,
  viewerCountry: string | null | undefined,
  now: Date = new Date(),
): number {
  const viewer = normalizeCountry(viewerCountry);
  const leagueCountry = normalizeCountry(item.leagueCountry);

  let score = 0;

  if (viewer && leagueCountry && viewer === leagueCountry) {
    score += WEIGHT_HOME_COUNTRY;
  }
  const tier = competitionTier(item.leagueName, item.leagueCountry);
  if (tier === "elite") score += WEIGHT_ELITE;
  else if (tier === "major") score += WEIGHT_TOP_LEAGUE;
  if (viewer && leagueCountry && viewer !== leagueCountry) {
    const viewerContinent = continentOf(viewer);
    if (viewerContinent && viewerContinent === continentOf(leagueCountry)) {
      score += WEIGHT_SAME_CONTINENT;
    }
  }

  const confidence = Number(item.confidence);
  if (Number.isFinite(confidence)) {
    score += Math.max(0, Math.min(WEIGHT_CONFIDENCE_MAX, confidence));
  }

  score += imminenceScore(item.kickoffAt, now);

  return score;
}

/**
 * Highest score first. Ties keep their original relative order, so an upstream
 * sort (kickoff ascending) still shows through where nothing distinguishes two
 * fixtures. Does not mutate the input.
 */
export function rankPredictions<T extends RankableFeedItem>(
  items: readonly T[],
  viewerCountry: string | null | undefined,
  now: Date = new Date(),
): T[] {
  return items
    .map((item, index) => ({
      index,
      item,
      score: scorePrediction(item, viewerCountry, now),
      senior: isSeniorMens(item.leagueName),
    }))
    .sort(
      (left, right) =>
        // Band first: every senior men's fixture precedes every women's or
        // youth one. Standing orders within each band.
        Number(right.senior) - Number(left.senior) ||
        right.score - left.score ||
        left.index - right.index,
    )
    .map((entry) => entry.item);
}
