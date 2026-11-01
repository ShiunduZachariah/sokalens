"use client";

import { useEffect, useRef } from "react";
import { validateAdSenseConfig } from "../lib/adDelivery";
import type { AdFormat, ProviderAdapterProps } from "./ads/types";

// Google replaces this <ins> with an iframe and tracks it on window.adsbygoogle.
declare global {
  interface Window {
    adsbygoogle?: unknown[];
  }
}

let adsenseLoader: Promise<void> | null = null;

function loadAdSense(clientId: string) {
  if (typeof window.adsbygoogle !== "undefined") return Promise.resolve();
  if (adsenseLoader) return adsenseLoader;
  adsenseLoader = new Promise<void>((resolve, reject) => {
    const script = document.createElement("script");
    script.async = true;
    script.crossOrigin = "anonymous";
    script.src = `https://pagead2.googlesyndication.com/pagead/js/adsbygoogle.js?client=${encodeURIComponent(clientId)}`;
    script.dataset.sokalensAdProvider = "adsense";
    script.addEventListener("load", () => resolve(), { once: true });
    script.addEventListener("error", () => reject(new Error("adsense_script_error")), { once: true });
    document.head.append(script);
  }).catch((error) => {
    adsenseLoader = null;
    throw error;
  });
  return adsenseLoader;
}

// AdSense's own shape hint. The canonical slots map by the shape they reserve:
// the two banner bands are horizontal, the rail is a vertical skyscraper, and
// in-feed is a rectangle because it is 300x250 on mobile even though it is a
// 728x90 band on desktop -- "rectangle" lets AdSense pick either.
const DISPLAY_FORMAT: Record<AdFormat, string> = {
  top: "horizontal",
  "in-feed": "rectangle",
  rail: "vertical",
  adhesion: "horizontal",
  banner: "horizontal",
  sidebar: "vertical",
  inline: "rectangle",
  footer: "horizontal",
};

export function AdSenseUnit({ attempt, format, onResult }: ProviderAdapterProps) {
  const pushed = useRef(false);
  const settled = useRef(false);
  const element = useRef<HTMLModElement>(null);
  const config = validateAdSenseConfig(attempt.publicConfig);

  useEffect(() => {
    if (!config) {
      if (!settled.current) {
        settled.current = true;
        onResult({ outcome: "failure", failureReason: "INVALID_ZONE" });
      }
      return;
    }
    const unit = element.current;
    if (!unit) return;
    const inspect = () => {
      const adStatus = unit.dataset.adStatus;
      if (settled.current || !adStatus) return;
      settled.current = true;
      if (adStatus === "filled") onResult({ outcome: "success" });
      else if (adStatus === "unfilled" || adStatus === "unfill-optimized") {
        onResult({ outcome: "failure", failureReason: "NO_FILL" });
      }
    };
    const observer = new MutationObserver(inspect);
    observer.observe(unit, { attributeFilter: ["data-ad-status"], attributes: true });
    inspect();
    let cancelled = false;
    // React 18 StrictMode runs effects twice in development; pushing the same
    // slot twice makes AdSense log "All 'ins' elements already have ads".
    if (!pushed.current) {
      void loadAdSense(config.clientId).then(() => {
        if (!cancelled && !settled.current && !pushed.current) {
          pushed.current = true;
          (window.adsbygoogle = window.adsbygoogle ?? []).push({});
        }
      }).catch(() => {
        if (!cancelled && !settled.current) {
          settled.current = true;
          onResult({ outcome: "failure", failureReason: "SCRIPT_ERROR" });
        }
      });
    }
    return () => {
      cancelled = true;
      observer.disconnect();
    };
  }, [config?.clientId, config?.slotId, onResult]);

  if (!config) return null;

  return (
    <ins
      className="adsbygoogle"
      data-ad-client={config.clientId}
      data-ad-format={DISPLAY_FORMAT[format]}
      data-ad-slot={config.slotId}
      data-full-width-responsive="true"
      ref={element}
      style={{ display: "block" }}
    />
  );
}
