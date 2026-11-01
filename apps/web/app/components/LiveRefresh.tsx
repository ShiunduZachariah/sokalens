"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";

/**
 * Keeps a live page current without the reader refreshing it.
 *
 * This calls `router.refresh()` rather than fetching JSON and patching the DOM,
 * which is the whole point: the server components re-run, so the timeline, the
 * score and the clock all arrive already rendered, from the same code path that
 * produced the first paint. There is no second rendering of an event in the
 * browser to drift from the server's, and the page stays server-rendered for
 * crawlers -- a client-side ticker would leave them the empty first frame.
 *
 * The clock in particular must not be computed here. It is the provider's
 * elapsed minute, and a browser-side timer keeps counting when the feed stops,
 * showing a confident 73' for a match nobody is receiving updates about.
 *
 * Refreshing stops while the tab is hidden and resumes -- with an immediate
 * refresh -- when it comes back. A backgrounded tab left open overnight would
 * otherwise poll thousands of times for nobody, and the reader who returns
 * wants the current score now, not in fifteen seconds.
 */

type LiveRefreshProps = {
  /** Off for finished and upcoming matches, so only live pages poll. */
  enabled?: boolean;
  /** Matches the 15s revalidate the match data already uses. */
  intervalMs?: number;
};

export function LiveRefresh({ enabled = true, intervalMs = 15_000 }: LiveRefreshProps) {
  const router = useRouter();

  useEffect(() => {
    if (!enabled) return;
    // Guard against a caller passing 0 or a negative: a zero-delay interval
    // would refresh in a tight loop and take the page down.
    const delay = Number.isFinite(intervalMs) && intervalMs >= 1000 ? intervalMs : 15_000;

    let timer: ReturnType<typeof setInterval> | undefined;

    const start = () => {
      if (timer !== undefined) return;
      timer = setInterval(() => router.refresh(), delay);
    };
    const stop = () => {
      if (timer === undefined) return;
      clearInterval(timer);
      timer = undefined;
    };
    const onVisibility = () => {
      if (document.visibilityState === "visible") {
        router.refresh();
        start();
      } else {
        stop();
      }
    };

    if (document.visibilityState === "visible") start();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      stop();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [enabled, intervalMs, router]);

  return null;
}
