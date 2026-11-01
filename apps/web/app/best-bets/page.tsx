import Link from "next/link";
import { BestBetExplorer, type BestBetItem } from "../components/BestBetExplorer";
import { ContentPageShell } from "../components/ContentPageShell";
import { LinkCard } from "../components/LinkCard";
import { PredictionSubnav } from "../components/PredictionSubnav";
import { fixtureHref } from "../lib/fixtureHref";
import {
  fetchTodayPredictionPool,
  marketLabel,
  marketSelectionLabel,
  matchLabel,
  type LivePrediction,
} from "../data/livePredictions";
import { selectBestBets } from "../lib/bestBets";
import { upcomingFixtures } from "../lib/upcoming";
import { canonicalPath } from "../site";
import { requestLocaleContext } from "../i18n/server";
import { localePath } from "../i18n/config";

export const metadata = {
  title: "Best Bets: Strong recommendations - SokaLens",
  description: "Strong recommendations ranked by confidence, risk, and available match context.",
  alternates: { canonical: canonicalPath("/best-bets") },
};

export default async function BestBetsPage() {
  const { locale } = await requestLocaleContext();
  let predictions: LivePrediction[] = [];
  let feedFailed = false;
  try {
    // The page promises the day's strongest picks, so it has to see the day.
    // At 40 it only ever considered the earliest fixtures.
    predictions = await fetchTodayPredictionPool();
  } catch {
    feedFailed = true;
  }

  // No threshold, and every market -- not just the feed's primary 1x2 row. The
  // old 65% gate was unreachable rather than strict: no market on any of the
  // day's 258 fixtures passed 53.8% on 2026-09-04. A match with two strongly
  // ranked markets earns two places, which is why the key below is per market.
  // Only fixtures a reader can still act on. The day pool spans the whole day
  // by design, so 36 of 258 had already kicked off on 2026-09-04 -- and a
  // shortlist led by an 02:00 kickoff is wrong.
  const bestBets = selectBestBets(upcomingFixtures(predictions));

  const explorerItems: BestBetItem[] = bestBets.map((item) => ({
    matchId: item.matchId,
    match: matchLabel(item),
    /*
      The two sides and their crests, for the stacked row (review 2.3, UI-04).
      `/v1/best-bets` serialises through `predictionSummaryResponse`, and task
      F1 put `home_team_logo`/`away_team_logo` on the provider fallback rows
      too, so every row in this pool -- engine and provider alike -- carries a
      crest rather than one drawing a shield beside the other's real badge.
    */
    homeTeam: item.homeTeam,
    awayTeam: item.awayTeam,
    homeTeamLogo: item.homeTeamLogo,
    awayTeamLogo: item.awayTeamLogo,
    league: item.leagueName || "Competition",
    country: item.country || "",
    kickoffAt: item.kickoffAt ?? null,
    market: marketLabel(item.marketKey, locale),
    marketKey: item.marketKey,
    prediction: marketSelectionLabel(item.marketKey, item.selection, locale),
    confidence: item.confidence,
    href: fixtureHref(item),
  }));

  const competitions = new Set(explorerItems.map((item) => item.league)).size;
  const fixtures = new Set(explorerItems.map((item) => item.matchId)).size;

  return (
    <ContentPageShell
      accent="bets"
      beforeBody={<PredictionSubnav />}
      crumbs={[{ label: "Best Bets" }]}
      eyebrow={feedFailed ? "Shortlist unavailable" : "Ranked by model confidence · today"}
      locale={locale}
      rail={
        <>
          {/*
            The design asks for prose here, not a checklist. A list of five
            criteria reads as terms and conditions; the one sentence it replaces
            says the same thing in the order a reader actually asks it -- what
            the number means, where the picks come from, and how they are
            judged afterwards.
          */}
          <div className="td-panel">
            <h2>How we rank</h2>
            <p className="bb-rank">
              Each pick shows the model&rsquo;s probability for that outcome. We list the
              day&rsquo;s highest-confidence picks across all markets, and every one is settled
              on the Performance page.
            </p>
          </div>
          <div className="td-panel td-panel--links">
            <h2>Keep exploring</h2>
            <LinkCard href="/predictions/today" label="Today&rsquo;s full list" locale={locale} />
            <LinkCard href="/multi-match-builder" label="Build an ACCA from these" locale={locale} />
            <LinkCard href="/results" label="How we performed" locale={locale} />
          </div>
        </>
      }
      /*
        `Fixtures` and `Picks` differ because a match with two strongly ranked
        markets earns two places. Showing only one of the two figures invites the
        reading that this is a list of 40 independent chances when it may be 40
        picks across 28 games -- which is the same correlation the list's own
        cluster notice exists to spell out.
      */
      stats={feedFailed ? undefined : [
        { label: "Picks", value: explorerItems.length, accent: true },
        { label: "Fixtures", value: fixtures },
        { label: "Competitions", value: competitions },
      ]}
      summary="Ranked model picks with confidence, market, and match context shown openly."
      title="Best"
    >
      {feedFailed ? (
        <div className="ac-empty" role="alert">
          <h3>Best Bets are temporarily unavailable</h3>
          <p>
            The recommendations feed could not be loaded, so no picks are being shown. No sample
            selections are substituted for live data.
          </p>
          <Link className="mp-btn" href={localePath("/predictions/today", locale)}>
            Browse today&apos;s predictions
          </Link>
        </div>
      ) : explorerItems.length > 0 ? (
        <BestBetExplorer items={explorerItems} />
      ) : (
        <div className="ac-empty" aria-live="polite">
          <h3>No fixtures to rank right now</h3>
          <p>
            There are no published fixtures left to play today &mdash; check back closer to
            kickoff. Confidence is shown on every pick so you can judge it yourself.
          </p>
          <Link className="mp-btn" href={localePath("/predictions/today", locale)}>
            Browse today&apos;s predictions
          </Link>
        </div>
      )}
    </ContentPageShell>
  );
}
