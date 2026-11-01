import Link from "next/link";
import { ContentPageShell } from "../components/ContentPageShell";
import { LeagueDirectory, type DirectoryLeague } from "../components/LeagueDirectory";
import { LinkCard } from "../components/LinkCard";
import { PredictionSubnav } from "../components/PredictionSubnav";
import { LEAGUE_CATALOGUE_LIMIT, fetchLeagues } from "../data/catalog";
import { seoLeaguePath } from "../lib/seo";
import { canonicalPath } from "../site";
import { requestLocaleContext } from "../i18n/server";
import { localePath } from "../i18n/config";

export const metadata = {
  title: "Supported leagues and competitions - SokaLens",
  description:
    "Browse supported football leagues, upcoming fixtures, and prediction availability.",
  alternates: {
    canonical: canonicalPath("/leagues"),
  },
};

export default async function LeaguesPage() {
  const { locale } = await requestLocaleContext();
  const result = await fetchLeagues(LEAGUE_CATALOGUE_LIMIT);
  const leagues = result.ok ? result.data : [];
  const directoryFailed = !result.ok;

  /*
   * Only competitions that have something on them, ordered by the catalogue's
   * own priority score -- so "featured" means what the catalogue says it means
   * rather than what this page decides.
   *
   * The endpoint now returns the complete catalogue, and ~350 of those leagues
   * hold no fixtures at all -- past, live or scheduled. Linking them would
   * point readers and crawlers at empty pages and spend crawl budget doing it.
   *
   * This is deliberately NOT the sitemap's predicate. The sitemap is built from
   * `seo_urls`, which the SEO sweep maintains with its own exclusion reasons, so
   * the two sets are related but not identical. That is fine in this direction:
   * linking a page the sweep has not yet judged indexable costs nothing, while
   * linking a page with no content on it costs crawl budget.
   */
  const directory: DirectoryLeague[] = [...leagues]
    .filter((league) => league.has_content)
    .sort((left, right) => (right.priority_score ?? 0) - (left.priority_score ?? 0))
    .map((league) => ({
      slug: league.slug,
      name: league.name,
      country: league.country,
      logoUrl: league.logo_url,
    }));

  // The catalogue holds more competitions than the directory lists; the
  // difference is the ones with no fixtures on them at all.
  const catalogued = leagues.length;

  return (
    <ContentPageShell
      accent="leagues"
      beforeBody={<PredictionSubnav />}
      crumbs={[{ label: "Leagues" }]}
      eyebrow={directoryFailed ? "Competitions · directory unavailable" : "Competitions"}
      locale={locale}
      rail={
        <div className="td-panel td-panel--links">
          <h2>Keep exploring</h2>
          <LinkCard href="/predictions/today" label="Today&rsquo;s predictions" locale={locale} />
          <LinkCard href="/best-bets" label="Best Bets" locale={locale} />
          <LinkCard href="/multi-match-builder" label="Generate an ACCA" locale={locale} />
        </div>
      }
      stats={[
        /*
          "Predictions live" counted `has_content`, which is whether a league
          has any fixture -- not whether anything is predicted. Now that the
          directory lists only leagues with content, that figure would equal the
          row count exactly and assert something it never measured.
        */
        { label: "Competitions", value: directory.length, accent: true },
        { label: "In the catalogue", value: catalogued },
        { label: "Countries", value: new Set(directory.map((league) => league.country).filter(Boolean)).size },
      ]}
      summary="Every competition we cover. Open a league for its fixtures, published picks and team pages."
      title="Football"
    >
      {directoryFailed ? (
        <div className="ac-empty" role="alert">
          <h3>League directory unavailable</h3>
          <p>
            The league directory could not be loaded right now. Try again in a moment, or browse
            the current fixture schedule.
          </p>
          <Link className="mp-btn" href={localePath("/matches/today", locale)}>
            View today&apos;s matches
          </Link>
        </div>
      ) : directory.length > 0 ? (
        <LeagueDirectory leagues={directory} />
      ) : (
        <div className="ac-empty" aria-live="polite">
          <h3>No leagues listed yet</h3>
          <p>
            No competitions are in the directory yet. Coverage varies by competition, and only
            leagues that exist in SokaLens data appear here.
          </p>
          <Link className="mp-btn" href={localePath("/matches/today", locale)}>
            View today&apos;s matches
          </Link>
        </div>
      )}
    </ContentPageShell>
  );
}
