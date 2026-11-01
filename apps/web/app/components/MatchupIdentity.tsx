import { TeamCrest } from "./TeamCrest";
import { TeamIdentity } from "./TeamIdentity";

/**
 * How a home-v-away pair is drawn. One component, three layouts, because the
 * V31 export draws the same pair three ways and a fourth composition beside
 * `TeamIdentity` and this one would be the mistake task A3 just finished
 * undoing.
 *
 * - `columns` is the pre-refresh card layout: two equal columns with a quiet
 *   "vs" between them. It is the default so the ~12 existing call sites keep
 *   the shape they were written against.
 * - `stacked` is review 2.3's PredictionRow block: home on line one, away on
 *   line two, each line carrying its own crest and its own `min-width: 0` so
 *   each name truncates on its own budget. That last part is the whole of
 *   UI-05 -- when the ellipsis sat on the joined "Home v Away" string, two long
 *   names lost both halves to one ellipsis and the away side always lost first.
 * - `inline` is `[crest] Home v [crest] Away` on one line, which is what
 *   builder.html draws for a ticket leg: a row too short to spend two lines on,
 *   where the pair is a label and the selection is the subject. (The league and
 *   team result rows are inline too, but they put a score chip BETWEEN the two
 *   names, so they compose `.sl-team` directly rather than coming through here.)
 *
 * The crest sizes are review 2.1's and are passed in per surface rather than
 * guessed here: the export sets the crest at 1.4em of the surface's own type
 * size, so a 15px row wants 20 and an 18px card head wants 24.
 */

type CrestSize = 16 | 20 | 24 | 32 | 44;

type MatchupIdentityProps = {
  homeTeam: string;
  awayTeam: string;
  homeTeamLogo?: string;
  awayTeamLogo?: string;
  className?: string;
  layout?: "columns" | "stacked" | "inline";
  size?: CrestSize;
};

/** Shared, aligned home-v-away identity used by prediction and builder cards. */
export function MatchupIdentity({
  homeTeam,
  awayTeam,
  homeTeamLogo = "",
  awayTeamLogo = "",
  className = "",
  layout = "columns",
  size,
}: MatchupIdentityProps) {
  if (layout === "columns") {
    return <div className={["matchup-identity", className].filter(Boolean).join(" ")}><TeamIdentity logo={homeTeamLogo} name={homeTeam} /><span className="matchup-identity__versus" aria-hidden="true">vs</span><TeamIdentity logo={awayTeamLogo} name={awayTeam} /></div>;
  }

  const crest = size ?? 20;
  const classes = ["sl-teams", layout === "inline" ? "sl-teams--inline" : "", className]
    .filter(Boolean)
    .join(" ");

  return (
    <span className={classes}>
      <span className="sl-team">
        <TeamCrest logoUrl={homeTeamLogo} name={homeTeam} size={crest} />
        <span className="sl-teamname">{homeTeam}</span>
      </span>
      {/*
        Visible text, not `aria-hidden`: on one line the pair reads "Home v
        Away", and hiding the separator would have a reader hear two team names
        with nothing between them. Stacked, the line break does that job and the
        mark is not rendered at all.
      */}
      {layout === "inline" ? <i className="sl-teams__v">v</i> : null}
      <span className="sl-team">
        <TeamCrest logoUrl={awayTeamLogo} name={awayTeam} size={crest} />
        <span className="sl-teamname">{awayTeam}</span>
      </span>
    </span>
  );
}
