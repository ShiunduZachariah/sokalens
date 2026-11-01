"use client";

import { Fragment, memo, useCallback, useEffect, useRef, useState } from "react";
import { OddsFormatControl, useOddsFormat } from "../components/OddsFormatControl";
import { AccaBundleCard } from "./AccaBundleCard";
import { marketLabel, marketSelectionLabel } from "../data/livePredictions";
import { countryLabel } from "../lib/competitionOrder";

import { formatOdds } from "../lib/odds";
import { encodeSlipToken } from "../lib/slipToken";
import { addBuilderLegs, builderLegId, type BuilderLeg } from "../multi-match-builder/builder-state";
import type { AccaBundle, AccaPage } from "./accaBundles";
import { normalizeAccaPage } from "../data/accaApi";
import { useLocale } from "../components/locale/LocaleProvider";
import type { Locale } from "../i18n/config";

type Props = { initialPage: AccaPage; initialMarket?: string; initialCountry?: string; initialSelection?: string; seed?: string };

// Exported so the generator composes its Builder legs the same way the
// catalogue does. A second copy of this mapping would drift, and the fields it
// sets -- groupId, source, oddsType -- are what the Builder reads to group and
// price a slip.
export function bundleLegs(bundle: AccaBundle): BuilderLeg[] {
  return bundle.legs.map((leg) => ({
    id: builderLegId(leg.id, leg.matchId, leg.marketKey, leg.selection),
    predictionId: leg.id,
    matchId: leg.matchId,
    matchTitle: `${leg.homeTeam} vs ${leg.awayTeam}`,
    /* The ACCA leg already carries both sides and both crests, so the Builder
       ticket draws the same pair rather than re-splitting the joined title. */
    homeTeam: leg.homeTeam,
    awayTeam: leg.awayTeam,
    homeTeamLogo: leg.homeTeamLogo,
    awayTeamLogo: leg.awayTeamLogo,
    marketKey: leg.marketKey,
    selection: leg.selection,
    odds: leg.fairOdds > 0 && Number.isFinite(leg.fairOdds) ? leg.fairOdds : null,
    oddsType: leg.fairOdds > 0 && Number.isFinite(leg.fairOdds) ? "fair" : "unavailable",
    kickoffAt: leg.kickoffAt,
    matchStatus: leg.matchStatus,
    source: "acca",
    groupId: bundle.id,
    groupLabel: bundle.label,
  }));
}

/**
 * Where a shared ACCA points, and what its card can know.
 *
 * The fragment is what scrolls the reader to the right slip, and it stays. But
 * a fragment never reaches the server, so on its own the link unfurls as the
 * ACCA index rather than as this ticket. The `slip` parameter carries the few
 * strings the share card draws, so the preview describes the slip that was
 * actually shared without any lookup.
 */
function accaSharePath(bundle: AccaBundle, locale: Locale) {
  const slip = encodeSlipToken({
    label: bundle.label,
    odds: Number.isFinite(bundle.combinedFairOdds) && bundle.combinedFairOdds > 0
      ? bundle.combinedFairOdds.toFixed(2)
      : undefined,
    legs: bundle.legs.map((leg) => ({
      market: marketLabel(leg.marketKey, locale),
      selection: marketSelectionLabel(leg.marketKey, leg.selection, locale),
    })),
  });
  return `/acca-predictions?slip=${encodeURIComponent(slip)}#acca-${encodeURIComponent(bundle.id)}`;
}

function bundleShareText(bundle: AccaBundle, locale: Locale) {
  const legs = bundle.legs.map((leg, index) => `${index + 1}. ${leg.homeTeam} vs ${leg.awayTeam} — ${marketLabel(leg.marketKey, locale)}: ${marketSelectionLabel(leg.marketKey, leg.selection, locale)}`);
  return [`${bundle.label} on SokaLens`, ...legs, "Statistical analysis, not a guarantee."].join("\n");
}

const AccaBundleGrid = memo(function AccaBundleGrid({
  added,
  bundles,
  oddsFormat,
  locale,
  onAdd,
}: {
  added: string;
  bundles: AccaBundle[];
  oddsFormat: ReturnType<typeof useOddsFormat>[0];
  locale: Locale;
  onAdd: (bundle: AccaBundle) => void;
}) {
  return (
    <div className="ac-grid">
      {bundles.map((bundle) => (
        <Fragment key={bundle.id}>
          <AccaBundleCard
            added={added === bundle.id}
            bundle={bundle}
            locale={locale}
            oddsFormat={oddsFormat}
            onAdd={onAdd}
            sharePath={accaSharePath(bundle, locale)}
            shareText={bundleShareText(bundle, locale)}
          />
          {/*
            No in-grid ad blocks any more.

            Ad spec 3.1 gives the ACCA list exactly one in-feed position --
            "after the tickets, before How to read" -- and that is a page-level
            position, rendered by `acca-predictions/page.tsx` below this grid.
            Blocks interleaved between slips were a second and third in-feed
            unit on the same page, and they broke the grid's own equal-height
            rows: a full-width block between two cards in a two-column grid
            leaves the odd slip beside empty space.
          */}
        </Fragment>
      ))}
    </div>
  );
});

export function AccaExplorer({ initialPage, initialMarket = "all", initialCountry = "all", initialSelection = "", seed = "" }: Props) {
  const { locale } = useLocale();
  const [market, setMarket] = useState(initialMarket);
  const [country, setCountry] = useState(initialCountry);
  const [league, setLeague] = useState("all");
  const [legs, setLegs] = useState("all");
  const [search, setSearch] = useState("");
  const [oddsFormat, setOddsFormat] = useOddsFormat();
  const [cursor, setCursor] = useState(initialPage.nextCursor);
  const [bundles, setBundles] = useState(initialPage.bundles);
  const [loading, setLoading] = useState(false);
  const [added, setAdded] = useState("");
  const initialRender = useRef(true);
  const loadInFlight = useRef(false);
  const seenCursors = useRef(new Set<string>());

  const loadPage = useCallback(async (nextPage: number, replace: boolean, signal?: AbortSignal) => {
    if (!replace && loadInFlight.current) return;
    if (!replace && (!cursor || seenCursors.current.has(cursor))) return;
    if (!replace) loadInFlight.current = true;
    setLoading(true);
    const query = new URLSearchParams({ tab: market, country, league, legs, locale, limit: "10" });
    // Same seed on every page, or the catalogue reorders mid-scroll and the
    // cursor points into a different sequence.
    if (seed) query.set("seed", seed);
    if (market === initialMarket && initialSelection) query.set("selection", initialSelection);
    if (!replace && cursor) query.set("cursor", cursor);
    // A filter corner the background composer never sampled is not the same
    // fact as "no such ACCA is possible". On the first page only -- a cursor
    // pages through stored inventory -- let the API compose one from the same
    // vetted pool when the catalogue query comes back empty.
    if (replace || !cursor) query.set("generate", "1");
    try {
      const response = await fetch(`/api/accas?${query}`, { signal, headers: { Accept: "application/json" } });
      if (!response.ok) return;
      const result = normalizeAccaPage(await response.json());
      if (!replace && cursor) seenCursors.current.add(cursor);
      setBundles((current) => {
        if (replace) return result.bundles;
        const existing = new Set(current.map((item) => item.id));
        return [...current, ...result.bundles.filter((item) => !existing.has(item.id))];
      });
      setCursor(result.nextCursor && result.nextCursor !== cursor ? result.nextCursor : undefined);
    } catch {
      // Keep the already-rendered catalogue usable during a transient network failure.
    } finally {
      if (!replace) loadInFlight.current = false;
      if (!signal?.aborted) setLoading(false);
    }
  }, [country, cursor, league, legs, locale, market, initialMarket, initialSelection, seed]);

  useEffect(() => {
    if (initialRender.current) {
      initialRender.current = false;
      return;
    }
    const controller = new AbortController();
    seenCursors.current.clear();
    void loadPage(1, true, controller.signal);
    return () => controller.abort();
  }, [country, league, legs, locale, market]);

  const addBundle = useCallback((bundle: AccaBundle) => {
    addBuilderLegs(bundleLegs(bundle));
    setAdded(bundle.id);
    window.setTimeout(() => setAdded(""), 2200);
  }, []);

  const filteredBundles = bundles.filter((bundle) => {
    const needle = search.trim().toLowerCase();
    return !needle || bundle.legs.some((leg) => `${leg.homeTeam} ${leg.awayTeam} ${leg.leagueName} ${leg.marketKey} ${leg.selection}`.toLowerCase().includes(needle));
  });

  const clearFilters = useCallback(() => {
    setMarket("all");
    setCountry("all");
    setLeague("all");
    setLegs("all");
    setSearch("");
  }, []);

  return (
    <>
      <section aria-label="Filter system ACCAs" className="ac-filters">
        <div className="ac-filters__head">
          <h2>Published ACCAs</h2>
          <span className="ac-filters__count">
            {initialPage.total > filteredBundles.length
              ? `Showing ${filteredBundles.length} of ${initialPage.total.toLocaleString()}`
              : `Showing ${filteredBundles.length}`}
          </span>
        </div>

        <div className="ac-filters__grid">
          <label className="ac-field ac-field--wide">
            Search teams or leagues
            <input
              onChange={(event) => setSearch(event.target.value)}
              placeholder="e.g. Girona, Liga MX"
              type="search"
              value={search}
            />
          </label>
          <label className="ac-field">
            Market
            <select onChange={(event) => setMarket(event.target.value)} value={market}>
              <option value="all">All markets</option>
              {initialPage.facets.markets.map((item) => (
                <option key={item} value={item}>{marketLabel(item, locale)}</option>
              ))}
            </select>
          </label>
          <label className="ac-field">
            Country
            <select onChange={(event) => setCountry(event.target.value)} value={country}>
              <option value="all">All countries</option>
              {initialPage.facets.countries.map((item) => (
                <option key={item} value={item}>{countryLabel(item)}</option>
              ))}
            </select>
          </label>
          {/* Leagues are meaningless until a country narrows them, which is
              what the artboard draws as a dashed, disabled control. */}
          <label className="ac-field">
            League
            <select
              disabled={country === "all"}
              onChange={(event) => setLeague(event.target.value)}
              value={league}
            >
              <option value="all">{country === "all" ? "Choose a country first" : "All leagues"}</option>
              {(initialPage.facets.leagueCountries[country] ?? []).map((item) => (
                <option key={item} value={item}>{item}</option>
              ))}
            </select>
          </label>
          <label className="ac-field">
            Legs
            <select onChange={(event) => setLegs(event.target.value)} value={legs}>
              <option value="all">Any size</option>
              {initialPage.facets.legCounts.map((item) => (
                <option key={item} value={item}>{item} legs</option>
              ))}
            </select>
          </label>
        </div>

        <div className="ac-filters__foot">
          <span>Odds format</span>
          <OddsFormatControl onChange={setOddsFormat} value={oddsFormat} />
          <span className="ac-filters__hint">
            Fair odds = the model&rsquo;s probability expressed as a price.
          </span>
        </div>
      </section>

      {filteredBundles.length > 0 ? (
        <>
          <AccaBundleGrid
            added={added}
            bundles={filteredBundles}
            locale={locale}
            oddsFormat={oddsFormat}
            onAdd={addBundle}
          />
          <div aria-live="polite" className="ac-loadmore">
            {loading ? (
              "Loading more ACCAs…"
            ) : cursor ? (
              <button className="mp-btn" onClick={() => void loadPage(2, false)} type="button">
                Load more ACCAs
              </button>
            ) : null}
          </div>
        </>
      ) : (
        <div className="ac-empty">
          <h3>No ACCAs match these filters</h3>
          <button className="mp-btn" onClick={clearFilters} type="button">
            Clear filters
          </button>
        </div>
      )}
    </>
  );
}
