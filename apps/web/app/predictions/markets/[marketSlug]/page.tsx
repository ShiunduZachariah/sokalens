import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import { cookies } from "next/headers";
import { fetchMarketPredictions } from "../../../data/livePredictions";
import { fromLivePrediction } from "../../../data/predictionItems";
import { FavoriteButton } from "../../../components/account/FavoriteButton";
import { marketHeadingParts, marketHubIsIndexable, marketSeoDefinition, marketSeoDefinitions } from "../../../lib/seo";
import { marketHubInventory } from "../../../lib/marketInventory";
import { validViewerTimezone } from "../../../lib/viewerTimezone";
import { canonicalPath } from "../../../site";
import { requestLocaleContext } from "../../../i18n/server";
import { localePath } from "../../../i18n/config";
import { PredictionListShell } from "../../../components/PredictionListShell";
import { MarketReadingGuide } from "../../../components/MarketReadingGuide";

type MarketPageProps = { params: Promise<{ marketSlug: string }> };

export async function generateMetadata({ params }: MarketPageProps): Promise<Metadata> {
  const definition = marketSeoDefinition((await params).marketSlug);
  if (!definition) return { title: "Prediction market not found - SokaLens", robots: { index: false, follow: true } };
  const path = `/predictions/markets/${definition.slug}`;
  return {
    title: `${definition.title} - SokaLens`,
    description: definition.description,
    alternates: { canonical: canonicalPath(path) },
    // Most hubs need live selections to merit indexing. Over 2.5 and BTTS
    // also have durable, specific reading guides, so they remain useful when
    // today's model feed has no qualifying fixture.
    robots: { index: marketHubIsIndexable(await marketHubInventory(definition.slug), definition.slug), follow: true },
  };
}

export default async function MarketPage({ params }: MarketPageProps) {
  const { locale } = await requestLocaleContext();
  const definition = marketSeoDefinition((await params).marketSlug);
  if (!definition) notFound();
  const timezone = validViewerTimezone((await cookies()).get("sokalens_timezone")?.value);
  const predictionsResult = await fetchMarketPredictions(definition.marketKey, undefined, timezone).catch(() => null);
  const predictions = predictionsResult?.filter((item) => !definition.selection || item.selection === definition.selection) ?? [];

  const faq = {
    "@context": "https://schema.org",
    "@type": "FAQPage",
    mainEntity: [
      {
        "@type": "Question",
        name: `How are ${definition.heading} generated?`,
        acceptedAnswer: { "@type": "Answer", text: "SokaLens publishes a market only when its model and data-quality checks support that fixture. Predictions are estimates, not guarantees." },
      },
    ],
  };

  /*
   * A market hub is a prediction list filtered to one market, so it renders the
   * same shell as Today and Weekend rather than its own card stack.
   *
   * The rows it used to draw by hand were missing everything the shared row has:
   * no crests, no 1/X/2 marker, no confidence column, no favourite control, and
   * no filters at all -- which on `btts`, the hub with the most inventory, meant
   * a reader scrolled several hundred undifferentiated cards. The selections are
   * already restricted to one market, so the explorer's market filter is inert
   * here and its country, league, date and search filters are what the page
   * gains.
   */
  const items = predictions.map((prediction) => fromLivePrediction(prediction, locale, timezone));

  /*
   * The other fourteen hubs, as a compact pill row above the feed.
   *
   * These links used to sit in a footer section. They have to stay somewhere on
   * every hub, not only on an empty one: the market hubs are the property's
   * best-converting pages and they are each other's nearest related page, so
   * this row is the densest useful internal linking on the site. A pill row
   * rather than the rail's 48px link cards, because fifteen of those would push
   * the feed itself below the fold.
   */
  const otherMarkets = (
    <nav aria-label="Other prediction markets" className="td-othermarkets">
      {marketSeoDefinitions
        .filter((item) => item.slug !== definition.slug)
        .map((item) => (
          <Link
            className="td-subnav__pill"
            href={localePath(`/predictions/markets/${item.slug}`, locale)}
            key={item.slug}
            prefetch={false}
          >
            {item.heading}
          </Link>
        ))}
    </nav>
  );

  const lead = (
    <>
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: JSON.stringify(faq) }} />
      <div className="td-marketlead">
        <FavoriteButton
          entityId={definition.slug}
          entityType="market"
          hideForGuests
          label={definition.heading}
          returnPath={`/predictions/markets/${definition.slug}`}
        />
        <p>
          Confidence scores how well supported a pick is, from data completeness, sample size, model
          agreement and similar checks. It is not the chance of the outcome, and not a promise of one. Read the reported record and its sample limitations on the{" "}
          <Link href={localePath("/results", locale)}>results page</Link>.
        </p>
        <p>The feed shows a capped sample of published selections from each day of the coming week. Open a fixture for its full market detail.</p>
      </div>
      <MarketReadingGuide slug={definition.slug} predictions={predictionsResult === null ? null : predictions} locale={locale} timezone={timezone} />
      <details className="td-note">
        <summary>How are {definition.heading} generated?</summary>
        <p>SokaLens publishes a market only when its model and data-quality checks support that fixture. Predictions are estimates, not guarantees.</p>
      </details>
      {otherMarkets}
    </>
  );

  const headingParts = marketHeadingParts(definition.heading);

  return (
    <PredictionListShell
      accent={headingParts.accent}
      breadcrumb={definition.heading}
      emptyState={
        <div className="ac-empty" aria-live="polite">
          <h3>{predictionsResult === null ? "Predictions are temporarily unavailable" : "No qualifying fixtures right now"}</h3>
          <p>
            {predictionsResult === null
              ? "The prediction feed could not be loaded. Try again shortly; this does not mean there are no qualifying matches."
              : "No published model selections for this market are available in the current feed. New selections appear after publication when the available data supports them."}
          </p>
          <Link className="mp-btn" href={localePath("/predictions/today", locale)}>
            Today&apos;s predictions
          </Link>
        </div>
      }
      eyebrow={`Market · ${definition.heading}`}
      items={items}
      leadContent={lead}
      locale={locale}
      stats={[
        { label: "Shown selections", value: predictionsResult === null ? "Unavailable" : items.length, accent: true },
        { label: "Shown competitions", value: predictionsResult === null ? "Unavailable" : new Set(items.map((item) => item.league)).size },
      ]}
      summary={definition.description}
      title={headingParts.title}
    />
  );
}
