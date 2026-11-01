import { AccaTypeTabs } from "../components/AccaTypeTabs";
import { AdSlot } from "../components/AdSlot";
import { FeedAdBlock } from "../components/FeedAdBlock";
import { AccaRings, ArrowRightIcon } from "../components/match/icons";
import { ResponsibleNote } from "../components/ResponsibleNote";
import { fetchAccaPage } from "../data/accaApi";
import type { Metadata } from "next";
import { absoluteUrl, canonicalPath } from "../site";
import { decodeSlipToken } from "../lib/slipToken";
import { AccaExplorer } from "./AccaExplorer";
import { requestLocaleContext } from "../i18n/server";
import { cookies } from "next/headers";
import { ACCA_SEED_COOKIE, resolveAccaSeed } from "../lib/accaSeed";

const TITLE = "Acca predictions and accumulator analysis - SokaLens";
const DESCRIPTION =
  "Careful accumulator prediction context with confidence, fair odds, market filters, and responsible analysis.";

/**
 * A shared ACCA gets a card describing that ACCA.
 *
 * The link keeps its `#acca-<id>` fragment, which is what scrolls the reader to
 * the right slip -- but a fragment never reaches the server, so on its own the
 * link unfurls as this index page. `?slip=` carries the few strings the card
 * draws, put there by the share control where the bundle is in scope.
 */
export async function generateMetadata({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}): Promise<Metadata> {
  const { slip } = await searchParams;
  const token = typeof slip === "string" ? slip : "";
  const summary = token ? decodeSlipToken(token) : null;

  const base: Metadata = {
    title: TITLE,
    description: DESCRIPTION,
    alternates: { canonical: canonicalPath("/acca-predictions") },
    robots: { index: true, follow: true },
  };

  if (!summary) return base;

  const heading = summary.label || `${summary.legs.length}-leg ACCA`;
  const image = absoluteUrl(`/og/slip?slip=${encodeURIComponent(token)}`);
  return {
    ...base,
    // One reader's slip, not a page of its own: the canonical above already
    // points at the index, and this variant should not compete with it.
    robots: { index: false, follow: true },
    openGraph: {
      title: `${heading} on SokaLens`,
      description: "Every leg is a published SokaLens prediction, with its fair odds and confidence.",
      url: absoluteUrl("/acca-predictions"),
      images: [{ url: image, width: 1200, height: 630, alt: heading }],
    },
    twitter: { card: "summary_large_image", title: `${heading} on SokaLens`, images: [image] },
  };
}

// CI has no production API connection, so a prerendered page would permanently
// capture the empty fallback. Keep HTML live while the underlying API fetches
// remain shared and revalidated every 15 seconds.
export const dynamic = "force-dynamic";

export default async function AccaPredictionsPage() {
  // The API shuffles by md5(composition_hash || seed), so a different seed
  // surfaces a different slice of a 2000+ slip catalogue instead of the same
  // ten every time.
  //
  // A Server Component cannot set a cookie, so absent one this mints a fresh
  // seed PER PAGE LOAD rather than per visitor. That is deliberate here: it
  // spreads exposure across the catalogue, and it is safe because the seed is
  // handed to AccaExplorer and reused for every load-more request, so the
  // cursor keeps walking one consistent sequence within a page view. If the
  // cookie is ever written (middleware or a route handler), this reads it and
  // the ordering becomes stable per visitor with no further change.
  const seed = resolveAccaSeed((await cookies()).get(ACCA_SEED_COOKIE)?.value);
  const { locale } = await requestLocaleContext();
  // Whether the catalogue is empty and whether it could not be read are
  // different facts, and this page used to render them identically.
  //
  // When the upstream call failed, the catch fell back to total: 0 and the
  // section below said "the engine has not published enough legs" -- a
  // statement about the prediction engine, shown when the real problem was that
  // nothing had been asked. A deploy that restarts the API is enough to trigger
  // it, so the page reported a product condition during an infrastructure blip
  // and sent whoever read it looking in the wrong place.
  let initialPage;
  let catalogueUnavailable = false;
  try {
    initialPage = await fetchAccaPage(new URLSearchParams({ tab: "all", period: "seven_days", limit: "10", seed, generate: "1" }));
  } catch {
    catalogueUnavailable = true;
    initialPage = { bundles: [], total: 0, page: 1, pageSize: 10, facets: { markets: [], countries: [], leagues: [], legCounts: [], leagueCountries: {} } };
  }

  return (
    <div className="mp mp-page">
      <main className="mp__inner" id="content">
        {/* Ad spec 3.1: directly under the site header, above the page title --
            not below the hero, where it used to sit. */}
        <AdSlot format="top" placement="page-top" />

        <section className="ac-hero">
          <AccaRings />
          <div className="ac-hero__copy">
            <span className="hm-hero__eyebrow">System-generated · Rolling seven-day horizon</span>
            <h1>
              ACCA predictions with clear markets and <em>fair odds.</em>
            </h1>
            <p className="ac-hero__lede">
              Browse system-generated ACCAs separately from the user-controlled Builder. Filter by
              market, country, or size, choose your preferred odds format, then explicitly copy a
              complete ACCA into your Builder ticket if you want to add more selections.
            </p>
            <div className="hm-hero__actions">
              <a className="mp-btn mp-btn--primary" href="/multi-match-builder">Generate an ACCA</a>
              <a className="mp-btn" href="/multi-match-builder">
                Build your own
                <ArrowRightIcon />
              </a>
            </div>
          </div>

          {/*
            The caveat sits beside the claim rather than at the foot of the
            page. This page sells combinations, and combinations are where the
            uncertainty compounds fastest.
          */}
          <aside className="ac-note">
            <div className="ac-note__head">
              <span aria-label="Eighteen plus only" className="ac-note__age">18+</span>
              <h2>Use predictions as analysis</h2>
            </div>
            <p>
              Predictions are informational and not guaranteed. They should not be treated as
              financial advice, and outcomes can change with team news, market movement, or match
              conditions.
            </p>
            <a href="/responsible-gambling">Responsible use</a>
          </aside>
        </section>

        <AccaTypeTabs current={null} locale={locale} />

        <section aria-labelledby="published-accas" className="mp-section" id="published-accas">
          <h2 className="sr-only" id="published-accas">Published ACCAs</h2>
          {initialPage.total > 0 ? (
            <AccaExplorer initialPage={initialPage} seed={seed} />
          ) : catalogueUnavailable ? (
            <div className="ac-empty">
              <h3>ACCAs are temporarily unavailable</h3>
              <p>
                This is a problem on our side, not a verdict on today&rsquo;s fixtures. Reloading in
                a moment usually resolves it.
              </p>
              <a className="mp-btn" href="/acca-predictions">Try again</a>
            </div>
          ) : (
            <div className="ac-empty">
              <h3>No eligible ACCA right now</h3>
              <p>
                The engine has not published enough independent probability-bearing legs for an ACCA
                yet. This updates automatically as fixture analysis clears the quality gates.
              </p>
            </div>
          )}
        </section>

        <FeedAdBlock />

        <section aria-label="How to read this page" className="ac-education">
          <div className="ac-edu">
            <span className="mp-section__num">01</span>
            <h2>How to read this page</h2>
            <p>
              A higher confidence value means the model sees a stronger signal inside the available
              data. It does not remove match uncertainty.
            </p>
          </div>
          <div className="ac-edu">
            <span className="mp-section__num">02</span>
            <h2>Why bundles are cautious</h2>
            <p>
              More legs add uncertainty, so every bundle is framed as analysis — never a guaranteed
              outcome.
            </p>
          </div>
          <div className="ac-edu ac-edu--cta">
            <h2>Pick your own legs, one per fixture.</h2>
            <a className="mp-btn" href="/multi-match-builder">
              Build your own analysis
              <ArrowRightIcon />
            </a>
          </div>
        </section>

        <div className="mp-tail">
          <ResponsibleNote />
        </div>
      </main>
    </div>
  );
}
