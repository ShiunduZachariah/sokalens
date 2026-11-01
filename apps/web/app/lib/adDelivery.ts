export type AdFailureReason =
  | "NO_FILL"
  | "SCRIPT_ERROR"
  | "TIMEOUT"
  | "NETWORK_ERROR"
  | "BLOCKED"
  | "INVALID_ZONE"
  | "PROVIDER_UNAVAILABLE";

export type AdProviderAttempt = {
  adapterKey: string;
  attemptToken: string;
  code: string;
  publicConfig: Record<string, unknown>;
  timeoutMs: number;
};

export type AdPlacementDecision = {
  allowed: boolean;
  providers: AdProviderAttempt[];
  reason: string;
};

export type AdSensePublicConfig = { clientId: string; slotId: string };

const MIN_TIMEOUT_MS = 500;
const MAX_TIMEOUT_MS = 5_000;
const DEFAULT_TIMEOUT_MS = 1_200;

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function boundedString(value: unknown, pattern: RegExp, maxLength: number) {
  return typeof value === "string" && value.length <= maxLength && pattern.test(value) ? value : null;
}

export function parseAdPlacementDecision(value: unknown): AdPlacementDecision | null {
  const source = record(value);
  if (!source || typeof source.allowed !== "boolean" || !Array.isArray(source.providers)) return null;
  const reason = typeof source.reason === "string" && source.reason.length <= 120 ? source.reason : "";
  const providers: AdProviderAttempt[] = [];
  for (const candidate of source.providers) {
    const provider = record(candidate);
    if (!provider) return null;
    const code = boundedString(provider.code, /^[a-z0-9][a-z0-9_-]*$/, 64);
    const adapterKey = boundedString(provider.adapter_key, /^[a-z0-9][a-z0-9_-]*$/, 64);
    const attemptToken = boundedString(provider.attempt_token, /^[A-Za-z0-9._~-]+$/, 2048);
    const publicConfig = record(provider.public_config);
    if (!code || !adapterKey || !attemptToken || !publicConfig) return null;
    const rawTimeout = typeof provider.timeout_ms === "number" && Number.isFinite(provider.timeout_ms)
      ? Math.round(provider.timeout_ms)
      : DEFAULT_TIMEOUT_MS;
    providers.push({
      adapterKey,
      attemptToken,
      code,
      publicConfig,
      timeoutMs: Math.min(MAX_TIMEOUT_MS, Math.max(MIN_TIMEOUT_MS, rawTimeout)),
    });
  }
  return { allowed: source.allowed, providers, reason };
}

export function validateAdSenseConfig(value: Record<string, unknown>): AdSensePublicConfig | null {
  const clientId = boundedString(value.client_id, /^ca-pub-[0-9]{10,20}$/, 32);
  const slotId = boundedString(value.slot_id, /^[0-9]{5,20}$/, 20);
  return clientId && slotId ? { clientId, slotId } : null;
}

export const adFailureReasons = new Set<AdFailureReason>([
  "NO_FILL",
  "SCRIPT_ERROR",
  "TIMEOUT",
  "NETWORK_ERROR",
  "BLOCKED",
  "INVALID_ZONE",
  "PROVIDER_UNAVAILABLE",
]);

/*
 * Adsterra.
 *
 * Three unit kinds, and the kind decides the script path -- `22` serves the
 * iframe banners, `21` the native widget, `14` the social bar. The path is
 * hardcoded here beside the kind for the same reason the origin is pinned
 * below: configuration is data, and a stored string that becomes part of a
 * script URL is a stored XSS primitive. public_config supplies a zone key and
 * a size; it never supplies a path, an origin or markup.
 */
export type AdsterraUnitKind = "banner" | "native" | "social";
export type AdsterraZone = {
  height: number;
  key: string;
  kind: AdsterraUnitKind;
  width: number;
};
export type AdsterraPublicConfig = { scriptOrigin: string; zone: AdsterraZone };

// The only origins an Adsterra script may be fetched from. Adsterra rotates
// these per publisher account, so the value is configured -- but it must match
// one of these exactly, and adding one is a code review, not a database write.
const ADSTERRA_SCRIPT_ORIGINS = new Set(["https://bauval.org"]);

const ADSTERRA_SCRIPT_PATHS: Record<AdsterraUnitKind, string> = {
  banner: "22",
  native: "21",
  social: "14",
};

function boundedInteger(value: unknown, minimum: number, maximum: number) {
  return typeof value === "number" && Number.isInteger(value) && value >= minimum && value <= maximum
    ? value
    : null;
}

export function adsterraScriptUrl(config: AdsterraPublicConfig) {
  return `${config.scriptOrigin}/${ADSTERRA_SCRIPT_PATHS[config.zone.kind]}/${config.zone.key}`;
}

/**
 * Reads the zone for one format out of an Adsterra provider's public_config.
 *
 * Returns null -- which the adapter reports as INVALID_ZONE, moving the slot on
 * to the next provider -- when the origin is not pinned, the format has no
 * zone, or the zone is malformed. A format with no zone is the normal case, not
 * an error: a publisher account has a banner for the leaderboard and nothing
 * for the rail, and the chain is how that is expressed.
 */
export function validateAdsterraConfig(
  value: Record<string, unknown>,
  format: string,
): AdsterraPublicConfig | null {
  const scriptOrigin = typeof value.script_origin === "string" && ADSTERRA_SCRIPT_ORIGINS.has(value.script_origin)
    ? value.script_origin
    : null;
  const zones = record(value.zones);
  if (!scriptOrigin || !zones) return null;
  const zone = record(zones[format]);
  if (!zone) return null;
  const key = boundedString(zone.zone_id, /^[0-9a-f]{32}$/, 32);
  const kind = zone.kind === "banner" || zone.kind === "native" || zone.kind === "social"
    ? zone.kind
    : null;
  if (!key || !kind) return null;
  // The native widget and the social bar size themselves; only a banner's
  // iframe needs a declared box, and Adsterra rejects one it did not issue.
  const width = kind === "banner" ? boundedInteger(zone.width, 1, 2000) : 0;
  const height = kind === "banner" ? boundedInteger(zone.height, 1, 2000) : 0;
  if (width === null || height === null) return null;
  return { scriptOrigin, zone: { height, key, kind, width } };
}
