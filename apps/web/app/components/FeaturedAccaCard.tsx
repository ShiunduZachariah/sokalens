import Link from "next/link";
import { MatchupIdentity } from "./MatchupIdentity";
import { marketLabel, marketSelectionLabel } from "../data/livePredictions";

import { formatOdds } from "../lib/odds";
import { fixtureHref } from "../lib/fixtureHref";
import type { Locale } from "../i18n/config";
import type { AccaBundle, AccaLeg } from "../acca-predictions/accaBundles";
import { summarizeFeaturedAcca } from "./featuredAcca";
import { LocalTime } from "./LocalTime";

function FeaturedAccaLeg({ leg, locale }: { leg: AccaLeg; locale: Locale }) {
  return <Link className="featured-acca-card__leg" href={fixtureHref(leg)}>
    <MatchupIdentity awayTeam={leg.awayTeam} awayTeamLogo={leg.awayTeamLogo} className="featured-acca-card__teams" homeTeam={leg.homeTeam} homeTeamLogo={leg.homeTeamLogo} />
    <div className="featured-acca-card__pick"><span>{marketLabel(leg.marketKey, locale)}</span><strong>{marketSelectionLabel(leg.marketKey, leg.selection, locale)}</strong></div>
    <small>{leg.leagueName || "Competition"} · {<LocalTime value={leg.kickoffAt} />}</small>
  </Link>;
}

export function FeaturedAccaCard({ bundle, locale }: { bundle: AccaBundle; locale: Locale }) {
  const summary = summarizeFeaturedAcca(bundle);
  const href = `/acca-predictions#acca-${encodeURIComponent(bundle.id)}`;
  const combinedOdds = Number(bundle.combinedFairOdds);
  return <article className="featured-acca-card">
    <header className="featured-acca-card__header"><div><span className="micro muted">{bundle.legs.length} selections · {bundle.marketKey === "all" ? "Mixed markets" : marketLabel(bundle.marketKey, locale)}</span><h3>{bundle.legs.length}-leg ACCA</h3></div>{Number.isFinite(bundle.combinedFairOdds) && combinedOdds > 0 ? <div className="featured-acca-card__odds"><small>Combined fair odds</small><strong>{formatOdds(combinedOdds, "decimal")}</strong></div> : null}</header>
    <div className="featured-acca-card__meta"><span>{summary.leagueCount} {summary.leagueCount === 1 ? "league" : "leagues"}</span><span>{summary.marketCount} {summary.marketCount === 1 ? "market" : "markets"}</span></div>
    <div className="featured-acca-card__legs">{summary.previewLegs.map((leg) => <FeaturedAccaLeg key={`${leg.matchId}:${leg.marketKey}:${leg.selection}`} leg={leg} locale={locale} />)}</div>
    <p className="featured-acca-card__remainder">{summary.remainderLabel}</p>
    <Link className="featured-acca-card__cta" href={href}>View {bundle.legs.length}-leg ACCA <span aria-hidden="true">→</span></Link>
  </article>;
}
