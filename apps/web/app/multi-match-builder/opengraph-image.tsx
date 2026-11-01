import { SHARE_CARD_CONTENT_TYPE, SHARE_CARD_SIZE, brandShareCard } from "../lib/shareCard";

/**
 * The card for the builder itself. A shared ticket overrides it from
 * generateMetadata, because the slip lives in a query parameter that the
 * `opengraph-image` convention never receives.
 */
export const alt = "Multi-match builder on SokaLens";
export const size = SHARE_CARD_SIZE;
export const contentType = SHARE_CARD_CONTENT_TYPE;

export default async function Image() {
  return brandShareCard("Build one ticket across every published market.");
}
