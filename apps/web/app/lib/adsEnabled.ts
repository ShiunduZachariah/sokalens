/**
 * Whether the site renders ads. Off by default: the site shows no ads, and the
 * API hides its ad endpoints behind ADS_ENABLED to match.
 *
 * NEXT_PUBLIC_ so the same answer reaches server and client components. It is
 * inlined at build time, so turning ads back on means rebuilding the web image
 * with the build argument set (see apps/web/Dockerfile).
 */
export const ADS_ENABLED = process.env.NEXT_PUBLIC_ADS_ENABLED === "true";
