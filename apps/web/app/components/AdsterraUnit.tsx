"use client";

import { useEffect, useRef, useState } from "react";
import { adsterraScriptUrl, validateAdsterraConfig, type AdsterraPublicConfig } from "../lib/adDelivery";
import type { ProviderAdapterProps } from "./ads/types";

/*
 * Adsterra's three unit kinds need three different mounts, and the difference
 * is not cosmetic.
 *
 * A BANNER reads the global `window.atOptions` when its script executes.
 * Adsterra's supplied code expects to run in the page document beside its
 * placement. Wrapping it in another iframe prevented it from creating an ad.
 * Banner scripts therefore mount in their real host, one at a time so their
 * different sizes cannot race over `atOptions`.
 *
 * A NATIVE widget writes into a container the page must already have rendered,
 * and the social bar injects its own fixed element. Both go in the main
 * document, because a frame is exactly what they must not be in.
 *
 * Every element here is built with DOM APIs rather than written as markup, the
 * way `AdSenseUnit` builds its loader. That is the house pattern and it is the
 * stronger one: there is no HTML string to escape into, so "configuration is
 * data, never code" is structural rather than argued. `phase3-verify` bans
 * embed markup in components for the same reason, and this adapter has none to
 * ban.
 *
 * Fill: a banner reports success once its provider-created frame has content,
 * and a native widget once its container does. Neither provider exposes a fill
 * signal, so an empty box is indistinguishable from a slow one until AdSlot's
 * own timeout fires -- which is why NO_FILL here is a timeout, reported by
 * AdSlot, rather than something this adapter can assert.
 */

const loaders = new Map<string, Promise<void>>();
let bannerQueue: Promise<void> = Promise.resolve();

/*
 * A native widget, and the social bar, write into an element they find by an id
 * derived from the zone. Two on one page means a duplicate id, and the provider
 * fills whichever one `getElementById` happens to return -- so the others never
 * fill, and each one spends its whole attempt timeout discovering that.
 *
 * The configuration is supposed to prevent this by mapping those kinds only to
 * placements that render once. This claim is the belt: the second mount of a
 * zone fails immediately with INVALID_ZONE, which moves the slot to the next
 * provider instead of leaving a hole, and makes the misconfiguration loud in
 * the outcome telemetry rather than silent on the page.
 *
 * Banners are exempt: their scripts can render repeated zones at distinct
 * hosts, and the queue protects the one global they share.
 */
const claimedSingletons = new Set<string>();

function loadScript(url: string) {
  const existing = loaders.get(url);
  if (existing) return existing;
  const loader = new Promise<void>((resolve, reject) => {
    const script = document.createElement("script");
    script.async = true;
    // Adsterra requires Cloudflare Rocket Loader to leave its tags alone.
    script.dataset.cfasync = "false";
    script.src = url;
    script.dataset.sokalensAdProvider = "adsterra";
    script.addEventListener("load", () => resolve(), { once: true });
    script.addEventListener("error", () => reject(new Error("adsterra_script_error")), { once: true });
    document.body.append(script);
  }).catch((error) => {
    loaders.delete(url);
    throw error;
  });
  loaders.set(url, loader);
  return loader;
}

// Keep the provider's documented script in the page document. A script load is
// only permission to start the next banner; it is not evidence of an ad fill.
function mountBanner(
  host: HTMLElement,
  config: AdsterraPublicConfig,
  onStart: () => void,
  settle: ProviderAdapterProps["onResult"],
) {
  const { height, key, width } = config.zone;
  let cancelled = false;
  const poll = window.setInterval(() => {
    for (const frame of host.querySelectorAll("iframe")) {
      try {
        const body = frame.contentDocument?.body;
        if (body && Array.from(body.children).some((child) => !["SCRIPT", "STYLE"].includes(child.tagName))) {
          window.clearInterval(poll);
          settle({ outcome: "success" });
          return;
        }
        if (!body && frame.src && frame.src !== "about:blank") {
          window.clearInterval(poll);
          settle({ outcome: "success" });
          return;
        }
      } catch {
        // A cross-origin creative cannot be inspected; its loaded URL is the
        // only available fill signal in that case.
        if (frame.src && frame.src !== "about:blank" && frame.contentWindow) {
          window.clearInterval(poll);
          settle({ outcome: "success" });
          return;
        }
      }
    }
  }, 120);

  const run = async () => {
    if (cancelled) return;
    onStart();
    const page = window as Window & { atOptions?: unknown };
    const previous = page.atOptions;
    page.atOptions = { format: "iframe", height, key, params: {}, width };
    const script = document.createElement("script");
    script.async = false;
    script.dataset.cfasync = "false";
    script.dataset.sokalensAdProvider = "adsterra";
    script.src = adsterraScriptUrl(config);
    await new Promise<void>((resolve) => {
      let finished = false;
      const release = () => {
        if (finished) return;
        finished = true;
        window.clearTimeout(watchdog);
        if (previous === undefined) delete page.atOptions;
        else page.atOptions = previous;
        resolve();
      };
      const watchdog = window.setTimeout(() => {
        script.remove();
        if (!cancelled) settle({ outcome: "failure", failureReason: "TIMEOUT" });
        release();
      }, 3_000);
      script.addEventListener("load", release, { once: true });
      script.addEventListener("error", () => {
        if (!cancelled) settle({ outcome: "failure", failureReason: "SCRIPT_ERROR" });
        release();
      }, { once: true });
      host.append(script);
    });
  };
  bannerQueue = bannerQueue.then(run, run);
  return () => {
    cancelled = true;
    window.clearInterval(poll);
    host.replaceChildren();
  };
}

export function AdsterraUnit({ attempt, format, onStart, onResult }: ProviderAdapterProps) {
  const settled = useRef(false);
  const host = useRef<HTMLDivElement>(null);
  // The top slot becomes 50px high below 900px. Wait for the client viewport
  // before choosing its zone, so a 728px script is never started on a phone.
  const [compactTop, setCompactTop] = useState<boolean | null>(null);
  useEffect(() => {
    if (format !== "top") return;
    const media = window.matchMedia("(max-width: 900px)");
    const update = () => setCompactTop(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, [format]);
  // AdSlot creates its result callback during render. A new callback must not
  // tear down a filled unit and mount it again when the slot becomes visible.
  const onResultRef = useRef(onResult);
  onResultRef.current = onResult;
  const onStartRef = useRef(onStart);
  onStartRef.current = onStart;
  const waitingForViewport = format === "top" && compactTop === null;
  const zoneFormat = format === "top" && compactTop ? "adhesion" : format;
  const config = waitingForViewport ? null : validateAdsterraConfig(attempt.publicConfig, zoneFormat);

  useEffect(() => {
    if (waitingForViewport) return;
    settled.current = false;
    const settle = (result: Parameters<typeof onResult>[0]) => {
      if (settled.current) return;
      settled.current = true;
      onResultRef.current(result);
    };
    if (!config) {
      settle({ outcome: "failure", failureReason: "INVALID_ZONE" });
      return;
    }
    const container = host.current;
    if (!container) return;

    if (config.zone.kind !== "banner") {
      if (claimedSingletons.has(config.zone.key)) {
        settle({ outcome: "failure", failureReason: "INVALID_ZONE" });
        return;
      }
      claimedSingletons.add(config.zone.key);
    }

    if (config.zone.kind === "banner") {
      return mountBanner(container, config, () => onStartRef.current?.(), settle);
    }

    // Adsterra's native widget looks up this exact id, so the container must
    // exist in the document before its script runs.
    const target = document.createElement("div");
    target.id = `container-${config.zone.key}`;
    container.append(target);
    const observer = new MutationObserver(() => {
      if (target.childElementCount > 0) {
        observer.disconnect();
        settle({ outcome: "success" });
      }
    });
    observer.observe(target, { childList: true, subtree: true });
    let cancelled = false;
    onStartRef.current?.();
    void loadScript(adsterraScriptUrl(config))
      .then(() => {
        // The social bar injects a fixed element of its own and never fills a
        // container, so its own load is the only signal there is.
        if (!cancelled && config.zone.kind === "social") settle({ outcome: "success" });
      })
      .catch(() => {
        if (!cancelled) settle({ outcome: "failure", failureReason: "SCRIPT_ERROR" });
      });
    return () => {
      cancelled = true;
      observer.disconnect();
      target.remove();
      claimedSingletons.delete(config.zone.key);
    };
  }, [config?.scriptOrigin, config?.zone.key, config?.zone.kind, config?.zone.width, config?.zone.height, format, waitingForViewport]);

  if (!config) return null;
  return <div ref={host} />;
}
