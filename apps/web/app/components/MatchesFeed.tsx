"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { Suspense, useEffect, useMemo, useRef, useState } from "react";

import {
  matchClockLabel,
  matchScore,
  matchStatusLabel,
  type LiveMatch,
} from "../data/liveMatches";
import { formatMarket, formatMarketOutcome } from "../lib/markets";
import { pluralize } from "../lib/format";
import { compareCompetitions, countryLabel, footballCountry } from "../lib/competitionOrder";
import { seoSlug } from "../lib/seo";
import { DateTabs } from "./DateTabs";
import { TeamIdentity } from "./TeamIdentity";
import { AdSlot } from "./AdSlot";
import { EmptyState } from "./EmptyState";
import { FilterBar } from "./FilterBar";
import { FavoriteButton } from "./account/FavoriteButton";
import { useLocale } from "./locale/LocaleProvider";
import { fixtureHref } from "../lib/fixtureHref";
import { DEFAULT_VIEWER_TIMEZONE } from "../lib/viewerTimezone";

import { LocalTime } from "./LocalTime";
const matchFilters = ["All", "Live", "Upcoming", "Finished"];

type MatchesFeedProps = {
  matches: LiveMatch[];
  selectedDate: string;
  initialFilter?: string;
  timezone?: string;
};

function matchesFilter(match: LiveMatch, filter: string) {
  switch (filter) {
    case "Live":
      return ["live", "halftime", "second_half", "extra_time", "penalties", "break"].includes(
        match.status,
      );
    case "Upcoming":
      return ["upcoming", "delayed"].includes(match.status);
    case "Finished":
      return match.status === "finished";
    default:
      return true;
  }
}

function statusPillClass(status: string) {
  if (["live", "halftime", "second_half", "extra_time", "penalties", "break"].includes(status)) {
    return "pill pill--coral";
  }
  if (["finished", "awarded", "walkover"].includes(status)) {
    return "pill pill--signal";
  }
  return "pill";
}

export function MatchesFeed({
  matches,
  selectedDate,
  initialFilter = "All",
  timezone = DEFAULT_VIEWER_TIMEZONE,
}: MatchesFeedProps) {
  const [activeFilter, setActiveFilter] = useState(initialFilter);
  const { locale } = useLocale();
  const [visibleLimit, setVisibleLimit] = useState(100);
  const router = useRouter();
  const loadMoreRef = useRef<HTMLDivElement | null>(null);

  // Keep live fixtures fresh: periodically re-fetch server-rendered data so
  // finished matches stop showing LIVE without a manual reload.
  useEffect(() => {
    const id = setInterval(() => {
      if (document.visibilityState === "visible") router.refresh();
    }, 60_000);
    return () => clearInterval(id);
  }, [router]);

  const filteredMatches = useMemo(() => matches
    .filter((match) => matchesFilter(match, activeFilter))
    .sort((left, right) => {
      const competition = compareCompetitions({ country: left.country, league: left.leagueName, slug: left.leagueSlug }, { country: right.country, league: right.leagueName, slug: right.leagueSlug });
      if (competition !== 0) return competition;
      return Date.parse(left.kickoffAt) - Date.parse(right.kickoffAt);
    }), [activeFilter, matches]);

  useEffect(() => setVisibleLimit(100), [activeFilter]);

  // Reveal the next in-memory batch as the visitor scrolls. The server has
  // already fetched the complete slate, so there are no page transitions.
  useEffect(() => {
    const node = loadMoreRef.current;
    if (!node || visibleLimit >= filteredMatches.length) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          setVisibleLimit((current) => Math.min(current + 100, filteredMatches.length));
        }
      },
      { rootMargin: "600px" },
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, [filteredMatches.length, visibleLimit]);

  const countries = useMemo(() => {
    const grouped = new Map<string, Map<string, LiveMatch[]>>();
    for (const match of filteredMatches.slice(0, visibleLimit)) {
      const country = footballCountry(match.country, match.leagueName, match.leagueSlug);
      const leagues = grouped.get(country) ?? new Map<string, LiveMatch[]>();
      leagues.set(match.leagueName, [...(leagues.get(match.leagueName) ?? []), match]);
      grouped.set(country, leagues);
    }
    return Array.from(grouped, ([country, leagues]) => ({
      country,
      leagues: Array.from(leagues, ([league, leagueMatches]) => ({ league, matches: leagueMatches })),
    }));
  }, [filteredMatches, visibleLimit]);

  return (
    <div className="matches-feed-layout">
      <Suspense fallback={null}>
        <DateTabs timezone={timezone} />
      </Suspense>
      <FilterBar
        active={activeFilter}
        filters={matchFilters}
        label="Match filters"
        onChange={setActiveFilter}
      />

      <AdSlot className="matches-feed__ad" format="banner" placement="feed-inline" />

      <section className="section" aria-labelledby="match-results-heading">
        <div className="section-header">
          <div>
            <h2 id="match-results-heading">Fixture results</h2>
            <p>
              {pluralize(
                filteredMatches.length,
                "match",
                "matches",
              )}{" "}
              shown for {selectedDate}.
            </p>
          </div>
        </div>
      </section>

      {countries.length > 0 ? (
        countries.map(({ country, leagues }, countryIndex) => (
          <details className="competition-tree competition-tree--country" key={country} open={countryIndex < 3}>
            <summary>
              <strong>{countryLabel(country)}</strong>
              <span>{pluralize(leagues.reduce((total, item) => total + item.matches.length, 0), "match", "matches")}</span>
            </summary>
            <div className="competition-tree__children">
            {leagues.map(({ league: leagueName, matches: leagueMatches }, leagueIndex) => (
              <details className="competition-tree competition-tree--league" key={`${country}-${leagueName}`} open={countryIndex === 0 && leagueIndex < 3}>
                <summary>
                  <strong>{leagueName}</strong>
                  <span>{pluralize(leagueMatches.length, "match", "matches")}</span>
                </summary>
                <FavoriteButton compact entityId={seoSlug(leagueName)} entityType="league" hideForGuests label={leagueName} returnPath={`/matches/today?date=${selectedDate}`} subheader />
                <div className="stack competition-tree__matches">
              {leagueMatches.map((match) => (
                <article className="match-card" key={match.id}>
                  <div className="match-card__meta">
                    <span className="pill pill--pitch">{match.leagueName}</span>
                    <span className={statusPillClass(match.status)}>{matchStatusLabel(match)}</span>
                  </div>
                  <div className="match-card__teams">
                    <TeamIdentity logo={match.homeTeamLogo} name={match.homeTeam} />
                    <div className="score-box">
                      <strong>{matchScore(match) ?? <LocalTime timeOnly value={match.kickoffAt} />}</strong>
                      <span className="micro">{matchClockLabel(match)}</span>
                    </div>
                    <TeamIdentity logo={match.awayTeamLogo} name={match.awayTeam} />
                  </div>
                  {match.prediction ? (
                    <div className="match-card__details">
                      <p className="match-card__signal">
                        <strong>{formatMarket(match.prediction.marketKey, locale)} — {formatMarketOutcome(match.prediction.marketKey, match.prediction.selection, locale)}</strong>
                      </p>
                    </div>
                  ) : null}
                  <div className="match-card__actions">
                    <Link className="button button--secondary" href={fixtureHref(match)}>
                      View analysis
                    </Link>
                    <FavoriteButton compact entityId={String(match.id)} entityType="match" hideForGuests label={`${match.homeTeam} vs ${match.awayTeam}`} returnPath={fixtureHref(match)} />
                  </div>
                </article>
              ))}
                </div>
              </details>
            ))}
            </div>
          </details>
        ))
      ) : (
        <EmptyState
          actionHref="/football"
          actionLabel="Browse football"
          message="No fixtures match the current day and status filter."
          title="No matches found"
        />
      )}
      {visibleLimit < filteredMatches.length ? (
        <div ref={loadMoreRef} className="section-footer-action">
          <span className="muted">Scroll to load more matches</span>
        </div>
      ) : null}
    </div>
  );
}
