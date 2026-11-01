import { MATCH_STAT_ROWS, type MatchStatistics, type TeamMatchStats } from "../../lib/matchStatistics";

/**
 * The Stats tab: eight rows, home on the left, away on the right.
 *
 * A dash, not a zero, for anything the provider did not report. The API keeps
 * absent and zero apart deliberately -- red cards are explicitly 0 in 4,371
 * rows of fixture_statistics and missing in 5,710 -- and printing "0" for the
 * second invents a measurement nobody took. A reader cannot tell the difference
 * afterwards, which is why it matters on a page whose claim is that its figures
 * are checkable.
 *
 * The bar is two tracks meeting in the middle, as the design draws it: the home
 * fill grows leftwards from the centre in the accent, the away fill rightwards
 * in a neutral. It shows share of the pair, not share of some absolute maximum,
 * because the question is who had more and by how much.
 *
 * Where only one side reported, there is no share to draw and the bar is
 * omitted rather than rendered at 100%, which would read as total dominance
 * instead of missing data.
 */
export function MatchStats({
  away,
  home,
  statistics,
}: {
  away: string;
  home: string;
  statistics: MatchStatistics;
}) {
  const rows = MATCH_STAT_ROWS.map((row) => {
    const homeValue = statistics.home[row.key as keyof TeamMatchStats];
    const awayValue = statistics.away[row.key as keyof TeamMatchStats];
    return { ...row, homeValue, awayValue };
  }).filter((row) => row.homeValue !== null || row.awayValue !== null);

  if (rows.length === 0) return null;

  return (
    <div className="ms-table">
      <div className="ms-table__head">
        <span className="ms-table__team">{home}</span>
        <span className="ms-table__team ms-table__team--away">{away}</span>
      </div>
      <ul className="ms-rows">
        {rows.map((row) => {
          const unit = row.unit ?? "";
          const total = (row.homeValue ?? 0) + (row.awayValue ?? 0);
          const comparable = row.homeValue !== null && row.awayValue !== null && total > 0;
          const homeShare = comparable ? ((row.homeValue as number) / total) * 100 : 0;

          return (
            <li className="ms-row" key={row.key}>
              <div className="ms-row__values">
                <span className="ms-row__value">
                  {row.homeValue === null ? "—" : `${row.homeValue}${unit}`}
                </span>
                <span className="ms-row__label">{row.label}</span>
                <span className="ms-row__value">
                  {row.awayValue === null ? "—" : `${row.awayValue}${unit}`}
                </span>
              </div>
              {comparable ? (
                <div aria-hidden="true" className="ms-row__bars">
                  <span className="ms-row__track ms-row__track--home">
                    <span className="ms-row__fill ms-row__fill--home" style={{ width: `${homeShare}%` }} />
                  </span>
                  <span className="ms-row__track ms-row__track--away">
                    <span className="ms-row__fill ms-row__fill--away" style={{ width: `${100 - homeShare}%` }} />
                  </span>
                </div>
              ) : null}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
