import type { TrackerState } from "../data/liveMatches";

/**
 * How a pre-match pick is doing against the live score.
 *
 * The value is the engine's, never this component's. The in-play data contract
 * (§8) is explicit about why: the tracker is computed server-side and sent as a
 * value so that web and Flutter cannot grade the same pick differently, and it
 * grades through the same code as settlement so it cannot contradict the outcome
 * the pick eventually settles to. Deriving "on track" here from a score and a
 * market key is the failure the stored state exists to prevent -- over 1.5 at
 * 2-0 is `won` to one implementation and still `on_track` to another, and a
 * reader who checks both apps sees the platform contradict itself.
 *
 * Two states deliberately render nothing:
 *
 * - **`unknown`** — the engine looked and could not judge it from what it holds:
 *   a corners pick before any corner data, or a reversed settlement not yet
 *   re-decided. The contract says to show no tracker for it, "not a colour".
 * - **no verdict at all** — sent as `tracker_state: null` (an absent key before
 *   the 2026-10-02 API change; the parser maps both to `undefined`). Nothing has
 *   judged the pick: the in-play pass has not covered this fixture, or it has not
 *   kicked off. Distinct from `unknown` in the data even though both draw nothing
 *   here, because when one of them is wrong that distinction says which.
 *
 * `off_track` is labelled "At risk" rather than "Off track", following the
 * design. It is the honest word: the state means "would not win if the match
 * ended now", which includes a draw under a home pick, and the match has not
 * ended. Nothing here is terminal except `won`, `lost` and `void`.
 */
const PRESENTATION: Record<TrackerState, { label: string; modifier: string } | null> = {
  on_track: { label: "On track", modifier: "on" },
  off_track: { label: "At risk", modifier: "risk" },
  won: { label: "Won", modifier: "won" },
  lost: { label: "Lost", modifier: "lost" },
  void: { label: "Void", modifier: "void" },
  unknown: null,
};

export function TrackerChip({ state }: { state?: TrackerState }) {
  const presentation = state ? PRESENTATION[state] : null;
  if (!presentation) return null;

  return (
    <span className={`mp-track mp-track--${presentation.modifier}`}>
      {/*
        The state is announced, not just coloured. Won and Lost are distinguished
        here by fill and a line-through respectively, and At risk from On track
        by a dashed border -- none of which a screen reader conveys, and the
        dashed/solid pair is also the kind of distinction that disappears for a
        reader who cannot separate the accent from the body text.
      */}
      <span className="sr-only">Pick status: </span>
      {presentation.label}
    </span>
  );
}
