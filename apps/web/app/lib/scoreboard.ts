import type { MatchPhase } from "./matchPhase.ts";
import type { MatchTimelineEvent } from "./matchTimeline.ts";
import { benefitingSide, isScoringEvent } from "./matchTimeline.ts";

/*
 * The scoreboard's derived figures.
 *
 * Separate from `components/match/Scoreboard.tsx` for a plain reason: Node can
 * import a `.ts` module directly but not a `.tsx` one, and these two functions are
 * the part worth testing against real event lists rather than against markup.
 */

/**
 * The half-time score, when the match has reached one.
 *
 * Built from the timeline rather than from a dedicated field, because the API
 * sends no half-time score: goals carry a minute, so the score at the interval is
 * the goals at or before minute 45. Added time is the reason for `<= 45` rather
 * than `< 46` being a judgement call — a 45+2 goal arrives as minute 45 with an
 * `extra`, and it belongs in the half-time score.
 *
 * Returns null before any goal, rather than "0–0": a goalless first half has not
 * necessarily happened yet, and the label would otherwise appear at kick-off.
 */
export function halfTimeLabel(events: MatchTimelineEvent[], phase: MatchPhase): string | null {
  if (phase === "prematch") return null;
  const firstHalf = events.filter((event) => isScoringEvent(event) && event.minute <= 45);
  const reachedHalfTime = events.some((event) => event.minute > 45);
  if (!reachedHalfTime) return null;
  const home = firstHalf.filter((event) => benefitingSide(event) === "home").length;
  const away = firstHalf.filter((event) => benefitingSide(event) === "away").length;
  return `HT ${home}–${away}`;
}

/**
 * Who scored, per side, under each team's name.
 *
 * A retracted goal is excluded by `isScoringEvent`, which is right here even
 * though the timeline shows it struck through: this line is the list of goals that
 * count, and a cancelled goal in it would contradict the score above it.
 *
 * Minutes are appended because two goals by one player is the common case and
 * "Haaland, Haaland" reads like a mistake.
 */
export function scorersFor(events: MatchTimelineEvent[], side: "home" | "away"): string {
  return events
    .filter((event) => isScoringEvent(event) && benefitingSide(event) === side)
    .map((event) => {
      const name = event.player.trim();
      const minute = `${event.minute}'`;
      // An own goal is credited to the side that benefits, so it has to say so or
      // the line reads as the wrong player scoring for the wrong team.
      const suffix = event.kind === "own_goal" ? " (og)" : event.kind === "penalty_goal" ? " (pen)" : "";
      return name ? `${name} ${minute}${suffix}` : `${minute}${suffix}`;
    })
    .join(", ");
}
