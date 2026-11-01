"use client";

import Link from "next/link";
import { Fragment, useDeferredValue, useId, useMemo, useState } from "react";

import { countryLabel } from "../lib/competitionOrder";
import { formatConfidence } from "../lib/format";
import { LocalTime } from "./LocalTime";
import { MatchupIdentity } from "./MatchupIdentity";
import { ChevronRightIcon } from "./match/icons";
import { FeedAdBlock } from "./FeedAdBlock";
import { feedAdPositions, splitForFeedAds } from "../lib/feedAds";
import { useLocale } from "./locale/LocaleProvider";
import { useAccount } from "./account/AccountContext";
import { BestBetsLocked } from "./BestBetsLocked";
import { localePath } from "../i18n/config";
import {
  applyBestBetFilters,
  bestBetFacets,
  EMPTY_BEST_BET_FILTERS,
  type BestBetFilters,
} from "../lib/bestBetFilters";

/**
 * Ad spec 3.1: the Best Bets in-feed unit goes after the 5th ranked row.
 * Zero-based, and the hero counts as row 1, so index 4.
 */
const BEST_BETS_FIRST_AD_AFTER_ROW = 4;

export type BestBetItem = {
  matchId: number;
  /**
   * The pre-joined "Home v Away" title. Still here because it is what the
   * market filter, the same-match cluster note and the share text all read, and
   * because it is the single-line fallback for a row whose separate names did
   * not arrive.
   */
  match: string;
  /**
   * The two sides and their crests, for the stacked team block (review 2.3).
   * Optional rather than required: the pool is engine rows beside provider
   * fallback rows, and a row that carries no names falls back to `match`
   * instead of rendering half a block.
   */
  homeTeam?: string;
  awayTeam?: string;
  homeTeamLogo?: string;
  awayTeamLogo?: string;
  league: string;
  country: string;
  /** ISO-8601 instant, never a formatted string: the browser localises it. */
  kickoffAt: string | null;
  market: string;
  marketKey: string;
  prediction: string;
  confidence: number;
  href: string;
};

/**
 * The shortlist: the strongest pick as a hero, the rest as ranked rows.
 *
 * Ranks are assigned BEFORE filtering: #3 stays #3 when a reader narrows to one
 * country, because the number means "third strongest today", not "third row on
 * screen". Renumbering on filter would quietly change what the page claims.
 *
 * The design replaced the old card stack with rows for a reason worth keeping:
 * a shortlist is a ranking, and forty cards each 180px tall cannot be compared.
 * Rows put the confidence figures in one column where a reader can read down
 * them.
 */
export function BestBetExplorer({ items }: { items: BestBetItem[] }) {
  const id = useId();
  const { locale } = useLocale();
  const { access, status } = useAccount();
  const [filters, setFilters] = useState<BestBetFilters>(EMPTY_BEST_BET_FILTERS);
  const deferredFilters = useDeferredValue(filters);

  const ranked = useMemo(() => items.map((item, index) => ({ ...item, rank: index + 1 })), [items]);
  const facets = useMemo(() => bestBetFacets(ranked, deferredFilters.country), [ranked, deferredFilters.country]);
  const visible = useMemo(() => applyBestBetFilters(ranked, deferredFilters), [ranked, deferredFilters]);

  /*
   * Matches appearing more than once, named outright.
   *
   * Two picks on the same fixture are not two independent chances -- "over 2.5"
   * and "home & over 2.5" win and lose together. A reader building an ACCA from
   * this list would be stacking correlated legs and believing they had
   * diversified, so the page says so rather than leaving it to be inferred from
   * a repeated team name. The Builder enforces the same rule by keeping one pick
   * per match.
   */
  const clusters = useMemo(() => {
    const counts = new Map<string, number>();
    for (const item of visible) counts.set(item.match, (counts.get(item.match) ?? 0) + 1);
    return [...counts.entries()].filter(([, count]) => count > 1).map(([match, count]) => ({ match, count }));
  }, [visible]);

  const update = (patch: Partial<BestBetFilters>) => setFilters((current) => ({ ...current, ...patch }));

  /*
   * Ad positions count the hero as the feed's first row. It is drawn above the
   * list but it is still the first thing scrolled past, so offsetting by it
   * keeps the first ad block the spec's distance into the page rather than one
   * row further.
   */
  // Unresolved reads as unlocked: see the note above the gate.
  const locked = status === "ready" && !access?.authenticated;
  const [hero, ...rest] = visible;
  // The hero is the top of the unfiltered ranking only. Promoting #17 to a hero
  // because a filter removed everything above it would present it as the day's
  // strongest pick, which it is not.
  const showHero = hero?.rank === 1;
  const rows = showHero ? rest : visible;
  const heroOffset = showHero ? 1 : 0;
  /*
   * Ad spec 3.1, Best Bets: "After the 5th ranked row".
   *
   * Zero-based across the whole feed, so index 4 IS the fifth ranked row, hero
   * included -- the hero is rank 1. Later units follow the 8-row cadence of
   * section 6, so the next one lands after rank 13.
   */
  const adPositions = feedAdPositions(rows.length + heroOffset, { firstAfterIndex: BEST_BETS_FIRST_AD_AFTER_ROW });

  return (
    <>
      {/*
        No filters while locked. The design shows none, and the reason is not
        aesthetic: every control here acts on a list the reader cannot read, so a
        country select over four blurred rows is a dead end dressed as a feature.
      */}
      {locked ? null : (
      <div className="bb-controls">
        <label className="lg-search" htmlFor={`${id}-q`}>
          <svg aria-hidden="true" fill="none" height="16" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24" width="16">
            <circle cx="11" cy="11" r="7" />
            <path d="M20 20l-4-4" />
          </svg>
          <input
            aria-label="Search team, league or market"
            id={`${id}-q`}
            onChange={(event) => update({ search: event.target.value })}
            placeholder="Team, league, or market"
            type="search"
            value={filters.search}
          />
        </label>

        <div className="td-toolbar">
          <label className="ac-field" htmlFor={`${id}-country`}>
            <span>Country</span>
            <select
              id={`${id}-country`}
              onChange={(event) => update({ country: event.target.value, league: "all" })}
              value={filters.country}
            >
              <option value="all">All countries</option>
              {facets.countries.map((country) => (
                <option key={country} value={country}>{countryLabel(country)}</option>
              ))}
            </select>
          </label>

          <label className="ac-field" htmlFor={`${id}-league`}>
            <span>League</span>
            <select
              disabled={filters.country === "all"}
              id={`${id}-league`}
              onChange={(event) => update({ league: event.target.value })}
              value={filters.league}
            >
              <option value="all">{filters.country === "all" ? "Select a country first" : "All leagues"}</option>
              {facets.leagues.map((league) => (
                <option key={league} value={league}>{league}</option>
              ))}
            </select>
          </label>
        </div>

        {/* Markets as chips rather than a select, as the design draws them:
            the market is the filter a reader of a shortlist actually reaches
            for, and a chip row shows what is on offer without being opened. */}
        <div aria-label="Filter by market" className="td-chiprow" role="group">
          <button
            aria-pressed={filters.market === "all"}
            className="td-subnav__pill"
            onClick={() => update({ market: "all" })}
            type="button"
          >
            All markets
          </button>
          {facets.markets.map((market) => (
            <button
              aria-pressed={filters.market === market}
              className="td-subnav__pill"
              key={market}
              onClick={() => update({ market })}
              type="button"
            >
              {market}
            </button>
          ))}
        </div>

        <p aria-live="polite" className="bb-count">
          Showing {visible.length} of {ranked.length}
        </p>
      </div>
      )}

      {clusters.length > 0 && !locked ? (
        <p className="bb-clusters">
          <strong className="bb-clusters__title">Same-match picks.</strong>{" "}
          {clusters.map((cluster) => (
            <span key={cluster.match}>{cluster.match} has {cluster.count} picks on this list. </span>
          ))}
          These outcomes are linked, so the Builder keeps one pick per match.
        </p>
      ) : null}

      {visible.length === 0 ? (
        <div className="ac-empty" aria-live="polite">
          <h3>No picks match these filters</h3>
          <p>Clear a filter to see the rest of today&apos;s shortlist.</p>
          <button className="mp-btn" onClick={() => setFilters(EMPTY_BEST_BET_FILTERS)} type="button">
            Clear filters
          </button>
        </div>
      ) : (
        <>
          {showHero ? (
            <Link className="bb-hero" href={localePath(hero.href, locale)}>
              <span className="bb-hero__rank">#1</span>
              <span className="bb-hero__copy">
                <span className="bb-hero__meta">
                  {hero.league}
                  {hero.kickoffAt ? <> · <LocalTime timeOnly value={hero.kickoffAt} /></> : null}
                </span>
                {/*
                  44px crests, not the rows' 20: best-bets.html sets the hero's
                  crest at 1.4em of its 32px headline, which is 44.8. It steps
                  down to 32 at tablet and mobile, where that headline is 24px,
                  and the CSS does that rather than a second render.
                */}
                {hero.homeTeam && hero.awayTeam ? (
                  <MatchupIdentity
                    awayTeam={hero.awayTeam}
                    awayTeamLogo={hero.awayTeamLogo}
                    className="bb-hero__match"
                    homeTeam={hero.homeTeam}
                    homeTeamLogo={hero.homeTeamLogo}
                    layout="stacked"
                    size={44}
                  />
                ) : (
                  <strong className="bb-hero__match">{hero.match}</strong>
                )}
                <span className="bb-hero__pick">
                  {hero.prediction} <span>{hero.market}</span>
                </span>
              </span>
              <span className="bb-hero__conf">
                <strong>{formatConfidence(hero.confidence)}</strong>
                <span>confidence</span>
              </span>
            </Link>
          ) : null}

          {locked ? null : (
            <div className="pf-sectionhead">
              <h2>Ranked picks</h2>
              <span>By model confidence</span>
            </div>
          )}

          {/*
            The gate starts at number two, which is the design's own split: the
            hero above is rendered outside it.

            `status !== "ready"` is treated as unlocked rather than locked. The
            account context resolves asynchronously, so locking during that window
            would flash a sign-up card at every signed-in reader on every visit --
            the same reasoning as InPlayGate, and the opposite default, because
            here the unresolved state is the one that must not be shown.
          */}

          {locked ? (
            <BestBetsLocked items={rows} startRank={heroOffset + 1} total={ranked.length} />
          ) : (
          <>
          {/*
            One <ol> per segment with the ad between them, so the ranked list
            stays a list of picks. Positions carry the hero: it is row one of the
            feed even though it is drawn above, so `heroOffset` puts the first ad
            after the fifth RANK rather than five rows past the hero.
          */}
          {splitForFeedAds(rows, heroOffset, adPositions).map((segment, segmentIndex) => (
          <Fragment key={segment.rows[0] ? `${segment.rows[0].matchId}-${segment.rows[0].marketKey}` : segmentIndex}>
          {/* No `start`: the rank a reader sees is drawn from `item.rank`, and
              `.bb-rows` has no list markers, so splitting the list changes
              nothing visible. */}
          <ol className="bb-rows">
            {segment.rows.map((item) => (
              <li key={`${item.matchId}-${item.marketKey}`}>
                <Link href={localePath(item.href, locale)}>
                  <span className="bb-row__rank">#{item.rank}</span>
                  <span className="bb-row__fixture">
                    {/* Stacked at 20px, which is what best-bets.html draws on a
                        ranked row: 1.4em of its 15px name. */}
                    {item.homeTeam && item.awayTeam ? (
                      <MatchupIdentity
                        awayTeam={item.awayTeam}
                        awayTeamLogo={item.awayTeamLogo}
                        className="bb-row__teams"
                        homeTeam={item.homeTeam}
                        homeTeamLogo={item.homeTeamLogo}
                        layout="stacked"
                        size={20}
                      />
                    ) : (
                      <strong>{item.match}</strong>
                    )}
                    <span className="bb-row__meta">
                      {item.league}
                      {item.kickoffAt ? <> · <LocalTime timeOnly value={item.kickoffAt} /></> : null}
                    </span>
                  </span>
                  <span className="bb-row__pick">
                    <strong>{item.prediction}</strong>
                    <span className="bb-row__meta">{item.market}</span>
                  </span>
                  <span className="bb-row__conf">{formatConfidence(item.confidence)}</span>
                  <span aria-hidden="true" className="lg-card__chevron"><ChevronRightIcon /></span>
                </Link>
              </li>
            ))}
          </ol>
          {segment.adAfter ? <FeedAdBlock /> : null}
          </Fragment>
          ))}
          </>
          )}
        </>
      )}
    </>
  );
}
