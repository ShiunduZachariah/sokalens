import { competitionPopularity } from "./competitionOrder.ts";

/**
 * Why the engine put this in front of you.
 *
 * The Builder already ranks matches by
 * `(isBestBet ? 100 : 0) + confidence + probability * 20` and shows nothing
 * about it, so a reader scrolling the list has no way to tell why the top card
 * is the top card. Same on the generator: it hands back a slip with no account
 * of what made those legs eligible. These tags are that account.
 *
 * ## Every tag is derived, and every tag says from what
 *
 * A tag carries a `detail` that names the number behind it, because a chip
 * reading "High confidence" with nothing underneath is marketing. The reader
 * can check each one against the figure on the same card.
 *
 * Nothing here is a new signal. There is deliberately no "Value" tag: value
 * means the price is better than it should be, which needs a bookmaker's odds
 * to compare against, and a fair-odds-only payload cannot support that claim.
 * Inventing one would be the single most misleading thing this file could do.
 *
 * ## The thresholds are the site's, not new ones
 *
 * 70% is already where `lib/matchPreview.ts` starts calling a reading "high"
 * in prose. A tag that drew the line somewhere else would contradict the
 * paragraph underneath it on the same page; `recommendationTags.test.mjs` pins
 * the two together.
 */
export type RecommendationTag = {
  /** Stable key, for React and for tests. Never shown. */
  key: string;
  label: string;
  /** One sentence naming the figure the tag was derived from. */
  detail: string;
  /**
   * `strong` is the accent fill, for the two tags the engine itself asserts.
   * Everything else is a quiet outline: a card wearing four filled chips tells
   * a reader nothing about which one matters.
   */
  tone: "strong" | "quiet";
};

/** Where `matchPreview` starts calling a confidence reading high. */
export const HIGH_CONFIDENCE = 70;
/**
 * Ranked-competition cutoff. `competitionPopularity` returns the index inside
 * its ranked list, and 100+ for anything only ranked by country — so "< 100"
 * is exactly "named in the ranked list" without this file keeping its own copy
 * of that list.
 */
const RANKED_COMPETITION_CEILING = 100;
/** A price short enough that the forecast, not the return, is the point. */
const SHORT_PRICE_MAX = 1.5;
/** A price long enough that a reader should be told before they are tempted. */
const LONG_SHOT_MIN = 5;

type Selection = {
  confidence: number;
  country: string;
  isBestBet: boolean;
  leagueName: string;
  probability: number;
};

function fairOddsOf(probability: number) {
  return probability > 0 && Number.isFinite(probability) ? 1 / probability : 0;
}

function isTopCompetition(selection: Selection) {
  return competitionPopularity(selection.country, selection.leagueName) < RANKED_COMPETITION_CEILING;
}

/**
 * The tags for one selection — one market on one fixture.
 *
 * Ordered most-asserted first, so a caller that can only fit two shows the two
 * that came from the engine rather than the two that came from arithmetic.
 */
export function selectionTags(selection: Selection): RecommendationTag[] {
  const tags: RecommendationTag[] = [];
  const odds = fairOddsOf(selection.probability);

  if (selection.isBestBet) {
    tags.push({
      key: "best-bet",
      label: "Best bet",
      detail: "The engine marked this selection one of its Best Bets for the day.",
      tone: "strong",
    });
  }
  if (selection.confidence >= HIGH_CONFIDENCE) {
    tags.push({
      key: "high-confidence",
      label: "High confidence",
      detail: `Rated ${Math.round(selection.confidence)}% confidence, at or above the ${HIGH_CONFIDENCE}% the model calls high.`,
      tone: "strong",
    });
  }
  if (isTopCompetition(selection)) {
    tags.push({
      key: "top-competition",
      label: "Top competition",
      detail: `${selection.leagueName} is one of the competitions with the deepest data behind it.`,
      tone: "quiet",
    });
  }
  if (odds > 0 && odds <= SHORT_PRICE_MAX) {
    tags.push({
      key: "short-price",
      label: "Short price",
      detail: `Fair odds of ${odds.toFixed(2)}: likely, and priced accordingly.`,
      tone: "quiet",
    });
  } else if (odds >= LONG_SHOT_MIN) {
    tags.push({
      key: "long-shot",
      label: "Long shot",
      detail: `Fair odds of ${odds.toFixed(2)}: the model gives this roughly a ${Math.round(100 / odds)}% chance.`,
      tone: "quiet",
    });
  }
  return tags;
}

/**
 * The tags for one fixture, from every selection the engine published on it.
 *
 * A match card carries up to twenty market buttons, and tagging each one would
 * cover the card in chips. These describe the fixture: the strongest reading on
 * it, whether a Best Bet is among them, and how many markets qualified at all —
 * which is itself a signal, because a fixture where the engine would only
 * publish one market is a fixture it knows less about.
 */
export function matchTags(selections: Selection[]): RecommendationTag[] {
  if (selections.length === 0) return [];
  const best = selections.reduce((top, item) => (item.confidence > top.confidence ? item : top));
  const tags: RecommendationTag[] = [];

  if (selections.some((item) => item.isBestBet)) {
    tags.push({
      key: "best-bet",
      label: "Best bet here",
      detail: "One of the engine's Best Bets for the day is on this fixture.",
      tone: "strong",
    });
  }
  if (best.confidence >= HIGH_CONFIDENCE) {
    tags.push({
      key: "high-confidence",
      label: "High confidence",
      detail: `Its strongest market is rated ${Math.round(best.confidence)}% confidence.`,
      tone: "strong",
    });
  }
  if (isTopCompetition(best)) {
    tags.push({
      key: "top-competition",
      label: "Top competition",
      detail: `${best.leagueName} is one of the competitions with the deepest data behind it.`,
      tone: "quiet",
    });
  }
  if (selections.length >= 5) {
    tags.push({
      key: "many-markets",
      label: `${selections.length} markets qualified`,
      detail: "The engine published this many markets on this fixture, which it only does where the statistics support them.",
      tone: "quiet",
    });
  }
  return tags;
}

type Slip = {
  combinedFairOdds: number;
  legs: Array<Selection & { kickoffAt: string; marketKey: string }>;
};

/**
 * The tags for a whole ACCA.
 *
 * The shape of the ticket, not a verdict on it: how many markets and countries
 * it spans, whether it settles in a day, where its price sits. A reader
 * comparing two generated slips has nothing else to go on, because both are
 * five rows of fixtures they have never heard of.
 */
export function slipTags(slip: Slip): RecommendationTag[] {
  const { legs } = slip;
  if (legs.length === 0) return [];
  const tags: RecommendationTag[] = [];

  if (legs.some((leg) => leg.isBestBet)) {
    tags.push({
      key: "best-bet-inside",
      label: "Best bet inside",
      detail: "At least one leg is one of the engine's Best Bets for the day.",
      tone: "strong",
    });
  }
  if (legs.every((leg) => leg.confidence >= HIGH_CONFIDENCE)) {
    // Every leg, not the average: an average hides one weak leg behind four
    // strong ones, and on an ACCA the weak leg is the one that settles it.
    tags.push({
      key: "all-high-confidence",
      label: "Every leg high confidence",
      detail: `No leg is rated below ${HIGH_CONFIDENCE}% confidence.`,
      tone: "strong",
    });
  }

  const markets = new Set(legs.map((leg) => leg.marketKey));
  tags.push(markets.size === 1
    ? {
      key: "single-market",
      label: "One market",
      detail: "Every leg is the same market, so the ticket rides on one kind of read.",
      tone: "quiet",
    }
    : {
      key: "mixed-markets",
      label: `${markets.size} markets`,
      detail: "The legs come from different markets, which spreads the ticket across different kinds of read.",
      tone: "quiet",
    });

  const days = new Set(legs.map((leg) => leg.kickoffAt.slice(0, 10)));
  if (days.size === 1) {
    tags.push({
      key: "same-day",
      label: "Settles in a day",
      detail: "Every leg kicks off on the same day.",
      tone: "quiet",
    });
  } else {
    tags.push({
      key: "spans-days",
      label: `Over ${days.size} days`,
      detail: "The legs are spread across several days, so the ticket is not decided until the last one.",
      tone: "quiet",
    });
  }

  if (legs.every((leg) => isTopCompetition(leg))) {
    tags.push({
      key: "top-competitions",
      label: "Top competitions only",
      detail: "Every leg is in one of the competitions with the deepest data behind it.",
      tone: "quiet",
    });
  }

  const odds = slip.combinedFairOdds;
  if (Number.isFinite(odds) && odds >= LONG_SHOT_MIN * 2) {
    tags.push({
      key: "long-shot",
      label: "Long shot",
      detail: `Combined fair odds of ${odds.toFixed(2)}: around a ${(100 / odds).toFixed(1)}% chance of every leg landing.`,
      tone: "quiet",
    });
  }
  return tags;
}
