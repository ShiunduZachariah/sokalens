"use client";

import { useEffect, useRef, useState } from "react";
import { AdSlot } from "./AdSlot";
import { useAccount } from "./account/AccountContext";

/**
 * One in-feed advertising block: exactly one unit, the full width of the
 * content column.
 *
 * ## Why one unit and not up to three
 *
 * It used to mount two units at 650px and three at 980px, measured with a
 * `ResizeObserver`. The 2026-10-15 spec removes that: section 3 gives the
 * in-feed slot "one per page, between content blocks ... Always the full width
 * of the content column", and section 6 adds the reason, which is UI-01. Three
 * centred 336px units in a 1200px column is the same mistake as the native
 * widget's squeezed box -- a row of narrow boxes that does not line up with the
 * cards above and below it. One unit spanning the column does.
 *
 * The measurement went with them. There is nothing left to measure: a single
 * full-width unit lays out from `width: 100%`, and the native container inside
 * `AdSlot` reads its own width through a container query rather than through
 * JavaScript.
 *
 * ## Why one placement id, not one per position
 *
 * Every block on every feed uses the single `feed-inline` placement. AdSense
 * allows the same ad unit to be rendered more than once on a page -- each `<ins>`
 * is an independent request and `adsbygoogle.push({})` fills the next unfilled
 * one -- so a dynamic feed needs ONE ad unit created by hand, not one per
 * position. That is what makes this feasible at all: the alternative was a
 * hand-keyed slot id for every position on every page, which is not a thing
 * anyone can maintain.
 *
 * The cost is that AdSense reports these as one unit, so revenue cannot be split
 * between the Builder's feed and the Today feed at the ad-network level. Page-level
 * attribution has to come from the site's own analytics instead.
 *
 * ## Why the request starts a viewport early
 *
 * Spec section 4, the in-feed row: the slot is `height: 0` until it fills, and
 * the request starts "1 viewport ahead with IntersectionObserver
 * `rootMargin: 100%`, so the expansion happens off-screen". That is the whole
 * reason an in-feed unit costs no CLS: by the time the reader reaches it, it has
 * either already expanded or already collapsed. `100%` rather than the old
 * `700px` because the margin has to scale with the viewport -- 700px is more
 * than a viewport ahead on a phone and less than one on a desktop.
 */
export function FeedAdBlock() {
  const { access, status } = useAccount();
  const [nearViewport, setNearViewport] = useState(false);
  const sentinelRef = useRef<HTMLSpanElement | null>(null);

  useEffect(() => {
    const sentinel = sentinelRef.current;
    if (!sentinel || nearViewport) return;
    if (typeof IntersectionObserver === "undefined") {
      setNearViewport(true);
      return;
    }
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        setNearViewport(true);
        observer.disconnect();
      }
    }, { rootMargin: "100%" });
    observer.observe(sentinel);
    return () => observer.disconnect();
  }, [access?.adsAllowed, nearViewport, status]);

  // Keep a one-pixel observer target in the feed until it approaches the
  // viewport. Long pages can then repeat ads without requesting every unit on
  // initial render. AdSlot is not mounted before this point.
  if (status !== "ready" || !access?.adsAllowed) return null;
  return (
    <div className="feed-ads">
      {nearViewport ? (
        <AdSlot format="in-feed" placement="feed-inline" />
      ) : (
        <span aria-hidden="true" ref={sentinelRef} style={{ display: "block", height: 1 }} />
      )}
    </div>
  );
}
