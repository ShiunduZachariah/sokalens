import Link from "next/link";
import { cookies } from "next/headers";

import { ContentPageShell } from "../components/ContentPageShell";
import { InPlayFilters } from "../components/inplay/InPlayFilters";
import { InPlayBoard } from "../components/InPlayBoard";
import { LinkCard } from "../components/LinkCard";
import { LiveRefresh } from "../components/LiveRefresh";
import { MatchupIdentity } from "../components/MatchupIdentity";
import { ViewerTimezone } from "../components/ViewerTimezone";
import { ChevronRightIcon } from "../components/match/icons";
import { fetchMatches } from "../data/liveMatches";
import { fixtureLifecycle, isLiveFixture } from "../lib/fixtureLifecycle";
import { kickoffCountdown, startingSoon } from "../lib/kickoffCountdown";
import { seoFixturePath } from "../lib/seo";
import { validViewerTimezone } from "../lib/viewerTimezone";
import { canonicalPath } from "../site";
import { localePath } from "../i18n/config";
import { requestLocaleContext } from "../i18n/server";

export const metadata = {
  title: "In-play football matches - SokaLens",
  description:
    "Every football match being played right now, with the live clock and score.",
  alternates: {
    canonical: canonicalPath("/in-play"),
  },
};

/**
 * Re-rendered on every request rather than cached.
 *
 * A board of live scores is the one page where a cached copy is actively
 * misleading: the underlying match data already revalidates every 15 seconds,
 * and serving a statically rendered shell around it would pin the board to
 * whenever the build ran.
 */
export const dynamic = "force-dynamic";

export default async function InPlayPage() {
  const { locale } = await requestLocaleContext();
  const cookieStore = await cookies();
  const timezone = validViewerTimezone(
    cookieStore.get("viewer_timezone")?.value,
  );

  // The provider reports in-play state on today's fixtures, so today's feed is
  // the whole population. Filtering here rather than asking the API for
  // status=live keeps one definition of "live" -- the shared lifecycle helper --
  // instead of a second list of status strings that can drift from it.
  // Two calls, not one. `/v1/matches/today` returns upcoming and in-play
  // fixtures and leaves finished ones out entirely: on a Saturday with 333
  // finished matches the first call returned none at all, so filtering its
  // result for "settled" could only ever produce an empty section. Finished
  // fixtures have to be asked for by status.
  const [matches, finishedMatches] = await Promise.all([
    fetchMatches({
      day: "today",
      timezone,
      limit: 200,
      includeUnavailable: true,
    }),
    fetchMatches({
      day: "today",
      timezone,
      limit: 60,
      includeUnavailable: true,
      statuses: ["finished"],
    }).catch(() => [] as Awaited<ReturnType<typeof fetchMatches>>),
  ]);
  const now = new Date();
  // A live status is necessary but not sufficient. Production holds fixtures
  // marked `second_half` whose kickoff is two months out -- 2562106 was one,
  // reading "3-2, 90'" for a match that has not been played. A status that
  // disagrees with the calendar is a stale provider row, and putting it on a
  // board headed "matches on the pitch right now" makes the page say something
  // plainly untrue. Requiring the kickoff to have passed costs nothing real: a
  // match cannot be in its second half before it starts.
  const liveMatches = matches.filter(
    (match) =>
      isLiveFixture(match.status) &&
      Date.parse(match.kickoffAt) <= now.getTime(),
  );
  // When something is live, "what starts next" means the next hour -- anything
  // further out competes with a match already on. When nothing is live, that
  // hour is often empty and the page has nothing on it at all, which is what
  // made it look broken. So the window opens to the rest of the day: a board
  // with no live matches should still answer "what is there to watch".
  const soon = liveMatches.length > 0
    ? startingSoon(matches, { now })
    : startingSoon(matches, { now, withinMinutes: 24 * 60 });
  /*
   * Today's finished matches, newest first.
   *
   * The board answers "what is on now" and "what starts next", and the design
   * closes the day with "what already happened". Without it the page loses
   * every match the moment it ends -- a reader who opened a game at 70' and
   * came back after full time found it had simply vanished, with nothing
   * saying it had finished.
   *
   * `fixtureLifecycle` is the same predicate the rest of the site settles on,
   * so a match counted as finished here is finished everywhere.
   */
  const finished = finishedMatches
    .filter((match) => fixtureLifecycle(match, now) === "settled")
    .sort((left, right) => Date.parse(right.kickoffAt) - Date.parse(left.kickoffAt))
    .slice(0, 24);

  return (
    <ContentPageShell
      accent="football"
      beforeBody={
        <>
          <ViewerTimezone timezone={timezone} />
          {/* The board is live by definition, so it always polls. */}
          <LiveRefresh />
        </>
      }
      crumbs={[{ label: "In-play" }]}
      eyebrow={
        <span className="ip-live">
          <span aria-hidden="true" className="ip-live__dot" />
          {liveMatches.length} live now · updates every 15 seconds
        </span>
      }
      locale={locale}
      rail={
        <div className="td-panel td-panel--links">
          <h2>Keep exploring</h2>
          {/* The 2026-10-02 canvas replaced this rail's dead `href="#"` links with
              Best Bets, Leagues and Performance. Today's fixture list is kept on
              top of those three: it is the page a reader lands on when nothing is
              live, and the empty state below links it too. */}
          <LinkCard href="/matches/today" label="Today&rsquo;s full fixture list" locale={locale} />
          <LinkCard href="/best-bets" label="Best Bets" locale={locale} />
          <LinkCard href="/leagues" label="Browse by league" locale={locale} />
          <LinkCard href="/results" label="How we performed" locale={locale} />
        </div>
      }
      stats={[
        { label: "In play", value: liveMatches.length, accent: true },
        { label: "Competitions", value: new Set(liveMatches.map((match) => match.leagueName)).size },
        { label: "Starting soon", value: soon.length },
        { label: "Finished today", value: finished.length },
      ]}
      summary="Matches on the pitch right now, with the clock and score as the provider reports them. Open any match for the full analysis."
      title="In-play"
    >
      {/*
        The design frames this as the page's central honesty note rather than a
        footnote: a board of live scores sitting next to a pre-match pick invites
        the reading that the pick is being re-modelled from the score. It is not,
        and it never is.

        The design also tracks each pick against the live score as On track / At
        risk / Won / Lost, with a tally above the board. That is not implemented:
        it needs a market-aware evaluator that can settle a partial score against
        a selection, which does not exist here, and the data contract the handoff
        refers to is not in this repository. Showing the states from a guess would
        be worse than not showing them.
      */}
      <p className="ip-note">
        <svg aria-hidden="true" fill="none" height="16" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24" width="16">
          <circle cx="12" cy="12" r="9" />
          <path d="M12 8h.01M11 12h1v5h1" />
        </svg>
        <span>
          Predictions on match pages are generated before kick-off and never changed. They are not
          re-modelled from the live score.
        </span>
      </p>

      <InPlayFilters counts={{ live: liveMatches.length, soon: soon.length, finished: finished.length }}>
        <section data-section="live">
          {/* The design names the board rather than leaving it to float under the
              note. Without a heading the first thing on the page is a list with
              no title, and the tally has nowhere to live. */}
          <div className="pf-sectionhead">
            <h2>Live now</h2>
            <span>
              {liveMatches.length === 0
                ? "nothing on the pitch"
                : `${liveMatches.length} ${liveMatches.length === 1 ? "match" : "matches"}`}
            </span>
          </div>
          <InPlayBoard locale={locale} matches={liveMatches} />
        </section>

        <section data-section="soon">

      {/*
        Kicking off within the hour. This was a bare heading over a single
        sentence, which read as a section that had failed to load -- and it was
        the only thing under an empty board, so a quiet evening looked like a
        broken page. What a reader wants next is what is about to start, soonest
        first.
      */}
      <div className="pf-sectionhead">
        <h2>Starting soon</h2>
        <span>
          {soon.length === 0
            ? "nothing else today"
            : liveMatches.length > 0
              ? "within the hour"
              : "today\u2019s remaining kick-offs"}
        </span>
      </div>
      {soon.length > 0 ? (
        <ol className="ip-soon">
          {soon.map((match) => (
            <li key={match.id}>
              {/*
                The whole row is the link. Someone scanning what is about to
                start wants the match, and making them find a small target
                inside the row is a worse answer than making the row the target.
              */}
              <Link href={localePath(seoFixturePath(match), locale)}>
                <span className="ip-soon__countdown">{kickoffCountdown(match.kickoffAt, now)}</span>
                {/*
                  Stacked with a crest each, at 20px -- in-play.html draws the
                  starting-soon pair on two lines at 1.4em of 15px, the same
                  block as a live row above it. Inline "Home v Away" was the
                  UI-05 defect: one ellipsis across both names.
                */}
                <MatchupIdentity
                  awayTeam={match.awayTeam}
                  awayTeamLogo={match.awayTeamLogo}
                  className="ip-soon__teams"
                  homeTeam={match.homeTeam}
                  homeTeamLogo={match.homeTeamLogo}
                  layout="stacked"
                  size={20}
                />
                <span className="ip-soon__league">{match.leagueName}</span>
                <span aria-hidden="true" className="lg-card__chevron"><ChevronRightIcon /></span>
              </Link>
            </li>
          ))}
        </ol>
      ) : (
        <div className="ac-empty" aria-live="polite">
          <h3>Nothing else is scheduled today</h3>
          <p>
            <Link href={localePath("/matches/today", locale)}>Today&apos;s full fixture list</Link> has
            everything else, including kick-off times and prediction availability.
          </p>
        </div>
      )}
        </section>

        <section data-section="finished">
        {finished.length > 0 ? (
        <>
          <div className="pf-sectionhead">
            <h2>Finished today</h2>
            <Link href={localePath("/results", locale)}>How we performed</Link>
          </div>
          <ol className="ip-soon ip-finished">
            {finished.map((match) => (
              <li key={match.id}>
                <Link href={localePath(seoFixturePath(match), locale)}>
                  <span className="ip-finished__ft">FT</span>
                  {/* Same block as the live and starting-soon rows: a finished
                      row is the same fixture, later. */}
                  <MatchupIdentity
                    awayTeam={match.awayTeam}
                    awayTeamLogo={match.awayTeamLogo}
                    className="ip-soon__teams"
                    homeTeam={match.homeTeam}
                    homeTeamLogo={match.homeTeamLogo}
                    layout="stacked"
                    size={20}
                  />
                  <span className="ip-soon__league">{match.leagueName}</span>
                  <span aria-hidden="true" className="lg-card__chevron"><ChevronRightIcon /></span>
                </Link>
              </li>
            ))}
          </ol>
        </>
        ) : null}
        </section>
      </InPlayFilters>
    </ContentPageShell>
  );
}
