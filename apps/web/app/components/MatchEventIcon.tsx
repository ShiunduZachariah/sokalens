import { SokaIcon, type SokaIconName } from "./SokaIcon";
import type { MatchTimelineEvent } from "../lib/matchTimeline";

/**
 * The glyph for one timeline event.
 *
 * The nine hand-drawn glyphs this file used to carry are replaced by the SokaLens
 * icon set, which has a real icon for every kind the provider sends and another
 * twenty-nine the in-play surfaces will want. The reasoning for drawing them
 * inline — "a font or a sprite would be a network request the timeline blocks on
 * for something that is decoration" — held for nine and does not for thirty-nine;
 * see `SokaIcon` for the trade.
 *
 * Two things from the old implementation are deliberately kept:
 *
 * **`aria-hidden`.** The event's meaning is already in the label beside it, and
 * announcing a picture of a ball before the word "Goal" makes a screen reader say
 * the same thing twice. `SokaIcon` hides by default.
 *
 * **`currentColor`.** The per-kind rules in `globals.css` keep working — a goal's
 * icon is pitch-green because its row is, and a retracted event's icon dims with
 * its row rather than needing a second set of rules that could disagree with the
 * first.
 */

/**
 * Provider event kind to icon.
 *
 * `penalty_goal` maps to `penalty-scored` rather than `goal`, which is the one
 * place this mapping disagrees with the LABEL: the label reads "Goal (Penalty)"
 * because a penalty that goes in IS a goal and listing it as a separate kind made
 * a reader scanning a 2-0 count three things. The icon can carry the method
 * without that cost, because it sits beside the word "Goal" rather than replacing
 * it.
 */
const ICONS: Record<string, SokaIconName> = {
  goal: "goal",
  own_goal: "own-goal",
  penalty_goal: "penalty-scored",
  penalty_missed: "penalty-missed",
  yellow_card: "yellow-card",
  red_card: "red-card",
  second_yellow_card: "second-yellow-red",
  substitution: "substitution",
  // Both VAR kinds get the plain monitor. `var_retraction` has a tempting
  // `var-overturned` icon, but the retraction is already carried by the label
  // ("Goal cancelled") and by the row's struck-through treatment; a third signal
  // on the same event is noise, and the overturned icon is reserved for a
  // decision the provider reports as overturned rather than inferred.
  var_retraction: "var",
  var_other: "var",
};

export function MatchEventIcon({ kind }: { kind: MatchTimelineEvent["kind"] }) {
  const name = ICONS[kind];

  // An event kind the provider has that this build does not know about. A neutral
  // dot keeps the column aligned instead of leaving a hole, and the label still
  // says whatever the provider called it.
  if (!name) {
    return (
      <svg
        aria-hidden="true"
        className={`event-icon event-icon--${kind}`}
        focusable="false"
        height="18"
        viewBox="0 0 24 24"
        width="18"
        xmlns="http://www.w3.org/2000/svg"
      >
        <circle cx="12" cy="12" r="3.5" fill="currentColor" />
      </svg>
    );
  }

  return <SokaIcon className={`event-icon event-icon--${kind}`} name={name} size={18} />;
}
