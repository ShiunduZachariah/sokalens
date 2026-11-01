import { LocalTime } from "../LocalTime";
import { ClockIcon } from "./icons";
import type { MatchPhase } from "../../lib/matchPhase";

/**
 * The centre column of the match hero: the figure, the status pill, and half-time.
 *
 * Three phases, three different things in the same place, which is the whole
 * reason this is its own component. Before kick-off the big figure is the KICK-OFF
 * TIME, not a score — and it is drawn in a lighter weight and a muted colour so a
 * reader cannot mistake "15:00" for a 15-0 scoreline. That confusion is not
 * hypothetical: the page previously rendered the time through the same class as
 * the score, at the same size and weight.
 */
export function ScoreFigure({
  kickoffAt,
  phase,
  score,
}: {
  kickoffAt: string;
  phase: MatchPhase;
  score: string | null;
}) {
  // A live match can arrive before its first score does, so a missing score falls
  // back to the kickoff time rather than to a 0-0 nobody reported.
  if (phase === "prematch" || score === null) {
    return (
      <div className="mp-figure mp-figure--time">
        <LocalTime timeOnly value={kickoffAt} />
      </div>
    );
  }
  return <div className="mp-figure">{score}</div>;
}

/**
 * The status pill, drawn differently in each phase.
 *
 * Live is outlined in the accent ink with a pulsing dot; finished is a filled grey
 * that reads as settled; pre-match is a quiet outline with a clock. The three are
 * visually distinct on purpose — this pill is the one element that says whether
 * the number beside it is still moving.
 */
export function StatusPill({ label, phase }: { label: string; phase: MatchPhase }) {
  if (phase === "live") {
    return (
      <span className="mp-pill mp-pill--live">
        <span aria-hidden="true" className="mp-pill__dot" />
        {label}
      </span>
    );
  }
  if (phase === "finished") {
    return <span className="mp-pill mp-pill--done">{label}</span>;
  }
  return (
    <span className="mp-pill mp-pill--pre">
      <ClockIcon />
      {label}
    </span>
  );
}
