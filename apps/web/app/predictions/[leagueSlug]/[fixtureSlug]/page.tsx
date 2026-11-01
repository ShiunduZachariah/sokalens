import type { Metadata } from "next";
import { notFound, permanentRedirect } from "next/navigation";
import { MatchDetail } from "../../../components/MatchDetail";
import { fetchMatchById } from "../../../data/liveMatches";
import { marketLabel, marketSelectionLabel } from "../../../data/livePredictions";
import { absoluteUrl, canonicalPath } from "../../../site";
import { fixtureIdFromSeoSlug, seoFixturePath } from "../../../lib/seo";
import { isIndexableFixture } from "../../../lib/fixtureLifecycle";
import { buildSportsEventStructuredData } from "../../../lib/sportsEventStructuredData";
import { buildBreadcrumbs } from "../../../lib/structuredData";
import { leagueSlugForName } from "../../../data/leagueLookup";
import { leaguePredictionPath } from "../../../lib/catalogSlug";
import { canonicalForRequest, localeAlternates, localePath } from "../../../i18n/config";
import { predictionLabel, translate } from "../../../i18n/catalogs";
import { requestLocaleContext } from "../../../i18n/server";

type FixtureSeoPageProps = {
  params: Promise<{ leagueSlug: string; fixtureSlug: string }>;
};

async function resolveFixture(params: FixtureSeoPageProps["params"]) {
  const { fixtureSlug } = await params;
  const id = fixtureIdFromSeoSlug(fixtureSlug);
  return id ? fetchMatchById(id) : null;
}

export async function generateMetadata({ params }: FixtureSeoPageProps): Promise<Metadata> {
  const { locale, publicPath } = await requestLocaleContext();
  const match = await resolveFixture(params);
  if (!match) notFound();
  const prediction = match.prediction ?? match.predictions[0] ?? null;
  const path = seoFixturePath(match);
  const localizedSelection = prediction ? predictionLabel(locale, prediction.marketKey, prediction.selection) : "";
  const readableSelection = prediction && localizedSelection === prediction.selection
    ? marketSelectionLabel(prediction.marketKey, prediction.selection, locale)
    : localizedSelection;
  const description = translate(locale, "match.metadata_description", { home: match.homeTeam, away: match.awayTeam });
  return {
    // Every localized match.metadata_title catalog value includes the SokaLens brand.
    title: translate(locale, "match.metadata_title", { home: match.homeTeam, away: match.awayTeam }),
    description: prediction ? `${readableSelection} · ${description}` : description,
    alternates: { canonical: canonicalForRequest(canonicalPath(path), publicPath, locale), languages: localeAlternates(path) },
    // This is now the only URL a fixture has, so it must answer for fixtures
    // that have no published prediction yet. Those carry nothing worth ranking
    // and stay out of the index until the engine publishes; `follow` keeps the
    // team, league and related-fixture links on the page working as crawl
    // paths in the meantime.
    // Indexable only while the fixture still has search demand. A match more
    // than SETTLED_INDEXABLE_DAYS past kickoff is retired with noindex on a
    // page that still returns 200, so inbound links keep working and `follow`
    // keeps passing equity to the team and league pages it links.
    //
    // hasIndexableHistory is the third condition and the newest: a fixture
    // whose teams have almost no settled history produces a page with nothing
    // specific on it, and ~7,300 such pages were spending a crawl allowance
    // that Googlebot was only using for 0.3% of origin requests. Withheld the
    // same way -- noindex on a 200, still followed -- so it costs nothing but
    // the crawl slot. The counts come free on the match payload; the sweep
    // computes the same two figures with the same predicate, and the pair is
    // pinned by a test, because a sitemap that advertises a noindexed URL is
    // the failure the seourl package exists to prevent.
    robots: {
      index: isIndexableFixture(match, Boolean(prediction)),
      follow: true,
    },
    openGraph: {
      type: "article",
      url: absoluteUrl(localePath(path, locale)),
      title: translate(locale, "match.metadata_title", { home: match.homeTeam, away: match.awayTeam }),
      description: prediction ? `${marketLabel(prediction.marketKey, locale)}: ${readableSelection}.` : description,
    },
  };
}

export default async function FixtureSeoPage({ params }: FixtureSeoPageProps) {
  const { locale, publicPath } = await requestLocaleContext();
  const resolved = await params;
  const match = await resolveFixture(Promise.resolve(resolved));
  if (!match) notFound();

  const expectedPath = seoFixturePath(match);
  const requestedPath = `/predictions/${resolved.leagueSlug}/${resolved.fixtureSlug}`;
  if (requestedPath !== expectedPath) permanentRedirect(localePath(expectedPath, locale));
  const structuredDataPath = canonicalForRequest(expectedPath, publicPath, locale);

  // The trail names the actual competition when its hub exists, so the
  // breadcrumb Google renders in place of the URL reads as a real position in
  // a structure rather than a generic two-step to "Leagues".
  const leagueSlug = match.leagueSlug || await leagueSlugForName(match.leagueName);
  const leagueHref = leaguePredictionPath(leagueSlug);
  const crumbs = [
    { name: translate(locale, "nav.predictions"), path: canonicalForRequest("/predictions/today", publicPath, locale) },
    leagueHref
      ? { name: match.leagueName, path: canonicalForRequest(leagueHref, publicPath, locale) }
      : { name: "Leagues", path: canonicalForRequest("/leagues", publicPath, locale) },
    { name: `${match.homeTeam} vs ${match.awayTeam}`, path: structuredDataPath },
  ];

  const structuredData = [
    buildSportsEventStructuredData(match, absoluteUrl(structuredDataPath)),
    buildBreadcrumbs(crumbs),
  ].filter(Boolean);

  return (
    <>
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: JSON.stringify(structuredData).replace(/</g, "\\u003c") }} />
      <MatchDetail match={match} />
    </>
  );
}
