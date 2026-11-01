/**
 * The live match timeline: its shape, the payload mapping, and the
 * presentation rules.
 *
 * Pure by design, and deliberately not in the data module: everything here is
 * decided without React, so it is asserted directly rather than through a
 * rendered component. What a reader is told an event was, and which events are
 * important enough for a board where a match is one card among dozens, are the
 * two judgements most likely to be got wrong.
 */

/**
 * One event on a match timeline, as `/v1/matches/{id}` serves it.
 *
 * `kind` is the derived meaning and is what the UI switches on. The provider's
 * raw type/detail pair is deliberately not carried into the UI: an own goal, a
 * penalty and an ordinary goal all arrive as type="Goal", and the moment a
 * component starts comparing detail strings the classification exists in two
 * places.
 *
 * `retracted` marks an event a later VAR decision withdrew. Such events stay
 * on the timeline -- see MatchTimeline for why removing them is worse than
 * showing them struck through.
 */
export type MatchTimelineEvent = {
  minute: number;
  clock: string;
  kind: string;
  side: "home" | "away" | null;
  player: string;
  assist: string;
  comments: string;
  retracted: boolean;
};

const TIMELINE_SIDES = new Set(["home", "away"]);

/**
 * Maps the API's payload onto the shape the UI reads.
 *
 * The API sends snake_case and omits every optional field; the UI expects
 * camelCase with all of them present. This is where a live timeline silently
 * becomes a list of blank rows, so it is asserted against payloads shaped
 * exactly as the endpoint serves them.
 */
export function toTimelineEvents(value: unknown): MatchTimelineEvent[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value
    .map((item) => {
      if (typeof item !== "object" || item === null) {
        return null;
      }
      const candidate = item as Record<string, unknown>;
      const minute = Number(candidate.minute);
      // An event with no minute cannot be placed. Defaulting to 0 would put a
      // phantom event at kickoff, which reads as a real one.
      if (!Number.isFinite(minute)) {
        return null;
      }
      const side = String(candidate.side ?? "");
      return {
        minute,
        // The server renders the clock because added time is two numbers and
        // 90+3 is not the 93rd minute. Falling back to the bare minute keeps a
        // malformed payload readable rather than blank.
        clock: String(candidate.clock ?? `${minute}'`),
        kind: String(candidate.event_kind ?? "unknown"),
        side: TIMELINE_SIDES.has(side) ? (side as "home" | "away") : null,
        player: String(candidate.player ?? ""),
        assist: String(candidate.assist ?? ""),
        comments: String(candidate.comments ?? ""),
        retracted: candidate.retracted === true,
      };
    })
    .filter((event): event is MatchTimelineEvent => event !== null);
}

const KIND_LABELS: Record<string, string> = {
  goal: "Goal",
  own_goal: "Own goal",
  // "Goal (Penalty)", not "Penalty". A penalty that goes in is a goal, and
  // labelling it by its method alone reads as a different kind of event: a
  // reader scanning a 2-0 saw "Goal", "Goal", "Penalty" and counted three
  // things that happened. Flashscore and Sofascore both lead with the goal
  // and carry the method in parentheses, for the same reason.
  penalty_goal: "Goal (Penalty)",
  penalty_missed: "Penalty missed",
  yellow_card: "Yellow card",
  red_card: "Red card",
  second_yellow_card: "Second yellow",
  substitution: "Substitution",
  var_retraction: "VAR",
  var_other: "VAR",
};

/** Events that change the score, and so may not be shown as ordinary entries. */
const SCORING_KINDS = new Set(["goal", "own_goal", "penalty_goal"]);

/** What the in-play board shows: the events that change how a match reads. */
const HIGHLIGHT_KINDS = new Set(["goal", "own_goal", "penalty_goal", "red_card", "second_yellow_card"]);

export function eventLabel(event: MatchTimelineEvent): string {
  const base = KIND_LABELS[event.kind] ?? "Event";
  // A withdrawn goal is not "a goal that also says retracted somewhere else":
  // the label itself has to carry the withdrawal, because that is the line a
  // reader scanning the timeline actually reads.
  if (event.retracted) {
    return `${base} cancelled`;
  }
  if (event.kind === "var_retraction") {
    return "Goal cancelled";
  }
  return base;
}

/** The API maps the provider event's team, which is the beneficiary even for an own goal. */
export function benefitingSide(event: MatchTimelineEvent): "home" | "away" | null {
  return event.side;
}

export function isScoringEvent(event: MatchTimelineEvent): boolean {
  return SCORING_KINDS.has(event.kind) && !event.retracted;
}

/**
 * The events worth surfacing on a card that is one of many.
 *
 * Goals and dismissals only: a full timeline belongs on the match page, and a
 * board that lists every substitution in every match is a board nobody can
 * scan. Retracted events are kept -- a cancelled goal is exactly the thing a
 * reader needs told, because they may have seen it given.
 */
export function timelineHighlights(events: MatchTimelineEvent[], limit = 6): MatchTimelineEvent[] {
  const highlights = events.filter((event) => HIGHLIGHT_KINDS.has(event.kind) || event.retracted);
  if (highlights.length <= limit) {
    return highlights;
  }
  // The most recent ones: a match with nine goals is better summarised by what
  // just happened than by what happened in the opening ten minutes.
  return highlights.slice(highlights.length - limit);
}

/**
 * The line under an event: who it happened to, and anything the provider added.
 *
 * Returns "" rather than a placeholder when the provider named nobody, which
 * happens routinely on VAR decisions.
 */
export function eventDetailLine(event: MatchTimelineEvent): string {
  const parts: string[] = [];
  if (event.player) {
    parts.push(event.player);
  }
  if (event.assist) {
    parts.push(`assist ${event.assist}`);
  }
  if (event.comments) {
    parts.push(event.comments);
  }
  return parts.join(" · ");
}

/** A phase of the match, in the order it is played. */
export type TimelinePeriod = {
  key: "first_half" | "second_half" | "extra_time" | "penalties";
  label: string;
  events: MatchTimelineEvent[];
};

const PERIOD_LABELS: Record<TimelinePeriod["key"], string> = {
  first_half: "First half",
  second_half: "Second half",
  extra_time: "Extra time",
  penalties: "Penalties",
};

/**
 * Which phase an event belongs to, from its minute alone.
 *
 * The provider gives an elapsed minute and a separate `extra`, so added time
 * arrives as 45+2 rather than 47. That is what makes the boundaries safe to
 * draw on the elapsed minute: a first-half event in added time is still
 * minute 45, and one 90+3 is still minute 90. Adding the two together first
 * would push every added-time event into the following period -- a 45+2 goal
 * would appear under "Second half", before the half had kicked off.
 */
export function eventPeriod(event: MatchTimelineEvent): TimelinePeriod["key"] {
  if (event.minute <= 45) return "first_half";
  if (event.minute <= 90) return "second_half";
  if (event.minute <= 120) return "extra_time";
  return "penalties";
}

/**
 * Groups events into the phases they were played in, dropping phases that did
 * not happen.
 *
 * Empty periods are omitted rather than rendered blank: most matches never
 * reach extra time, and a permanent empty "Extra time" heading would read as
 * missing data rather than a match that finished in ninety minutes.
 */
export function groupEventsByPeriod(events: MatchTimelineEvent[]): TimelinePeriod[] {
  const order: TimelinePeriod["key"][] = ["first_half", "second_half", "extra_time", "penalties"];
  const buckets = new Map<TimelinePeriod["key"], MatchTimelineEvent[]>();
  for (const event of events) {
    const key = eventPeriod(event);
    buckets.set(key, [...(buckets.get(key) ?? []), event]);
  }
  return order
    .filter((key) => (buckets.get(key) ?? []).length > 0)
    .map((key) => ({ key, label: PERIOD_LABELS[key], events: buckets.get(key) ?? [] }));
}

/** The score after a goal, as the timeline shows it beside that goal. */
export type RunningScore = { home: number; away: number };

/**
 * The score as it stood after each goal, keyed by the event's position.
 *
 * A timeline that shows only who scored makes a reader count backwards to work
 * out what the score was at the hour mark. Every scoreboard puts the running
 * total beside the goal, and the data to do it is already here.
 *
 * Two rules do the work, and both already exist elsewhere:
 *
 *  - `isScoringEvent` excludes a retracted goal, so a VAR-cancelled goal does
 *    not advance the score. The row still appears, struck through, because the
 *    cancellation is information the reader wants -- but the number beside it
 *    must be the score that actually stood.
 *  - `benefitingSide` uses the provider's team, which is already the team
 *    credited with an own goal, just as the row's placement does.
 *
 * Returns a map from index rather than mutating the events, so the caller can
 * render a score only where there is one and the event type stays a plain
 * description of what the provider reported.
 */
export function runningScores(events: MatchTimelineEvent[], initial: RunningScore = { home: 0, away: 0 }): Map<number, RunningScore> {
  const scores = new Map<number, RunningScore>();
  let home = initial.home;
  let away = initial.away;
  events.forEach((event, index) => {
    if (!isScoringEvent(event)) return;
    const side = benefitingSide(event);
    if (side === "home") home += 1;
    else if (side === "away") away += 1;
    // A goal whose team never mapped cannot be credited to either side. It
    // still shows on the timeline; it just cannot move a number, because
    // guessing would put the match at a score nobody played.
    else return;
    scores.set(index, { home, away });
  });
  return scores;
}

/**
 * Goals scored within one period, for the half's own header.
 *
 * "1ST HALF 1-1" is the half's score, not the match's running total, which is
 * why this counts within the period rather than reading the last running
 * score in it.
 */
export function periodScore(events: MatchTimelineEvent[]): RunningScore {
  let home = 0;
  let away = 0;
  for (const event of events) {
    if (!isScoringEvent(event)) continue;
    const side = benefitingSide(event);
    if (side === "home") home += 1;
    else if (side === "away") away += 1;
  }
  return { home, away };
}
