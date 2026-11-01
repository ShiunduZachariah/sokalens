"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { Fragment, useDeferredValue, useEffect, useId, useMemo, useRef, useState } from "react";
import { pluralize } from "../lib/format";
import { compareCompetitions, countryLabel, footballCountry } from "../lib/competitionOrder";
import { seoSlug } from "../lib/seo";

import {
  AvailabilityBadge,
  type AvailabilityState,
  type AvailabilityStatus,
} from "./SignalBadge";
import { FavoriteButton } from "./account/FavoriteButton";
import { FeedAdBlock } from "./FeedAdBlock";
import { feedAdPositions, splitForFeedAds } from "../lib/feedAds";
import { ChevronRightIcon } from "./match/icons";
import { TeamCrest } from "./TeamCrest";
import { resultCells } from "../lib/resultMarker";
import { localePath } from "../i18n/config";
import { useLocale } from "./locale/LocaleProvider";
import { emptyMarketTable, unpackMarkets, type MarketTable, type PackedMarkets } from "../lib/packedMarkets";

import { LocalTime } from "./LocalTime";
import { ADS_ENABLED } from "../lib/adsEnabled";

/**
 * "Sun 12 Oct" rather than "2026-10-12".
 *
 * The grouping key has to stay the ISO date -- it is what the date filter
 * compares against and what React keys on -- but an ISO string is developer
 * output, not a section heading a reader scans.
 *
 * Anchored at noon UTC and formatted in UTC. The key is already a calendar
 * date in the viewer's zone, so re-interpreting it against a browser timezone
 * is how a Sunday heading turns into Saturday for anyone west of Greenwich.
 */
const UNDATED_GROUP = "Date to be confirmed";

function dayGroupLabel(key: string, locale: string) {
  if (key === UNDATED_GROUP) return key;
  const anchored = new Date(`${key}T12:00:00Z`);
  if (Number.isNaN(anchored.getTime())) return key;
  // Composed from parts rather than taking Intl's default order: `en` formats
  // as "Mon, Oct 12" (US), and the design is day-first -- "Sun 4 Oct". The
  // names stay locale-aware; only the order is fixed.
  const parts = new Intl.DateTimeFormat(locale, {
    day: "numeric",
    month: "short",
    timeZone: "UTC",
    weekday: "short",
  }).formatToParts(anchored);
  const part = (type: Intl.DateTimeFormatPartTypes) =>
    parts.find((entry) => entry.type === type)?.value ?? "";
  const [weekday, day, month] = [part("weekday"), part("day"), part("month")];
  return weekday && day && month ? `${weekday} ${day} ${month}` : key;
}

export type PredictionExplorerItem = {
  availability?: AvailabilityState | AvailabilityStatus;
  confidence: number;
  date?: string;
  href: string;
  id: number | string;
  isBestBet?: boolean;
  /** ISO-8601 instant, never a formatted string: the browser localises it. */
  kickoffAt: string | null;
  league: string;
  country: string;
  market: string;
  /** Untranslated market key and selection, for the 1 / X / 2 marker. */
  marketKey?: string;
  rawSelection?: string;
  markets?: Array<{ market: string; prediction: string; confidence: number }>;
  /**
   * `markets` in compact wire form. A caller sends one or the other, never
   * both.
   */
  packedMarkets?: PackedMarkets;
  match: string;
  matchId: number;
  homeTeam?: string;
  awayTeam?: string;
  homeTeamLogo?: string;
  awayTeamLogo?: string;
  prediction: string;
  reasoning?: string;
};

type Props = {
  emptyMessage: string;
  items: PredictionExplorerItem[];
  /** Label tables for `item.packedMarkets`; unused when items carry `markets`. */
  marketTable?: MarketTable;
  title: string;
};

/** How the board is divided. Mirrors the artboard's Competition / Kick-off. */
type GroupBy = "competition" | "kickoff" | "day";

type Row = PredictionExplorerItem & { showLeague: boolean };
type LeagueGroup = { name: string; hasName: boolean; rows: Row[] };
type Group = { key: string; title: string; total: number; leagues: LeagueGroup[] };

function selectedMarketFor(item: PredictionExplorerItem, market: string) {
  if (market !== "all") {
    const selected = item.markets?.find((entry) => entry.market === market);
    if (selected) return selected;
  }
  return { market: item.market, prediction: item.prediction, confidence: item.confidence };
}

function availabilityStatus(value: AvailabilityState | AvailabilityStatus): AvailabilityStatus {
  if (value === "available" || value === "full") return "full";
  if (value === "limited" || value === "partial") return "partial";
  if (value === "stats-only") return "stats-only";
  return "unavailable";
}

/**
 * One fixture row.
 *
 * The whole row is a single link, as the artboard draws it, and the match
 * favourite survives that: it sits OUTSIDE the anchor and is positioned over
 * the row's right edge. Nesting it inside would be a button inside a link --
 * a hit-target conflict and invalid HTML -- and simply dropping it would take
 * "save a match" away from the densest surface that offers it, which is the
 * one place a reader scanning a full slate actually wants it.
 *
 * The share control does not survive. It opens a menu, which cannot be laid
 * over a link without swallowing the row's own target; it remains on the match
 * page this row opens. Country and league favourites stay on the group
 * headers, where they already were.
 */
function FixtureRow({ item, market }: { item: Row; market: string }) {
  const { locale } = useLocale();
  const selected = selectedMarketFor(item, market);
  const cells = resultCells(item.marketKey ?? "", item.rawSelection ?? "");
  const status = item.availability ? availabilityStatus(item.availability) : null;

  return (
    <li className="td-rowwrap">
      <Link className="td-row" href={localePath(item.href, locale)}>
        <span className="td-row__time">
          {item.kickoffAt ? <LocalTime timeOnly value={item.kickoffAt} /> : "TBC"}
        </span>

        <span className="td-row__fixture">
          {/*
            Stacked, one team per line, each with its crest (review UI-04 and
            UI-05). The single "Home v Away" string truncated as one unit, so a
            row with two long names lost BOTH to one ellipsis -- the reported
            case was "GAP Connah S Quay FC v Penyb...". Two lines each with
            their own min-width:0 means each name truncates on its own budget,
            and the away side is never the one that always loses.

            item.match is the pre-joined title and stays as the fallback for a
            row that carries no separate team names.
          */}
          {item.homeTeam && item.awayTeam ? (
            <span className="td-row__teams">
              <span className="td-row__team">
                <TeamCrest logoUrl={item.homeTeamLogo} name={item.homeTeam} size={20} />
                <span className="td-row__teamname">{item.homeTeam}</span>
              </span>
              <span className="td-row__team">
                <TeamCrest logoUrl={item.awayTeamLogo} name={item.awayTeam} size={20} />
                <span className="td-row__teamname">{item.awayTeam}</span>
              </span>
            </span>
          ) : (
            <span className="td-row__teams">{item.match}</span>
          )}
          {/*
            The league line shows when the group does not already name it --
            which is every row under Kick-off grouping, and none under
            Competition.
          */}
          {item.showLeague ? (
            <span className="td-row__meta">
              {item.league}
              {item.country ? ` · ${countryLabel(footballCountry(item.country, item.league))}` : ""}
            </span>
          ) : null}
          {status && status !== "full" ? <AvailabilityBadge status={status} /> : null}
        </span>

        {/*
          Match-result picks only; see app/lib/resultMarker.ts. aria-hidden
          because the selection is named in words in the next column.
        */}
        {cells ? (
          <span aria-hidden="true" className="td-1x2">
            {cells.map((cell) => (
              <span data-picked={cell.picked} key={cell.key} title={cell.label}>
                {cell.key}
              </span>
            ))}
          </span>
        ) : (
          <span />
        )}

        <span className="td-row__pick">
          <span className="td-row__selection">{selected.prediction}</span>
          <span className="td-row__market">{selected.market}</span>
        </span>

        <span className="td-row__conf">{Math.round(selected.confidence)}%</span>
        <span aria-hidden="true" className="td-row__chevron"><ChevronRightIcon /></span>
      </Link>
      <span className="td-row__save">
        <FavoriteButton
          compact
          entityId={String(item.matchId)}
          entityType="match"
          hideForGuests
          label={item.match}
          returnPath={localePath(item.href, locale)}
        />
      </span>
    </li>
  );
}

export function PredictionExplorer({
  emptyMessage,
  items: wireItems,
  marketTable = emptyMarketTable,
  title,
}: Props) {
  const items = useMemo(
    () => wireItems.map((item) =>
      item.packedMarkets
        ? { ...item, markets: unpackMarkets(item.packedMarkets, marketTable) ?? item.markets }
        : item,
    ),
    [marketTable, wireItems],
  );

  const id = useId();
  // Day headings are formatted per locale, so the explorer needs it too --
  // until now only FixtureRow read the provider.
  const { locale } = useLocale();
  const router = useRouter();
  const [country, setCountry] = useState("all");
  const [league, setLeague] = useState("all");
  const [market, setMarket] = useState("all");
  const [date, setDate] = useState("all");
  const [search, setSearch] = useState("");
  const [bestOnly, setBestOnly] = useState(false);
  /*
   * Grouping defaults to the day when the slate spans more than one, because
   * that is the first question a reader of a multi-day page asks: the weekend
   * page grouped by competition buries Sunday's matches inside Saturday's
   * leagues, and the design groups it by day for exactly that reason. A
   * single-day page has nothing to gain from a day group of one, so it keeps
   * competition.
   *
   * `spansDays` is read once, as the initial state, rather than kept in sync: a
   * reader who has chosen a grouping must not have it changed under them when
   * the feed refreshes across midnight.
   */
  const spansDays = useMemo(
    () => new Set(items.map((item) => item.date).filter(Boolean)).size > 1,
    [items],
  );
  const [groupBy, setGroupBy] = useState<GroupBy>(spansDays ? "day" : "competition");
  const [visibleLimit, setVisibleLimit] = useState(40);
  const loadMoreRef = useRef<HTMLDivElement | null>(null);

  const deferredCountry = useDeferredValue(country);
  const deferredLeague = useDeferredValue(league);
  const deferredMarket = useDeferredValue(market);
  const deferredDate = useDeferredValue(date);
  const deferredSearch = useDeferredValue(search);
  const deferredBestOnly = useDeferredValue(bestOnly);

  const dates = useMemo(
    () => Array.from(new Set(items.map((item) => item.date).filter(Boolean) as string[])).sort(),
    [items],
  );
  const countries = useMemo(
    () => Array.from(new Set(items.map((item) => footballCountry(item.country, item.league)))).sort((left, right) =>
      countryLabel(left).localeCompare(countryLabel(right)),
    ),
    [items],
  );
  const leagues = useMemo(
    () => country === "all"
      ? []
      : Array.from(new Set(items.filter((item) => footballCountry(item.country, item.league) === country).map((item) => item.league))).sort(),
    [country, items],
  );
  const markets = useMemo(
    () => Array.from(new Set(items.flatMap((item) =>
      (item.markets ?? [{ market: item.market }]).map((entry) => entry.market),
    ))).sort(),
    [items],
  );
  const hasBestBets = items.some((item) => item.isBestBet);

  const visibleItems = useMemo(() => {
    return items.filter(
      (item) =>
        (deferredDate === "all" || item.date === deferredDate) &&
        (deferredCountry === "all" || footballCountry(item.country, item.league) === deferredCountry) &&
        (deferredLeague === "all" || item.league === deferredLeague) &&
        (deferredMarket === "all" || (item.markets ?? [{ market: item.market }]).some((entry) => entry.market === deferredMarket)) &&
        (!deferredBestOnly || item.isBestBet) &&
        (!deferredSearch.trim() || `${item.match} ${item.league} ${item.country} ${item.market} ${item.prediction}`.toLowerCase().includes(deferredSearch.trim().toLowerCase())),
    ).sort((left, right) => {
      // Kick-off grouping is one time-ordered list, so competition order must
      // not come first or the single group would read out of sequence.
      if (groupBy === "kickoff") {
        return Date.parse(left.kickoffAt ?? "") - Date.parse(right.kickoffAt ?? "");
      }
      const competition = compareCompetitions(left, right);
      if (competition !== 0) return competition;
      // Sorting used to parse the *formatted* label, which is not a date and
      // always gave NaN, so this comparator silently did nothing.
      return Date.parse(left.kickoffAt ?? "") - Date.parse(right.kickoffAt ?? "");
    });
  }, [deferredBestOnly, deferredCountry, deferredDate, deferredLeague, deferredMarket, deferredSearch, groupBy, items]);

  const displayedItems = useMemo(() => visibleItems.slice(0, visibleLimit), [visibleItems, visibleLimit]);

  const groups = useMemo<Group[]>(() => {
    if (groupBy === "kickoff") {
      // One flat list in time order. Each row names its own competition,
      // because the group no longer does.
      const rows: Row[] = displayedItems.map((item) => ({ ...item, showLeague: true }));
      return rows.length === 0 ? [] : [{
        key: "kickoff",
        title: "By kick-off time",
        total: rows.length,
        leagues: [{ name: "", hasName: false, rows }],
      }];
    }

    if (groupBy === "day") {
      // Day, then competition within it -- the design's Weekend layout.
      const byDay = new Map<string, Map<string, Row[]>>();
      for (const item of displayedItems) {
        const key = item.date ?? UNDATED_GROUP;
        const leagueMap = byDay.get(key) ?? new Map<string, Row[]>();
        leagueMap.set(item.league, [...(leagueMap.get(item.league) ?? []), { ...item, showLeague: false }]);
        byDay.set(key, leagueMap);
      }
      return Array.from(byDay, ([key, leagueMap]) => {
        const leagueGroups = Array.from(leagueMap, ([name, rows]) => ({ name, hasName: true, rows }));
        return {
          key,
          title: dayGroupLabel(key, locale),
          total: leagueGroups.reduce((sum, entry) => sum + entry.rows.length, 0),
          leagues: leagueGroups,
        };
      }).sort((left, right) => left.key.localeCompare(right.key));
    }

    const byCountry = new Map<string, Map<string, Row[]>>();
    for (const item of displayedItems) {
      const key = footballCountry(item.country, item.league);
      const leagueMap = byCountry.get(key) ?? new Map<string, Row[]>();
      leagueMap.set(item.league, [...(leagueMap.get(item.league) ?? []), { ...item, showLeague: false }]);
      byCountry.set(key, leagueMap);
    }
    return Array.from(byCountry, ([key, leagueMap]) => {
      const leagueGroups = Array.from(leagueMap, ([name, rows]) => ({ name, hasName: true, rows }));
      return {
        key,
        title: countryLabel(key),
        total: leagueGroups.reduce((sum, entry) => sum + entry.rows.length, 0),
        leagues: leagueGroups,
      };
    });
  }, [displayedItems, groupBy]);

  /*
   * Feed-ad bookkeeping.
   *
   * `feedStart` is each league block's first row position in the WHOLE feed, so
   * the cadence counts across group and league boundaries -- a slate of
   * three-match leagues would otherwise never reach the cadence and never show
   * an ad. Derived from `groups` in render order, so it cannot disagree with
   * what is drawn.
   *
   * ## The first position is the spec's, not the cadence's
   *
   * Ad spec 3.1, for Today and every other prediction list: "After the first
   * competition group, only if there are 2+ groups". So the first unit sits at
   * the last row of group one rather than wherever eight rows happen to land,
   * and the cadence takes over from there.
   *
   * The "2+ groups" qualifier is about THIS position, not about the page. With
   * one group there is no "after the first group" that is not also "after the
   * whole feed", which would put the slot directly above the footer -- the
   * `MIN_ROWS_BELOW` case `feedAds` already refuses, and the reason 3.2 forbids
   * an ad between a group header and its first row. A single-group slate
   * therefore falls back to the ordinary 8-row cadence rather than losing its
   * in-feed inventory altogether, which is what section 6 permits ("at most one
   * in-feed unit per 8 rows") and what section 3 asks for (every page gets
   * in-feed inventory).
   */
  const { feedStarts, adPositions } = useMemo(() => {
    const starts = new Map<string, number>();
    let cursor = 0;
    for (const group of groups) {
      for (const leagueGroup of group.leagues) {
        starts.set(`${group.key}::${leagueGroup.name}`, cursor);
        cursor += leagueGroup.rows.length;
      }
    }
    const firstGroupRows = groups[0]?.total ?? 0;
    const firstAfterIndex = groups.length >= 2 && firstGroupRows > 0 ? firstGroupRows - 1 : undefined;
    return { adPositions: ADS_ENABLED ? feedAdPositions(cursor, { firstAfterIndex }) : new Set<number>(), feedStarts: starts };
  }, [groups]);

  useEffect(() => setVisibleLimit(40), [bestOnly, country, date, league, market, groupBy]);

  useEffect(() => {
    const interval = setInterval(() => {
      if (document.visibilityState === "visible") router.refresh();
    }, 60_000);
    return () => clearInterval(interval);
  }, [router]);

  useEffect(() => {
    const node = loadMoreRef.current;
    if (!node || visibleLimit >= visibleItems.length) return;
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        setVisibleLimit((current) => Math.min(current + 40, visibleItems.length));
      }
    }, { rootMargin: "700px" });
    observer.observe(node);
    return () => observer.disconnect();
  }, [visibleItems.length, visibleLimit]);

  const clearFilters = () => {
    setDate("all");
    setCountry("all");
    setLeague("all");
    setMarket("all");
    setSearch("");
    setBestOnly(false);
  };

  return (
    <section aria-labelledby={`${id}-title`} className="td-main">
      <h2 className="sr-only" id={`${id}-title`}>{title}</h2>

      <div aria-label="Prediction filters" className="td-toolbar">
        <label className="ac-field ac-field--wide">
          Search
          <input
            onChange={(event) => setSearch(event.target.value)}
            placeholder="Team or competition"
            type="search"
            value={search}
          />
        </label>

        {dates.length > 0 ? (
          <label className="ac-field">
            Date
            <select onChange={(event) => setDate(event.target.value)} value={date}>
              <option value="all">All dates</option>
              {/* The value stays ISO because the filter compares on it; only the
                  label is for reading. This select showed "2026-10-12" until the
                  horizon made it span days and anyone looked at it. */}
              {dates.map((item) => (
                <option key={item} value={item}>{dayGroupLabel(item, locale)}</option>
              ))}
            </select>
          </label>
        ) : null}

        <label className="ac-field">
          Country
          <select
            onChange={(event) => { setCountry(event.target.value); setLeague("all"); }}
            value={country}
          >
            <option value="all">All countries</option>
            {countries.map((item) => <option key={item} value={item}>{countryLabel(item)}</option>)}
          </select>
        </label>

        <label className="ac-field">
          League
          <select disabled={country === "all"} onChange={(event) => setLeague(event.target.value)} value={league}>
            <option value="all">{country === "all" ? "Choose a country first" : "All leagues"}</option>
            {leagues.map((item) => <option key={item} value={item}>{item}</option>)}
          </select>
        </label>

        <label className="ac-field">
          Market
          <select onChange={(event) => setMarket(event.target.value)} value={market}>
            <option value="all">All markets</option>
            {markets.map((item) => <option key={item} value={item}>{item}</option>)}
          </select>
        </label>

        <div className="ac-field">
          <span>Group by</span>
          {/*
            Buttons rather than a select: there are at most three options, all
            worth seeing, and each is one press away. "Day" appears only on a
            slate that spans more than one.
          */}
          <div aria-label="Group by" className="td-groupby" role="group">
            {spansDays ? (
              <button
                aria-pressed={groupBy === "day"}
                onClick={() => setGroupBy("day")}
                type="button"
              >
                Day
              </button>
            ) : null}
            <button
              aria-pressed={groupBy === "competition"}
              onClick={() => setGroupBy("competition")}
              type="button"
            >
              Competition
            </button>
            <button
              aria-pressed={groupBy === "kickoff"}
              onClick={() => setGroupBy("kickoff")}
              type="button"
            >
              Kick-off
            </button>
          </div>
        </div>

        {hasBestBets ? (
          <label className="ac-field" htmlFor={`${id}-best-only`}>
            <span>Best bets</span>
            <span className="td-groupby">
              <button
                aria-pressed={bestOnly}
                id={`${id}-best-only`}
                onClick={() => setBestOnly((value) => !value)}
                type="button"
              >
                Best bets only
              </button>
            </span>
          </label>
        ) : null}
      </div>

      <p aria-live="polite" className="td-row__meta">
        {pluralize(visibleItems.length, "predicted match", "predicted matches")}
        {displayedItems.length < visibleItems.length ? ` · showing ${displayedItems.length}` : ""}
      </p>

      {visibleItems.length > 0 ? (
        groups.map((group) => (
          <section className="td-group" key={group.key}>
            <div className="td-group__head">
              <h2>{group.title}</h2>
              {/*
                No favourite control on a country header (review UI-03/UI-09).
                A country is a grouping, not something to follow, and the heart
                sat inside this span -- which is what pushed it against
                "8 predictions" and overlapped it. The count is the only thing
                in here now.
              */}
              <span className="td-group__count">
                {pluralize(group.total, "prediction", "predictions")}
              </span>
            </div>

            {group.leagues.map((leagueGroup) => (
              <div key={leagueGroup.name || "all"}>
                {leagueGroup.hasName ? (
                  <h3 className="td-league">
                    {leagueGroup.name}
                    <FavoriteButton
                      compact
                      entityId={seoSlug(leagueGroup.name)}
                      entityType="league"
                      hideForGuests
                      label={leagueGroup.name}
                      returnPath="/predictions/today"
                      subheader
                    />
                  </h3>
                ) : null}
                {/*
                  The rows in one or more real <ol>s, with any ad block BETWEEN
                  them rather than inside one. An ad rendered as an <li> makes a
                  screen reader announce "list, 11 items" for ten predictions and
                  an advertisement -- the ad is a complementary region, not an
                  item in the list. Splitting is free here because `.td-rows`
                  carries no numbering and no outer border.
                */}
                {splitForFeedAds(
                  leagueGroup.rows,
                  feedStarts.get(`${group.key}::${leagueGroup.name}`) ?? 0,
                  adPositions,
                ).map((segment, segmentIndex) => (
                  <Fragment key={segment.rows[0]?.id ?? segmentIndex}>
                    <ol className="td-rows">
                      {segment.rows.map((row) => (
                        <FixtureRow item={row} key={row.id} market={deferredMarket} />
                      ))}
                    </ol>
                    {segment.adAfter ? <FeedAdBlock /> : null}
                  </Fragment>
                ))}
              </div>
            ))}
          </section>
        ))
      ) : (
        <div className="ac-empty">
          <h3>No predictions found</h3>
          <p>{emptyMessage}</p>
          <button className="mp-btn" onClick={clearFilters} type="button">Clear filters</button>
        </div>
      )}

      {displayedItems.length < visibleItems.length ? (
        <div aria-label="Loading more predictions" className="td-loadmore" ref={loadMoreRef}>
          <span>Scroll to load more matches</span>
        </div>
      ) : null}
    </section>
  );
}
