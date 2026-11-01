"use client";

import { useCallback, useEffect, useState } from "react";

import { bundleLegs } from "../acca-predictions/AccaExplorer";
import { addBuilderLegs } from "../multi-match-builder/builder-state";

// How long the whole search may take before it answers honestly.
//
// Eight seconds is the worst case a reader waits, chosen against a phone on a
// poor connection rather than a desktop: long enough for the background
// composer to stock a corner and for two or three round trips to complete,
// short enough that nobody is left watching a spinner wondering.
const ACCA_SEARCH_BUDGET_MS = 8_000;
// One stalled request must not consume the budget. Slightly above the API's own
// 2750ms ceiling so a server that is about to answer is not cut off.
const ACCA_ATTEMPT_TIMEOUT_MS = 3_200;
// Long enough to give the composer a chance and to spare a metered connection,
// short enough to fit two retries inside the budget.
const ACCA_RETRY_DELAY_MS = 1_800;
import { validateAccaGeneratorInput } from "../acca-generator/validation";
import type { AccaBundle } from "../acca-predictions/accaBundles";
import { normalizeAccaPage } from "../data/accaApi";
import { marketLabel, marketSelectionLabel } from "../data/livePredictions";
import { countryLabel } from "../lib/competitionOrder";

import { MatchupIdentity } from "./MatchupIdentity";
import { useLocale } from "./locale/LocaleProvider";
import { fixtureHref } from "../lib/fixtureHref";

import { LocalTime } from "./LocalTime";
import { RecommendationTags } from "./RecommendationTags";
import { selectionTags, slipTags } from "../lib/recommendationTags";
type Facets = ReturnType<typeof normalizeAccaPage>["facets"];

function GeneratorResult({ bundle }: { bundle: AccaBundle }) {
  const { locale } = useLocale();
  const [added, setAdded] = useState(false);

  // A composed ACCA exists only in this response -- it is not in the browsable
  // catalogue and a refresh loses it. Without a way to keep it, the reader's
  // only option was to copy it by hand, which the old copy told them to do.
  const add = useCallback(() => {
    addBuilderLegs(bundleLegs(bundle));
    setAdded(true);
    // Reverts so a second composition can be added without a reload, and so
    // the button stops claiming a state that is no longer what it would do.
    window.setTimeout(() => setAdded(false), 2200);
  }, [bundle]);

  return (
    <article aria-labelledby="generator-result" className="gn-result">
      <header className="gn-result__head">
        <div>
          <span className="mp-eyebrow">Generated for you</span>
          <h3 id="generator-result">{bundle.label}</h3>
          {bundle.origin === "on_demand" ? (
            <p className="gn-result__origin">
              Composed from vetted predictions matching your filters. It is not in the browsable
              catalogue, so add it to the ticket to keep it.
            </p>
          ) : null}
          {/*
            What shape this slip is, since the reader did not choose its legs.
            Two generated slips are both five rows of fixtures they have never
            heard of; the tags are the only thing that distinguishes them.
          */}
          <RecommendationTags max={4} tags={slipTags(bundle)} />
        </div>
        {Number.isFinite(bundle.combinedFairOdds) && bundle.combinedFairOdds > 0 ? (
          <div className="gn-result__odds">
            <small>Combined fair odds</small>
            <strong>{bundle.combinedFairOdds.toFixed(2)}</strong>
          </div>
        ) : null}
      </header>

      <ol className="gn-result__legs">
        {bundle.legs.map((leg) => (
          <li key={`${leg.matchId}:${leg.marketKey}:${leg.selection}`}>
            <a href={fixtureHref(leg)}>
              <MatchupIdentity
                awayTeam={leg.awayTeam}
                awayTeamLogo={leg.awayTeamLogo}
                className="gn-result__teams"
                homeTeam={leg.homeTeam}
                homeTeamLogo={leg.homeTeamLogo}
              />
              <span className="gn-result__pick">
                <strong>{marketSelectionLabel(leg.marketKey, leg.selection, locale)}</strong>
                <span>{marketLabel(leg.marketKey, locale)}</span>
                <span>{leg.leagueName} · <LocalTime value={leg.kickoffAt} /></span>
                {/* Two per leg: on a five-leg slip the row is narrow, and the
                    first two are the ones the engine asserted rather than the
                    ones derived from the price. */}
                <RecommendationTags max={2} tags={selectionTags(leg)} />
              </span>
            </a>
          </li>
        ))}
      </ol>

      <button className="mp-btn mp-btn--primary" onClick={add} type="button">
        {added
          ? "Added to ticket"
          : `Add these ${bundle.legs.length} ${bundle.legs.length === 1 ? "leg" : "legs"} to my ticket`}
      </button>
    </article>
  );
}

/*
 * The leg bounds the stepper and the input share.
 *
 * Named rather than repeated so the stepper's clamp and the input's `min`/`max`
 * cannot drift apart -- a stepper that stops at 20 beside an input that accepts
 * 50 is a validation error a reader can only discover by submitting.
 */
const MIN_GENERATOR_LEGS = 2;
const MAX_GENERATOR_LEGS = 20;

export function AccaGenerator() {
  const { locale } = useLocale();
  const [legs, setLegs] = useState("5");
  const [min, setMin] = useState("1.5");
  const [max, setMax] = useState("1000");
  const [market, setMarket] = useState("mixed");
  const [country, setCountry] = useState("all");
  const [league, setLeague] = useState("all");
  const [facets, setFacets] = useState<Facets>({ markets: [], countries: [], leagues: [], leagueCountries: {}, legCounts: [] });
  const [result, setResult] = useState<AccaBundle | null>(null);
  const [searched, setSearched] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  // Set once a first attempt has come back empty, so the wait can explain
  // itself rather than looking like a page that has stopped responding.
  const [stillLooking, setStillLooking] = useState(false);

  useEffect(() => {
    const query = new URLSearchParams({ tab: "all", period: "seven_days", limit: "1" });
    fetch(`/api/accas?${query}`).then(async (response) => response.ok ? normalizeAccaPage(await response.json()) : null)
      .then((page) => { if (page) setFacets(page.facets); }).catch(() => undefined);
  }, []);

  async function search() {
    setError("");
    let input;
    try { input = validateAccaGeneratorInput(legs, min, max); }
    catch (reason) { setError(reason instanceof Error ? reason.message : "Check the selected constraints."); return; }
    setLoading(true);
    setSearched(true);
    setStillLooking(false);
    try {
      const query = new URLSearchParams({
        legs: String(input.legs), min_fair_odds: String(input.minOdds), max_fair_odds: String(input.maxOdds),
        period: "seven_days", tab: market, country, league, limit: "1", seed: `${Date.now()}`,
        // Compose from the vetted pool when the precomputed inventory has no
        // slip for these filters, instead of reporting that none can exist.
        generate: "1",
      });
      // A miss is not a dead end. The API records the corner and the
      // background composer stocks it, usually within a cycle, so asking again
      // shortly after often succeeds where the first attempt did not.
      //
      // Bounded to ACCA_SEARCH_BUDGET_MS in total. Past that the honest answer
      // is that nothing matched -- a spinner that never resolves is worse than
      // a clear no, especially on a phone where it costs battery and data to
      // keep asking.
      //
      // Each attempt carries its own abort, so one stalled request on a bad
      // connection cannot eat the whole budget while the others never run.
      const startedAt = Date.now();
      let page = null;
      let attempt = 0;
      while (Date.now() - startedAt < ACCA_SEARCH_BUDGET_MS) {
        attempt += 1;
        if (attempt > 1) setStillLooking(true);
        const controller = new AbortController();
        const abort = window.setTimeout(() => controller.abort(), ACCA_ATTEMPT_TIMEOUT_MS);
        try {
          const response = await fetch(`/api/accas?${query}`, { signal: controller.signal });
          if (!response.ok) throw new Error("ACCA search is temporarily unavailable.");
          page = normalizeAccaPage(await response.json());
          if (page.bundles.length > 0) break;
        } catch (attemptError) {
          // An aborted attempt is not a failure of the search, only of that
          // request. Give up on the whole thing only when the budget is gone.
          if (Date.now() - startedAt >= ACCA_SEARCH_BUDGET_MS) throw attemptError;
        } finally {
          window.clearTimeout(abort);
        }
        if (Date.now() - startedAt + ACCA_RETRY_DELAY_MS >= ACCA_SEARCH_BUDGET_MS) break;
        await new Promise((resolve) => window.setTimeout(resolve, ACCA_RETRY_DELAY_MS));
      }
      if (!page) throw new Error("ACCA search is temporarily unavailable.");
      setResult(page.bundles[0] ?? null);
      setFacets((current) => page.facets.countries.length ? page.facets : current);
    } catch (reason) {
      setResult(null);
      setError(reason instanceof Error ? reason.message : "ACCA search is temporarily unavailable.");
    } finally {
      setLoading(false);
      setStillLooking(false);
    }
  }

  return (
    <section aria-labelledby="generator-controls" className="gn">
      <h3 className="sr-only" id="generator-controls">Generate an ACCA</h3>

      <div className="gn-controls">
        <label className="ac-field">
          <span>Market</span>
          <select value={market} onChange={(event) => setMarket(event.target.value)}>
            <option value="mixed">Mixed markets</option>
            {facets.markets.filter((item) => item !== "all" && item !== "mixed").map((item) => (
              <option key={item} value={item}>{marketLabel(item, locale)}</option>
            ))}
          </select>
        </label>

        <label className="ac-field">
          <span>Country</span>
          <select value={country} onChange={(event) => { setCountry(event.target.value); setLeague("all"); }}>
            <option value="all">All countries</option>
            {facets.countries.map((item) => <option key={item} value={item}>{countryLabel(item)}</option>)}
          </select>
        </label>

        <label className="ac-field">
          <span>League</span>
          <select disabled={country === "all"} value={league} onChange={(event) => setLeague(event.target.value)}>
            <option value="all">{country === "all" ? "Select a country first" : "All leagues"}</option>
            {(facets.leagueCountries[country] ?? []).map((item) => <option key={item} value={item}>{item}</option>)}
          </select>
        </label>

        {/*
          A stepper, as the design draws it, rather than a bare number input.
          Legs is the control a reader actually nudges -- "same filters, one fewer
          leg" is the commonest adjustment -- and on a phone a number spinner is a
          4px target. The input stays, so typing 12 still works and the value
          remains reachable for assistive technology.
        */}
        <div className="ac-field gn-stepper">
          <span id="gn-legs-label">Legs</span>
          <div className="gn-stepper__row">
            <button
              aria-label="Fewer legs"
              disabled={Number(legs) <= MIN_GENERATOR_LEGS}
              onClick={() => setLegs(String(Math.max(MIN_GENERATOR_LEGS, Number(legs) - 1)))}
              type="button"
            >
              &minus;
            </button>
            <input
              aria-labelledby="gn-legs-label"
              inputMode="numeric"
              max={MAX_GENERATOR_LEGS}
              min={MIN_GENERATOR_LEGS}
              onChange={(event) => setLegs(event.target.value)}
              step={1}
              type="number"
              value={legs}
            />
            <button
              aria-label="More legs"
              disabled={Number(legs) >= MAX_GENERATOR_LEGS}
              onClick={() => setLegs(String(Math.min(MAX_GENERATOR_LEGS, Number(legs) + 1)))}
              type="button"
            >
              +
            </button>
          </div>
        </div>

        <label className="ac-field">
          <span>Minimum fair odds</span>
          <input type="number" inputMode="decimal" min="1" max="1000" step="0.1" value={min} onChange={(event) => setMin(event.target.value)} />
        </label>

        <label className="ac-field">
          <span>Maximum fair odds</span>
          <input type="number" inputMode="decimal" min="1" max="1000" step="0.1" value={max} onChange={(event) => setMax(event.target.value)} />
        </label>
      </div>

      <button className="mp-btn mp-btn--primary gn-run" disabled={loading} onClick={search} type="button">
        {loading ? "Searching\u2026" : result ? "Try another" : "Find an ACCA"}
      </button>

      {error ? <p className="form-error" role="alert">{error}</p> : null}

      {/*
        Two messages, not one. "Searching…" that never changes reads as a hang;
        saying that nothing stored fits and one is being built is the difference
        between waiting and wondering. `stillLooking` is set once a first attempt
        has come back empty.
      */}
      {loading ? (
        <p className="gn-progress" role="status" aria-live="polite">
          <span aria-hidden="true" className="gn-spinner" />
          {stillLooking
            ? "No stored ACCA fits these filters yet \u2014 building one\u2026"
            : "Searching the catalogue\u2026"}
        </p>
      ) : null}

      {result ? (
        <GeneratorResult bundle={result} />
      ) : searched && !loading && !error ? (
        <div className="ac-empty" aria-live="polite">
          <h3>No ACCA found for these filters</h3>
          <p>Try a wider odds range, fewer legs, or another market.</p>
        </div>
      ) : !loading ? (
        /* Before the first search this panel was blank below the controls,
           which reads as a section that failed rather than one waiting. */
        <div className="gn-waiting">
          <h3>Your generated ACCA appears here</h3>
          <p>
            Set the filters above and press Find an ACCA. You can review every leg before
            adding it to your ticket.
          </p>
        </div>
      ) : null}
    </section>
  );
}
