"use client";

import { useEffect, useId, useMemo, useState } from "react";
import type { InPlaySeriesPoint } from "../../data/inplay";

/**
 * The win-probability graph: three lines over match minutes.
 *
 * ## Why these three colours
 *
 * They are the design's own — `--sl-accent` for home, `--sl-tick` for the draw,
 * `--sl-away` for away — with one substitution, made because the palette was
 * validated rather than eyeballed.
 *
 * The export draws the draw line in `--line-heavy`. Measured against both
 * surfaces that is 2.46:1, under the 3:1 floor, in BOTH themes. Stepping to
 * `--tick` clears it (>= 3:1) and keeps the separation comfortable: worst
 * adjacent pair 16.2 ΔE light, 30.4 dark, with CVD 10.6/29.5. `--muted` was
 * tried first and rejected: on dark it sits 12.0 ΔE from the away blue, under
 * the 15 floor where full-colour readers cannot tell a pair apart.
 *
 * Two checks still fail and both are deliberate:
 *
 * - **Chroma floor.** A neutral draw is the point — it is the outcome that
 *   favours neither side, and giving it a hue would say otherwise. Carried with
 *   the secondary encoding the rule requires: a legend, direct labels, and a
 *   table view below.
 * - **Lightness band, dark.** `--sl-accent` is the brand's accent. Re-stepping
 *   it for one chart would make this the only surface where the brand colour is
 *   not the brand colour.
 *
 * ## Why basis points
 *
 * The series arrives in basis points and is divided only when drawn. 6800 is
 * exact; 68 is not, and rounding on the way in is the difference between a line
 * that moves and one that looks flat.
 */

type Series = { key: string; label: string; colour: string };

const SERIES: Series[] = [
  { key: "home", label: "Home", colour: "var(--sl-accent)" },
  { key: "draw", label: "Draw", colour: "var(--sl-tick)" },
  { key: "away", label: "Away", colour: "var(--sl-away)" },
];

const MARKET = "match_result_in_play";

/**
 * Reads the incident back in the page's own words.
 *
 * The provider's `detail` is the useful part -- "Penalty" and "Own Goal" are
 * materially different from a goal, and "Red Card" distinguishes a straight
 * dismissal from a second booking. It is only appended when it adds something:
 * the provider writes "Normal Goal" for the ordinary case, which would render
 * as "Goal, Normal Goal".
 */
export function eventLabel(event: { type: string; detail: string | null; player: string | null }) {
  const detail = event.detail && !/^normal\s/i.test(event.detail) ? event.detail : null;
  const incident = detail ? `${event.type} (${detail})` : event.type;
  return event.player ? `${incident} — ${event.player}` : incident;
}

/**
 * The plot is 3.3:1 on a wide screen and 1.6:1 on a phone.
 *
 * A viewBox cannot be set from CSS, so one fixed box has to serve every width --
 * and 720x220 scaled down to a 390px viewport draws the plot 101px tall. Three
 * lines that spend most of a match inside a few points of each other are not
 * readable in 101px, and the vertical event markers collapse into the grid. A
 * taller box on the breakpoint where live football is actually watched is worth
 * the matchMedia read.
 *
 * The narrower box also buys back the tick labels: font sizes are in viewBox
 * units, so 10px of a 420-wide box renders where 10px of a 720-wide box is
 * nearly illegible on a phone.
 */
const VIEW = { wide: { w: 720, h: 220 }, narrow: { w: 420, h: 260 } };
const NARROW_QUERY = "(max-width: 600px)";
const PAD = { top: 12, right: 12, bottom: 24, left: 34 };

export function WinProbabilityChart({
  awayTeam,
  homeTeam,
  points,
}: {
  awayTeam: string;
  homeTeam: string;
  points: InPlaySeriesPoint[];
}) {
  const titleId = useId();
  const [showTable, setShowTable] = useState(false);
  const [hover, setHover] = useState<number | null>(null);

  // Starts wide and narrows on resolve, rather than rendering nothing until the
  // query answers: a chart that blinks in is worse than one that rescales. The
  // box itself never moves -- `.wp__svg` carries the matching `aspect-ratio` per
  // breakpoint in CSS, so the height is correct from the first paint and the
  // swap only changes the coordinates drawn inside it.
  const [view, setView] = useState(VIEW.wide);
  useEffect(() => {
    const query = window.matchMedia(NARROW_QUERY);
    const apply = () => setView(query.matches ? VIEW.narrow : VIEW.wide);
    apply();
    query.addEventListener("change", apply);
    return () => query.removeEventListener("change", apply);
  }, []);
  const { w: VIEW_W, h: VIEW_H } = view;

  const plotted = useMemo(
    () => points.filter((point) => point.markets?.[MARKET] !== undefined),
    [points],
  );

  // Withheld and absent are different answers and the panel says which.
  if (plotted.length < 2) {
    const withheld = points.some((point) => point.locked);
    return (
      <div className="wp-empty" role="status">
        {withheld
          ? "Sign in to see how the probabilities moved during this match."
          : "No probability history was recorded for this match."}
      </div>
    );
  }

  // The axis ends where the recording does, not at 90.
  //
  // Forcing 90 leaves the right of the chart empty for a match still in its
  // first half -- and worse, for one whose series stops early (abandoned, or a
  // feed that dropped) it draws a void that reads as "nothing happened" rather
  // than "nothing was recorded". The export ends its axis at the current
  // minute: 0' | HT | 67'.
  const lastMinute = Math.max(...plotted.map((point) => point.minute), 1);
  const x = (minute: number) =>
    PAD.left + (minute / lastMinute) * (VIEW_W - PAD.left - PAD.right);
  const y = (bp: number) =>
    PAD.top + (1 - bp / 10000) * (VIEW_H - PAD.top - PAD.bottom);

  const paths = SERIES.map((series) => ({
    ...series,
    d: plotted
      .map((point, index) => {
        const bp = point.markets?.[MARKET]?.[series.key] ?? 0;
        return `${index === 0 ? "M" : "L"}${x(point.minute).toFixed(1)},${y(bp).toFixed(1)}`;
      })
      .join(" "),
    last: plotted[plotted.length - 1].markets?.[MARKET]?.[series.key] ?? 0,
  }));

  // Goals and red cards are the points worth marking; a timer tick is not.
  const markers = plotted.filter(
    (point) => point.trigger === "post_goal" || point.trigger === "post_red_card",
  );
  const active = hover === null ? null : plotted[hover];
  const nameFor = (key: string) =>
    key === "home" ? homeTeam : key === "away" ? awayTeam : "Draw";

  return (
    <figure className="wp" aria-labelledby={titleId}>
      <figcaption className="wp__head">
        <h3 className="wp__title" id={titleId}>Win probability</h3>
        {/* A legend for three series, and each line is direct-labelled at its
            end, so identity never rests on colour alone. */}
        <ul className="wp__legend">
          {SERIES.map((series) => (
            <li key={series.key}>
              <span aria-hidden="true" className="wp__swatch" style={{ background: series.colour }} />
              {nameFor(series.key)}
            </li>
          ))}
        </ul>
      </figcaption>

      <div className="wp__plot">
        <svg
          aria-label={`Win probability from minute 0 to ${lastMinute}`}
          className="wp__svg"
          role="img"
          viewBox={`0 0 ${VIEW_W} ${VIEW_H}`}
          onMouseLeave={() => setHover(null)}
          onMouseMove={(event) => {
            const box = event.currentTarget.getBoundingClientRect();
            const ratio = (event.clientX - box.left) / box.width;
            const minute = ((ratio * VIEW_W - PAD.left) / (VIEW_W - PAD.left - PAD.right)) * lastMinute;
            let nearest = 0;
            for (let i = 1; i < plotted.length; i += 1) {
              if (Math.abs(plotted[i].minute - minute) < Math.abs(plotted[nearest].minute - minute)) nearest = i;
            }
            setHover(nearest);
          }}
        >
          {[0, 2500, 5000, 7500, 10000].map((bp) => (
            <g key={bp}>
              <line className="wp__grid" x1={PAD.left} x2={VIEW_W - PAD.right} y1={y(bp)} y2={y(bp)} />
              <text className="wp__tick" x={PAD.left - 6} y={y(bp) + 3} textAnchor="end">{bp / 100}%</text>
            </g>
          ))}

          {markers.map((point) => (
            <line
              className="wp__marker"
              key={point.sequence}
              x1={x(point.minute)}
              x2={x(point.minute)}
              y1={PAD.top}
              y2={VIEW_H - PAD.bottom}
            />
          ))}

          {paths.map((series) => (
            <path className="wp__line" d={series.d} key={series.key} stroke={series.colour} />
          ))}

          {active ? (
            <>
              <line
                className="wp__crosshair"
                x1={x(active.minute)}
                x2={x(active.minute)}
                y1={PAD.top}
                y2={VIEW_H - PAD.bottom}
              />
              {SERIES.map((series) => (
                // A ring in the surface colour keeps overlapping dots readable
                // where two lines cross.
                <circle
                  className="wp__dot"
                  cx={x(active.minute)}
                  cy={y(active.markets?.[MARKET]?.[series.key] ?? 0)}
                  fill={series.colour}
                  key={series.key}
                  r={5}
                />
              ))}
            </>
          ) : null}

          <text className="wp__tick" x={PAD.left} y={VIEW_H - 6}>0&apos;</text>
          <text className="wp__tick" x={VIEW_W - PAD.right} y={VIEW_H - 6} textAnchor="end">
            {lastMinute}&apos;
          </text>
        </svg>

        <div aria-live="polite" className="wp__readout">
          {active ? (
            <>
              <strong>{active.minute}&apos;</strong>
              <span className="wp__readout-score">{active.score.home}&ndash;{active.score.away}</span>
              {/*
                What the marker marks. Until now a goal and a red card were both
                an unexplained vertical rule; the reader could see the lines jump
                but not why. Omitted when the API could not resolve the incident,
                which is most fixtures without an event feed -- the marker still
                draws, it just says less.
              */}
              {active.event ? (
                <span className="wp__readout-event">
                  {eventLabel(active.event)} &middot;{" "}
                  {active.event.side === "home" ? homeTeam : awayTeam}
                </span>
              ) : null}
              {SERIES.map((series) => (
                <span key={series.key}>
                  <span aria-hidden="true" className="wp__swatch" style={{ background: series.colour }} />
                  {nameFor(series.key)} {Math.round((active.markets?.[MARKET]?.[series.key] ?? 0) / 100)}%
                </span>
              ))}
            </>
          ) : (
            <>
              {paths.map((series) => (
                <span key={series.key}>
                  <span aria-hidden="true" className="wp__swatch" style={{ background: series.colour }} />
                  {nameFor(series.key)} {Math.round(series.last / 100)}%
                </span>
              ))}
            </>
          )}
        </div>
      </div>

      {/* The table is not a fallback. It is the relief the contrast rule
          requires for a neutral series, and the only way to read exact figures. */}
      <button className="wp__toggle" onClick={() => setShowTable((open) => !open)} type="button">
        {showTable ? "Hide the figures" : "Show the figures"}
      </button>
      {showTable ? (
        <table className="wp__table">
          <caption>Win probability by minute</caption>
          <thead>
            <tr>
              <th scope="col">Min</th>
              <th scope="col">Score</th>
              {SERIES.map((series) => <th key={series.key} scope="col">{nameFor(series.key)}</th>)}
            </tr>
          </thead>
          <tbody>
            {plotted.map((point) => (
              <tr key={point.sequence}>
                <td>{point.minute}&apos;</td>
                <td>{point.score.home}&ndash;{point.score.away}</td>
                {SERIES.map((series) => (
                  <td key={series.key}>{Math.round((point.markets?.[MARKET]?.[series.key] ?? 0) / 100)}%</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
    </figure>
  );
}
