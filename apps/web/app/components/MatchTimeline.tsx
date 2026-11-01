import { MatchEventIcon } from "./MatchEventIcon";
import {
  benefitingSide,
  eventDetailLine,
  eventLabel,
  groupEventsByPeriod,
  isScoringEvent,
  periodScore,
  runningScores,
  timelineHighlights,
  type MatchTimelineEvent,
} from "../lib/matchTimeline";

/**
 * The live match timeline.
 *
 * A server component with no client state, for the same reason the in-play
 * board is one: the match clock is the provider's elapsed minute, and a
 * browser-side timer keeps counting when the feed stops.
 *
 * Retracted events are shown struck through rather than removed. A timeline
 * that deletes a cancelled goal cannot explain why the score does not match
 * the goal the reader watched being celebrated -- and the cancellation is
 * itself the information they came for. This is the same reason the table
 * stores `is_retracted` as a flag instead of deleting the row.
 */

type MatchTimelineProps = {
  events: MatchTimelineEvent[];
  homeTeam: string;
  awayTeam: string;
  /** Live matches say so; a finished match's timeline is simply the record. */
  live?: boolean;
};

type CompactTimelineProps = {
  events: MatchTimelineEvent[];
};

function sideLabel(event: MatchTimelineEvent, homeTeam: string, awayTeam: string) {
  const side = benefitingSide(event);
  if (side === null) {
    // The provider's team id has no canonical mapping. Attributing it to a
    // team would be a guess the reader cannot check.
    return "";
  }
  return side === "home" ? homeTeam : awayTeam;
}

export function MatchTimeline({ events, homeTeam, awayTeam, live = false }: MatchTimelineProps) {
  /*
   * A live match with nothing in it yet still gets a timeline, saying so.
   *
   * Returning null here meant a goalless first ten minutes rendered no
   * timeline at all, so the panel appeared out of nowhere on the first goal.
   * The design carries "No events yet." for exactly this state. A FINISHED
   * match with no events is different -- there is nothing still to come, and
   * an empty panel on a 0-0 that is already over says less than no panel.
   */
  if (events.length === 0) {
    if (!live) return null;
    return (
      <section aria-labelledby="match-timeline-heading" className="section">
        <div className="section-header">
          <div>
            <h2 id="match-timeline-heading">Timeline</h2>
          </div>
        </div>
        <p className="match-timeline__empty">No events yet.</p>
      </section>
    );
  }

  let runningTotal = { home: 0, away: 0 };
  return (
    <section aria-labelledby="match-timeline-heading" className="section">
      <div className="section-header">
        <div>
          <h2 id="match-timeline-heading">Timeline</h2>
          <p>
            {live
              ? "Events as the provider reports them, updated every 15 seconds."
              : "Events as the provider recorded them."}
          </p>
        </div>
      </div>
      {groupEventsByPeriod(events).map((period) => {
        const half = periodScore(period.events);
        const scores = runningScores(period.events, runningTotal);
        runningTotal = { home: runningTotal.home + half.home, away: runningTotal.away + half.away };
        return (
        <div className="match-timeline__period" key={period.key}>
          {/* The half's own score, not the match aggregate: "2ND HALF 1-0"
              means one goal was scored in it. */}
          <h3 className="match-timeline__period-label">
            <span>{period.label}</span>
            <span className="match-timeline__period-score">
              {half.home} - {half.away}
            </span>
          </h3>
          <ol className="match-timeline">
            {period.events.map((event, index) => {
              const team = sideLabel(event, homeTeam, awayTeam);
              const detail = eventDetailLine(event);
              // The provider's team is the side an event benefited, including
              // an own goal. Keep the row and its running score on that side.
              const side = benefitingSide(event);
              const score = scores.get(index);
              // A substitution names two players: the one arriving, then the
              // one replaced. The feed puts the arriving player in `player`
              // and the replaced one in `assist`, so the emphasis follows
              // that -- getting them the wrong way round would report the
              // wrong player as having come off.
              // Name over detail, as a block, so the icon and the running score
              // sit beside the pair rather than inside the text flow. The clock
              // is NOT in here: it is the row's centre column.
              const text =
                event.kind === "substitution" ? (
                  <span className="match-timeline__text">
                    <strong className="match-timeline__label">{event.player}</strong>
                    {event.assist ? (
                      <span className="match-timeline__replaced">{event.assist}</span>
                    ) : null}
                  </span>
                ) : (
                  <span className="match-timeline__text">
                    <strong className="match-timeline__label">
                      {event.player || eventLabel(event)}
                    </strong>
                    {!event.player && team ? (
                      <span className="match-timeline__team">{team}</span>
                    ) : null}
                    {(!isScoringEvent(event) || event.kind === "own_goal" || event.kind === "penalty_goal") && detail && event.player ? (
                      <span className="match-timeline__detail">{eventLabel(event)}</span>
                    ) : null}
                  </span>
                );
              // One DOM order for both sides; the home row reverses it in CSS,
              // so a goal reads [1 - 0] M. Ortega (o) on the left and
              // (o) S. Molina [1 - 1] on the right, mirrored about the clock.
              const body = (
                <>
                  <span className="match-timeline__icon">
                    <MatchEventIcon kind={event.kind} />
                  </span>
                  {text}
                  {score ? (
                    <span className="match-timeline__score">
                      {score.home} &ndash; {score.away}
                    </span>
                  ) : null}
                </>
              );
              return (
                <li
                  className={`match-timeline__event match-timeline__event--${event.kind}${
                    event.retracted ? " match-timeline__event--retracted" : ""
                  } match-timeline__event--${side ?? "unknown"}`}
                  // The provider sends no event id, so identity is the clock
                  // plus what happened plus who it happened to. The index
                  // closes the remaining gap: two identical-looking events in
                  // one minute are rare but real (two bookings from one melee).
                  key={`${event.clock}-${event.kind}-${event.player}-${index}`}
                >
                  {/* Three columns mirrored about the minute, as the export
                      draws them: the clock is the spine, and which side an
                      event sits on is how a reader tells whose it was. */}
                  <span className="match-timeline__body match-timeline__body--home">
                    {side === "home" ? body : null}
                  </span>
                  <span className="match-timeline__clock">{event.clock}</span>
                  <span className="match-timeline__body match-timeline__body--away">
                    {side !== "home" ? body : null}
                  </span>
                </li>
              );
            })}
          </ol>
        </div>
        );
      })}
    </section>
  );
}

/**
 * The board variant: goals and dismissals on one line.
 *
 * The in-play board shows dozens of matches at once, so a full timeline per
 * card would bury the scores it exists to show. Renders nothing when a match
 * has no highlights, which keeps a goalless card exactly as it was.
 */
export function CompactMatchTimeline({ events }: CompactTimelineProps) {
  const highlights = timelineHighlights(events);
  if (highlights.length === 0) {
    return null;
  }

  return (
    <ul className="match-timeline match-timeline--compact">
      {highlights.map((event, index) => (
        <li
          className={`match-timeline__chip match-timeline__chip--${event.kind}${
            event.retracted ? " match-timeline__chip--retracted" : ""
          }`}
          key={`${event.clock}-${event.kind}-${event.player}-${index}`}
        >
          <span className="match-timeline__clock">{event.clock}</span>
          <span className="match-timeline__label">{event.player || eventLabel(event)}</span>
        </li>
      ))}
    </ul>
  );
}
