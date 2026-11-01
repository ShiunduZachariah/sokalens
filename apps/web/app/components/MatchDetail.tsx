import Link from "next/link";
import { AdSlot } from "./AdSlot";
import { FeedAdBlock } from "./FeedAdBlock";
import { FixtureLinkMesh } from "./FixtureLinkMesh";
import { OddsStrip } from "./OddsStrip";
import { ResponsibleNote } from "./ResponsibleNote";
import { ShareButton } from "./ShareButton";
import { FavoriteButton } from "./account/FavoriteButton";
import { MatchPredictionCard } from "../matches/[id]/MatchPredictionCard";
import { MatchTimeline } from "./MatchTimeline";
import { TeamCrest } from "./TeamCrest";
import { MatchStats } from "./match/MatchStats";
import { WinProbabilityPanel } from "./inplay/WinProbabilityPanel";
import { MatchTabs } from "./MatchTabs";
import { TrackerTally } from "./TrackerTally";
import { InPlayGate } from "./InPlayGate";
import { InPlayMarkets, type MatchPhase } from "./inplay/InPlayMarkets";
import { LiveRefresh } from "./LiveRefresh";
import { AddToSlipButton } from "./match/AddToSlipButton";
import { FormPanel } from "./match/FormPanel";
import { GoalsProfile } from "./match/GoalsProfile";
import { ArrowRightIcon, PitchLines } from "./match/icons";
import { matchClockLabel, matchScore, matchStatusLabel, type LiveMatch } from "../data/liveMatches";
import { isLiveFixture } from "../lib/fixtureLifecycle";
import { isFinishedStatus } from "../lib/matchStatus";
import { ScoreFigure, StatusPill } from "./match/Scoreboard";
import { halfTimeLabel, scorersFor } from "../lib/scoreboard";
import { marketLabel, marketSelectionLabel } from "../data/livePredictions";
import { pluralize } from "../lib/format";
import { fixtureHref } from "../lib/fixtureHref";
import { localePath } from "../i18n/config";
import { predictionLabel, translate } from "../i18n/catalogs";
import { requestLocaleContext } from "../i18n/server";
import { leaguePredictionPath, teamPath } from "../lib/catalogSlug";
import { hasImage, imageSrc } from "../lib/imageSrc";
import { LocalTime } from "./LocalTime";
import { MatchPreview } from "./MatchPreview";
import { RelatedFixtures } from "./RelatedFixtures";
import { fetchLeague, fetchTeam, type TeamDetail } from "../data/catalog";
import { leagueSlugForName } from "../data/leagueLookup";
import { buildMatchPreview, marketReasons } from "../lib/matchPreview";
import { relatedFixtures } from "../lib/relatedFixtures";
import { teamForm } from "../lib/teamDepth";

function initials(team: string) {
  const words = team.trim().split(/\s+/).filter(Boolean);
  return words.slice(0, 3).map((word) => word[0]).join("").toUpperCase() || "FC";
}

function unavailablePredictionCopy(match: LiveMatch) {
  switch (match.predictionStateReason) {
    case "earlier_fixture_pending":
      return "This analysis is temporarily held because one of the teams has an earlier unfinished fixture. It will refresh automatically after that result is final.";
    case "backfill_pending":
      return "The engine is filling the remaining team history in the background and will publish automatically when the quality gate clears.";
    case "PROVIDER_CONFIRMED_NO_HISTORY":
      return "The available providers could not supply enough reliable match history for this fixture.";
    default:
      return "Match details are available, but no prediction has been published for this fixture yet.";
  }
}

/**
 * The team's figures from before this fixture. A team feed read two weeks
 * later also holds matches played since, and a preview of this fixture must
 * not quote results it could not have known.
 */
function statsBefore(team: TeamDetail | null, match: LiveMatch) {
  if (!team) return null;
  const kickoff = Date.parse(match.kickoffAt);
  const earlier = team.recent_matches.filter((item) => item.id !== match.id
    && (Number.isNaN(kickoff) || Date.parse(item.kickoff_at) < kickoff));
  return teamForm({ name: team.name, recent_matches: earlier });
}

async function loadFixtureContext(match: LiveMatch) {
  const leagueSlug = match.leagueSlug || await leagueSlugForName(match.leagueName);
  // Each read is optional and cached for a minute by the data layer; a
  // failure costs the section that needed it, never the page.
  const [home, away, league] = await Promise.all([
    match.homeTeamSlug ? fetchTeam(match.homeTeamSlug) : null,
    match.awayTeamSlug ? fetchTeam(match.awayTeamSlug) : null,
    leagueSlug ? fetchLeague(leagueSlug) : null,
  ]);
  return {
    home: home?.ok ? home.data : null,
    away: away?.ok ? away.data : null,
    league: league?.ok ? league.data : null,
    leagueSlug,
  };
}

function TeamColumn({
  away = false,
  href,
  logo,
  name,
  scorers,
}: { away?: boolean; href: string; logo: string; name: string; scorers?: string }) {
  const body = (
    <>
      <span className="mp-team__crest">
        {hasImage(logo) ? (
          // eslint-disable-next-line @next/next/no-img-element
          <img alt="" decoding="async" height={112} loading="lazy" src={imageSrc(logo)} width={112} />
        ) : (
          initials(name)
        )}
      </span>
      <span className="mp-team__name">{name}</span>
      {/*
        Scorers replace the HOME/AWAY label once there are any. The label is
        orientation a reader needs before kick-off and stops needing the moment the
        column carries names and minutes, and the design gives this slot a fixed
        min-height so swapping one for the other does not move the scoreline.
      */}
      <span className="mp-team__side">{scorers || (away ? "AWAY" : "HOME")}</span>
    </>
  );
  const className = away ? "mp-team mp-team--away" : "mp-team";
  return href
    ? <Link className={className} href={href}>{body}</Link>
    : <span className={className}>{body}</span>;
}

/**
 * The fixture page.
 *
 * Rebuilt to docs/ai/designs/2026-09-27-sokalens-ui-refresh/Main.dc.html. The
 * artboards are flat -- hero, then numbered sections -- rather than the sub-nav
 * of tabs this page used to lead with, which is also the better shape for a
 * crawler: the markets and the numbers are now in the first response with no
 * interaction in front of them.
 *
 * The tabs are kept for one job the artboards do not cover, because every board
 * draws an upcoming fixture: the timeline and the in-play panel of a live or
 * finished match, which are alternative views of the same moment rather than
 * two more sections to scroll past. MatchTabs still keeps every panel mounted
 * and merely hidden, which is the invariant MatchTabs.test.mjs guards.
 *
 * Everything the mockups leave out is deliberately still here: ad slots, the
 * odds strip, localisation, favourites, void reasons and live refresh. Those
 * are omissions in a picture, not deletions in a spec.
 */
export async function MatchDetail({ match }: { match: LiveMatch }) {
  const { locale } = await requestLocaleContext();
  const context = await loadFixtureContext(match);
  const homeStats = statsBefore(context.home, match);
  const awayStats = statsBefore(context.away, match);

  const previewInput = {
    id: match.id,
    homeTeam: match.homeTeam,
    awayTeam: match.awayTeam,
    homeForm: match.homeForm,
    awayForm: match.awayForm,
    homeStats,
    awayStats,
    prediction: match.prediction ?? match.predictions[0] ?? null,
    predictions: match.predictions,
    odds: match.odds,
    homeDiscipline: match.discipline?.home ?? null,
    awayDiscipline: match.discipline?.away ?? null,
  };

  // English only: the sentence bank is not translated, and English prose on a
  // localized page would be a worse page, not a deeper one.
  const preview = locale === "en" ? buildMatchPreview(previewInput) : null;
  const reasons = locale === "en" ? marketReasons(previewInput) : new Map<string, string>();

  const related = relatedFixtures({
    matchId: match.id,
    homeNext: context.home?.next_match,
    awayNext: context.away?.next_match,
    leagueUpcoming: context.league?.upcoming_matches,
  });

  const prediction = match.prediction ?? match.predictions[0] ?? null;
  const providerFallback = prediction?.source === "provider";
  const selection = prediction ? predictionLabel(locale, prediction.marketKey, prediction.selection) : "";
  const readableSelection = prediction && selection === prediction.selection
    ? marketSelectionLabel(prediction.marketKey, prediction.selection, locale)
    : selection;
  const topMarketKey = prediction ? prediction.marketKey.trim().toLowerCase() : "";

  const homeHref = teamPath(match.homeTeamSlug);
  const awayHref = teamPath(match.awayTeamSlug);
  const leagueHref = leaguePredictionPath(context.leagueSlug);
  const matchTitle = `${match.homeTeam} vs ${match.awayTeam}`;
  // Shared with the in-play board and the today filter, so a match cannot be
  // live on one surface and not on another.
  const live = isLiveFixture(match.status);
  /*
   * The design's three readings of the in-play surface.
   *
   * `prematch` is the one this page used to have no answer for: the section was
   * rendered only for a live or finished fixture, so a reader two hours before
   * kick-off saw no in-play tab at all, and the markets component -- when it did
   * render -- told them no markets were "published yet". The canvas says what is
   * actually true: live markets open at kick-off.
   */
  const phase: MatchPhase = live ? "live" : isFinishedStatus(match.status) ? "finished" : "prematch";
  // Terminal for listing purposes does not mean a final whistle was observed.
  const inPlayUnavailable = isFinishedStatus(match.status) && match.status !== "finished";
  const halfTime = halfTimeLabel(match.timeline, phase);
  const score = matchScore(match);
  const confidence = prediction ? Math.round(prediction.confidence) : 0;

  // The hero's headline note, and the prose block further down, come from the
  // same builder; taking the model's paragraph avoids writing a second voice.
  const heroNote = preview?.find((section) => section.heading === "The model's view")?.sentences.join(" ") ?? "";

  return (
    <div className="mp mp-page">
      {/* Live pages re-render themselves; finished ones never poll. */}
      <LiveRefresh enabled={live} />

      <main className="mp__inner" id="content">
        <nav aria-label="Breadcrumb" className="mp-crumbs">
          <Link href={localePath("/predictions/today", locale)}>
            {translate(locale, "nav.predictions")}
          </Link>
          <span aria-hidden="true">/</span>
          {leagueHref ? (
            <Link href={localePath(leagueHref, locale)}>{match.leagueName || "Football"}</Link>
          ) : (
            <span>{match.leagueName || "Football"}</span>
          )}
          <span aria-hidden="true">/</span>
          <span className="mp-crumbs__here">{matchTitle}</span>
        </nav>

        {/*
          Ad spec 3: the top slot sits directly under the site header, EXCEPT
          on a live match, where it goes below the scoreboard "so the score
          stays at the top of the screen". A reader checking a live score
          should not have to scroll past an advert to find it.

          Positioned by match phase on the server rather than by viewport. That
          is a deliberate trade which over-applies the exception slightly --
          the spec scopes it to mobile and this moves the slot on a live
          desktop page too -- because both alternatives are worse. Rendering
          the slot twice and hiding one by media query is not an option: a
          hidden slot still requests an ad, so it would spend an impression to
          show nothing. Reordering with flex would mean making `.mp__inner` a
          flex column at mobile, and its children carry their own margins --
          margins do not collapse between flex items, so every gap on the
          mobile match page would shift to move one advert. On desktop there is
          room for the scoreboard and the slot either way, so over-applying
          costs little and the other two risk a lot.
        */}
        {phase === "prematch" ? <AdSlot format="top" placement="page-top" /> : null}

        <section className="mp-hero">
          <PitchLines />

          {/*
            League and date only. The status chip that used to sit here is gone:
            the scoreboard's own pill carries it now, and two status indicators a
            few pixels apart invite the reader to check whether they agree. The
            full date stays, because the big figure below shows a time only before
            kick-off and a score after it.
          */}
          <div className="mp-hero__meta">
            <span>{match.leagueName || "Football"}</span>
            <span aria-hidden="true">·</span>
            <span><LocalTime value={match.kickoffAt} /></span>
          </div>

          {/*
            The visible team names are the h1's own words, styled far larger
            than a heading normally is, so the accessible heading is kept as
            one sr-only element rather than splitting it across three columns.
          */}
          <h1 className="sr-only">{matchTitle}</h1>

          <div className="mp-teams">
            <TeamColumn
              href={homeHref ? localePath(homeHref, locale) : ""}
              logo={match.homeTeamLogo}
              name={match.homeTeam}
              scorers={scorersFor(match.timeline, "home")}
            />
            <div className="mp-kickoff">
              <ScoreFigure kickoffAt={match.kickoffAt} phase={phase} score={score} />
              <StatusPill
                label={live ? matchClockLabel(match) : matchStatusLabel(match)}
                phase={phase}
              />
              {halfTime ? <span className="mp-kickoff__ht">{halfTime}</span> : null}
              {match.venueName ? (
                <span className="mp-kickoff__label">{match.venueName}</span>
              ) : null}
            </div>
            <TeamColumn
              away
              href={awayHref ? localePath(awayHref, locale) : ""}
              logo={match.awayTeamLogo}
              name={match.awayTeam}
              scorers={scorersFor(match.timeline, "away")}
            />
          </div>

          <div className="mp-pick">
            <div className="mp-pick__lede">
              <span className="mp-eyebrow">
                {prediction
                  ? `${providerFallback ? "Provider pre-match estimate" : "SokaLens pick"} · ${marketLabel(prediction.marketKey, locale)}`
                  : translate(locale, "match.prediction")}
              </span>
              <div className="mp-pick__headline">
                <h2 className="mp-pick__value">
                  {prediction ? readableSelection : translate(locale, "match.no_prediction")}
                </h2>
                {prediction ? <span className="mp-pick__pct">{confidence}%</span> : null}
              </div>
            </div>

            <div className="mp-pick__body">
              {prediction ? (
                <div className="mp-bar">
                  <div
                    aria-label={`${providerFallback ? "Provider estimate" : "Model confidence"} ${confidence}%`}
                    aria-valuemax={100}
                    aria-valuemin={0}
                    aria-valuenow={confidence}
                    className="mp-bar__track"
                    role="meter"
                  >
                    <div className="mp-bar__fill" style={{ width: `${confidence}%` }} />
                    {/* The halfway mark, so 44% cannot read as "nearly there". */}
                    <div className="mp-bar__tick" />
                  </div>
                  <div aria-hidden="true" className="mp-bar__scale">
                    <span>0%</span>
                    <span>50%</span>
                    <span>100%</span>
                  </div>
                </div>
              ) : null}
              <p className="mp-pick__note">
                {prediction
                  ? providerFallback ? "Provider pre-match estimate. SokaLens has not published an engine prediction for this fixture." : heroNote || translate(locale, "match.current_selection", {
                      market: marketLabel(prediction.marketKey, locale),
                    })
                  : unavailablePredictionCopy(match)}
              </p>
            </div>

            <div className="mp-pick__actions">
              {prediction && !providerFallback ? (
                <AddToSlipButton
                  className="mp-btn mp-btn--primary"
                  target={{
                    confidence: prediction.confidence,
                    kickoffAt: match.kickoffAt,
                    marketKey: prediction.marketKey,
                    matchId: match.id,
                    matchStatus: match.status,
                    matchTitle,
                    homeTeam: match.homeTeam,
                    awayTeam: match.awayTeam,
                    homeTeamLogo: match.homeTeamLogo,
                    awayTeamLogo: match.awayTeamLogo,
                    predictionId: prediction.id,
                    selection: prediction.selection,
                  }}
                />
              ) : null}
              <FavoriteButton
                entityId={String(match.id)}
                entityType="match"
                label={matchTitle}
                returnPath={fixtureHref(match)}
              />
              <ShareButton
                entityId={String(match.id)}
                entityType="match"
                path={fixtureHref(match)}
                text={`${matchTitle} on SokaLens`}
                title={matchTitle}
              />
            </div>
          </div>
        </section>

        {/* The live/finished position of the top slot: below the scoreboard,
            so the score is the first thing on screen. See the note above. */}
        {phase === "prematch" ? null : <AdSlot format="top" placement="page-top" />}

        {/* 01 — every published market. Flat, as drawn: no interaction in
            front of the page's main content. */}
        <section aria-labelledby="markets-title" className="mp-section" id="all-predictions">
          <div className="mp-section__head">
            <div>
              <span className="mp-section__num">01 — MARKETS</span>
              <h2 className="mp-h2" id="markets-title">
                {match.predictions.length > 0 ? "Every market, one read" : translate(locale, "match.all_predictions")}
              </h2>
            </div>
            {match.predictions.length > 0 ? (
              <span className="mp-section__aside">
                {providerFallback
                  ? `${pluralize(match.predictions.length, "provider estimate")} · pre-match only`
                  : `${pluralize(match.predictions.length, "prediction")} · confidence = model probability`}
              </span>
            ) : null}
          </div>

          {match.predictions.length > 0 ? (
            <div className="mp-markets">
              {match.predictions.map((item) => (
                <MatchPredictionCard
                  kickoffAt={match.kickoffAt}
                  key={`${item.source}-${item.id}-${item.marketKey}`}
                  matchId={match.id}
                  matchStatus={match.status}
                  matchTitle={matchTitle}
                  awayTeam={match.awayTeam}
                  awayTeamLogo={match.awayTeamLogo}
                  homeTeam={match.homeTeam}
                  homeTeamLogo={match.homeTeamLogo}
                  prediction={item}
                  reason={reasons.get(item.marketKey.trim().toLowerCase())}
                  top={item.marketKey.trim().toLowerCase() === topMarketKey}
                />
              ))}
            </div>
          ) : (
            <div className="mp-state">
              <h3>{translate(locale, "match.no_prediction")}</h3>
              <p>{unavailablePredictionCopy(match)}</p>
            </div>
          )}
        </section>

        {/*
          The match page's one in-feed unit. Ad spec 3.1: "After Markets,
          before Form & goals". It used to sit much further down, below the
          onward-journey section, where it was the last thing on the page rather
          than a break in the content.
        */}
        <FeedAdBlock />

        {/* 02 — the numbers. */}
        <section aria-labelledby="numbers-title" className="mp-section" id="team-analysis">
          <div className="mp-section__head">
            <div>
              <span className="mp-section__num">02 — THE NUMBERS</span>
              <h2 className="mp-h2" id="numbers-title">Form &amp; goals</h2>
            </div>
          </div>
          <div className="mp-grid-two">
            <FormPanel
              away={{ form: match.awayForm, name: match.awayTeam, slug: match.awayTeamSlug }}
              home={{ form: match.homeForm, name: match.homeTeam, slug: match.homeTeamSlug }}
              locale={locale}
            />
            <GoalsProfile
              away={{ name: match.awayTeam, stats: awayStats }}
              home={{ name: match.homeTeam, stats: homeStats }}
            />
          </div>
        </section>

        {/*
          Rendered in all three phases. Before kick-off it explains what opens at
          kick-off, during the match it is the live board, and after full time it is
          the final read -- the design gives each its own voice. The timeline tab
          still appears only when there is a timeline to show.
        */}
        <section aria-labelledby="in-play-title" className="mp-section" id="in-play">
          <div className="mp-section__head">
            <div>
              <span className="mp-section__num">
                {inPlayUnavailable ? "STATUS" : live ? "LIVE" : phase === "finished" ? "SUMMARY" : "FROM KICK-OFF"}
              </span>
              <h2 className="mp-h2" id="in-play-title">
                {inPlayUnavailable || live ? "In-play" : phase === "finished" ? "How it unfolded" : "In-play, from kick-off"}
              </h2>
            </div>
            <span className="mp-section__aside">{matchStatusLabel(match)}</span>
          </div>
          {/*
            How the pre-match picks are standing, from the engine's own
            tracker. Outside the gate on purpose: `tracker_state` rides the
            ordinary match payload, not `/v1/inplay/`, and the same states are
            already on the market cards above.
          */}
          <TrackerTally predictions={match.predictions} />
          <MatchTabs
            initialTabId={phase === "finished" && match.timeline.length > 0 ? "summary" : "in-play"}
            tabs={[
              // Live matches get the tab even with nothing in it yet, so the
              // timeline does not appear from nowhere on the first goal.
              ...(match.timeline.length > 0 || live
                ? [{
                    id: "summary",
                    // The design's own label. "Timeline" described the
                    // content; "Summary" is what the tab is called in the
                    // export, and it is the tab a reader lands on.
                    label: "Summary",
                    content: (
                      <MatchTimeline
                        awayTeam={match.awayTeam}
                        events={match.timeline}
                        homeTeam={match.homeTeam}
                        live={live}
                      />
                    ),
                  }]
                : []),
              {
                id: "in-play",
                label: "In-play predictions",
                content: (
                  <InPlayGate>
                    {inPlayUnavailable ? (
                      <p className="ip-note" role="status">
                        In-play predictions are unavailable for this match. Its status is {matchStatusLabel(match)}.
                      </p>
                    ) : (
                      <>
                        <p className="mp-pick__note">
                          The predictions above were generated before kick-off and are never
                          re-modelled from the live score. The markets below are modelled from it.
                        </p>
                        <InPlayMarkets fixtureId={match.id} phase={phase} />
                        {/* The graph of how those markets got here. Below the
                            cards because the current price is what a reader
                            opened the tab for; the history explains it. */}
                        <WinProbabilityPanel
                          awayTeam={match.awayTeam}
                          fixtureId={match.id}
                          homeTeam={match.homeTeam}
                        />
                      </>
                    )}
                  </InPlayGate>
                ),
              },
              // Stats is the design's third tab. Present only when a provider
              // has actually reported something: an empty eight-row table of
              // dashes is worse than no tab, because it reads as a broken
              // feature rather than an absent one.
              ...(match.statistics
                ? [{
                    id: "stats",
                    label: "Stats",
                    content: (
                      <MatchStats
                        away={match.awayTeam}
                        home={match.homeTeam}
                        statistics={match.statistics}
                      />
                    ),
                  }]
                : []),
            ]}
          />
        </section>

        {/* The written preview: the fixture-specific copy that tells this page
            apart from the other several thousand fixture pages. */}
        {preview ? (
          <MatchPreview awayTeam={match.awayTeam} homeTeam={match.homeTeam} sections={preview} />
        ) : null}

        {/* 03 — the link mesh, as drawn. */}
        <section aria-labelledby="more-title" className="mp-section">
          <div className="mp-section__head">
            <div>
              <span className="mp-section__num">03 — SAME KICK-OFF</span>
              <h2 className="mp-h2" id="more-title">
                More {match.leagueName || "football"} matches
              </h2>
            </div>
            {leagueHref ? (
              <Link className="mp-fixture__cta" href={localePath(leagueHref, locale)}>
                All {match.leagueName || "league"} predictions
                <ArrowRightIcon />
              </Link>
            ) : null}
          </div>

          {related.length > 0 ? (
            <div className="mp-fixtures">
              {related.slice(0, 4).map(({ match: item }) => (
                <Link
                  className="mp-fixture"
                  href={localePath(fixtureHref(item), locale)}
                  key={item.id}
                >
                  <span className="mp-fixture__time">
                    {item.kickoff_at ? <LocalTime value={item.kickoff_at} /> : item.league_name}
                  </span>
                  {/*
                    Review 3: "the match header already had crests; fixture
                    lists now have them too". These two lines were already
                    stacked, so this adds the crest and nothing else. 24px:
                    1.4em of the card's 17px name is 23.8.
                  */}
                  <span className="mp-fixture__team sl-team">
                    <TeamCrest logoUrl={item.home_team_logo} name={item.home_team} size={24} />
                    <span className="sl-teamname">{item.home_team}</span>
                  </span>
                  <span className="mp-fixture__team sl-team">
                    <TeamCrest logoUrl={item.away_team_logo} name={item.away_team} size={24} />
                    <span className="sl-teamname">{item.away_team}</span>
                  </span>
                  <span className="mp-fixture__cta">
                    View prediction
                    <ArrowRightIcon size={14} />
                  </span>
                </Link>
              ))}
            </div>
          ) : null}

          {/*
            Kept alongside the cards rather than replaced by them: these two
            carry the crawl paths and the anchor text that name what they lead
            to, which the four cards above deliberately do not repeat.
          */}
          <FixtureLinkMesh match={match} />
          <RelatedFixtures fixtures={related} leagueName={match.leagueName} locale={locale} />
        </section>

        {match.odds.length > 0 ? (
          <section aria-labelledby="odds-title" className="mp-section" id="odds">
            <div className="mp-section__head">
              <div>
                <span className="mp-section__num">04 — AVAILABLE PRICES</span>
                <h2 className="mp-h2" id="odds-title">Available odds</h2>
              </div>
              <span className="mp-section__aside">{pluralize(match.odds.length, "price")}</span>
            </div>
            <OddsStrip odds={match.odds} />
          </section>
        ) : null}

        <div className="mp-disclaimer">
          <span aria-label="Eighteen plus only" className="mp-disclaimer__age">18+</span>
          <p>
            Statistical estimates, never guaranteed outcomes. Use predictions as analysis,
            not financial advice.
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
