"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { AccaGenerator } from "../components/AccaGenerator";
import { AdSlot } from "../components/AdSlot";
import { FeedAdBlock } from "../components/FeedAdBlock";
import { OddsFormatControl, useOddsFormat } from "../components/OddsFormatControl";
import { PageHeader } from "../components/PageHeader";
import { ResponsibleNote } from "../components/ResponsibleNote";
import { ShareButton } from "../components/ShareButton";
import { MatchupIdentity } from "../components/MatchupIdentity";
import { marketLabel, marketSelectionLabel, matchLabel, nairobiDate, type LivePrediction, type LivePredictionMarket } from "../data/livePredictions";
import { countryLabel, footballCountry } from "../lib/competitionOrder";
import { useLocale } from "../components/locale/LocaleProvider";

import { LocalTime } from "../components/LocalTime";
import { RecommendationTags } from "../components/RecommendationTags";
import { matchTags } from "../lib/recommendationTags";
import {
  BUILDER_EVENT_NAME,
  MIN_LEGS,
  builderLegId,
  decodeBuilderSelection,
  encodeBuilderSelection,
  fairOdds,
  formatOdds,
  hasBuilderLegStarted,
  pruneBuilderSelection,
  totalOdds,
  writeBuilderSelection,
  type BuilderLeg,
} from "./builder-state";

type BuilderCandidate = LivePrediction & LivePredictionMarket & { candidateId: string; odds: number | null; oddsType: BuilderLeg["oddsType"] };

function candidateOdds(prediction: LivePrediction, market: LivePredictionMarket) {
  if (market.marketKey === prediction.marketKey && market.selection === prediction.selection && prediction.selectionOdds) {
    return { odds: prediction.selectionOdds, oddsType: "market" as const };
  }
  const fair = fairOdds(market.probability);
  return fair === null ? { odds: null, oddsType: "unavailable" as const } : { odds: fair, oddsType: "fair" as const };
}

function toLeg(candidate: BuilderCandidate): BuilderLeg {
  return {
    id: candidate.candidateId,
    predictionId: candidate.id,
    matchId: candidate.matchId,
    matchTitle: matchLabel(candidate),
    homeTeam: candidate.homeTeam,
    awayTeam: candidate.awayTeam,
    homeTeamLogo: candidate.homeTeamLogo,
    awayTeamLogo: candidate.awayTeamLogo,
    marketKey: candidate.marketKey,
    selection: candidate.selection,
    odds: candidate.odds,
    oddsType: candidate.oddsType,
    kickoffAt: candidate.kickoffAt,
    matchStatus: candidate.matchStatus,
    source: "manual",
  };
}

type HorizonPage = { predictions?: LivePrediction[]; total?: number; page?: number; limit?: number };
const BUILDER_MATCH_BATCH_SIZE = 24;

export type BuilderMode = "build" | "generate";

export function BuilderClient({
  initialMode = "build",
  predictions: initialPredictions,
}: {
  /** Which input the page opens on. `/acca-generator` opens on Generate. */
  initialMode?: BuilderMode;
  predictions: LivePrediction[];
}) {
  const { locale } = useLocale();
  const [mode, setMode] = useState<BuilderMode>(initialMode);
  const router = useRouter();
  const [predictions, setPredictions] = useState(initialPredictions);
  const [selected, setSelected] = useState<BuilderLeg[]>([]);
  const [expiredBuilderLegs, setExpiredBuilderLegs] = useState<BuilderLeg[]>([]);
  const [sharedTicketPresent, setSharedTicketPresent] = useState(false);
  const [sharedResolvedCount, setSharedResolvedCount] = useState(0);
  const [sharedExpiredCount, setSharedExpiredCount] = useState(0);
  const [country, setCountry] = useState("all");
  const [league, setLeague] = useState("all");
  const [market, setMarket] = useState("all");
  const [search, setSearch] = useState("");
  const [oddsFormat, setOddsFormat] = useOddsFormat();
  /*
    Narrow screens only. Above the breakpoint the ticket is a sticky rail and
    this never applies; below it the rail goes static and lands under a match
    list hundreds of rows long, which is the whole complaint -- the reader
    cannot see what they have picked without scrolling past everything they
    have not. The design answers with a bar above the bottom nav.
  */
  const [ticketOpen, setTicketOpen] = useState(false);
  /*
    The open ticket is pinned above the bar, not scrolled to.

    Scrolling to it was my own invention and it failed on production: the match
    list lazy-loads, so scrolling toward the rail appended more matches above it
    and pushed it further down. Measured on sokalens.com, the rail's top went
    24518 -> 3669 -> 73856 while the page chased it, dragging the reader 74,000px
    down a list that kept growing. The design pins it instead -- a sticky column
    holding the sheet and the bar together -- which cannot chase anything. See
    the CSS for `.bd-ticket[data-open="true"]`.
  */
  const [nextPage, setNextPage] = useState(1);
  const [hasMore, setHasMore] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [visibleMatchLimit, setVisibleMatchLimit] = useState(BUILDER_MATCH_BATCH_SIZE);
  const loadMoreRef = useRef<HTMLDivElement | null>(null);
  const sharedLegsRef = useRef<ReturnType<typeof decodeBuilderSelection>>([]);
  const horizonDates = useMemo(() => Array.from({ length: 7 }, (_, offset) => nairobiDate(offset)), []);

  const candidates = useMemo(() => predictions.flatMap((prediction) => prediction.markets.map((item) => {
    const odds = candidateOdds(prediction, item);
    return {
      ...prediction,
      ...item,
      ...odds,
      candidateId: builderLegId(prediction.id, prediction.matchId, item.marketKey, item.selection),
    };
  })).filter((candidate) => !hasBuilderLegStarted(toLeg(candidate))), [predictions]);

  useEffect(() => {
    const update = () => {
      const result = pruneBuilderSelection();
      setSelected(result.active);
      if (result.expired.length > 0) {
        setExpiredBuilderLegs((current) => [...new Map([...current, ...result.expired].map((leg) => [leg.id, leg])).values()]);
      }
    };
    const ticket = new URLSearchParams(window.location.search).get("ticket") ?? "";
    sharedLegsRef.current = decodeBuilderSelection(ticket);
    setSharedTicketPresent(ticket.length > 0);
    if (ticket.length > 0 && sharedLegsRef.current.length === 0) setSharedExpiredCount(1);
    update();
    const interval = window.setInterval(update, 1_000);
    window.addEventListener(BUILDER_EVENT_NAME, update);
    return () => {
      window.clearInterval(interval);
      window.removeEventListener(BUILDER_EVENT_NAME, update);
    };
  }, []);

  useEffect(() => {
    if (sharedLegsRef.current.length === 0) return;
    const imported = sharedLegsRef.current.flatMap((reference) => {
      const candidate = candidates.find((item) =>
        item.id === reference.predictionId &&
        item.matchId === reference.matchId &&
        item.marketKey === reference.marketKey &&
        item.selection === reference.selection
      );
      if (!candidate) return [];
      const leg = toLeg(candidate);
      return hasBuilderLegStarted(leg) ? [] : [leg];
    });
    setSharedResolvedCount(imported.length);
    setSharedExpiredCount(sharedLegsRef.current.length - imported.length);
    if (imported.length === 0) return;
    const next = [...new Map([...pruneBuilderSelection().active, ...imported].map((leg) => [leg.id, leg])).values()];
    writeBuilderSelection(next);
    setSelected(next);
  }, [candidates]);

  useEffect(() => {
    setPredictions((current) => {
      const byFixture = new Map(current.map((prediction) => [prediction.matchId, prediction]));
      for (const prediction of initialPredictions) byFixture.set(prediction.matchId, prediction);
      return [...byFixture.values()].sort((left, right) => Date.parse(left.kickoffAt) - Date.parse(right.kickoffAt));
    });
  }, [initialPredictions]);

  useEffect(() => {
    const id = setInterval(() => router.refresh(), 30_000);
    return () => clearInterval(id);
  }, [router]);

  const loadMore = useCallback(async () => {
    if (loadingMore || !hasMore) return;
    setLoadingMore(true);
    try {
      const pages = await Promise.allSettled(horizonDates.map(async (date) => {
        const response = await fetch(`/api/predictions/horizon?date=${encodeURIComponent(date)}&page=${nextPage}`);
        if (!response.ok) throw new Error(`Prediction page failed with ${response.status}`);
        return response.json() as Promise<HorizonPage>;
      }));
      const loaded = pages
        .filter((page): page is PromiseFulfilledResult<HorizonPage> => page.status === "fulfilled")
        .map((page) => page.value);
      const incoming = loaded.flatMap((page) => page.predictions ?? []);
      setPredictions((current) => {
        const byFixture = new Map(current.map((prediction) => [prediction.matchId, prediction]));
        for (const prediction of incoming) byFixture.set(prediction.matchId, prediction);
        return [...byFixture.values()].sort((left, right) => Date.parse(left.kickoffAt) - Date.parse(right.kickoffAt));
      });
      setHasMore(loaded.some((page) => (page.page ?? nextPage) * (page.limit ?? 100) < (page.total ?? 0)));
      setNextPage((page) => page + 1);
    } finally {
      setLoadingMore(false);
    }
  }, [hasMore, horizonDates, loadingMore, nextPage]);

  useEffect(() => {
    const target = loadMoreRef.current;
    if (!target || !hasMore) return;
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) void loadMore();
    }, { rootMargin: "600px" });
    observer.observe(target);
    return () => observer.disconnect();
  }, [hasMore, loadMore]);

  const persist = (next: BuilderLeg[]) => {
    setSelected(next);
    writeBuilderSelection(next);
  };

  const selectedIds = useMemo(() => new Set(selected.map((leg) => leg.id)), [selected]);
  const accumulated = totalOdds(selected);
  const sharedTicketPath = selected.length > 0 ? `/multi-match-builder?ticket=${encodeURIComponent(encodeBuilderSelection(selected))}` : "/multi-match-builder";
  const sharedTicketText = selected.length > 0
    ? ["My SokaLens Builder slip", ...selected.map((leg, index) => `${index + 1}. ${leg.matchTitle} — ${marketLabel(leg.marketKey, locale)}: ${marketSelectionLabel(leg.marketKey, leg.selection, locale)}`), "Statistical analysis, not a guarantee."].join("\n")
    : "Build a multi-match analysis on SokaLens.";
  const countries = useMemo(() => [...new Set(candidates.map((item) => footballCountry(item.country, item.leagueName)))].sort(), [candidates]);
  const leagues = useMemo(() => country === "all" ? [] : [...new Set(candidates.filter((item) => footballCountry(item.country, item.leagueName) === country).map((item) => item.leagueName))].sort(), [candidates, country]);
  const markets = useMemo(() => [...new Set(candidates.map((item) => item.marketKey))].sort((a, b) => marketLabel(a, locale).localeCompare(marketLabel(b, locale), locale)), [candidates, locale]);
  const visible = useMemo(() => candidates.filter((item) =>
    (country === "all" || footballCountry(item.country, item.leagueName) === country) &&
    (league === "all" || item.leagueName === league) &&
    (market === "all" || item.marketKey === market) &&
    (!search.trim() || `${item.homeTeam} ${item.awayTeam} ${item.leagueName} ${item.marketKey} ${item.selection}`.toLowerCase().includes(search.trim().toLowerCase()))
  ), [candidates, country, league, market, search]);
  const matches = useMemo(() => {
    const grouped = new Map<number, BuilderCandidate[]>();
    for (const item of visible) grouped.set(item.matchId, [...(grouped.get(item.matchId) ?? []), item]);
    return [...grouped.values()].sort((a, b) => {
      const recommendation = (items: BuilderCandidate[]) => Math.max(...items.map((item) =>
        (item.isBestBet ? 100 : 0) + item.confidence + (item.probability * 20),
      ));
      const scoreDifference = recommendation(b) - recommendation(a);
      return scoreDifference || Date.parse(a[0].kickoffAt) - Date.parse(b[0].kickoffAt);
    });
  }, [visible]);
  const displayedMatches = useMemo(() => matches.slice(0, visibleMatchLimit), [matches, visibleMatchLimit]);

  useEffect(() => setVisibleMatchLimit(BUILDER_MATCH_BATCH_SIZE), [country, league, market]);

  useEffect(() => {
    const target = loadMoreRef.current;
    if (!target || visibleMatchLimit >= matches.length) return;
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        setVisibleMatchLimit((current) => Math.min(current + BUILDER_MATCH_BATCH_SIZE, matches.length));
      }
    }, { rootMargin: "600px" });
    observer.observe(target);
    return () => observer.disconnect();
  }, [matches.length, visibleMatchLimit]);

  const toggle = (candidate: BuilderCandidate) => {
    if (selectedIds.has(candidate.candidateId)) persist(selected.filter((leg) => leg.id !== candidate.candidateId));
    else persist([...selected, toLeg(candidate)]);
  };

  const needLabel =
    selected.length === 0
      ? "Pick your first selection"
      : selected.length < MIN_LEGS
        ? `${MIN_LEGS - selected.length} more for a multi`
        : `${selected.length} ${selected.length === 1 ? "selection" : "selections"}`;

  /*
    Mirrors the design's bar: count pill, what the ticket needs, running total.
    It is a real <button> controlling the rail by id, so the state it toggles is
    announced rather than only drawn.
  */
  const ticketBar = (
    <button
      aria-controls="builder-ticket"
      aria-expanded={ticketOpen}
      className="bd-bar"
      onClick={() => setTicketOpen((open) => !open)}
      type="button"
    >
      <span className="bd-bar__count">{selected.length}</span>
      <span className="bd-bar__copy">
        <span className="bd-bar__title">Your ticket</span>
        <span className="bd-bar__need">{needLabel}</span>
      </span>
      <span className="bd-bar__total">
        <span className="bd-bar__total-label">Total odds</span>
        <strong>{formatOdds(accumulated, oddsFormat)}</strong>
      </span>
    </button>
  );

  const ticketRail = (
    <aside
      aria-labelledby="builder-selection-title"
      className="bd-ticket"
      data-open={ticketOpen ? "true" : "false"}
      id="builder-ticket"
    >
      <div className="bd-ticket__head">
        <h2 id="builder-selection-title">Your ticket</h2>
        <span>
          {selected.length} {selected.length === 1 ? "selection" : "selections"}
          {selected.length > 0 && selected.length < MIN_LEGS ? ` · ${MIN_LEGS - selected.length} more for a multi` : ""}
        </span>
      </div>

      <div className="bd-ticket__total">
        <span>Total odds</span>
        <strong>{formatOdds(accumulated, oddsFormat)}</strong>
        <small>Market odds where available; otherwise fair odds.</small>
      </div>

      <div className="bd-ticket__controls">
        <OddsFormatControl value={oddsFormat} onChange={setOddsFormat} />
        {selected.length > 0 ? (
          <ShareButton
            compact
            entityId={selected.map((leg) => leg.id).join("~")}
            entityType="builder"
            path={sharedTicketPath}
            text={sharedTicketText}
            title="SokaLens Builder slip"
          />
        ) : null}
      </div>

      {sharedTicketPresent && sharedResolvedCount === 0 ? (
        <div className="bd-notice bd-notice--full" role="status" aria-live="polite">
          <strong>This shared slip has expired.</strong>
          <span>Every selection has started or is no longer available, so no stale odds are shown.</span>
        </div>
      ) : sharedExpiredCount > 0 || expiredBuilderLegs.length > 0 ? (
        <div className="bd-notice" role="status" aria-live="polite">
          <strong>Some selections were removed.</strong>
          <span>Matches are removed at kickoff because their status and odds can change.</span>
          {expiredBuilderLegs.length > 0 ? <span>{expiredBuilderLegs.map((leg) => leg.matchTitle).join(", ")}</span> : null}
        </div>
      ) : null}

      {selected.length > 0 ? (
        <>
          <ol className="bd-legs">
            {selected.map((leg) => (
              <li key={leg.id}>
                <span className="bd-leg__copy">
                  {leg.groupLabel ? (
                    <span className="bd-leg__tag">ACCA · {leg.groupLabel}</span>
                  ) : null}
                  {/*
                    INLINE, not stacked -- and checked against builder.html
                    rather than assumed from the ACCA card, which stacks. The
                    ticket leg's team line there is a single 13px row reading
                    `[crest] Home v [crest] Away`, with the crest at 1.4em of
                    13px, which is 18.2 and takes the review's 16. The ticket is
                    a narrow sticky rail and the leg's subject is the SELECTION;
                    spending two lines per leg on the fixture would halve how
                    many legs a reader can see at once.
                  */}
                  {leg.homeTeam && leg.awayTeam ? (
                    <MatchupIdentity
                      awayTeam={leg.awayTeam}
                      awayTeamLogo={leg.awayTeamLogo}
                      className="bd-leg__teams"
                      homeTeam={leg.homeTeam}
                      homeTeamLogo={leg.homeTeamLogo}
                      layout="inline"
                      size={16}
                    />
                  ) : (
                    <strong>{leg.matchTitle}</strong>
                  )}
                  <span className="bd-leg__market">
                    {marketLabel(leg.marketKey, locale)} · {marketSelectionLabel(leg.marketKey, leg.selection, locale)}
                  </span>
                </span>
                <span className="bd-leg__price">
                  <strong>{formatOdds(leg.odds, oddsFormat)}</strong>
                  <small>{leg.oddsType === "fair" ? "Fair" : leg.oddsType === "market" ? "Market" : "No odds"}</small>
                </span>
                <button
                  aria-label={`Remove ${leg.matchTitle}`}
                  className="bd-leg__remove"
                  onClick={() => persist(selected.filter((item) => item.id !== leg.id))}
                  type="button"
                >
                  &times;
                </button>
              </li>
            ))}
          </ol>
          <button className="mp-btn" onClick={() => persist([])} type="button">Clear all</button>
        </>
      ) : (
        <>
          {/*
            The design answers an empty ticket with three numbered steps rather
            than a paragraph: on a panel with nothing in it, what a reader wants
            is the order of operations, not prose about where legs come from.
          */}
          <ol className="bd-steps">
            <li><span>1</span><p>Tap prices on the left, or generate an ACCA</p></li>
            <li><span>2</span><p>Review legs and total odds here</p></li>
            <li><span>3</span><p>Save or share your ticket</p></li>
          </ol>
          <p className="bd-ticket__empty">
            {/* Both routes in, named, because the ticket is shared between the
                two modes and a reader looking at an empty one in Pick mode can
                still fill it from Generate -- or from the ACCA catalogue. */}
            Add a market from the list, switch to <strong>Generate for me</strong>, or add a whole
            ACCA from the <a href="/acca-predictions">ACCA catalogue</a>.
          </p>
        </>
      )}

      {/*
        No rail ad here. Ad spec 3.2, first bullet: never place an ad "inside or
        overlapping the Builder ticket (sidebar or bottom bar)", and the 3.1
        table gives the Builder no rail at all -- its only slots are `top` and
        one in-feed unit. This aside IS the ticket: it is the page's primary
        control, sticky on desktop and a bottom bar on mobile, and a 300x600
        pinned to the bottom of it was an ad inside the ticket.
      */}
    </aside>
  );

  return (
    <div className="mp mp-page">
      <main className="mp__inner" id="content">
        {/* Ad spec 3.1: directly under the site header, above the page title. */}
        <AdSlot format="top" placement="page-top" />

        <PageHeader
          accent="ticket"
          eyebrow="Rolling seven-day horizon"
          summary="Add any number of selections. Match Result, totals, BTTS, corners, cards and every other published market are labelled independently; pre-built ACCAs stay named groups inside the same ticket."
          title="Build your"
        >
          <nav aria-label="Breadcrumb" className="mp-crumbs">
            <a href="/">Home</a>
            <span aria-hidden="true">/</span>
            <span className="mp-crumbs__here">Builder</span>
          </nav>
        </PageHeader>

        {/*
          The two ways to fill one ticket, as the design merges them.
          `/acca-generator` and `/multi-match-builder` were separate pages with
          separate filters and separate empty states, and a reader who could not
          find a slip in one had no idea the other existed. The ticket is shared
          -- the generator writes into it through `addBuilderLegs` -- so the modes
          are genuinely two inputs to one thing rather than two pages stapled
          together.

          A tablist rather than links: switching mode must not discard a ticket
          in progress, and a navigation would re-mount the page.
        */}
        <div aria-label="How do you want to build?" className="bd-modes" role="tablist">
          <button
            aria-selected={mode === "build"}
            className="bd-mode"
            onClick={() => setMode("build")}
            role="tab"
            type="button"
          >
            <span className="bd-mode__label">Pick selections</span>
            <span className="bd-mode__sub">Choose each leg yourself</span>
          </button>
          <button
            aria-selected={mode === "generate"}
            className="bd-mode"
            onClick={() => setMode("generate")}
            role="tab"
            type="button"
          >
            <span className="bd-mode__label">Generate for me</span>
            <span className="bd-mode__sub">Set legs &amp; odds, we compose it</span>
          </button>
        </div>

        <div className="td-body">
          <div className="td-main">
            {mode === "build" ? (
              <>
                {/* The design heads this column, because without a heading the
                    filters read as the page's filters rather than this panel's. */}
                <div className="pf-sectionhead">
                  <h2>Browse published ACCAs</h2>
                  <span>Tap a price to add it · one pick per match</span>
                </div>
                <div className="bd-filters">
                  <label className="lg-search">
                    <svg aria-hidden="true" fill="none" height="16" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24" width="16">
                      <circle cx="11" cy="11" r="7" />
                      <path d="M20 20l-4-4" />
                    </svg>
                    <input
                      aria-label="Search team, league or market"
                      onChange={(event) => setSearch(event.target.value)}
                      placeholder="Team, league, or market"
                      type="search"
                      value={search}
                    />
                  </label>
                  <div className="td-toolbar">
                    <label className="ac-field">
                      <span>Country</span>
                      <select value={country} onChange={(event) => { setCountry(event.target.value); setLeague("all"); }}>
                        <option value="all">All countries</option>
                        {countries.map((item) => <option key={item} value={item}>{countryLabel(item)}</option>)}
                      </select>
                    </label>
                    <label className="ac-field">
                      <span>League</span>
                      <select disabled={country === "all"} value={league} onChange={(event) => setLeague(event.target.value)}>
                        <option value="all">{country === "all" ? "Select a country first" : "All leagues"}</option>
                        {leagues.map((item) => <option key={item} value={item}>{item}</option>)}
                      </select>
                    </label>
                    <label className="ac-field">
                      <span>Market</span>
                      <select value={market} onChange={(event) => setMarket(event.target.value)}>
                        <option value="all">All markets</option>
                        {markets.map((item) => <option key={item} value={item}>{marketLabel(item, locale)}</option>)}
                      </select>
                    </label>
                  </div>
                  <p className="bd-count" aria-live="polite">
                    {visible.length} selections across {matches.length} matches
                  </p>
                </div>

                {matches.length > 0 ? (
                  <div className="bd-matches">
                    {displayedMatches.map((items) => {
                      const match = items[0];
                      return (
                        <article className="bd-match" key={match.matchId}>
                          <header className="bd-match__head">
                            {/*
                              Stacked, at 24px. Verified against builder.html
                              rather than inferred from the ACCA card beside it:
                              its card head is
                              `display: flex; flex-direction: column; gap: 4px`
                              with each side a row carrying a crest at 1.4em of
                              the head's 18px type, which is 25.2.

                              The two-column `columns` layout this used to pass
                              is still the default for the ~12 other
                              MatchupIdentity call sites; only this surface's
                              artboard asks for the stack.
                            */}
                            <MatchupIdentity
                              awayTeam={match.awayTeam}
                              awayTeamLogo={match.awayTeamLogo}
                              className="bd-match__teams"
                              homeTeam={match.homeTeam}
                              homeTeamLogo={match.homeTeamLogo}
                              layout="stacked"
                              size={24}
                            />
                            <span className="bd-match__meta">
                              <strong>{match.leagueName}</strong>
                              <span><LocalTime value={match.kickoffAt} /></span>
                            </span>
                          </header>
                          {/*
                            Why this card is where it is. The list is already
                            ordered by `isBestBet`, confidence and probability
                            (see `matches` above) and said nothing about it, so
                            the top card looked arbitrary. The tags are read
                            from the same fields that ordering uses, so they
                            cannot describe a different ranking than the one
                            the reader is scrolling.
                          */}
                          <RecommendationTags tags={matchTags(items)} />
                          <div className="bd-markets">
                            {items.map((item) => {
                              const selectedNow = selectedIds.has(item.candidateId);
                              return (
                                <button
                                  aria-label={`${selectedNow ? "Remove" : "Add"} ${marketLabel(item.marketKey, locale)}: ${marketSelectionLabel(item.marketKey, item.selection, locale)}`}
                                  aria-pressed={selectedNow}
                                  className="bd-market"
                                  disabled={hasBuilderLegStarted(toLeg(item))}
                                  key={item.candidateId}
                                  onClick={() => toggle(item)}
                                  type="button"
                                >
                                  <span className="bd-market__copy">
                                    <strong>{marketLabel(item.marketKey, locale)}</strong>
                                    <small>{marketSelectionLabel(item.marketKey, item.selection, locale)}</small>
                                  </span>
                                  <span className="bd-market__foot">
                                    <span className="bd-market__price">
                                      <strong>{formatOdds(item.odds, oddsFormat)}</strong>
                                      <small>{item.oddsType === "fair" ? "Fair odds" : item.oddsType === "market" ? "Market odds" : "Unavailable"}</small>
                                    </span>
                                    <span className="bd-market__action">{selectedNow ? "Added" : "Add"}</span>
                                  </span>
                                </button>
                              );
                            })}
                          </div>
                        </article>
                      );
                    })}
                  </div>
                ) : (
                  <div className="ac-empty" aria-live="polite">
                    <h3>No published markets match these filters</h3>
                    <p>Clear a filter, or switch to Generate and let the engine compose a slip.</p>
                  </div>
                )}

                {/*
                  The Builder's one in-feed unit. Ad spec 3.1: "After the match
                  cards, before 'Show more matches'", which is the element
                  below; V31's builder.html has exactly one in-feed unit, in
                  exactly this position.

                  It replaces a cadence that dropped a block after every tenth
                  match card and a second trailing block after the whole mode
                  switch. A Builder match card is 150-400px tall depending on
                  how many markets it carries, so a per-card cadence could not
                  honour section 5's 30% density cap -- that cap is written
                  against page height, and a block every ten cards on a slate of
                  tall cards is a different density every time the filters move.
                  One unit at a fixed position is measurable.

                  Only in Pick mode: V31's builder-generate.html carries no
                  in-feed unit, and the Generate panel is a form, which 3.2
                  excludes.
                */}
                <FeedAdBlock />

                <div className="bd-loadmore" ref={loadMoreRef} aria-live="polite">
                  {visibleMatchLimit < matches.length
                    ? `Scroll to show more matches (${displayedMatches.length} of ${matches.length})`
                    : loadingMore
                      ? "Loading more published matches\u2026"
                      : hasMore ? "Scroll to load more matches" : "All published matches in the seven-day horizon are loaded."}
                </div>
              </>
            ) : (
              <AccaGenerator />
            )}
          </div>

          {ticketRail}
        </div>

        {ticketBar}

        <div className="mp-disclaimer">
          <span aria-label="Eighteen plus only" className="mp-disclaimer__age">18+</span>
          <p>
            Statistical estimates, never guaranteed outcomes. Use predictions as analysis, not
            financial advice.
          </p>
          <a href="/responsible-gambling">Responsible gambling</a>
        </div>

        <div className="mp-tail">
          <ResponsibleNote />
        </div>
      </main>
    </div>
  );
}
