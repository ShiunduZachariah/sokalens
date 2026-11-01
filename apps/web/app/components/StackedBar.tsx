/**
 * Won / lost / void as one bar.
 *
 * `role="img"` with a written label rather than three labelled spans: a screen
 * reader given the segments reads three percentages with no statement of what
 * they are shares of, which is worse than the sentence. The visible legend
 * carries the counts for everyone else.
 *
 * Widths are percentages of the settled total, and void is included in that
 * total even though it is excluded from the hit rate. That is deliberate and is
 * the whole reason the bar has three segments: a day that was 50% won, 20% lost
 * and 30% void has a 71% hit rate, and a two-segment bar would imply the 71%
 * came from the whole day's slate.
 */
export function StackedBar({
  compact = false,
  lost,
  voided,
  won,
}: {
  compact?: boolean;
  lost: number;
  voided: number;
  won: number;
}) {
  const total = won + lost + voided;
  // Nothing settled yet draws the empty track rather than dividing by zero.
  const share = (value: number) => (total > 0 ? (value / total) * 100 : 0);
  const pct = (value: number) => Math.round(share(value));

  return (
    <span
      aria-label={`Won ${pct(won)}%, lost ${pct(lost)}%, void ${pct(voided)}%`}
      className={compact ? "pf-bar pf-bar--compact" : "pf-bar"}
      role="img"
    >
      <span className="pf-bar__won" style={{ width: `${share(won)}%` }} />
      <span className="pf-bar__lost" style={{ width: `${share(lost)}%` }} />
      <span className="pf-bar__void" style={{ width: `${share(voided)}%` }} />
    </span>
  );
}
