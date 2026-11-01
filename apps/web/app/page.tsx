import Link from "next/link";
import { headers } from "next/headers";
import { Suspense } from "react";
import { AdSlot } from "./components/AdSlot";
import { FeedAdBlock } from "./components/FeedAdBlock";
import { ResponsibleNote } from "./components/ResponsibleNote";
import { HomePredictionCard, type HomeCardPrediction } from "./components/home/HomePredictionCard";
import { ArrowRightIcon, ArrowUpRightIcon, TargetRings, TickIcon } from "./components/match/icons";
import {
  fetchTodayPredictionPool,
  marketLabel,
  marketSelectionLabel,
  type LivePrediction,
} from "./data/livePredictions";
import { howItWorks } from "./data/product";
import { predictionFixturePath } from "./lib/seo";
import { rankPredictions } from "./lib/regionRanking";
import { upcomingFixtures } from "./lib/upcoming";
import { resolveViewerCountry } from "./lib/viewerCountry";
import { localeAlternates, localePath } from "./i18n/config";
import { predictionLabel, translate } from "./i18n/catalogs";
import { requestLocaleContext } from "./i18n/server";
import { fetchAccaPage } from "./data/accaApi";
import { FeaturedAccaCard } from "./components/FeaturedAccaCard";
import { fixtureHref } from "./lib/fixtureHref";
import { LocalTime } from "./components/LocalTime";
import { TeamCrest } from "./components/TeamCrest";

type HomePredictionFeed = {
  predictions: HomeCardPrediction[];
  state: "empty" | "live" | "unavailable";
};

async function loadHomePredictionFeed(
  locale: Parameters<typeof predictionLabel>[0],
  viewerCountry: string,
): Promise<HomePredictionFeed> {
  // The whole day, not the next 24 kickoffs. Ranking a kickoff-ordered prefix
  // silently excluded Europe's evening fixtures, which are the day's latest.
  const predictionsResult = await fetchTodayPredictionPool().then(
    (value) => ({ status: "fulfilled" as const, value }),
    () => ({ status: "rejected" as const, value: [] as LivePrediction[] }),
  );

  // Rank for the reader, not just by competition. competitionPopularity is a
  // single global order, so a Kenyan fixture and a Brazilian one were shown in
  // the same sequence to everyone. rankPredictions puts the reader's own
  // country first, then the globally recognised leagues, then their continent,
  // and only then falls back to confidence and kickoff.
  const ranked = predictionsResult.status === "fulfilled"
    ? rankPredictions(
        upcomingFixtures(predictionsResult.value).map((prediction) => ({
          ...prediction,
          leagueCountry: prediction.country,
        })),
        viewerCountry,
      ).slice(0, 9)
    : [];

  return {
    state: predictionsResult.status === "rejected"
      ? "unavailable"
      : ranked.length > 0 ? "live" : "empty",
    predictions: ranked.slice(0, 6).map((prediction) => {
      const localized = predictionLabel(locale, prediction.marketKey, prediction.selection);
      return {
        away: prediction.awayTeam,
        awayLogo: prediction.awayTeamLogo,
        confidence: prediction.confidence,
        home: prediction.homeTeam,
        homeLogo: prediction.homeTeamLogo,
        href: prediction.id > 0 ? predictionFixturePath(prediction) : fixtureHref(prediction),
        kickoffAt: prediction.kickoffAt ?? null,
        league: prediction.leagueName || "Competition",
        market: marketLabel(prediction.marketKey, locale),
        marketKey: prediction.marketKey,
        prediction: localized === prediction.selection
          ? marketSelectionLabel(prediction.marketKey, prediction.selection, locale)
          : localized,
        rawSelection: prediction.selection,
      };
    }),
  };
}

/**
 * The hero's headline card: the day's strongest signal.
 *
 * "Strongest" is the highest confidence in the ranked set, which is what the
 * artboard's own script does. It is picked from the ranked list rather than the
 * raw pool so the card stays regionally relevant, and it is deliberately the
 * same object as the first grid card rather than a second query.
 */
async function StrongestSignal({ feedPromise }: { feedPromise: Promise<HomePredictionFeed> }) {
  const { predictions, state } = await feedPromise;
  const top = predictions.reduce<HomeCardPrediction | null>(
    (best, item) => (best === null || item.confidence > best.confidence ? item : best),
    null,
  );

  if (!top) {
    return (
      <div className="hm-signal">
        <div className="hm-signal__top">
          <span className="hm-signal__badge">
            {state === "empty" ? "No signals today" : "Feed unavailable"}
          </span>
        </div>
        <div className="hm-signal__fixture">
          <span className="hm-signal__team">
            {state === "empty" ? "Nothing published yet" : "Cannot load signals"}
          </span>
        </div>
        {/*
          A quiet slate is not filled with sample picks, and neither is a failed
          read -- either would put invented data where the reader expects the
          model's own output.
        */}
        <p className="mp-pick__note">
          {state === "empty"
            ? "Today's prediction list is up to date. SokaLens does not fill a quiet slate with sample picks."
            : "Today's predictions could not be loaded, so no sample picks are shown as current data."}
        </p>
      </div>
    );
  }

  const confidence = Math.round(top.confidence);
  return (
    <Link className="hm-signal" href={top.href}>
      <span className="hm-signal__top">
        <span className="hm-signal__badge">Strongest signal today</span>
        <span className="hm-signal__time">
          {top.kickoffAt ? <LocalTime timeOnly value={top.kickoffAt} /> : "TBC"}
        </span>
      </span>
      {/*
        44px crests, the largest the review names: home.html sets the signal
        card's crest at 1.4em of its 30px name, which is 42. It steps down to 32
        below 900px where that name is 24px -- in CSS, since the component
        cannot see the breakpoint.

        The "vs" between the two lines stays. It is the one surface where the
        export keeps it alongside stacked names, and dropping it would leave two
        unlabelled lines in a card whose whole job is to name one fixture.
      */}
      <span className="hm-signal__fixture">
        <span className="hm-signal__league">{top.league}</span>
        <span className="hm-signal__team sl-team">
          <TeamCrest logoUrl={top.homeLogo} name={top.home} size={44} />
          <span className="sl-teamname">{top.home}</span>
        </span>
        <span className="hm-signal__v">vs</span>
        <span className="hm-signal__team sl-team">
          <TeamCrest logoUrl={top.awayLogo} name={top.away} size={44} />
          <span className="sl-teamname">{top.away}</span>
        </span>
      </span>
      <span className="hm-signal__pick">
        <span>
          <span>Model pick</span>
          <span className="hm-signal__value">{top.prediction}</span>
        </span>
        <span className="hm-signal__conf">{confidence}%</span>
      </span>
      <span aria-hidden="true" className="mp-market__track">
        <i className="mp-bar__fill" style={{ width: `${confidence}%` }} />
      </span>
    </Link>
  );
}

async function TodayGrid({ feedPromise }: { feedPromise: Promise<HomePredictionFeed> }) {
  const { predictions, state } = await feedPromise;

  if (predictions.length === 0) {
    return (
      <div className="mp-state">
        <h3>{state === "empty" ? "No live predictions for today" : "Prediction feed unavailable"}</h3>
        <p>
          {state === "empty"
            ? "There are no published signals in today's match window. Check the fixture schedule or return when the next signals are ready."
            : "Current predictions could not be loaded. Sample rows are intentionally hidden from this section."}
        </p>
      </div>
    );
  }

  return (
    <div className="hm-cards">
      {predictions.map((prediction) => (
        <HomePredictionCard key={prediction.href} prediction={prediction} />
      ))}
    </div>
  );
}

/** Skeletons matching the real card's height, so the grid does not jump. */
function TodayGridFallback() {
  return (
    <div aria-hidden="true" className="hm-cards">
      {Array.from({ length: 6 }, (_, index) => (
        <div className="hm-card" key={index}>
          <span className="hm-card__top">
            <span className="hm-card__league">Loading competition</span>
            <span className="hm-card__time">TBC</span>
          </span>
          <span className="hm-card__teams">
            <span className="hm-card__team">&nbsp;</span>
            <span className="hm-card__team">&nbsp;</span>
          </span>
          <span className="hm-card__pick">
            <span><span className="hm-card__sub">Preparing prediction…</span></span>
          </span>
        </div>
      ))}
    </div>
  );
}

async function FeaturedAccas({ locale }: { locale: Parameters<typeof predictionLabel>[0] }) {
  if (process.env.ACCA_INVENTORY_V2_ENABLED !== "true") return null;
  let page;
  try {
    // Featured inventory spans the same rolling horizon as the catalogue. A
    // "today" query can legitimately be empty while valid upcoming ACCAs
    // exist later in the seven-day window.
    // Show four when inventory permits; the grid adapts to fewer cards without
    // leaving an empty cell.
    page = await fetchAccaPage(new URLSearchParams({ tab: "all", period: "seven_days", limit: "4" }));
  } catch {
    page = null;
  }

  return (
    <section aria-labelledby="featured-accas" className="mp-section">
      <div className="mp-section__head">
        <div>
          <span className="mp-section__num">03 — ACCAS</span>
          <h2 className="mp-h2" id="featured-accas">Featured ACCAs</h2>
        </div>
        <Link className="mp-fixture__cta" href={localePath("/acca-predictions", locale)}>
          Browse all ACCAs
          <ArrowRightIcon />
        </Link>
      </div>
      {page?.bundles.length ? (
        /*
          Four cards fill a two-column desktop grid. If inventory provides only
          three, display those in one row; one or two stack full-width. The
          count drives the layout so no row has an empty cell.

          Its own class rather than `.ac-grid`: that one is the catalogue's
          two-column grid, and the catalogue is paginated so it is always full.
          Sharing it is what tied Home's layout to a page with different
          arithmetic.
        */
        <div className="hm-accas" data-count={Math.min(page.bundles.length, 4)}>
          {page.bundles.slice(0, 4).map((bundle) => (
            <FeaturedAccaCard bundle={bundle} key={bundle.id} locale={locale} />
          ))}
        </div>
      ) : (
        <div className="mp-state">
          <h3>
            {page ? "No featured ACCAs are available right now" : "Featured ACCAs are temporarily unavailable"}
          </h3>
          <p>
            {page
              ? "The background inventory is waiting for enough qualified fixtures. Check the catalogue as new matches are published."
              : "Predictions continue independently. Please try the ACCA catalogue again shortly."}
          </p>
        </div>
      )}
    </section>
  );
}

export async function generateMetadata() {
  const { locale, publicPath } = await requestLocaleContext();
  return {
    title: translate(locale, "site.title"),
    description: translate(locale, "site.description"),
    alternates: { canonical: publicPath, languages: localeAlternates(publicPath) },
  };
}

export default async function HomePage() {
  const { locale } = await requestLocaleContext();
  const t = (key: Parameters<typeof translate>[1], values?: Record<string, string | number>) =>
    translate(locale, key, values);
  // Cloudflare fronts production and supplies cf-ipcountry; an unresolved
  // country is "" and the ranking falls back to globally recognised leagues.
  const viewerCountry = resolveViewerCountry(await headers(), locale);
  const feedPromise = loadHomePredictionFeed(locale, viewerCountry);

  return (
    <div className="mp mp-page">
      <main className="mp__inner" id="content">
        {/*
          Ad spec 3.1: directly under the site header, above the page title. It
          used to render below the hero and the jump nav -- a full screen down
          on mobile, where it also shared that screen with the adhesion band.
          V31's home.html puts it between `</header>` and the `<h1>`.
        */}
        <AdSlot format="top" placement="page-top" />

        <section className="hm-hero">
          <TargetRings />
          <div className="hm-hero__copy">
            <span className="hm-hero__eyebrow">{t("home.hero_eyebrow")}</span>
            <h1>
              {t("home.hero_title")}
              <br />
              <em>{t("home.hero_emphasis")}</em>
            </h1>
            <p className="hm-hero__lede">{t("home.summary")}</p>
            <ul className="hm-proof">
              <li><TickIcon /> No certainty claims</li>
              <li><TickIcon /> Versioned predictions</li>
              <li><TickIcon /> Data-led signals</li>
            </ul>
            <div className="hm-hero__actions">
              <Link className="mp-btn mp-btn--primary" href={localePath("/predictions/today", locale)}>
                {t("home.explore")}
                <ArrowUpRightIcon />
              </Link>
              <Link className="mp-btn" href={localePath("/acca-predictions", locale)}>
                Browse ACCAs
                <ArrowUpRightIcon />
              </Link>
            </div>
          </div>

          <Suspense fallback={<div className="hm-signal" aria-busy="true" />}>
            <StrongestSignal feedPromise={feedPromise} />
          </Suspense>
        </section>

        <nav aria-label="Prediction shortcuts" className="hm-jump">
          <span className="hm-jump__label">Jump to</span>
          <Link className="mp-pill" href={localePath("/predictions/tomorrow", locale)}>
            Tomorrow&apos;s predictions
          </Link>
          <Link className="mp-pill" href={localePath("/predictions/weekend", locale)}>
            Weekend predictions
          </Link>
          <Link className="mp-pill" href={localePath("/predictions/markets/over-2-5-goals", locale)}>
            Over 2.5 goals
          </Link>
          <Link className="mp-pill" href={localePath("/predictions/markets/btts", locale)}>
            Both teams to score
          </Link>
          <Link className="mp-pill" href={localePath("/leagues", locale)}>
            League predictions
          </Link>
        </nav>

        <section aria-labelledby="today-predictions" className="mp-section">
          <div className="mp-section__head">
            <div>
              <span className="mp-section__num">01 — TODAY</span>
              <h2 className="mp-h2" id="today-predictions">Today&apos;s predictions</h2>
            </div>
            <Link className="mp-fixture__cta" href={localePath("/predictions/today", locale)}>
              All predictions
              <ArrowRightIcon />
            </Link>
          </div>
          <Suspense fallback={<TodayGridFallback />}>
            <TodayGrid feedPromise={feedPromise} />
          </Suspense>
        </section>

        {/*
          The home page's one in-feed unit.

          Ad spec 3.1 reads "between Featured ACCAs and How it works"; V31's
          home.html puts the slot between the Today grid and "How SokaLens
          works", with Featured ACCAs after the method section. The two
          disagree about which block comes first and agree exactly on this: the
          unit goes DIRECTLY ABOVE "How it works". That is where it is, so both
          readings hold whichever way the section order lands.
        */}
        <FeedAdBlock />

        <section aria-labelledby="how-it-works" className="hm-method">
          <div className="hm-method__intro">
            <span className="mp-section__num">02 — METHOD</span>
            <h2 className="mp-h2" id="how-it-works">How SokaLens works</h2>
            <p>
              Predictions are informational and not guaranteed. They should not be treated as
              financial advice, and outcomes can change with team news, market movement, or match
              conditions.
            </p>
            <Link href={localePath("/results", locale)}>How we performed</Link>
          </div>
          <ol className="hm-steps">
            {howItWorks.map((item) => (
              <li key={item.step}>
                <span className="hm-steps__num">{item.step}</span>
                <div>
                  <h3>{item.title}</h3>
                  <p>{item.copy}</p>
                </div>
              </li>
            ))}
          </ol>
        </section>

        <Suspense fallback={null}>
          <FeaturedAccas locale={locale} />
        </Suspense>

        <div className="mp-disclaimer">
          <span aria-label="Eighteen plus only" className="mp-disclaimer__age">18+</span>
          <p>
            Statistical estimates, never guaranteed outcomes. Use predictions as analysis, not
            financial advice.
          </p>
          <Link href={localePath("/responsible-gambling", locale)}>Responsible gambling</Link>
        </div>

        <div className="mp-tail">
          <ResponsibleNote />
        </div>
      </main>
    </div>
  );
}
