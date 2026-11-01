import { HIDDEN_PUBLIC_MARKETS, marketLabel, marketSelectionLabel } from "./markets.ts";
import type { TeamDiscipline } from "./discipline.ts";
import type { TeamForm } from "./teamDepth.ts";

/**
 * One published pick, read the way a person would say it, with the recorded
 * figures that support or contradict it.
 *
 * The catalogue labels are written for a market table: "Over 9.5 Corners",
 * "Home Yes", "Draw / Away". Inside a sentence those read as jargon, so each
 * market family is translated into the outcome it actually describes -- "10 or
 * more corners", "Arsenal to win with both teams scoring", "level at half-time
 * and Chelsea to win".
 *
 * Every reader returns null for a selection it cannot parse. The engine's
 * vocabulary has drifted before ("no_goal" and "no_goals" both exist), and a
 * sentence built from a half-parsed selection would state something false on a
 * page whose whole job is to be checkable. A null costs one line of the
 * preview; a wrong line costs the reader's trust.
 */

export type PickTeams = { homeTeam: string; awayTeam: string };

export type PickContext = PickTeams & {
  homeForm?: string;
  awayForm?: string;
  homeStats?: TeamForm | null;
  awayStats?: TeamForm | null;
  /** Card records from before kickoff (cards = yellow + red). */
  homeDiscipline?: TeamDiscipline | null;
  awayDiscipline?: TeamDiscipline | null;
};

export type ReadablePick = {
  marketKey: string;
  selection: string;
  confidence: number;
  /** ISO time the pick was published; card lines need it (see CARD_UNIT_FIXED_AT). */
  publishedAt?: string;
};

export type PickRead = {
  /** The outcome as a phrase: "Pharco to win", "10 or more corners". */
  outcome: string;
  /** A full sentence of recorded evidence, when there is any to cite. */
  evidence?: string;
};

/** A read plus what the page needs around it. */
export type DescribedPick = PickRead & {
  /** The catalogue market name, e.g. "Double Chance". */
  market: string;
  /** True when no dedicated reader exists and the outcome is the bare label. */
  generic: boolean;
  confidence: number;
};

type Side = "home" | "away";
type Result = Side | "draw";

/** Below this many settled matches a rate is noise, not a figure. */
export const MIN_MATCHES_FOR_RATES = 4;

const RESULT_ALIASES: Record<string, Result> = {
  home: "home", "1": "home", draw: "draw", x: "draw", away: "away", "2": "away",
};

function normalize(value: string) {
  return String(value ?? "").trim().toLowerCase();
}

function teamName(teams: PickTeams, side: Side) {
  return side === "home" ? teams.homeTeam : teams.awayTeam;
}

function otherSide(side: Side): Side {
  return side === "home" ? "away" : "home";
}

/** "Rangers'" rather than "Rangers's". */
export function possessive(name: string) {
  return /s$/i.test(name) ? `${name}'` : `${name}'s`;
}

/**
 * "over_9.5" as a count a reader can picture.
 *
 * Half lines are how the engine publishes almost everything, and "over 9.5"
 * means "10 or more"; saying so is both plainer and exact. A whole line is
 * kept as "more than 3" because the push case is real there. Anything that is
 * not a finite, non-negative line is refused.
 */
export function lineOutcome(side: string, rawLine: string, unit: string): string | null {
  if (!/^\d+(\.\d+)?$/.test(rawLine)) return null;
  const line = Number(rawLine);
  if (!Number.isFinite(line) || line < 0 || line > 1000) return null;
  const half = Math.abs(line % 1 - 0.5) < 1e-9;
  if (side === "over") {
    if (!half) return `more than ${line} ${unit}`;
    const atLeast = Math.ceil(line);
    return `${atLeast} or more ${unit}`;
  }
  if (side === "under") {
    if (!half) return `fewer than ${line} ${unit}`;
    const atMost = Math.floor(line);
    return atMost === 0 ? `no ${unit}` : `${atMost} or fewer ${unit}`;
  }
  return null;
}

function parseLine(selection: string): { side: "over" | "under"; line: string } | null {
  const match = /^(over|under)_(\S+)$/.exec(selection);
  return match ? { side: match[1] as "over" | "under", line: match[2] } : null;
}

function usable(stats: TeamForm | null | undefined): stats is TeamForm {
  return Boolean(stats && Number.isFinite(stats.matches) && stats.matches >= MIN_MATCHES_FOR_RATES);
}

function rate(part: number, total: number) {
  return Math.round((part / total) * 100);
}

function formPoints(form: string | undefined) {
  const letters = normalize(form ?? "").toUpperCase().split(/[\s,]+/).filter((letter) => letter === "W" || letter === "D" || letter === "L").slice(0, 5);
  if (letters.length < 3) return null;
  return {
    games: letters.length,
    points: letters.reduce((sum, letter) => sum + (letter === "W" ? 3 : letter === "D" ? 1 : 0), 0),
  };
}

// ---------------------------------------------------------------- evidence

/**
 * A points gap over the same run of games that counts as a real difference.
 * Shared with the preview's Form section so the page never calls one gap
 * "little between the sides" and "supports the pick" in consecutive lines.
 */
export const MEANINGFUL_FORM_GAP = 4;

/**
 * Form evidence for a result pick. Says "supports" or "cuts against" only when
 * the gap is real; three points over five games is one result, not a trend.
 */
function formEvidence(context: PickContext, result: Result): string | undefined {
  const home = formPoints(context.homeForm);
  const away = formPoints(context.awayForm);
  if (!home || !away || home.games !== away.games) return undefined;
  const gap = home.points - away.points;
  const summary = `${context.homeTeam} have ${home.points} points from their last ${home.games}, ${context.awayTeam} ${away.points}`;
  if (result === "draw") {
    return Math.abs(gap) < MEANINGFUL_FORM_GAP ? `${summary}, so there is little to separate them.` : `${summary}, a wider gap than a draw usually suggests.`;
  }
  if (Math.abs(gap) < MEANINGFUL_FORM_GAP) return `${summary}, so recent form barely separates them.`;
  const favoured: Side = gap > 0 ? "home" : "away";
  return favoured === result ? `${summary}, which supports it.` : `${summary}, which cuts against it.`;
}

function averageMatchGoals(context: PickContext) {
  const { homeStats, awayStats } = context;
  if (!usable(homeStats) || !usable(awayStats)) return null;
  const home = (homeStats.goalsFor + homeStats.goalsAgainst) / homeStats.matches;
  const away = (awayStats.goalsFor + awayStats.goalsAgainst) / awayStats.matches;
  return (home + away) / 2;
}

function totalsEvidence(context: PickContext, side: "over" | "under", line: number): string | undefined {
  const average = averageMatchGoals(context);
  if (average === null) return undefined;
  const figure = `These sides' matches average ${average.toFixed(1)} goals`;
  const agrees = side === "over" ? average > line : average < line;
  return agrees ? `${figure}, in line with the pick.` : `${figure}, which points the other way.`;
}

function bttsEvidence(context: PickContext, yes: boolean): string | undefined {
  const { homeStats, awayStats } = context;
  if (!usable(homeStats) || !usable(awayStats)) return undefined;
  const home = rate(homeStats.bothTeamsScored, homeStats.matches);
  const away = rate(awayStats.bothTeamsScored, awayStats.matches);
  const figure = `Both teams scored in ${home}% of ${possessive(context.homeTeam)} matches and ${away}% of ${possessive(context.awayTeam)}`;
  const high = home >= 50 && away >= 50;
  const low = home < 50 && away < 50;
  if (!high && !low) return `${figure}.`;
  return high === yes ? `${figure}, which supports it.` : `${figure}, which cuts against it.`;
}

function scoringEvidence(context: PickContext, side: Side): string | undefined {
  const stats = side === "home" ? context.homeStats : context.awayStats;
  const other = side === "home" ? context.awayStats : context.homeStats;
  if (!usable(stats) || !usable(other)) return undefined;
  const mine = stats.goalsFor / stats.matches;
  const theirs = other.goalsFor / other.matches;
  return `${teamName(context, side)} score ${mine.toFixed(1)} a game to ${possessive(teamName(context, otherSide(side)))} ${theirs.toFixed(1)}.`;
}

function cleanSheetEvidence(context: PickContext, side: Side): string | undefined {
  const stats = side === "home" ? context.homeStats : context.awayStats;
  if (!usable(stats)) return undefined;
  return `${teamName(context, side)} have kept a clean sheet in ${rate(stats.cleanSheets, stats.matches)}% of their recorded matches.`;
}

function teamGoalsEvidence(context: PickContext, side: Side, direction: "over" | "under", line: number): string | undefined {
  const stats = side === "home" ? context.homeStats : context.awayStats;
  const opponent = side === "home" ? context.awayStats : context.homeStats;
  if (!usable(stats) || !usable(opponent)) return undefined;
  const scored = stats.goalsFor / stats.matches;
  const conceded = opponent.goalsAgainst / opponent.matches;
  const expected = (scored + conceded) / 2;
  const figure = `${teamName(context, side)} score ${scored.toFixed(1)} a game and ${teamName(context, otherSide(side))} concede ${conceded.toFixed(1)}`;
  const agrees = direction === "over" ? expected > line : expected < line;
  return agrees ? `${figure}, which supports it.` : `${figure}, which cuts against it.`;
}

// ---------------------------------------------------------------- readers

function resultOutcome(teams: PickTeams, result: Result) {
  return result === "draw" ? "a draw" : `${teamName(teams, result)} to win`;
}

function halfTimeOutcome(teams: PickTeams, result: Result) {
  return result === "draw" ? "level at half-time" : `${teamName(teams, result)} ahead at half-time`;
}

function fullTimeClause(teams: PickTeams, result: Result) {
  return result === "draw" ? "a draw at full time" : `${teamName(teams, result)} to win`;
}

function readResult(context: PickContext, selection: string): PickRead | null {
  const result = RESULT_ALIASES[selection];
  if (!result) return null;
  return { outcome: resultOutcome(context, result), evidence: formEvidence(context, result) };
}

function readDoubleChance(context: PickContext, selection: string): PickRead | null {
  // "home_or_draw" and "home_draw" from the engine; "1x", "x2", "12" from
  // some feeds.
  const tokens = /^[1x2]{2}$/.test(selection) ? selection.split("") : selection.replace(/_or_/g, "_").split("_");
  const results = tokens.map((token) => RESULT_ALIASES[token]);
  if (results.length !== 2 || results.some((result) => !result)) return null;
  const sides = new Set(results);
  if (sides.size !== 2) return null;
  if (sides.has("home") && sides.has("draw")) return { outcome: `${context.homeTeam} to avoid defeat`, evidence: formEvidence(context, "home") };
  if (sides.has("away") && sides.has("draw")) return { outcome: `${context.awayTeam} to avoid defeat`, evidence: formEvidence(context, "away") };
  if (sides.has("home") && sides.has("away")) return { outcome: "a winner, not a draw", evidence: undefined };
  return null;
}

function readDrawNoBet(context: PickContext, selection: string): PickRead | null {
  const result = RESULT_ALIASES[selection];
  if (result !== "home" && result !== "away") return null;
  return { outcome: `${teamName(context, result)} to win, with a draw voiding the pick`, evidence: formEvidence(context, result) };
}

function readTotalGoals(context: PickContext, selection: string): PickRead | null {
  const parsed = parseLine(selection);
  if (!parsed) return null;
  const outcome = lineOutcome(parsed.side, parsed.line, "goals");
  if (!outcome) return null;
  return { outcome: `${outcome} in the match`, evidence: totalsEvidence(context, parsed.side, Number(parsed.line)) };
}

function readYesNo(selection: string) {
  return selection === "yes" ? true : selection === "no" ? false : null;
}

function readBtts(context: PickContext, selection: string): PickRead | null {
  const yes = readYesNo(selection);
  if (yes === null) return null;
  return {
    outcome: yes ? "both teams to score" : "at least one side to fail to score",
    evidence: bttsEvidence(context, yes),
  };
}

function readBttsTotal(context: PickContext, selection: string): PickRead | null {
  const match = /^(yes|no)_(over|under)_(\S+)$/.exec(selection);
  if (!match) return null;
  const yes = match[1] === "yes";
  const total = lineOutcome(match[2], match[3], "goals");
  if (!total) return null;
  // Two combinations collapse to something far more specific than their
  // labels suggest, and saying so is the honest reading.
  if (yes && match[2] === "under" && match[3] === "2.5") {
    return { outcome: "both teams to score but no more than 2 goals, which only a 1-1 satisfies" };
  }
  const btts = yes ? "both teams to score" : "only one side (or neither) to score";
  return { outcome: `${btts}, with ${total}`, evidence: totalsEvidence(context, match[2] as "over" | "under", Number(match[3])) };
}

function readBttsResult(context: PickContext, selection: string): PickRead | null {
  const match = /^(home|draw|away)_(yes|no)$/.exec(selection);
  if (!match) return null;
  const result = match[1] as Result;
  const yes = match[2] === "yes";
  if (result === "draw") return { outcome: yes ? "a score draw (both teams scoring)" : "a goalless draw" };
  const team = teamName(context, result);
  return yes
    ? { outcome: `${team} to win with both teams scoring`, evidence: bttsEvidence(context, true) }
    : { outcome: `${team} to win without conceding`, evidence: cleanSheetEvidence(context, result) };
}

function readResultTotal(context: PickContext, selection: string): PickRead | null {
  const match = /^(home|draw|away)_(over|under)_(\S+)$/.exec(selection);
  if (!match) return null;
  const total = lineOutcome(match[2], match[3], "goals");
  if (!total) return null;
  const result = match[1] as Result;
  return {
    outcome: `${resultOutcome(context, result)} in a match with ${total}`,
    evidence: totalsEvidence(context, match[2] as "over" | "under", Number(match[3])),
  };
}

function readTeamGoals(context: PickContext, selection: string): PickRead | null {
  const match = /^(home|away)_(over|under)_(\S+)$/.exec(selection);
  if (!match) return null;
  const side = match[1] as Side;
  const total = lineOutcome(match[2], match[3], "goals");
  if (!total) return null;
  return {
    outcome: `${teamName(context, side)} to score ${total}`,
    evidence: teamGoalsEvidence(context, side, match[2] as "over" | "under", Number(match[3])),
  };
}

function readFirstGoal(context: PickContext, selection: string): PickRead | null {
  if (selection === "no_goal" || selection === "no_goals" || selection === "none") return { outcome: "no goals in the match" };
  const result = RESULT_ALIASES[selection];
  if (result !== "home" && result !== "away") return null;
  return { outcome: `${teamName(context, result)} to score first`, evidence: scoringEvidence(context, result) };
}

function readCleanSheet(context: PickContext, selection: string): PickRead | null {
  const match = /^(home|away)_(yes|no)$/.exec(selection);
  if (!match) return null;
  const side = match[1] as Side;
  const team = teamName(context, side);
  return {
    outcome: match[2] === "yes" ? `${team} to keep a clean sheet` : `${team} to concede at least once`,
    evidence: cleanSheetEvidence(context, side),
  };
}

function readWinToNil(context: PickContext, selection: string): PickRead | null {
  const result = RESULT_ALIASES[selection];
  if (result !== "home" && result !== "away") return null;
  return { outcome: `${teamName(context, result)} to win without conceding`, evidence: cleanSheetEvidence(context, result) };
}

function readWinEitherHalf(context: PickContext, selection: string): PickRead | null {
  const result = RESULT_ALIASES[selection];
  if (result !== "home" && result !== "away") return null;
  return { outcome: `${teamName(context, result)} to win at least one half`, evidence: formEvidence(context, result) };
}

function readHalfTime(context: PickContext, selection: string): PickRead | null {
  const result = RESULT_ALIASES[selection];
  if (!result) return null;
  return { outcome: halfTimeOutcome(context, result) };
}

function readHalfTimeFullTime(context: PickContext, selection: string): PickRead | null {
  const match = /^(?:ht_)?(home|draw|away)_(?:ft_)?(home|draw|away)$/.exec(selection);
  if (!match) return null;
  const half = match[1] as Result;
  const full = match[2] as Result;
  if (half === full) {
    return half === "draw"
      ? { outcome: "level at half-time and at full time" }
      : { outcome: `${teamName(context, half)} to lead at half-time and win`, evidence: formEvidence(context, half) };
  }
  return { outcome: `${halfTimeOutcome(context, half)}, then ${fullTimeClause(context, full)}` };
}

function readHighestHalf(_context: PickContext, selection: string): PickRead | null {
  if (selection === "first_half") return { outcome: "more goals in the first half than the second" };
  if (selection === "second_half") return { outcome: "more goals in the second half than the first" };
  if (selection === "equal") return { outcome: "the same number of goals in each half" };
  return null;
}

function readOddEven(_context: PickContext, selection: string): PickRead | null {
  if (selection === "odd") return { outcome: "an odd number of goals (1, 3, 5...)" };
  if (selection === "even") return { outcome: "an even number of goals (0, 2, 4...)" };
  return null;
}

function readBttsBothHalves(_context: PickContext, selection: string): PickRead | null {
  const yes = readYesNo(selection);
  if (yes === null) return null;
  return { outcome: yes ? "both teams to score in each half" : "at least one half without both teams scoring" };
}

function readPossession(context: PickContext, selection: string): PickRead | null {
  const result = RESULT_ALIASES[selection];
  if (result !== "home" && result !== "away") return null;
  return { outcome: `${teamName(context, result)} to have more of the ball` };
}

/** Line markets over a match statistic: corners, cards, fouls, shots. */
function statLineReader(unit: string, owner?: Side) {
  return (context: PickContext, selection: string): PickRead | null => {
    const parsed = parseLine(selection);
    if (!parsed) return null;
    const outcome = lineOutcome(parsed.side, parsed.line, unit);
    if (!outcome) return null;
    return { outcome: owner ? `${outcome} for ${teamName(context, owner)}` : `${outcome} in the match` };
  };
}

type Reader = (context: PickContext, selection: string) => PickRead | null;

/**
 * When the engine began forecasting and settling cards in one unit.
 *
 * Until then total_cards read fixture_statistics.card_points, which held two
 * scales (yellow + 2*red from the importer, yellow*10 + red*25 from the live
 * statistics job), so a published "over 5.5" could mean six cards or one
 * yellow. The engine now counts yellow + red from the raw columns. A card
 * pick published before this instant was forecast on the mixed data and is
 * not described; one published after it is.
 */
export const CARD_UNIT_FIXED_AT = "2026-09-25T00:00:00Z";

const CARD_MARKETS = new Set(["total_cards", "cards"]);

function publishedAfterCardFix(publishedAt: string | undefined) {
  const at = Date.parse(String(publishedAt ?? ""));
  return Number.isFinite(at) && at >= Date.parse(CARD_UNIT_FIXED_AT);
}

function averageMatchCards(context: PickContext) {
  const records = [context.homeDiscipline, context.awayDiscipline]
    .filter((record): record is TeamDiscipline => Boolean(record && record.matches >= MIN_MATCHES_FOR_RATES));
  if (records.length === 0) return null;
  return records.reduce((sum, record) => sum + record.matchCards / record.matches, 0) / records.length;
}

function readCards(context: PickContext, selection: string): PickRead | null {
  const parsed = parseLine(selection);
  if (!parsed) return null;
  const outcome = lineOutcome(parsed.side, parsed.line, "cards");
  if (!outcome) return null;
  const average = averageMatchCards(context);
  if (average === null) return { outcome: `${outcome} in the match` };
  const line = Number(parsed.line);
  const agrees = parsed.side === "over" ? average > line : average < line;
  const figure = `Recent matches involving these sides average ${average.toFixed(1)} cards`;
  return {
    outcome: `${outcome} in the match`,
    evidence: agrees ? `${figure}, in line with the pick.` : `${figure}, which points the other way.`,
  };
}

const READERS: Record<string, Reader> = {
  "1x2": readResult,
  match_result: readResult,
  double_chance: readDoubleChance,
  draw_no_bet: readDrawNoBet,
  total_goals: readTotalGoals,
  btts: readBtts,
  btts_total_goals: readBttsTotal,
  btts_match_result: readBttsResult,
  result_total_goals: readResultTotal,
  team_ou_goals: readTeamGoals,
  team_over_under_goals: readTeamGoals,
  team_total_goals: readTeamGoals,
  team_goals: readTeamGoals,
  team_to_score_first: readFirstGoal,
  clean_sheet: readCleanSheet,
  win_to_nil: readWinToNil,
  win_either_half: readWinEitherHalf,
  half_time_result: readHalfTime,
  ht_ft: readHalfTimeFullTime,
  halftime_fulltime: readHalfTimeFullTime,
  half_time_full_time: readHalfTimeFullTime,
  highest_scoring_half: readHighestHalf,
  odd_even_total_goals: readOddEven,
  btts_both_halves: readBttsBothHalves,
  possession_winner: readPossession,
  total_corners: statLineReader("corners"),
  corners: statLineReader("corners"),
  team_corners_home: statLineReader("corners", "home"),
  team_corners_away: statLineReader("corners", "away"),
  total_cards: readCards,
  cards: readCards,
  total_fouls: statLineReader("fouls"),
  fouls: statLineReader("fouls"),
  total_shots: statLineReader("shots"),
  shots_on_target: statLineReader("shots on target"),
  throw_ins: statLineReader("throw-ins"),
};

/** Markets this module reads in its own words. Everything else falls back. */
export const READABLE_MARKETS = new Set(Object.keys(READERS));

/**
 * A pick the page may describe at all: a public market with a finite
 * confidence between 1 and 100.
 *
 * The floor is 1, not 0, because the API's confidence is a percentage and a
 * value such as 0.63 is far likelier to be a probability on the wrong scale
 * than a real 0.63% pick -- the engine never publishes a selection that
 * unlikely. Guessing the scale would print a wrong number in a sentence, so
 * anything outside 1-100 is refused.
 */
export function isDescribable(pick: ReadablePick) {
  const key = normalize(pick.marketKey);
  return Boolean(key)
    && Boolean(normalize(pick.selection))
    && !HIDDEN_PUBLIC_MARKETS.has(key)
    && Number.isFinite(pick.confidence)
    && pick.confidence >= 1
    && pick.confidence <= 100;
}

/**
 * Read a pick, or return null when it cannot be described truthfully.
 *
 * Markets without a dedicated reader fall back to the catalogue label, which
 * is plain but never wrong; a malformed selection on a known market does not
 * fall back, because the known reader has already said it cannot parse it.
 */
export function readPick(context: PickContext, pick: ReadablePick): DescribedPick | null {
  if (!isDescribable(pick)) return null;
  const key = normalize(pick.marketKey);
  const selection = normalize(pick.selection);
  const market = marketLabel(key);
  const confidence = Math.round(pick.confidence);
  if (CARD_MARKETS.has(key) && !publishedAfterCardFix(pick.publishedAt)) return null;
  const reader = READERS[key];
  if (reader) {
    const read = reader(context, selection);
    return read ? { ...read, market, generic: false, confidence } : null;
  }
  // Plain tokens only, and at least one letter or digit: "__" is a selection
  // whose label is an empty string.
  if (!/^[a-z0-9_.\- ]+$/.test(selection) || !/[a-z0-9]/.test(selection)) return null;
  const outcome = marketSelectionLabel(key, selection).trim();
  return outcome ? { outcome, market, generic: true, confidence } : null;
}
