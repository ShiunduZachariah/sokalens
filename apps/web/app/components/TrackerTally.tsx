import type { LiveMatchPrediction, TrackerState } from "../data/liveMatches";

/**
 * How this fixture's pre-match picks are doing, counted.
 *
 * The design puts this above the in-play board as a single line. It is built
 * from `tracker_state` on the match payload's predictions, which the in-play
 * data contract serves on the ordinary match payload. Both this tally and the
 * live market ladder are public; the same tracker states are visible on the
 * market cards above it.
 *
 * Renders nothing when no pick carries a state, which is the common case: the
 * key is absent until the in-play pass has covered the fixture, and a tally
 * reading "0 on track · 0 at risk" would claim the picks had been judged and
 * found wanting.
 */
const COUNTED: Array<{ state: TrackerState; label: string }> = [
  { state: "won", label: "won" },
  { state: "on_track", label: "on track" },
  { state: "off_track", label: "at risk" },
  { state: "lost", label: "lost" },
  { state: "void", label: "void" },
];

export function TrackerTally({ predictions }: { predictions: LiveMatchPrediction[] }) {
  const counts = new Map<TrackerState, number>();
  for (const prediction of predictions) {
    // `unknown` is counted by neither: the engine reached it as a judgement, but
    // it means "cannot be judged from what we hold", so adding it to a tally of
    // standings would imply it is one.
    if (prediction.trackerState && prediction.trackerState !== "unknown") {
      counts.set(prediction.trackerState, (counts.get(prediction.trackerState) ?? 0) + 1);
    }
  }

  const parts = COUNTED.filter(({ state }) => (counts.get(state) ?? 0) > 0);
  if (parts.length === 0) return null;

  return (
    <p className="mp-tally">
      {parts.map(({ label, state }, index) => (
        <span key={state}>
          {index > 0 ? <span aria-hidden="true"> · </span> : null}
          <strong>{counts.get(state)}</strong> {label}
        </span>
      ))}
    </p>
  );
}
