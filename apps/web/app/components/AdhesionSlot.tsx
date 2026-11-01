"use client";

import { usePathname } from "next/navigation";
import { useState } from "react";
import { AdSlot } from "./AdSlot";
import { stripLocalePrefix } from "../i18n/config";
import { ADS_ENABLED } from "../lib/adsEnabled";

/**
 * Routes that never carry the adhesion band.
 *
 * The 2026-09-30 ad spec excludes the Builder explicitly, and the reason is
 * structural rather than editorial: on tablet and mobile the Builder's ticket
 * is itself a sticky bottom bar that opens a sheet. An ad band pinned above the
 * tab bar would either sit under that ticket or fight it for the same edge, and
 * the ticket is the page's primary control.
 *
 * `/multi-match-builder` and `/acca-generator` are both listed because the
 * design merges them into one Builder page; whichever of the two routes ends up
 * serving it, the exclusion holds. Matching is by prefix so a future
 * `/multi-match-builder/<something>` inherits it rather than quietly gaining a
 * band.
 */
const EXCLUDED_PREFIXES = ["/multi-match-builder", "/acca-generator"];

/**
 * The mobile adhesion ad band, fixed above the tab bar.
 *
 * A client component because the exclusion depends on the current route, and it
 * lives in the root layout so a new page cannot ship without it -- the same
 * reasoning as the footer's age and responsibility treatment.
 *
 * It renders nothing at all when unfilled: AdSlot returns null unless
 * entitlement has resolved to ads-allowed and real creative exists. So on a Pro
 * account, or while `adsenseSlots` has no id for this placement, there is no
 * element, no border and no reserved height -- which is what the design's
 * "ads unfilled" boards show.
 */
function EnabledAdhesionSlot() {
  const pathname = usePathname();
  const route = stripLocalePrefix(pathname);
  /*
   * Dismissal, per spec section 3's "320x50 in a 60px bar, with a close
   * button".
   *
   * React state and not storage. This component lives in the root layout, so
   * the state survives every client-side navigation for as long as the reader
   * is on the site, which is what "dismissed" means here. Persisting it would
   * make the band a one-time unit rather than a per-visit one, and would put a
   * consent-adjacent decision in local storage for no revenue reason.
   */
  const [dismissed, setDismissed] = useState(false);

  if (dismissed) return null;

  if (EXCLUDED_PREFIXES.some((prefix) => route === prefix || route.startsWith(`${prefix}/`))) {
    return null;
  }

  return <AdSlot format="adhesion" onDismiss={() => setDismissed(true)} placement="mobile-adhesion" />;
}

/** Renders nothing while ads are switched off; see `lib/adsEnabled`. */
export function AdhesionSlot() {
  return ADS_ENABLED ? <EnabledAdhesionSlot /> : null;
}
