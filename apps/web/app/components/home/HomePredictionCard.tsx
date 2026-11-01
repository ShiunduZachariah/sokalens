import Link from "next/link";
import { LocalTime } from "../LocalTime";
import { TeamCrest } from "../TeamCrest";
import { resultCells } from "../../lib/resultMarker";

export type HomeCardPrediction = {
  away: string;
  /** Provider crest URLs; an absent one falls through to the initials shield. */
  awayLogo: string;
  confidence: number;
  home: string;
  homeLogo: string;
  href: string;
  kickoffAt: string | null;
  league: string;
  /** Human market name, e.g. "Match Result". */
  market: string;
  marketKey: string;
  /** Human selection, e.g. "Draw". */
  prediction: string;
  /** Untranslated selection, for deriving the 1/X/2 marker. */
  rawSelection: string;
};

/**
 * One fixture card in the home page's "today" grid.
 *
 * The 1 · X · 2 marker only renders for match-result picks. The artboard's mock
 * data is entirely match-result, so it never shows what the marker should do
 * for a Both Teams to Score or Total Goals pick -- and the real feed ranks
 * every market. Three empty cells would read as "none of these outcomes",
 * which is a claim about the match, so those rows get no marker at all.
 *
 * The marker itself is aria-hidden. It restates the pick that the card already
 * names in words directly beside it, so announcing "1, X, 2, draw picked" as
 * well would be the same fact twice. Spans rather than a list for the same
 * reason: there is no list here to navigate.
 */
export function HomePredictionCard({ prediction }: { prediction: HomeCardPrediction }) {
  const cells = resultCells(prediction.marketKey, prediction.rawSelection);
  const confidence = Math.round(prediction.confidence);

  return (
    <Link className="hm-card" href={prediction.href}>
      <span className="hm-card__top">
        <span className="hm-card__league">{prediction.league}</span>
        <span className="hm-card__time">
          {prediction.kickoffAt ? <LocalTime timeOnly value={prediction.kickoffAt} /> : "TBC"}
        </span>
      </span>

      {/*
        24px crests: home.html draws the pick card's team lines at 18px with the
        crest at 1.4em of that, which is 25.2. The card was already stacked, so
        this adds the crest and nothing else -- `hm-card__teams` keeps the 6px
        gap it had rather than taking the shared 4px.
      */}
      <span className="hm-card__teams">
        <span className="hm-card__team sl-team">
          <TeamCrest logoUrl={prediction.homeLogo} name={prediction.home} size={24} />
          <span className="sl-teamname">{prediction.home}</span>
        </span>
        <span className="hm-card__team sl-team">
          <TeamCrest logoUrl={prediction.awayLogo} name={prediction.away} size={24} />
          <span className="sl-teamname">{prediction.away}</span>
        </span>
      </span>

      <span className="hm-card__pick">
        {/*
          Stacked, not inline. Both of these were plain inline spans in a bare
          wrapper, so when the 30px value wrapped to a second line -- "Home win"
          does at 375px -- the market name flowed in beside that last line box
          and printed on top of it. Measured on production: the two boxes still
          intersect today.
        */}
        <span className="hm-card__pickText">
          <span className="hm-card__value">{prediction.prediction}</span>
          <span className="hm-card__sub">{prediction.market}</span>
        </span>
        {cells ? (
          <span aria-hidden="true" className="hm-1x2">
            {cells.map((cell) => (
              <span data-picked={cell.picked} key={cell.key} title={cell.label}>
                {cell.key}
              </span>
            ))}
          </span>
        ) : null}
      </span>

      <span className="hm-card__conf">
        <strong>{confidence}%</strong>
        <span aria-hidden="true" className="mp-market__track">
          <i className="mp-bar__fill" style={{ width: `${confidence}%` }} />
        </span>
        <span>confidence</span>
      </span>
    </Link>
  );
}
