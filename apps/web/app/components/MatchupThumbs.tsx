import { TeamCrest } from "./TeamCrest";

type MatchupThumbsProps = {
  homeTeam: string;
  awayTeam: string;
  homeTeamLogo?: string;
  awayTeamLogo?: string;
};

/**
 * A compact home/away crest pair for dense rows.
 *
 * MatchupIdentity stacks the two teams over three lines, which is right for a
 * card and wrong for a table row -- it would triple the row height and lose the
 * scannable single line. This keeps the row's shape and puts the two crests
 * inline ahead of the fixture name, falling back to the generated crest per
 * team so one missing logo does not leave a gap.
 */
export function MatchupThumbs({ homeTeam, awayTeam, homeTeamLogo = "", awayTeamLogo = "" }: MatchupThumbsProps) {
  return (
    <span aria-hidden="true" className="matchup-thumbs">
      <TeamCrest logoUrl={homeTeamLogo} name={homeTeam} size={20} />
      <TeamCrest logoUrl={awayTeamLogo} name={awayTeam} size={20} />
    </span>
  );
}
