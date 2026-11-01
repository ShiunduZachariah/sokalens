import { SHARE_CARD_CONTENT_TYPE, SHARE_CARD_SIZE, brandShareCard } from "../lib/shareCard";

/**
 * The card for the ACCA index, and the fallback for a shared slip whose
 * `?slip=` token is missing or unreadable.
 */
export const alt = "Accumulator predictions on SokaLens";
export const size = SHARE_CARD_SIZE;
export const contentType = SHARE_CARD_CONTENT_TYPE;

export default async function Image() {
  return brandShareCard("Accumulators with the fair odds and confidence behind every leg.");
}
