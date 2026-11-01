/**
 * Whether the ads surface is switched on. The public site shows no ads, so the
 * API hides /v1/admin/ads behind the same ADS_ENABLED flag; the admin hides the
 * page and its navigation entry to match. Read at request time, server side.
 */
export function adsEnabled(): boolean {
  return process.env.ADS_ENABLED === "true";
}
