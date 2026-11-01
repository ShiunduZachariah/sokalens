/**
 * A slip, small enough to travel in the share URL.
 *
 * An ACCA is shared as `/acca-predictions#acca-<id>`. A fragment is never sent
 * to the server, so nothing rendering the share card can know which slip was
 * shared -- the card falls back to the page's own, which describes the ACCA
 * index rather than the ticket someone pasted into a group chat.
 *
 * Rather than add a lookup by id, the slip's summary travels with the link. It
 * is a handful of short strings, it is exactly what the card needs to draw, and
 * it means unfurling a popular slip costs no upstream calls at all -- which
 * matters because every chat app that sees the link fetches the card, and they
 * arrive together.
 *
 * Deliberately not a general-purpose serializer: it carries what a card can
 * show and nothing else, so a longer slip cannot grow the URL without bound.
 */

export type SlipLeg = { market: string; selection: string };
export type SlipSummary = { label?: string; odds?: string; legs: SlipLeg[] };

const MAX_LEGS = 20;
const MAX_TOKEN_LENGTH = 4_096;
const MAX_FIELD_LENGTH = 120;

// Encoded with web primitives rather than Buffer, because this module is
// imported by AccaExplorer, which is a client component.
//
// Buffer's "base64url" is a Node encoding. In the browser Next substitutes a
// Buffer polyfill that does not implement it, so the call threw
// `TypeError: Unknown encoding: base64url` during render and took the whole
// /acca-predictions page down -- the browser showed its own "This page couldn't
// load" error, not an application one. Nothing caught it because the tests run
// under Node, where Buffer *does* support base64url: the encoding existed in
// every environment except the one that shipped.
//
// btoa/atob are Latin-1 only, so the string is converted through UTF-8 bytes
// first. Skipping that step would corrupt any non-ASCII team or market name
// rather than fail loudly, which is the worse outcome.
function toBase64URL(value: string): string {
  const bytes = new TextEncoder().encode(value);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function fromBase64URL(value: string): string {
  const base64 = value.replace(/-/g, "+").replace(/_/g, "/");
  const padded = base64 + "=".repeat((4 - (base64.length % 4)) % 4);
  const binary = atob(padded);
  const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0));
  return new TextDecoder().decode(bytes);
}

export function encodeSlipToken(summary: SlipSummary): string {
  const legs = summary.legs
    .slice(0, MAX_LEGS)
    .map((leg) => [leg.market.slice(0, MAX_FIELD_LENGTH), leg.selection.slice(0, MAX_FIELD_LENGTH)]);
  const payload = [summary.label?.slice(0, MAX_FIELD_LENGTH) ?? "", summary.odds?.slice(0, 24) ?? "", legs];
  return toBase64URL(JSON.stringify(payload));
}

/**
 * Returns null for anything it does not fully trust.
 *
 * This value arrives from a URL a stranger can edit, and it is rendered into an
 * image. Length caps and per-field validation keep a hostile link from turning
 * the card renderer into a denial-of-service or a canvas for arbitrary text.
 */
export function decodeSlipToken(token: string): SlipSummary | null {
  if (!token || token.length > MAX_TOKEN_LENGTH || !/^[A-Za-z0-9_-]+$/.test(token)) return null;
  try {
    const parsed: unknown = JSON.parse(fromBase64URL(token));
    if (!Array.isArray(parsed) || parsed.length !== 3) return null;

    const [label, odds, legs] = parsed;
    if (typeof label !== "string" || label.length > MAX_FIELD_LENGTH) return null;
    if (typeof odds !== "string" || odds.length > 24) return null;
    if (!Array.isArray(legs) || legs.length === 0 || legs.length > MAX_LEGS) return null;

    const decoded: SlipLeg[] = [];
    for (const leg of legs) {
      if (!Array.isArray(leg) || leg.length !== 2) return null;
      const [market, selection] = leg;
      if (typeof market !== "string" || market.length < 1 || market.length > MAX_FIELD_LENGTH) return null;
      if (typeof selection !== "string" || selection.length < 1 || selection.length > MAX_FIELD_LENGTH) return null;
      decoded.push({ market, selection });
    }

    return { label: label || undefined, odds: odds || undefined, legs: decoded };
  } catch {
    return null;
  }
}
