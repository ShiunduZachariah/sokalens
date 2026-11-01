import { SHARE_CARD_CONTENT_TYPE, SHARE_CARD_SIZE, brandShareCard } from "./lib/shareCard";

/**
 * The card for every page that is not a fixture, team or competition.
 *
 * It replaces public/og.png, which was a 1.5MB picture reading "SokaStats" --
 * a brand this site does not use. Generating it from code means it cannot fall
 * out of date behind a rename again.
 */
export const alt = "SokaLens - data-led football predictions";
export const size = SHARE_CARD_SIZE;
export const contentType = SHARE_CARD_CONTENT_TYPE;

export default async function Image() {
  return brandShareCard("Football predictions with the reasoning kept in view.");
}
