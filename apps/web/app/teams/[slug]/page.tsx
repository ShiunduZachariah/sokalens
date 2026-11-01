import Link from "next/link";
import { notFound, permanentRedirect } from "next/navigation";
import { ContentPageShell } from "../../components/ContentPageShell";
import { FixtureGroups, ResultRows } from "../../components/FixtureRows";
import { LinkCard } from "../../components/LinkCard";
import { ResponsibleNote } from "../../components/ResponsibleNote";
import { FavoriteButton } from "../../components/account/FavoriteButton";
import { fetchTeam } from "../../data/catalog";
import { pluralize } from "../../lib/format";
import { canonicalPath } from "../../site";
import { formatAverage, formatRate, isIndexableTeam, teamForm } from "../../lib/teamDepth";
import { seoLeaguePath } from "../../lib/seo";
import { requestLocaleContext } from "../../i18n/server";
import { localePath } from "../../i18n/config";
import { legacyTeamCanonicalSlug } from "../../lib/legacyTeamSlugs";

type TeamPageProps = {
  params: Promise<{ slug: string }>;
};

function TeamUnavailable() {
  return (
    <main className="page-shell" id="content">
      <div className="main-column">
        <section className="state-card" role="alert">
          <h1 className="page-title">Team data is temporarily unavailable</h1>
          <p className="muted">
            This team&apos;s fixture history could not be loaded right now. Try again in a moment,
            or browse the current fixture schedule instead.
          </p>
          <p className="section-footer-action">
            <Link className="button button--secondary" href="/matches/today">
              View today&apos;s matches
            </Link>{" "}
            <Link className="button" href="/leagues">
              Browse leagues
            </Link>
          </p>
        </section>
        <ResponsibleNote compact />
      </div>
    </main>
  );
}

export async function generateMetadata({ params }: TeamPageProps) {
  const { slug } = await params;
  const result = await fetchTeam(legacyTeamCanonicalSlug(slug));
  if (!result.ok) {
    if (result.error === "not_found") notFound();
    return {
      title: "Team data temporarily unavailable - SokaLens",
      description: "Team data is temporarily unavailable.",
      alternates: { canonical: canonicalPath(`/teams/${slug}`) },
      robots: { index: false, follow: true },
    };
  }
  const team = result.data;
  const form = teamForm(team);
  const indexable = isIndexableTeam(form);
  // A description built from this club's own numbers, not a template with the
  // name swapped in. It is also what a searcher sees in the result, and the
  // reason to click it rather than the next of 414 identical entries.
  const description = indexable
    ? `${team.name} form: ${form.won}W ${form.drawn}D ${form.lost}L from the last ${form.matches}, ${formatAverage(form.goalsFor, form.matches)} goals scored and ${formatAverage(form.goalsAgainst, form.matches)} conceded per match. Fixtures, results and published predictions.`
    : `${team.name} fixtures, recent results, and upcoming matches from SokaLens.`;
  return {
    title: `${team.name} fixtures and predictions - SokaLens`,
    description,
    alternates: {
      canonical: canonicalPath(`/teams/${team.slug}`),
    },
    // Team pages produced 3 clicks from 2,206 impressions. A page with no
    // settled result cannot compute anything specific to the club, so it stays
    // out of the index until it can -- while still being followed, because it
    // remains a crawl path to the fixtures it links.
    //
    // This said "fewer than five settled results" until 2026-09-22.
    // MINIMUM_INDEXABLE_MATCHES has been 1 for some time, and the gap mattered:
    // the worker's sweep has to ask the same question this page does, and a
    // comment claiming a threshold of five is what someone reads before
    // writing the query on the other side. It already went wrong once in the
    // other direction -- the sweep accepted any past kickoff while this counts
    // only settled results, and 80 clubs were advertised in the sitemap and
    // then noindexed by this line.
    robots: { index: indexable, follow: true },
  };
}

export default async function TeamPage({ params }: TeamPageProps) {
  const { locale } = await requestLocaleContext();
  const { slug } = await params;
  const result = await fetchTeam(legacyTeamCanonicalSlug(slug));

  if (!result.ok) {
    if (result.error === "not_found") {
      notFound();
    }
    return <TeamUnavailable />;
  }

  const team = result.data;
  // Only the catalog's approved alias resolution can nominate a replacement.
  if (team.slug !== slug) permanentRedirect(localePath(`/teams/${team.slug}`, locale));
  const form = teamForm(team);
  const indexable = isIndexableTeam(form);

  return (
    <ContentPageShell
      accent="fixtures"
      crumbs={[
        { href: "/leagues", label: "Leagues" },
        /*
          Linked only when the API resolved a slug. The crumb has always shown
          the competition's name; without the slug it was dead text, so the one
          obvious journey off a team page -- into the league it plays in -- went
          nowhere. League URLs are slug-addressed and the display name is not
          unique ("Second League" exists in five countries), so there was no way
          to build the href from what the page already had.
        */
        team.league_slug
          ? { href: seoLeaguePath(team.league_slug), label: team.league_name }
          : { label: team.league_name || team.country || "Team" },
        { label: team.name },
      ]}
      eyebrow={`${team.country || team.league_name || "Club"} \u00b7 Team`}
      locale={locale}
      rail={
        <div className="td-panel td-panel--links">
          <h2>Keep exploring</h2>
          <LinkCard href="/predictions/today" label="Today&rsquo;s predictions" locale={locale} />
          <LinkCard href="/leagues" label="Browse by league" locale={locale} />
          <LinkCard href="/results" label="How we performed" locale={locale} />
        </div>
      }
      /*
        The stat strip carries this club's own record rather than a count of rows
        on the page. It is the only thing distinguishing this page from the next
        413, which is the same reason the metadata description is built from these
        figures -- and why a club with nothing settled is left out of the index
        rather than shipped as a template with the name swapped in.
      */
      stats={indexable ? [
        { label: "Record", value: `${form.won}W ${form.drawn}D ${form.lost}L` },
        { label: "Win rate", value: formatRate(form.won, form.matches), accent: true },
        { label: "Scored / match", value: formatAverage(form.goalsFor, form.matches) },
        { label: "Conceded / match", value: formatAverage(form.goalsAgainst, form.matches) },
      ] : undefined}
      summary={
        team.league_name
          ? `Fixtures, recent results and published predictions \u2014 ${team.league_name}.`
          : "Fixtures, recent results and published predictions."
      }
      title={team.name}
    >
      <div className="lg-actions">
        <FavoriteButton
          entityId={team.slug}
          entityType="team"
          label={team.name}
          returnPath={`/teams/${team.slug}`}
        />
      </div>

      {indexable ? (
        <>
          <div className="pf-sectionhead">
            <h2>Season profile</h2>
            <span>last {form.matches} matches</span>
          </div>
          <ul className="tm-tiles">
            <li><span>Clean sheets</span><strong>{formatRate(form.cleanSheets, form.matches)}</strong></li>
            <li><span>Both teams scored</span><strong>{formatRate(form.bothTeamsScored, form.matches)}</strong></li>
            <li><span>Over 2.5 goals</span><strong>{formatRate(form.overTwoFive, form.matches)}</strong></li>
          </ul>
          <p className="tm-note">
            Computed from settled results in the SokaLens database, not editorial estimates.
          </p>
        </>
      ) : null}

      {team.next_match ? (
        <>
          <div className="pf-sectionhead">
            <h2>Next fixture</h2>
          </div>
          <FixtureGroups locale={locale} matches={[team.next_match]} />
        </>
      ) : null}

      <div className="pf-sectionhead">
        <h2>Recent results</h2>
        <span>{pluralize(team.recent_matches.length, "match")}</span>
      </div>
      {team.recent_matches.length > 0 ? (
        <ResultRows locale={locale} matches={team.recent_matches} />
      ) : (
        <div className="ac-empty" aria-live="polite">
          <h3>No recent results yet</h3>
          <p>
            Nothing has settled for this club in the SokaLens database yet. Its fixtures still
            appear above as they are published.
          </p>
        </div>
      )}
    </ContentPageShell>
  );
}
