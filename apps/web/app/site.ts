export const siteConfig = {
  name: "SokaLens",
  // Never emit localhost canonicals/sitemaps when the deployment forgot to
  // provide NEXT_PUBLIC_SITE_URL. Local requests still resolve relative links.
  url: process.env.NEXT_PUBLIC_SITE_URL ?? "https://sokalens.com",
  description:
    "Data-led football predictions and fixture intelligence.",
  // Google AdSense publisher id. This is a public identifier (it ships in
  // the page source and in ads.txt by design), not a secret, so it lives in
  // config rather than an env var that every build would have to thread
  // through as a NEXT_PUBLIC_* build arg.
  adsenseClientId: "ca-pub-5331089211665627",
  // Monetag domain-ownership token. Public by the same reasoning as the
  // AdSense id -- it exists to be read out of the page source. It verifies the
  // domain only; it serves no ads, and no Monetag zone has been issued, which
  // is why `providerRegistry` still fails Monetag closed.
  monetagVerification: "7f6f4ea5f1bebaf1f47c65830a18c9e4",
};

// Monetag's page-level zones. Public identifiers, like the AdSense publisher
// id: they ship in the page source and are useless without the account. The
// ORIGINS that serve them are pinned in `MonetagTags.tsx` and are not
// configurable, because the origin decides what code runs on the page.
export const monetagTags = {
  // Multitag: Monetag's bundle, which decides its own formats server-side.
  multitagZone: "11947078",
  // Vignette: a full-screen interstitial between page views. See the note in
  // MonetagTags.tsx about what this costs on mobile search.
  vignetteZone: "11947083",
};

export function canonicalPath(path: string) {
  return path;
}

export function absoluteUrl(path: string) {
  return new URL(path, siteConfig.url).toString();
}
