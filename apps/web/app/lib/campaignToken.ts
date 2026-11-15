import { createHmac, timingSafeEqual } from "node:crypto";

/**
 * Signed content for the campaign cards the worker posts to Telegram,
 * Facebook and WhatsApp (services/worker/internal/marketing/card.go).
 *
 * The card route is public, and it draws whatever words it is given on the
 * SokaLens template. Unsigned, anyone could put a promise of certain winnings in
 * our colours by editing a URL, which is the one thing a prediction brand in a
 * reviewed category cannot afford. So the worker signs the encoded payload
 * with MARKETING_CARD_SECRET, and anything that does not verify is refused.
 *
 * The signature covers the base64url payload exactly as sent, so neither side
 * ever re-serialises JSON and has to agree with the other about key order.
 */

export const CAMPAIGN_KINDS = ["picks", "results", "goal", "stat", "preview", "record"] as const;
export type CampaignKind = (typeof CAMPAIGN_KINDS)[number];
export type CampaignFormat = "square" | "story";

export type CampaignItem = {
  league: string;
  home: string;
  away: string;
  homeLogo: string;
  awayLogo: string;
  kickoff: string;
  pick: string;
  confidence: number;
  score: string;
  result: "won" | "lost" | "void" | "pending" | "";
};

export type CampaignMatch = {
  league: string;
  home: string;
  away: string;
  homeLogo: string;
  awayLogo: string;
  kickoff: string;
  score: string;
  minute: string;
  event: string;
  pick: string;
};

export type CampaignCard = {
  kind: CampaignKind;
  format: CampaignFormat;
  eyebrow: string;
  headline: string;
  body: string;
  note: string;
  items: CampaignItem[];
  match: CampaignMatch | null;
  lines: string[];
  probs: { home: number; draw: number; away: number } | null;
};

/** Mirrors MaxTokenLength in card.go. */
export const MAX_CAMPAIGN_TOKEN_LENGTH = 6000;
const MAX_ITEMS = 5;
const MAX_LINES = 7;

function text(value: unknown, max = 120): string {
  if (typeof value !== "string") return "";
  // Control characters have no business on a card and can break the layout.
  return value.replace(/[\u0000-\u001f\u007f]/g, " ").trim().slice(0, max);
}

function count(value: unknown, max: number): number {
  return typeof value === "number" && Number.isFinite(value) ? Math.max(0, Math.min(max, Math.round(value))) : 0;
}

function signature(payload: string, secret: string): Buffer {
  return createHmac("sha256", secret).update(payload).digest();
}

/** Signs a card exactly as the worker does. Used by tests and local previews. */
export function signCampaignPayload(payload: string, secret: string): string {
  return `${payload}.${signature(payload, secret).toString("base64url")}`;
}

/**
 * Verifies and decodes a token. Returns null for anything that is not a
 * well-formed card signed with this secret.
 */
export function parseCampaignToken(token: string | null | undefined, secret: string | undefined): CampaignCard | null {
  if (!token || !secret || secret.length < 32 || token.length > MAX_CAMPAIGN_TOKEN_LENGTH) return null;
  const dot = token.indexOf(".");
  if (dot <= 0 || dot !== token.lastIndexOf(".")) return null;
  const payload = token.slice(0, dot);
  let presented: Buffer;
  try {
    presented = Buffer.from(token.slice(dot + 1), "base64url");
  } catch {
    return null;
  }
  const expected = signature(payload, secret);
  if (presented.length !== expected.length || !timingSafeEqual(presented, expected)) return null;

  let raw: Record<string, unknown>;
  try {
    raw = JSON.parse(Buffer.from(payload, "base64url").toString("utf8"));
  } catch {
    return null;
  }
  if (!raw || typeof raw !== "object" || raw.v !== 1) return null;
  const kind = raw.k;
  if (typeof kind !== "string" || !(CAMPAIGN_KINDS as readonly string[]).includes(kind)) return null;
  const format: CampaignFormat = raw.f === "story" ? "story" : "square";

  const items = Array.isArray(raw.i)
    ? raw.i.slice(0, MAX_ITEMS).map((value): CampaignItem => {
        const item = (value ?? {}) as Record<string, unknown>;
        const result = item.r;
        return {
          league: text(item.lg, 60),
          home: text(item.h, 40),
          away: text(item.a, 40),
          homeLogo: text(item.hl, 300),
          awayLogo: text(item.al, 300),
          kickoff: text(item.k, 30),
          pick: text(item.p, 60),
          confidence: count(item.c, 100),
          score: text(item.s, 12),
          result: result === "won" || result === "lost" || result === "void" || result === "pending" ? result : "",
        };
      })
    : [];

  const rawMatch = raw.m && typeof raw.m === "object" ? (raw.m as Record<string, unknown>) : null;
  const match: CampaignMatch | null = rawMatch
    ? {
        league: text(rawMatch.lg, 60),
        home: text(rawMatch.h, 40),
        away: text(rawMatch.a, 40),
        homeLogo: text(rawMatch.hl, 300),
        awayLogo: text(rawMatch.al, 300),
        kickoff: text(rawMatch.k, 40),
        score: text(rawMatch.s, 12),
        minute: text(rawMatch.mi, 12),
        event: text(rawMatch.ev, 60),
        pick: text(rawMatch.p, 60),
      }
    : null;

  const rawProbs = raw.p && typeof raw.p === "object" ? (raw.p as Record<string, unknown>) : null;
  const probs = rawProbs
    ? { home: count(rawProbs.h, 10000), draw: count(rawProbs.d, 10000), away: count(rawProbs.a, 10000) }
    : null;

  return {
    kind: kind as CampaignKind,
    format,
    eyebrow: text(raw.e, 60),
    headline: text(raw.h, 40),
    body: text(raw.b, 120),
    note: text(raw.n, 80),
    items,
    match,
    lines: Array.isArray(raw.l) ? raw.l.slice(0, MAX_LINES).map((line) => text(line, 80)).filter(Boolean) : [],
    probs: probs && probs.home + probs.draw + probs.away > 0 ? probs : null,
  };
}
