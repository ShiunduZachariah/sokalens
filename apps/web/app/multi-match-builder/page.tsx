import type { Metadata } from "next";
import { fetchPredictionHorizon, type LivePrediction } from "../data/livePredictions";
import { absoluteUrl, canonicalPath } from "../site";
import { decodeBuilderSelection } from "./builder-state";
import { BuilderClient } from "./BuilderClient";

const TITLE = "Multi-Match Builder - SokaLens";
const DESCRIPTION =
  "Build your own unlimited multi-match ticket from every published SokaLens predictions market across the next seven days.";

/**
 * A shared slip gets a card describing the slip.
 *
 * The ticket lives in `?ticket=`, and `opengraph-image` files only receive
 * route params -- so the card is a route handler and this points at it with the
 * same token the reader shared. Without this the most deliberately-shared URL
 * on the site unfurls as the generic builder page.
 *
 * The card is only claimed when a ticket is actually present. A bare
 * /multi-match-builder keeps the default card, which is the honest one for it.
 */
export async function generateMetadata({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}): Promise<Metadata> {
  const { ticket } = await searchParams;
  const token = typeof ticket === "string" ? ticket : "";
  const legs = token ? decodeBuilderSelection(token) : [];

  const base: Metadata = {
    title: TITLE,
    description: DESCRIPTION,
    alternates: { canonical: canonicalPath("/multi-match-builder") },
    robots: { index: true, follow: true },
  };

  if (legs.length === 0) return base;

  const shared = `${legs.length}-leg Builder slip on SokaLens`;
  const image = absoluteUrl(`/og/slip?ticket=${encodeURIComponent(token)}`);
  return {
    ...base,
    // A shared ticket is one person's slip, not a page worth indexing on its
    // own: the canonical above already points at the builder itself.
    robots: { index: false, follow: true },
    openGraph: {
      title: shared,
      description: "Every leg is a published SokaLens prediction. Open the slip to see the reasoning.",
      url: absoluteUrl(`/multi-match-builder?ticket=${encodeURIComponent(token)}`),
      images: [{ url: image, width: 1200, height: 630, alt: shared }],
    },
    twitter: { card: "summary_large_image", title: shared, images: [image] },
  };
}

export const dynamic = "force-dynamic";

export default async function MultiMatchBuilderPage() {
  let predictions: LivePrediction[] = [];
  try {
    // Render a useful first batch immediately. BuilderClient streams every
    // remaining API page as the visitor scrolls instead of serializing the
    // entire seven-day fixture horizon into the initial HTML response.
    predictions = await fetchPredictionHorizon(7, false, 10);
  } catch {
    predictions = [];
  }
  return <BuilderClient predictions={predictions} />;
}
