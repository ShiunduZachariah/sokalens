import Link from "next/link";
import type { LivePrediction } from "../data/livePredictions";
import { localePath, type Locale } from "../i18n/config";
import { predictionFixturePath } from "../lib/seo";

/** A small durable guide for the two market hubs in the search recovery pilot. */
export function MarketReadingGuide({ slug, predictions, locale, timezone }: {
  slug: string;
  predictions: LivePrediction[] | null;
  locale: Locale;
  timezone: string;
}) {
  if (slug !== "over-2-5-goals" && slug !== "btts") return null;
  const over = slug === "over-2-5-goals";
  const fixtures = predictions === null ? null : [...new Map(predictions.map((item) => [item.matchId, item])).values()];
  const kickoffs = (fixtures ?? []).map((item) => Date.parse(item.kickoffAt)).filter(Number.isFinite).sort((a, b) => a - b);
  const dateFormat = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "long", year: "numeric", timeZone: timezone });
  return (
    <section className="td-note" aria-labelledby="market-reading-guide">
      <h2 id="market-reading-guide">{over ? "How to read Over 2.5 goals predictions" : "How to read BTTS predictions"}</h2>
      <p>{over
        ? "Over 2.5 means at least three goals between the two teams. Scores of 2–1 and 3–0 clear the line; 1–1 and 2–0 do not. This page selects the Over 2.5 line, not every total-goals prediction."
        : "BTTS Yes means each team scores at least once; BTTS No means at least one side does not score. Read the named selection on each row: this page can include both Yes and No predictions."}</p>
      <p>{over
        ? "Over 2.5 and both teams to score answer different questions. A 3–0 result clears Over 2.5 without BTTS; a 1–1 result has BTTS without clearing Over 2.5."
        : "BTTS is not the same as a high-scoring match. A 1–1 result satisfies BTTS Yes but stays under 2.5 goals; a 3–0 result clears Over 2.5 but satisfies BTTS No."}</p>
      <p>
        Open a fixture to compare the published selection with the team history, goals profile and
        available match analysis. Check the sample and missing data before comparing confidence
        figures. A higher probability does not establish betting value without a price, and past
        results do not guarantee the next result.
      </p>
      <p>{fixtures === null
        ? "The current fixture list could not be loaded. Market definitions remain available; no coverage count is inferred from the failed request."
        : `${fixtures.length} ${fixtures.length === 1 ? "fixture" : "fixtures"} in the current model feed for this page. Coverage changes with publication and data availability; this is not a list of every match being played.`}</p>
      {kickoffs.length > 0 ? <p>Fixture dates in this loaded feed: {dateFormat.format(kickoffs[0])} to {dateFormat.format(kickoffs[kickoffs.length - 1])} ({timezone}).</p> : null}
      {fixtures && fixtures.length > 0 ? (
        <>
          <h3>Explore the current fixture analysis</h3>
          <ul>
            {fixtures.slice(0, 3).map((item) => (
              <li key={item.matchId}>
                <Link href={localePath(predictionFixturePath(item), locale)} prefetch={false}>
                  {item.homeTeam} vs {item.awayTeam}
                </Link> — {item.leagueName}
              </li>
            ))}
          </ul>
        </>
      ) : null}
      <p>
        Compare with <Link href={localePath(`/predictions/markets/${over ? "btts" : "over-2-5-goals"}`, locale)} prefetch={false}>
          {over ? "BTTS predictions" : "Over 2.5 predictions"}
        </Link>, plan ahead with <Link href={localePath("/predictions/tomorrow", locale)} prefetch={false}>tomorrow&apos;s fixtures</Link>,
        or read <Link href={localePath("/results", locale)} prefetch={false}>reported performance and its coverage notes</Link>.
      </p>
    </section>
  );
}
