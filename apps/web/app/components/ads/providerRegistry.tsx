"use client";

import { useEffect } from "react";
import { AdSenseUnit } from "../AdSenseUnit";
import { AdsterraUnit } from "../AdsterraUnit";
import type { ProviderAdapter, ProviderAdapterProps } from "./types";

function UnsupportedProvider({ onResult }: ProviderAdapterProps) {
  useEffect(() => {
    onResult({ outcome: "failure", failureReason: "INVALID_ZONE" });
  }, [onResult]);
  return null;
}

// Configuration is data, never executable code. An adapter here builds its own
// script URL from a pinned origin and a validated zone key; accepting
// admin-supplied URLs or HTML would create stored XSS.
//
// Monetag still fails closed. The only Monetag credential this site holds is
// the ownership meta tag in the document head (`site.ts`), which verifies the
// domain and serves nothing. No Monetag zone has been issued, so there is
// nothing for an adapter to load, and a provider that cannot fill must not be
// in the chain -- it would spend a slot's timeout budget to render nothing.
const adapters: Record<string, ProviderAdapter> = {
  adsense: AdSenseUnit,
  adsterra: AdsterraUnit,
  monetag: UnsupportedProvider,
};

export function providerAdapter(adapterKey: string): ProviderAdapter {
  return adapters[adapterKey] ?? UnsupportedProvider;
}
