import type { AccaBundle } from "../acca-predictions/accaBundles";

export function summarizeFeaturedAcca(bundle: AccaBundle) {
  const previewLegs = bundle.legs.slice(0, 2);
  const remaining = Math.max(0, bundle.legs.length - previewLegs.length);
  return {
    previewLegs,
    remainderLabel: remaining === 0 ? `All ${bundle.legs.length} selections shown` : `+${remaining} more selections`,
    leagueCount: new Set(bundle.legs.map((leg) => leg.leagueName).filter(Boolean)).size,
    marketCount: new Set(bundle.legs.map((leg) => leg.marketKey).filter(Boolean)).size,
  };
}
