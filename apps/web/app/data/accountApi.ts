export type ApiSuccess<T> = {
  data: T;
  ok: true;
  status: number;
};

export type ApiFailure = {
  code?: string;
  message: string;
  ok: false;
  status: number;
};

export type ApiResult<T> = ApiSuccess<T> | ApiFailure;

export type AccessContext = {
  actor: "guest" | "user";
  adsAllowed: boolean;
  authenticated: boolean;
  csrfToken?: string;
  email?: string;
  emailVerified: boolean;
  entitlements: string[];
  guestAnalysisLimit?: number;
  guestAnalysisRemaining?: number;
  tier: "guest" | "basic" | "pro";
  userId?: string;
};

export type AccountProfile = {
  countryCode: string;
  displayName: string;
  email: string;
  emailVerified: boolean;
  language: string;
  marketingConsent: boolean;
  oddsFormat: string;
  oddsFormatSelected: boolean;
  personalizationEnabled: boolean;
  timezone: string;
};

export type AccountSession = {
  current: boolean;
  deviceName: string;
  expiresAt?: string;
  id: string;
  signedInAt?: string;
};

export type FavoriteItem = {
  contextLabel?: string;
  entityId: string;
  entityType: string;
  href?: string;
  id: string;
  matches?: FavoriteMatchPreview[];
  name: string;
  subtitle?: string;
};

export type FavoriteMatchPreview = {
  awayScore?: number;
  awayTeam: string;
  homeScore?: number;
  homeTeam: string;
  href: string;
  id: string;
  kickoffAt: string;
  leagueName?: string;
  status: string;
};

export type RecommendationItem = {
  confidence?: number;
  editorial: boolean;
  freshnessAt?: string;
  href?: string;
  id: string;
  itemType: string;
  reason: string;
  sponsored: boolean;
  startsAt?: string;
  subtitle?: string;
  title: string;
};

export type NotificationItem = {
  createdAt?: string;
  href?: string;
  id: string;
  message: string;
  read: boolean;
  title: string;
};

export type NotificationPreferences = {
  digestEnabled: boolean;
  favoriteAlerts: boolean;
  inAppEnabled: boolean;
  kickoffReminders: boolean;
};

export type SubscriptionSummary = {
  cancelAtPeriodEnd: boolean;
  currentPeriodEnd?: string;
  entitlements: string[];
  manageAvailable: boolean;
  planName: string;
  status: string;
  tier: string;
};

export type PublicPlan = {
  active: boolean;
  amountMinor?: number;
  billingInterval: string;
  checkoutEnabled: boolean;
  currency?: string;
  entitlements: string[];
  formattedPrice?: string;
  key: string;
  name: string;
  recommended: boolean;
  savingsLabel?: string;
  savingsPercent?: number;
  summary?: string;
  tier: string;
};

type JsonRecord = Record<string, unknown>;

export function asRecord(value: unknown): JsonRecord {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as JsonRecord)
    : {};
}

export function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

export function readString(record: JsonRecord, ...keys: string[]): string | undefined {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "string" && value.trim()) return value.trim();
    if (typeof value === "number" && Number.isFinite(value)) return String(value);
  }
  return undefined;
}

export function readBoolean(record: JsonRecord, ...keys: string[]): boolean | undefined {
  for (const key of keys) {
    if (typeof record[key] === "boolean") return record[key] as boolean;
  }
  return undefined;
}

export function readNumber(record: JsonRecord, ...keys: string[]): number | undefined {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === "number" && Number.isFinite(value)) return value;
    if (typeof value === "string" && value.trim() && Number.isFinite(Number(value))) return Number(value);
  }
  return undefined;
}

function messageFromPayload(payload: unknown, fallback: string) {
  const record = asRecord(payload);
  const error = asRecord(record.error);
  const errorCode = typeof record.error === "string" ? record.error.replaceAll("_", " ") : undefined;
  return readString(record, "message", "detail", "error_description") ?? readString(error, "message", "detail") ?? errorCode ?? fallback;
}

export async function accountApiRequest<T>(
  path: string,
  init: RequestInit = {},
): Promise<ApiResult<T>> {
  const normalizedPath = path.startsWith("/") ? path : `/${path}`;
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");

  const method = (init.method ?? "GET").toUpperCase();
  const attempts = method === "GET" || method === "DELETE" ? 2 : 1;
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      const response = await fetch(`/api/account${normalizedPath}`, {
        ...init,
        cache: "no-store",
        credentials: "include",
        headers,
      });
      // A 204 has no body to read. Parsing it as JSON throws, the catch
      // below treats that as a transport failure, and the caller is told its
      // request failed after it succeeded -- which is what made removing a
      // favorite report "Account services are temporarily unavailable".
      if (nullBodyStatus(response.status)) {
        return { data: undefined as T, ok: true, status: response.status };
      }
      const contentType = response.headers.get("content-type") ?? "";
      const payload: unknown = contentType.includes("application/json")
        ? await response.json()
        : await response.text();

      if (!response.ok) {
        const record = asRecord(payload);
        const code = readString(record, "code", "error_code");
        if (attempt + 1 < attempts && response.status === 503 && code === "account_api_unavailable") {
          await new Promise((resolve) => setTimeout(resolve, 150));
          continue;
        }
        return {
          code,
          message: messageFromPayload(payload, "The request could not be completed."),
          ok: false,
          status: response.status,
        };
      }

      return { data: payload as T, ok: true, status: response.status };
    } catch {
      if (attempt + 1 < attempts) {
        await new Promise((resolve) => setTimeout(resolve, 150));
        continue;
      }
    }
  }
  return {
    code: "account_api_unavailable",
    message: "Account services are temporarily unavailable. Your public pages still work.",
    ok: false,
    status: 503,
  };
}

export async function accountApiRequestFirst<T>(paths: string[]): Promise<ApiResult<T>> {
  let lastResult: ApiResult<T> | undefined;
  for (const path of paths) {
    const result = await accountApiRequest<T>(path);
    if (result.ok || (result.status !== 404 && result.status !== 405)) return result;
    lastResult = result;
  }
  return lastResult ?? {
    code: "account_route_unavailable",
    message: "Account services are temporarily unavailable.",
    ok: false,
    status: 503,
  };
}

export function normalizeAccessContext(payload: unknown): AccessContext {
  const root = asRecord(payload);
  const source = Object.keys(asRecord(root.access)).length ? asRecord(root.access) : root;
  const principal = asRecord(source.principal);
  const user = Object.keys(asRecord(source.user)).length ? asRecord(source.user) : principal;
  const rawEntitlements = source.entitlements ?? source.capabilities;
  const entitlements = Array.isArray(rawEntitlements)
    ? rawEntitlements.filter((value): value is string => typeof value === "string")
    : Object.entries(asRecord(rawEntitlements))
        .filter(([, value]) => value === true || (typeof value === "number" && value > 0))
        .map(([key]) => key);
  const principalType = readString(principal, "type", "kind");
  const authenticated = readBoolean(source, "authenticated", "is_authenticated") ?? (principalType ? principalType === "user" : Object.keys(user).length > 0);
  const rawTier = (readString(source, "tier", "account_tier") ?? (authenticated ? "basic" : "guest")).toLowerCase();
  const tier: AccessContext["tier"] = rawTier === "pro" ? "pro" : authenticated ? "basic" : "guest";

  return {
    actor: authenticated ? "user" : "guest",
    adsAllowed: readBoolean(source, "ads_allowed", "adsAllowed") ?? !entitlements.includes("pro_no_ads"),
    authenticated,
    csrfToken: readString(source, "csrf_token", "csrfToken"),
    email: readString(user, "email") ?? readString(source, "email"),
    emailVerified: readBoolean(user, "email_verified", "emailVerified") ?? readBoolean(source, "email_verified", "emailVerified") ?? false,
    entitlements,
    guestAnalysisLimit: readNumber(source, "guest_analysis_limit", "guestAnalysisLimit"),
    guestAnalysisRemaining: readNumber(source, "guest_analysis_remaining", "guestAnalysisRemaining", "remaining_views"),
    tier,
    userId: readString(user, "id", "public_id") ?? readString(source, "user_id", "userId"),
  };
}

export function normalizeProfile(payload: unknown): AccountProfile {
  const root = asRecord(payload);
  const source = Object.keys(asRecord(root.profile)).length ? asRecord(root.profile) : root;
  return {
    countryCode: readString(source, "country_code", "countryCode") ?? "",
    displayName: readString(source, "display_name", "displayName", "name") ?? "",
    email: readString(source, "email") ?? "",
    emailVerified: readBoolean(source, "email_verified", "emailVerified") ?? false,
    language: readString(source, "preferred_language", "preferredLanguage", "language") ?? "en",
    marketingConsent: readBoolean(source, "marketing_consent", "marketingConsent") ?? false,
    oddsFormat: readString(source, "odds_format", "oddsFormat") ?? "decimal",
    oddsFormatSelected: readBoolean(source, "odds_format_selected", "oddsFormatSelected") ?? false,
    personalizationEnabled: readBoolean(source, "personalization_enabled", "personalizationEnabled") ?? true,
    timezone: readString(source, "timezone") ?? "Africa/Nairobi",
  };
}

export function normalizePlans(payload: unknown): PublicPlan[] {
  const root = asRecord(payload);
  const values = asArray(root.plans).length ? asArray(root.plans) : asArray(payload);
  return values.map((value) => {
    const plan = asRecord(value);
    const prices = asArray(plan.prices);
    const price = Object.keys(asRecord(plan.price)).length ? asRecord(plan.price) : asRecord(prices[0]);
    const metadata = asRecord(plan.metadata);
    const rawEntitlements = plan.entitlements;
    const entitlements = Array.isArray(rawEntitlements)
      ? rawEntitlements.map((item) => typeof item === "string" ? item : readString(asRecord(item), "name", "key", "entitlement_key")).filter((item): item is string => Boolean(item))
      : Object.keys(asRecord(rawEntitlements));
    return {
      active: readBoolean(plan, "active") ?? true,
      amountMinor: readNumber(price, "amount_minor", "amountMinor") ?? readNumber(plan, "amount_minor", "amountMinor"),
      billingInterval: readString(plan, "billing_interval", "billingInterval", "interval") ?? "none",
      checkoutEnabled: readBoolean(plan, "checkout_enabled", "checkoutEnabled") ?? false,
      currency: readString(price, "currency") ?? readString(plan, "currency"),
      entitlements,
      formattedPrice: readString(price, "formatted", "formatted_price") ?? readString(plan, "formatted_price", "formattedPrice"),
      key: readString(plan, "plan_key", "key", "id") ?? "",
      name: readString(plan, "name") ?? "Plan",
      recommended: readBoolean(plan, "recommended") ?? readBoolean(metadata, "recommended") ?? false,
      savingsLabel: readString(plan, "savings_label", "savingsLabel") ?? readString(metadata, "savings_label", "savingsLabel"),
      savingsPercent: readNumber(plan, "savings_percent", "savingsPercent"),
      summary: readString(plan, "summary", "description") ?? readString(metadata, "summary", "description"),
      tier: readString(plan, "tier") ?? "basic",
    };
  }).filter((plan) => plan.key && plan.active);
}

export function formatPlanPrice(plan: PublicPlan) {
  if (plan.formattedPrice) return plan.formattedPrice;
  if (plan.amountMinor === undefined || !plan.currency) return undefined;
  try {
    return new Intl.NumberFormat(undefined, {
      currency: plan.currency.toUpperCase(),
      style: "currency",
    }).format(plan.amountMinor / 100);
  } catch {
    return `${plan.currency.toUpperCase()} ${(plan.amountMinor / 100).toFixed(2)}`;
  }
}

export function safeInternalHref(value: string | undefined, fallback = "/matches/today") {
  if (!value || !value.startsWith("/") || value.startsWith("//") || /[\\\u0000-\u001f\u007f]/.test(value)) {
    return fallback;
  }
  try {
    const decodedOnce = decodeURI(value);
    if (decodedOnce.startsWith("//") || /[\\\u0000-\u001f\u007f]/.test(decodedOnce)) return fallback;
    if (/%(?:25)*(?:5c|0[0-9a-f]|1[0-9a-f]|7f)/i.test(value)) return fallback;
    const origin = "https://return-path.sokalens.invalid";
    const parsed = new URL(value, origin);
    if (parsed.origin !== origin || parsed.pathname.startsWith("//")) return fallback;
    const pathname = parsed.pathname.replace(/^\/(?:en|es|fr|pt|ar|de|it)(?=\/|$)/, "") || "/";
    const matchEntity = pathname.match(/^\/matches\/([^/]+)$/);
    if (matchEntity && matchEntity[1] !== "today" && !/^[1-9]\d*$/.test(matchEntity[1])) return fallback;
    const teamEntity = pathname.match(/^\/teams\/([^/]+)$/);
    if (teamEntity && !validCatalogSlug(decodeURIComponent(teamEntity[1]))) return fallback;
    const leagueEntity = pathname.match(/^\/predictions\/leagues\/([^/]+)$/);
    if (leagueEntity && !validCatalogSlug(decodeURIComponent(leagueEntity[1]))) return fallback;
    const destination = `${parsed.pathname}${parsed.search}${parsed.hash}`;
    return destination.startsWith("//") ? fallback : destination;
  } catch {
    return fallback;
  }
}

export function normalizeCountryCode(value: string) {
  return value.trim().toUpperCase();
}
import { validCatalogSlug } from "../lib/catalogSlug.ts";
import { nullBodyStatus } from "../lib/nullBodyStatus.ts";
