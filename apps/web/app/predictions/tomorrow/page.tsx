import Link from "next/link";
import { cookies } from "next/headers";
import TodayPredictionsPage from "../today/page";
import { fetchAllTodayPredictionsWithProvider, type LivePrediction } from "../../data/livePredictions";
import { resolveSelectedDay } from "../../lib/dateWindow";

import { predictionFixturePath } from "../../lib/seo";
import { buildFaq, structuredDataScript } from "../../lib/structuredData";
import { DEFAULT_VIEWER_TIMEZONE, validViewerTimezone } from "../../lib/viewerTimezone";
import { canonicalPath } from "../../site";
import { predictionLabel } from "../../i18n/catalogs";
import { localePath } from "../../i18n/config";
import { requestLocaleContext } from "../../i18n/server";
import { marketLabel } from "../../lib/markets";
import { TopPickCard } from "../../components/TopPickCard";

/** A durable date hub: explain the scope and show the available model picks. */

const TOP_PICK_COUNT = 10;

export const metadata = {
  title: "Tomorrow's Football Predictions & Match Analysis - SokaLens",
  description: "Explore tomorrow's football predictions by league, fixture, market and model confidence. Check the date, coverage, team analysis and prediction limitations.",
  alternates: { canonical: canonicalPath("/predictions/tomorrow") },
};

function topPicks(predictions: LivePrediction[]) {
  const bestPerMatch = new Map<number, LivePrediction>();
  for (const prediction of predictions) {
    const existing = bestPerMatch.get(prediction.matchId);
    if (!existing || prediction.confidence > existing.confidence) bestPerMatch.set(prediction.matchId, prediction);
  }
  // One pick per fixture: ten rows for the same match reads as padding, and
  // the reader came for ten matches.
  return [...bestPerMatch.values()]
    .sort((left, right) => right.confidence - left.confidence)
    .slice(0, TOP_PICK_COUNT);
}

function longDate(isoDate: string) {
  return new Intl.DateTimeFormat("en-GB", {
    // isoDate already names the selected calendar day in the viewer's zone.
    timeZone: "UTC",
    weekday: "long",
    day: "numeric",
    month: "long",
  }).format(new Date(`${isoDate}T12:00:00Z`));
}

export default async function TomorrowPredictionsPage() {
  const { locale } = await requestLocaleContext();
  const timezone = validViewerTimezone((await cookies()).get("sokalens_timezone")?.value) || DEFAULT_VIEWER_TIMEZONE;
  const isoDate = resolveSelectedDay({ day: "tomorrow" }, new Date(), timezone);
  const feed = await fetchAllTodayPredictionsWithProvider(isoDate, timezone).catch(() => null);
  const picks = topPicks(feed?.engine ?? []);
  const fixtureCount = feed === null ? null : new Set(feed.engine.map((item) => item.matchId)).size;
  const dateLabel = longDate(isoDate);

  const faq = structuredDataScript([
    buildFaq([
      {
        question: "When are tomorrow's football predictions published?",
        answer: `This page covers ${dateLabel} in ${timezone}. Model selections appear after publication when the available history and quality checks support a fixture. Coverage may change before kickoff, and not every scheduled match has a model prediction.`,
      },
      {
        question: "What does the confidence figure mean?",
        answer: "Confidence is a score for how well supported the named selection is, built from data completeness, sample size, model agreement and similar checks. It is not the chance the selection wins, a guarantee or a verified success rate. Compare the exact market and its price; a higher score alone does not establish betting value. The results page explains the reported record and its coverage.",
      },
      {
        question: "Does SokaLens take bets?",
        answer: "No. SokaLens is not a bookmaker and accepts no stakes. It publishes model output and its own settled accuracy record.",
      },
    ]),
  ]);

  const lead = (
    <>
      <section aria-labelledby="tomorrow-coverage" className="td-note">
        <h2 id="tomorrow-coverage">Football predictions for {dateLabel}</h2>
        <p>Date boundary: {isoDate} in {timezone}. Kickoff times may display in your browser&apos;s local timezone.</p>
        <p>{fixtureCount === null
          ? "The model feed is temporarily unavailable. We cannot confirm tomorrow’s coverage from this request."
          : `${fixtureCount} ${fixtureCount === 1 ? "fixture" : "fixtures"} with model selections in the loaded feed. The shortlist below shows up to ten fixtures, one selection per match; it is not a complete list of all scheduled football.`}</p>
        <p>
          The shortlist ranks SokaLens model selections. The full fixture feed can also include
          provider estimates where model coverage is unavailable; these are not included in
          this ranking. Open a match to read the team history, goals profile and available analysis
          before comparing selections.
        </p>
        <p>
          Looking for a specific market? Compare <Link href={localePath("/predictions/markets/over-2-5-goals", locale)} prefetch={false}>Over 2.5 goals predictions</Link>{" "}
          with <Link href={localePath("/predictions/markets/btts", locale)} prefetch={false}>BTTS predictions</Link>.
          Those market pages explain the different score requirements and show their current feed;
          the list on this page is specifically for tomorrow.
        </p>
      </section>
      {picks.length > 0 ? (
        <section aria-labelledby="tomorrow-top-picks" className="td-toppicks">
          <div className="td-toppicks__head">
            <h2 id="tomorrow-top-picks">Tomorrow&apos;s model shortlist</h2>
            <p>Up to ten fixtures ranked by model confidence, one selection per match. Different markets have different outcome requirements; confidence alone is not a recommendation.</p>
          </div>
          <ol className="td-toppicks__list">
            {picks.map((prediction, index) => (
              <TopPickCard
                key={prediction.matchId}
                locale={locale}
                pick={{
                  href: predictionFixturePath(prediction),
                  homeTeam: prediction.homeTeam,
                  awayTeam: prediction.awayTeam,
                  homeTeamLogo: prediction.homeTeamLogo,
                  awayTeamLogo: prediction.awayTeamLogo,
                  league: prediction.leagueName || "Competition",
                  kickoffAt: prediction.kickoffAt ?? null,
                  market: marketLabel(prediction.marketKey, locale),
                  selection: predictionLabel(locale, prediction.marketKey, prediction.selection),
                  confidence: prediction.confidence,
                }}
                rank={index + 1}
              />
            ))}
          </ol>
        </section>
      ) : null}

      {/*
        The FAQ in visible copy, which is the only kind Google will honour FAQ
        markup for. It answers the two questions the query actually carries.
      */}
      <section aria-labelledby="tomorrow-faq" className="td-note">
        <h2 id="tomorrow-faq">About tomorrow&apos;s predictions</h2>
        <dl className="td-faq">
          <dt>When are tomorrow&apos;s football predictions published?</dt>
          <dd>This page covers {dateLabel} in {timezone}. Model selections appear after publication when the available history and quality checks support a fixture. Coverage may change before kickoff, and not every scheduled match has a model prediction.</dd>
          <dt>What does the confidence figure mean?</dt>
          <dd>Confidence is a score for how well supported the named selection is, built from data completeness, sample size, model agreement and similar checks. It is not the chance the selection wins, a guarantee or a verified success rate. Compare the exact market and its price; a higher score alone does not establish betting value. The <Link href={localePath("/results", locale)}>results page</Link> explains the reported record and its coverage.</dd>
          <dt>Does SokaLens take bets?</dt>
          <dd>No. SokaLens is not a bookmaker and accepts no stakes. It publishes model output and its own settled accuracy record.</dd>
        </dl>
      </section>
    </>
  );

  return (
    <>
      {faq ? <script dangerouslySetInnerHTML={{ __html: faq }} type="application/ld+json" /> : null}
      <TodayPredictionsPage forcedDay="tomorrow" hubLabel="Tomorrow’s" leadContent={lead} />
    </>
  );
}
