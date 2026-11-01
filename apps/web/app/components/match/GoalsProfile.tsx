import type { TeamForm } from "../../lib/teamDepth";

type Props = {
  away: { name: string; stats: TeamForm | null };
  home: { name: string; stats: TeamForm | null };
};

type Row = {
  awayLabel: string;
  awayWidth: number;
  homeLabel: string;
  homeWidth: number;
  label: string;
};

/**
 * One mirrored row. Bars are scaled against the larger of the two values, so
 * the row compares the sides to each other rather than to an absolute ceiling
 * that would leave most rows nearly empty.
 *
 * A missing figure renders an em dash at zero width, never a zero: "0%" is a
 * measurement, and the two are not the same claim.
 */
function row(label: string, home: number | null, away: number | null, unit: string, decimals = 0): Row {
  const max = Math.max(home ?? 0, away ?? 0) || 1;
  const format = (value: number | null) =>
    value === null ? "—" : `${decimals ? value.toFixed(decimals) : Math.round(value)}${unit}`;
  return {
    label,
    homeLabel: format(home),
    awayLabel: format(away),
    homeWidth: home === null ? 0 : Math.round((home / max) * 100),
    awayWidth: away === null ? 0 : Math.round((away / max) * 100),
  };
}

function perMatch(total: number, matches: number) {
  return matches > 0 ? total / matches : null;
}

function rate(part: number, matches: number) {
  return matches > 0 ? (part / matches) * 100 : null;
}

/**
 * Goals profile for both sides, from figures already computed by `teamForm()`.
 *
 * The caller must pass stats derived through `statsBefore()`, which drops
 * matches played after this fixture's kickoff. A team feed read two weeks later
 * also holds results this preview could not have known, and quoting them would
 * make the page wrong in a way a reader can check against the score above it.
 */
export function GoalsProfile({ away, home }: Props) {
  const h = home.stats;
  const a = away.stats;
  const hMatches = h?.matches ?? 0;
  const aMatches = a?.matches ?? 0;

  // Nothing to draw without a settled match on at least one side.
  if (hMatches === 0 && aMatches === 0) {
    return (
      <div className="mp-panel">
        <div className="mp-panel__head">
          <h3>Goals profile</h3>
        </div>
        <p className="mp-panel__note" style={{ borderTop: 0, paddingTop: 0 }}>
          Neither side has enough settled match history on file to compute a goals
          profile. It appears here once the results arrive.
        </p>
      </div>
    );
  }

  const rows: Row[] = [
    row("Goals scored / match", perMatch(h?.goalsFor ?? 0, hMatches), perMatch(a?.goalsFor ?? 0, aMatches), "", 1),
    row("Goals conceded / match", perMatch(h?.goalsAgainst ?? 0, hMatches), perMatch(a?.goalsAgainst ?? 0, aMatches), "", 1),
    row("Both teams scored", rate(h?.bothTeamsScored ?? 0, hMatches), rate(a?.bothTeamsScored ?? 0, aMatches), "%"),
    row("Clean sheets", rate(h?.cleanSheets ?? 0, hMatches), rate(a?.cleanSheets ?? 0, aMatches), "%"),
  ];

  // Pooled across both sides' matches, which is what the sentence claims.
  const totalMatches = hMatches + aMatches;
  const overTwoFive = totalMatches > 0
    ? Math.round((((h?.overTwoFive ?? 0) + (a?.overTwoFive ?? 0)) / totalMatches) * 100)
    : null;
  const goals = (h?.goalsFor ?? 0) + (h?.goalsAgainst ?? 0) + (a?.goalsFor ?? 0) + (a?.goalsAgainst ?? 0);
  const averageGoals = totalMatches > 0 ? (goals / totalMatches).toFixed(1) : null;

  return (
    <div className="mp-panel">
      <div className="mp-goals__head">
        <span className="mp-goals__side">
          <i aria-hidden="true" className="mp-dot" />
          <span>{home.name}</span>
        </span>
        <h3>Goals profile</h3>
        <span className="mp-goals__side mp-goals__side--away">
          <span>{away.name}</span>
          <i aria-hidden="true" className="mp-dot mp-dot--away" />
        </span>
      </div>

      {rows.map((item) => (
        <div className="mp-stat" key={item.label}>
          <div className="mp-stat__head">
            <span className="mp-stat__value">{item.homeLabel}</span>
            <span className="mp-stat__label">{item.label}</span>
            <span className="mp-stat__value">{item.awayLabel}</span>
          </div>
          <div aria-hidden="true" className="mp-stat__bars">
            <span className="mp-stat__half mp-stat__half--home">
              <i style={{ width: `${item.homeWidth}%` }} />
            </span>
            <span className="mp-stat__half mp-stat__half--away">
              <i style={{ width: `${item.awayWidth}%` }} />
            </span>
          </div>
        </div>
      ))}

      <div className="mp-goals__totals">
        <span className="mp-goals__total">
          <strong>{overTwoFive === null ? "—" : `${overTwoFive}%`}</strong>
          <span>of these teams&rsquo; games went over 2.5 goals</span>
        </span>
        <span className="mp-goals__total">
          <strong>{averageGoals ?? "—"}</strong>
          <span>average goals in these sides&rsquo; matches</span>
        </span>
      </div>

      {/* The denominator, stated. A rate over three matches and one over forty
          are not the same evidence, and the reader cannot tell them apart
          from the percentage alone. */}
      <p className="mp-goals__sample">
        Sample: {hMatches} {hMatches === 1 ? "match" : "matches"} ({home.name}) ·{" "}
        {aMatches} {aMatches === 1 ? "match" : "matches"} ({away.name})
      </p>
    </div>
  );
}
