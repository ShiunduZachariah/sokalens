import Link from "next/link";
import { notFound } from "next/navigation";
import { ContentPageShell } from "../../components/ContentPageShell";
import { FixtureGroups, ResultRows } from "../../components/FixtureRows";
import { LinkCard } from "../../components/LinkCard";
import { ResponsibleNote } from "../../components/ResponsibleNote";
import { FavoriteButton } from "../../components/account/FavoriteButton";
import { fetchLeague } from "../../data/catalog";
import { validCatalogSlug } from "../../lib/catalogSlug";
import { pluralize } from "../../lib/format";
import { canonicalPath } from "../../site";
import { seoLeaguePath } from "../../lib/seo";
import { localePath } from "../../i18n/config";
import { requestLocaleContext } from "../../i18n/server";

type LeaguePageProps = {
  params: Promise<{ slug: string }>;
};

function LeagueUnavailable() {
  return (
    <main className="page-shell" id="content">
      <div className="content-grid">
        <div className="main-column">
          <section className="state-card" role="alert">
            <h1 className="page-title">League data is temporarily unavailable</h1>
            <p className="muted">
              The league directory could not be loaded right now. Try again in a moment, or browse
              the current fixture schedule instead.
            </p>
            <p className="section-footer-action">
              <Link className="button button--secondary" href="/leagues">
                Browse all leagues
              </Link>{" "}
              <Link className="button" href="/matches/today">
                View today&apos;s matches
              </Link>
            </p>
          </section>
        </div>
        <aside className="side-rail" aria-label="League support">
          <ResponsibleNote compact />
        </aside>
      </div>
    </main>
  );
}

export async function generateMetadata({ params }: LeaguePageProps) {
  const { slug } = await params;
  const result = await fetchLeague(slug);
  if (!result.ok) {
    if (result.error === "not_found") notFound();
    return {
      title: "League data temporarily unavailable - SokaLens",
      description: "League data is temporarily unavailable.",
      alternates: { canonical: canonicalPath(seoLeaguePath(slug)) },
      robots: { index: false, follow: true },
    };
  }
  const league = result.data;
  return {
    title: `${league.name} fixtures, results & teams - SokaLens`,
    description: `${league.name} fixtures, recent results, and teams from SokaLens.`,
    alternates: {
      canonical: canonicalPath(seoLeaguePath(slug)),
    },
    robots: { index: Boolean(league.upcoming_matches.length > 0 || league.recent_matches.length > 0), follow: true },
  };
}

export default async function LeaguePage({ params }: LeaguePageProps) {
  const { locale } = await requestLocaleContext();
  const { slug } = await params;
  const result = await fetchLeague(slug);

  if (!result.ok) {
    if (result.error === "not_found") {
      notFound();
    }
    return <LeagueUnavailable />;
  }

  const league = result.data;
  const teams = league.teams ?? [];
  const recent = league.recent_matches ?? [];
  const upcoming = league.upcoming_matches ?? [];

  return (
    <ContentPageShell
      accent="predictions"
      crumbs={[{ href: "/leagues", label: "Leagues" }, { label: league.name }]}
      eyebrow={`${league.country || league.sport || "Competition"} \u00b7 League hub`}
      locale={locale}
      rail={
        <div className="td-panel td-panel--links">
          <h2>Keep exploring</h2>
          <LinkCard href="/leagues" label="All competitions" locale={locale} />
          <LinkCard href="/predictions/today" label="Today&rsquo;s predictions" locale={locale} />
          <LinkCard href="/best-bets" label="Best Bets" locale={locale} />
          <LinkCard href="/results" label="How we performed" locale={locale} />
        </div>
      }
      stats={[
        { label: "Upcoming", value: upcoming.length, accent: true },
        { label: "Recent results", value: recent.length },
        { label: "Teams", value: teams.length },
      ]}
      summary="Fixtures, published picks and recent results for every team in the competition."
      title={league.name}
    >
      {/*
        The favourite control sits above the fixtures rather than in the masthead:
        the design's masthead is the 96px title and nothing else, and a button
        inside it competes with the one thing the page is named after.
      */}
      <div className="lg-actions">
        <FavoriteButton
          entityId={league.slug}
          entityType="league"
          label={league.name}
          returnPath={seoLeaguePath(league.slug)}
        />
      </div>

      <div className="pf-sectionhead">
        <h2>Upcoming fixtures</h2>
      </div>
      {upcoming.length > 0 ? (
        <FixtureGroups locale={locale} matches={upcoming} />
      ) : (
        <div className="ac-empty" aria-live="polite">
          <h3>No fixtures listed right now</h3>
          <p>No fixtures are listed for this competition yet. Check back closer to kickoff.</p>
        </div>
      )}

      {recent.length > 0 ? (
        <>
          <div className="pf-sectionhead">
            <h2>Recent results</h2>
          </div>
          <ResultRows locale={locale} matches={recent} />
        </>
      ) : null}

      <div className="pf-sectionhead">
        <h2>Teams</h2>
        <span>{pluralize(teams.length, "team")}</span>
      </div>
      {teams.length > 0 ? (
        /*
          Team links as a pill row, as the design draws them. These are the only
          route from a league hub into its ~20 team pages, so they are the edge
          that makes the team tree crawlable at all -- which is the argument for
          listing every team rather than a featured few.

          A team whose slug the catalogue has not minted yet links to a search for
          its name instead of to a 404.
        */
        <ul aria-label={`Teams in ${league.name}`} className="lg-teams">
          {teams.map((team) => {
            const slugged = validCatalogSlug(team.slug);
            const href = slugged ? `/teams/${team.slug}` : `/search?q=${encodeURIComponent(team.name)}`;
            return (
              <li key={team.id}>
                <Link href={localePath(href, locale)} prefetch={false}>{team.name}</Link>
                {/*
                  The save control is a SIBLING of the anchor, not a child: a
                  button nested inside a link is invalid and steals the pill's own
                  target. Same arrangement the prediction row uses, and the same
                  reason. Without it the league hub would be the only surface
                  offering a team that cannot save one.
                */}
                {slugged ? (
                  <FavoriteButton
                    compact
                    entityId={team.slug}
                    entityType="team"
                    hideForGuests
                    label={team.name}
                    returnPath={href}
                  />
                ) : null}
              </li>
            );
          })}
        </ul>
      ) : (
        <div className="ac-empty" aria-live="polite">
          <h3>No teams listed yet</h3>
          <p>No teams are listed for this competition yet.</p>
        </div>
      )}
    </ContentPageShell>
  );
}
