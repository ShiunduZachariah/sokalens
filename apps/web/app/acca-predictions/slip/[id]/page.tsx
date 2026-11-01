import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import { ContentPageShell } from "../../../components/ContentPageShell";
import { LinkCard } from "../../../components/LinkCard";
import { RecommendationTags } from "../../../components/RecommendationTags";
import { slipTags } from "../../../lib/recommendationTags";
import { fetchAccaSlip } from "../../../data/accaApi";
import { marketLabel, marketSelectionLabel } from "../../../lib/markets";
import { encodeSlipToken } from "../../../lib/slipToken";
import { absoluteUrl, canonicalPath } from "../../../site";
import { localePath } from "../../../i18n/config";
import { requestLocaleContext } from "../../../i18n/server";
import { AccaSlipLegs, AccaSlipSummary } from "../../AccaSlipDetail";
import { accaSlipView, slipMarketLabel, slipWindowLabel } from "../../accaSlip";
import type { AccaBundle } from "../../accaBundles";

/**
 * One stored ACCA, at a URL of its own.
 *
 * ## Why `/acca-predictions/slip/{id}` and not `/acca-predictions/{id}`
 *
 * `/acca-predictions/[marketSlug]` already occupies that level, and Next cannot
 * tell a slip id from a market slug in one dynamic segment. Routing both
 * through `[marketSlug]` and branching on "does this look like a uuid" would
 * make `generateStaticParams` for the five market hubs a lie and would send
 * every typo'd market slug to a slip lookup. A static `slip` segment is
 * unambiguous and takes priority over the sibling dynamic one.
 *
 * ## Why this page is dynamic and uncached
 *
 * Leg statuses change while the matches are being played — that is what the
 * page is for. `fetchAccaSlip` reads the API's uncached by-id source, and
 * caching the page around it would show a settled slip as in progress for as
 * long as the cache held.
 *
 * ## Why it is not indexed
 *
 * A slip is one arrangement of selections out of a catalogue of thousands, it
 * is generated rather than written, and it stops being interesting the moment
 * its last match finishes. The market hubs and the index are the pages built to
 * rank; this one exists so a reader can open, share and return to a specific
 * ticket. `follow` stays on, so the fixture links are still crawled.
 */
type Props = { params: Promise<{ id: string }> };

export const dynamic = "force-dynamic";

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { id } = await params;
  const path = `/acca-predictions/slip/${id}`;
  let slip: AccaBundle | null = null;
  try {
    slip = await fetchAccaSlip(id);
  } catch {
    slip = null;
  }
  if (!slip) {
    return { title: "ACCA not found - SokaLens", robots: { index: false, follow: true } };
  }
  const market = slipMarketLabel(slip.marketKey, "en");
  const title = `${slip.legs.length}-leg ${market} ACCA`;
  const description = `Every leg of this ${slip.legs.length}-leg ${market} ACCA, with each selection's fair odds and the combined price. Statistical analysis, not a guarantee.`;
  return {
    title: `${title} - SokaLens`,
    description,
    alternates: { canonical: canonicalPath(path) },
    robots: { index: false, follow: true },
    openGraph: { title, description, url: absoluteUrl(path) },
  };
}

export default async function AccaSlipPage({ params }: Props) {
  const { locale } = await requestLocaleContext();
  const { id } = await params;

  let slip: AccaBundle | null;
  try {
    slip = await fetchAccaSlip(id);
  } catch {
    // An outage is not a missing slip, and 404ing on one would tell search
    // engines to drop a URL that is fine. Rendering the shell with an apology
    // keeps the page's own navigation available.
    return (
      <ContentPageShell
        crumbs={[{ href: "/acca-predictions", label: "ACCAs" }, { label: "ACCA" }]}
        eyebrow="System ACCAs"
        locale={locale}
        summary="The ACCA catalogue is temporarily unavailable. The ticket is still there — this page could not read it."
        title="Can't load this ACCA"
      >
        <LinkCard href="/acca-predictions" label="All ACCAs" locale={locale} />
      </ContentPageShell>
    );
  }
  if (!slip) notFound();

  // Read once, here: see AccaSlipDetail on why the clock is not read in render.
  const view = accaSlipView(slip, Date.now());
  const market = slipMarketLabel(slip.marketKey, locale);
  const heading = `${slip.legs.length}-leg`;
  const window = slipWindowLabel(view.firstKickoffAt, view.lastKickoffAt);
  const path = `/acca-predictions/slip/${slip.id}`;
  const shareLabel = `${slip.legs.length}-leg ${market} ACCA`;
  const shareText = [
    `${shareLabel} on SokaLens`,
    ...view.legs.map((leg, index) => `${index + 1}. ${leg.homeTeam} vs ${leg.awayTeam} — ${marketLabel(leg.marketKey, locale)}: ${marketSelectionLabel(leg.marketKey, leg.selection, locale)}`),
    "Statistical analysis, not a guarantee.",
  ].join("\n");
  // The slip token is what the share card unfurls from, and it stays even
  // though this URL resolves on its own: the card is drawn from the query
  // string without a lookup, so a social crawler that never reaches the API
  // still describes the right ticket.
  const sharePath = `${path}?slip=${encodeURIComponent(encodeSlipToken({
    label: shareLabel,
    odds: Number.isFinite(slip.combinedFairOdds) && slip.combinedFairOdds > 0 ? slip.combinedFairOdds.toFixed(2) : undefined,
    legs: view.legs.map((leg) => ({
      market: marketLabel(leg.marketKey, locale),
      selection: marketSelectionLabel(leg.marketKey, leg.selection, locale),
    })),
  }))}`;

  return (
    <ContentPageShell
      accent={market}
      beforeBody={
        <>
          {view.result ? (
            <div className={`as-result as-result--${view.result.tone}`} role="status">
              <strong>{view.result.label}</strong>
              <span>{view.result.line}</span>
            </div>
          ) : null}
          {/* What shape this ticket is: markets, countries, how long until it
              settles, where its price sits. A reader who arrived from a share
              link did not choose these legs and has nothing else to go on. */}
          <RecommendationTags max={4} tags={slipTags(slip)} />
          {/* One segment per leg, in kick-off order. Decorative: every leg's
              own state is on its row, as a word rather than a colour. */}
          <div aria-hidden="true" className="as-segments">
            {view.legs.map((leg) => (
              <i
                className={`as-segments__seg as-segments__seg--${view.result ? leg.state : "open"}`}
                key={`${leg.matchId}:${leg.marketKey}:${leg.selection}`}
              />
            ))}
          </div>
        </>
      }
      crumbs={[
        { href: "/acca-predictions", label: "ACCAs" },
        { label: shareLabel },
      ]}
      eyebrow={window ? `System-generated · ${window} · UTC` : "System-generated · UTC"}
      locale={locale}
      rail={
        <>
          <AccaSlipSummary
            bundle={slip}
            shareLabel={shareLabel}
            sharePath={sharePath}
            shareText={shareText}
            view={view}
          />
          <div className="td-panel td-panel--links">
            <h2>Keep exploring</h2>
            <LinkCard href="/acca-predictions" label="All ACCAs" locale={locale} />
            <LinkCard href="/acca-generator" label="Generate an ACCA" locale={locale} />
            <LinkCard href="/multi-match-builder" label="Open the Builder" locale={locale} />
          </div>
        </>
      }
      summary="Every leg comes from a separate fixture, each one independently qualified by the engine before it was bundled."
      title={heading}
    >
      <AccaSlipLegs locale={locale} view={view} />

      <section className="as-next">
        <div>
          <h2>Browse the other ACCAs</h2>
          <p>Filter by market, country or size — or build your own ticket.</p>
        </div>
        <div className="as-next__actions">
          <Link className="mp-btn" href={localePath("/acca-predictions", locale)}>All ACCAs</Link>
          <Link className="mp-btn mp-btn--dark" href={localePath("/multi-match-builder", locale)}>
            Build your own
          </Link>
        </div>
      </section>
    </ContentPageShell>
  );
}
