import type { Metadata } from "next";
import { fetchPredictionHorizon, type LivePrediction } from "../data/livePredictions";
import { canonicalPath } from "../site";
import { BuilderClient } from "../multi-match-builder/BuilderClient";

export const dynamic = "force-dynamic";

/**
 * The Builder, opened on Generate.
 *
 * The design merges this page and `/multi-match-builder` into one Builder with
 * two modes, and the two always shared a ticket -- the generator writes legs
 * through `addBuilderLegs`. They were separate pages with separate filters and
 * separate empty states, so a reader who found nothing in one had no idea the
 * other existed.
 *
 * This route is kept rather than redirected, and its canonical points at the
 * Builder. Both URLs are in `staticUrls` and both have been crawled, so a 301
 * here is a real decision about which one keeps what it has earned -- not a
 * refactor. A canonical consolidates the two for search while leaving every
 * existing link working and the 301 still available later. `/acca-generator` is
 * dropped from the sitemap in the same change, because a canonicalised duplicate
 * should not be submitted.
 */
export const metadata: Metadata = {
  title: "ACCA Generator - SokaLens",
  description:
    "Build an ACCA from vetted predictions using your preferred legs, markets, country and fair-odds range.",
  alternates: { canonical: canonicalPath("/multi-match-builder") },
};

export default async function AccaGeneratorPage() {
  let predictions: LivePrediction[] = [];
  try {
    // The same first batch the Builder's own route fetches: a reader who
    // switches to Pick mode must not find an empty list, and BuilderClient
    // streams the rest as they scroll.
    predictions = await fetchPredictionHorizon(7, false, 10);
  } catch {
    predictions = [];
  }
  return <BuilderClient initialMode="generate" predictions={predictions} />;
}
