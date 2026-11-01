import Link from "next/link";
import { cookies } from "next/headers";
import { ContentPageShell } from "../components/ContentPageShell";
import { DateTabs } from "../components/DateTabs";
import { LinkCard } from "../components/LinkCard";
import { MatchupIdentity } from "../components/MatchupIdentity";
import { StackedBar } from "../components/StackedBar";
import { ViewerTimezone } from "../components/ViewerTimezone";
import { fetchHitRateReport, fetchPredictionResults, type PredictionResultRow } from "../data/predictionResults";
import { resolveSelectedDay } from "../lib/dateWindow";
import { formatMarket } from "../lib/markets";
import { validViewerTimezone } from "../lib/viewerTimezone";
import { canonicalPath } from "../site";
import { requestLocaleContext } from "../i18n/server";
import { localePath } from "../i18n/config";
import { fixtureHref } from "../lib/fixtureHref";

export const metadata = {
  title: "Performance status: prediction results and accuracy - SokaLens",
  description: "Settled SokaLens predictions, market results, and verified accuracy by market.",
  alternates: { canonical: canonicalPath("/results") },
};

type MatchSummary = {
  matchId: number;
  /** The joined "Home vs Away", kept as the fallback for a payload that
   *  predates the two sides being served separately. */
  title: string;
  homeTeam: string;
  awayTeam: string;
  homeTeamLogo: string;
  awayTeamLogo: string;
  league: string;
  href: string;
  won: number;
  lost: number;
  voided: number;
};

/**
 * One row per match, not one per prediction.
 *
 * The design groups the settled day by fixture, and the reason is the arithmetic
 * a reader does: a day with 600 settled markets across 90 matches is 600
 * indistinguishable table rows, and no reader counts them. Ninety rows each
 * carrying their own hit rate answer "how did we do on this match", which is the
 * question. The per-market record is still published in full -- it is what the
 * overall figures above are computed from.
 */
function summarise(rows: PredictionResultRow[]): MatchSummary[] {
  const byMatch = new Map<number, MatchSummary>();
  for (const row of rows) {
    const existing = byMatch.get(row.matchId) ?? {
      matchId: row.matchId,
      title: row.matchTitle,
      homeTeam: row.homeTeam,
      awayTeam: row.awayTeam,
      homeTeamLogo: row.homeTeamLogo,
      awayTeamLogo: row.awayTeamLogo,
      league: [row.country, row.leagueName].filter(Boolean).join(" · "),
      href: fixtureHref(row),
      won: 0,
      lost: 0,
      voided: 0,
    };
    // `pending` is neither settled nor void, so it contributes to no segment and
    // to no denominator. A pending market on an otherwise settled match must not
    // drag that match's hit rate down.
    if (row.outcome === "won") existing.won += 1;
    else if (row.outcome === "lost") existing.lost += 1;
    else if (row.outcome === "void") existing.voided += 1;
    byMatch.set(row.matchId, existing);
  }
  return [...byMatch.values()]
    .filter((match) => match.won + match.lost + match.voided > 0)
    .sort((left, right) => {
      const rate = (match: MatchSummary) => {
        const decided = match.won + match.lost;
        return decided > 0 ? match.won / decided : -1;
      };
      return rate(right) - rate(left);
    });
}

function hitRate(won: number, lost: number) {
  const decided = won + lost;
  return decided > 0 ? `${Math.round((won / decided) * 100)}%` : "—";
}

export default async function ResultsPage({ searchParams }: { searchParams?: Promise<Record<string, string | string[] | undefined>> }) {
  const { locale } = await requestLocaleContext();
  const params = (await searchParams) ?? {};
  const timezone = validViewerTimezone((await cookies()).get("sokalens_timezone")?.value);
  const date = resolveSelectedDay({
    date: typeof params.date === "string" ? params.date : undefined,
    day: typeof params.day === "string" ? params.day : undefined,
  }, new Date(), timezone);
  const [report, daily] = await Promise.all([
    fetchHitRateReport().catch(() => ({ settled: 0, won: 0, lost: 0, void: 0, hitRate: 0, markets: [] })),
    // `null` means the request failed, which is NOT the same as a day with
    // nothing settled. Collapsing the two is what let this page publish
    // "0 settled" for 2026-10-04 -- a day with roughly nineteen thousand
    // settled predictions behind a query that timed out.
    fetchPredictionResults(date, timezone).catch(() => null),
  ]);

  const dayUnavailable = daily === null;
  const matches = summarise(daily?.rows ?? []);

  // The day's settled count comes from the API's own total, not from summing
  // the rows this page happened to receive.
  //
  // Those are not the same number and never were: the request asks for one page
  // of 100, so summing it reported "100 settled" on every busy day -- a figure
  // that is the page size, not a measurement. On 2026-10-03 the page said 100
  // where the API's own total was 15,053.
  const settledOnDay = daily?.total ?? 0;
  const predictionsShown = matches.reduce((sum, match) => sum + match.won + match.lost + match.voided, 0);
  // The list below is one page of the day, ordered by kickoff. Say so when it
  // is not the whole day, rather than letting a partial list read as complete.
  const listIsPartial = predictionsShown < settledOnDay;

  return (
    <ContentPageShell
      accent="performed"
      beforeBody={
        <>
          <ViewerTimezone timezone={timezone} />
          <DateTabs basePath="/results" label="Result date" timezone={timezone} variant="refreshed" />
        </>
      }
      crumbs={[{ label: "Performance" }]}
      eyebrow="Settled predictions · since launch"
      locale={locale}
      rail={
        <div className="td-panel td-panel--links">
          <h2>Keep exploring</h2>
          <LinkCard href="/best-bets" label="Best Bets" locale={locale} />
          <LinkCard href="/predictions/today" label="Today&rsquo;s predictions" locale={locale} />
          <LinkCard href="/multi-match-builder" label="Generate an ACCA" locale={locale} />
        </div>
      }
      stats={[
        { label: "Settled", value: report.settled },
        { label: "Won", value: report.won, accent: true },
        { label: "Lost", value: report.lost },
        { label: "Void", value: report.void },
      ]}
      summary="Review recorded wins, losses and void results by match and market. Missing or unconfirmed outcomes cannot establish accuracy."
      title="How we"
    >
      <section className="section" aria-labelledby="results-correction-note">
        <h2 id="results-correction-note">BTTS &amp; Total Goals results are under review</h2>
        <p>
          We identified a grading error that applied a 2.5-goal line to selections
          published at other goal lines. Some historical losses in this combined
          market are incorrect. Its historical hit rate and overall figures that
          include it should not be used to judge model performance until the audited
          correction is complete. This notice concerns the combined market, not
          standalone BTTS or goal-total selections.
        </p>
      </section>
      {/* The headline figure, with the formula written out beside it. A reader
          who cannot see how the number is computed has no reason to trust it. */}
      <section aria-labelledby="results-hitrate" className="pf-headline">
        <div className="pf-headline__row">
          <div className="pf-headline__figure">
            <span className="pf-headline__formula" id="results-hitrate">
              Hit rate · won ÷ (won + lost)
            </span>
            <strong>{report.hitRate.toFixed(1)}%</strong>
          </div>
          <p>
            Voids (postponed or abandoned markets) are excluded from the hit rate but always
            listed.
          </p>
        </div>

        <StackedBar lost={report.lost} voided={report.void} won={report.won} />

        <div className="pf-legend">
          <span><i className="pf-swatch pf-swatch--won" />Won {report.won}</span>
          <span><i className="pf-swatch pf-swatch--lost" />Lost {report.lost}</span>
          <span><i className="pf-swatch pf-swatch--void" />Void {report.void}</span>
        </div>
      </section>

      <div className="pf-sectionhead">
        <h2>Latest settled day</h2>
        <span>
          {date}
          {dayUnavailable ? null : <> · {settledOnDay.toLocaleString()} settled</>}
          {listIsPartial ? <> · showing {predictionsShown.toLocaleString()} across {matches.length} {matches.length === 1 ? "match" : "matches"}</> : null}
        </span>
      </div>

      {dayUnavailable ? (
        <div className="ac-empty" aria-live="polite">
          <h3>This day&apos;s results could not be loaded</h3>
          <p>
            The settled-results request did not return in time. Nothing is missing from the
            record &mdash; the overall figures above are unaffected. Try again, or pick another date.
          </p>
        </div>
      ) : matches.length > 0 ? (
        <ul className="pf-matches">
          {matches.map((match) => (
            <li key={match.matchId}>
              <Link href={localePath(match.href, locale)}>
                <span className="pf-match__top">
                  <span className="pf-match__fixture">
                    {/*
                      Stacked with crests, which is how performance.html draws
                      it: its team container is `flex-direction: column;
                      gap: 4px`, and there is no score chip between the names
                      to force them inline the way the league and team result
                      rows are. Crest 20 = 1.4em of the row's 15px.

                      `title` is the fallback, not dead code: a cached payload
                      from before the serialiser carried the two sides has
                      empty names, and one crest beside a blank line would
                      read as a bug where the joined title reads as a row.
                    */}
                    {match.homeTeam && match.awayTeam ? (
                      <MatchupIdentity
                        awayTeam={match.awayTeam}
                        awayTeamLogo={match.awayTeamLogo}
                        homeTeam={match.homeTeam}
                        homeTeamLogo={match.homeTeamLogo}
                        layout="stacked"
                        size={20}
                      />
                    ) : (
                      <strong>{match.title}</strong>
                    )}
                    <span className="pf-match__meta">
                      {match.league} · {match.won + match.lost + match.voided} predictions
                    </span>
                  </span>
                  <span className="pf-match__rate">
                    <strong>{hitRate(match.won, match.lost)}</strong>
                    <span className="pf-match__meta">hit rate</span>
                  </span>
                </span>
                <StackedBar compact lost={match.lost} voided={match.voided} won={match.won} />
                <span className="pf-match__meta">
                  {match.won} won · {match.lost} lost · {match.voided} void · Open match &rarr;
                </span>
              </Link>
            </li>
          ))}
        </ul>
      ) : (
        <div className="ac-empty" aria-live="polite">
          <h3>Nothing settled for this day yet</h3>
          <p>
            Markets settle after full time. Pick another date, or check back once the day&apos;s
            fixtures have finished.
          </p>
        </div>
      )}

      {/*
        The markets we settle, with each one's own record. The design draws these
        as plain pills; carrying the per-market hit rate costs nothing and is the
        figure that tells a reader which markets to trust.
      */}
      {report.markets.length > 0 ? (
        <>
          <div className="pf-sectionhead">
            <h2>Markets we settle</h2>
          </div>
          <ul className="pf-markets">
            {report.markets.map((market) => (
              <li key={market.marketKey}>
                <span>{formatMarket(market.marketKey, locale)}</span>
                <span className="pf-markets__rate">
                  {hitRate(market.won, market.lost)}
                  <small>{market.settled} settled</small>
                </span>
              </li>
            ))}
          </ul>
        </>
      ) : null}
    </ContentPageShell>
  );
}
