import type { AccaBundle, AccaLeg } from "./accaBundles";
import { marketLabel } from "../lib/markets.ts";
import type { Locale } from "../i18n/config.ts";

/**
 * The detail page's reading of one stored ACCA.
 *
 * Everything here is pure and takes `now` explicitly, because the three states
 * the design draws (open, in progress, settled) are a function of the clock and
 * a page that read the clock itself could not be tested without one.
 *
 * ## The one state the backend does not send
 *
 * `GET /v1/accas/{id}` grades each leg `pending | won | lost | void`, and
 * `services/api/internal/acca/source.go` is explicit that the mapping is copied
 * from the match page so the two cannot disagree. There is no `live`: a leg
 * whose match is being played right now has no result row yet, so it reads
 * `pending` exactly like one that kicks off tomorrow.
 *
 * The design needs the difference — "3 won · 2 live · 1 to play" is the whole
 * point of the in-progress banner — so it is derived here from the one signal
 * the payload does carry: a `pending` leg whose kick-off has passed is being
 * played. That is a presentational inference and it is deliberately NOT sent to
 * anything that grades, prices or settles. It can be wrong in one direction
 * only, and harmlessly: a postponed fixture reads `live` until the provider
 * voids it. Calling it `pending` instead would be wrong for every match
 * actually in progress, which is the common case on this page.
 *
 * `match_status` would settle it properly, and the leg aggregate does not select
 * it. Raised in `docs/integrations/in-play-data-contract.md` rather than worked
 * around further.
 */
export type AccaLegStatus = "pending" | "won" | "lost" | "void";

/** The leg states the page renders, which is the wire set plus `live`. */
export type AccaLegDisplayState = AccaLegStatus | "live";

/** How long after kick-off a leg is still treated as being played. */
const LIVE_WINDOW_MS = 3.5 * 60 * 60 * 1000;

export function legDisplayState(leg: AccaLeg, now: number): AccaLegDisplayState {
  const status = leg.status ?? "pending";
  if (status !== "pending") return status;
  const kickoff = Date.parse(leg.kickoffAt);
  if (!Number.isFinite(kickoff) || kickoff > now) return "pending";
  // Past the window a pending leg is a leg the grader has not reached, not a
  // match still being played. Saying "live" about a fixture that finished two
  // days ago would be a lie the reader can check.
  return now - kickoff <= LIVE_WINDOW_MS ? "live" : "pending";
}

export type AccaSlipLeg = AccaLeg & { state: AccaLegDisplayState };

/** One day's legs, in kick-off order, keyed by its UTC date. */
export type AccaSlipDay = { key: string; legs: AccaSlipLeg[] };

/**
 * Legs in kick-off order, grouped by UTC day.
 *
 * UTC, from the ISO date prefix rather than a `Date` — the eyebrow says UTC, and
 * grouping by the viewer's local day would put a 23:00 kick-off in a different
 * group on the server than in the browser and hydrate differently.
 */
export function groupLegsByDay(legs: AccaSlipLeg[]): AccaSlipDay[] {
  const days: AccaSlipDay[] = [];
  for (const leg of legs) {
    const key = leg.kickoffAt.slice(0, 10);
    const last = days[days.length - 1];
    if (last && last.key === key) last.legs.push(leg);
    else days.push({ key, legs: [leg] });
  }
  return days;
}

export type AccaSlipResult = {
  tone: "live" | "won" | "lost";
  label: string;
  /** The counts line under the label. */
  line: string;
};

export type AccaSlipView = {
  legs: AccaSlipLeg[];
  days: AccaSlipDay[];
  /** null while every leg is still to come: there is nothing to report yet. */
  result: AccaSlipResult | null;
  /** First and last kick-off, from the legs rather than the slip envelope. */
  firstKickoffAt: string;
  lastKickoffAt: string;
};

function count(legs: AccaSlipLeg[], state: AccaLegDisplayState) {
  return legs.filter((leg) => leg.state === state).length;
}

/**
 * What the slip's odds settle at, ignoring void legs.
 *
 * A void leg is removed from the ticket rather than lost, so the price it was
 * priced at no longer applies. Reporting the original combined odds on a slip
 * with a void leg would overstate the return.
 */
export function settlingOdds(legs: AccaSlipLeg[]) {
  return legs
    .filter((leg) => leg.state !== "void")
    .reduce((total, leg) => total * (leg.fairOdds > 0 && Number.isFinite(leg.fairOdds) ? leg.fairOdds : 1), 1);
}

export function accaSlipView(bundle: AccaBundle, now: number): AccaSlipView {
  const legs = bundle.legs
    .map((leg) => ({ ...leg, state: legDisplayState(leg, now) }))
    .sort((left, right) => Date.parse(left.kickoffAt) - Date.parse(right.kickoffAt));

  const won = count(legs, "won");
  const lost = count(legs, "lost");
  const voided = count(legs, "void");
  const live = count(legs, "live");
  const pending = count(legs, "pending");

  // Nothing has started and nothing is graded: the slip is a forecast, and a
  // banner reading "0 won · 0 live · 6 to play" tells the reader less than the
  // legs already do.
  const result: AccaSlipResult | null = won + lost + voided + live === 0
    ? null
    : live + pending > 0
      ? { tone: "live", label: "In progress", line: `${won} won · ${live} live · ${pending} to play` }
      : lost > 0
        ? { tone: "lost", label: "Lost", line: `${won} won · ${lost} lost · ${voided} void` }
        : { tone: "won", label: "Won", line: `${won} won · ${voided} void · settles at ${settlingOdds(legs).toFixed(2)}` };

  return {
    legs,
    days: groupLegsByDay(legs),
    result,
    firstKickoffAt: legs[0]?.kickoffAt ?? "",
    lastKickoffAt: legs[legs.length - 1]?.kickoffAt ?? "",
  };
}

/** The label on a leg's status pill. `pending` is "Not started" per the design. */
export const legStateLabels: Record<AccaLegDisplayState, string> = {
  pending: "Not started",
  live: "Live",
  won: "Won",
  lost: "Lost",
  void: "Void",
};

/**
 * How the slip's market is named in the heading and the crumb.
 *
 * `market_tab` is `all` for a slip whose legs come from different markets, and
 * `marketLabel("all")` humanises that to "All" — which reads as "all markets"
 * when it means "several". The design's word is "Mixed", and it is the honest
 * one.
 */
export function slipMarketLabel(marketKey: string, locale: Locale): string {
  const key = marketKey.trim().toLowerCase();
  if (key === "" || key === "all" || key === "mixed") return "Mixed";
  return marketLabel(key, locale);
}

/**
 * "Sep 27" when every leg is on one day, "Sep 27 – Sep 29" when they are not.
 *
 * UTC, to agree with the eyebrow that says so and with the day grouping. A
 * reader's own timezone is applied to each kick-off time by `LocalTime`; this
 * line is the catalogue's own framing of the window, which is UTC everywhere.
 */
export function slipWindowLabel(firstKickoffAt: string, lastKickoffAt: string): string {
  const first = monthDay(firstKickoffAt);
  const last = monthDay(lastKickoffAt);
  if (!first) return "";
  if (!last || first === last) return first;
  return `${first} – ${last}`;
}

function monthDay(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleDateString("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });
}
