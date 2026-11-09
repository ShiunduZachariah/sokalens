const defaultRequestTimeoutMs = 3_000;
const minimumRequestTimeoutMs = 500;
const maximumRequestTimeoutMs = 10_000;

type PredictionApiFetchOptions = {
  revalidate: number;
  /**
   * How long, in seconds, past `revalidate` a cached response may still be
   * served. Past that, the read goes to the API and waits for the answer.
   *
   * Without it, Next's data cache has no upper bound at all. A cached fetch
   * past its `revalidate` window is still returned to the caller, and the
   * refresh happens in the background for the *next* request. A page nobody
   * opened since before kickoff therefore shows its first visitor the
   * pre-kickoff payload: "Upcoming", no score, every market pending. On
   * 2026-11-09 Central Cordoba v Lanus read that way more than four hours after
   * full time, while the league hub beside it already listed the 1-1.
   *
   * Set it on every read that carries match state (status, score, settlement),
   * so a quiet page can be at most `revalidate + staleWhileRevalidate` seconds
   * behind the API, the same bound the edge applies with its own
   * `stale-while-revalidate`. Hot pages are unaffected: their cached copy is
   * always young enough to serve.
   *
   * Left unset for reads where an old copy is harmless or is the point, such
   * as the sitemap inventory, which keeps its last good set through an outage.
   */
  staleWhileRevalidate?: number;
};

function configuredRequestTimeoutMs() {
  const parsed = Number.parseInt(process.env.PREDICTION_API_TIMEOUT_MS ?? "", 10);
  if (!Number.isFinite(parsed)) {
    return defaultRequestTimeoutMs;
  }
  return Math.min(maximumRequestTimeoutMs, Math.max(minimumRequestTimeoutMs, parsed));
}

export function predictionApiBaseUrl() {
  return (
    process.env.PREDICTION_API_BASE_URL?.trim() ||
    process.env.API_BASE_URL?.trim() ||
    "http://localhost:8080"
  ).replace(/\/$/, "");
}

/**
 * Age of a response in seconds, from the `Date` header the API stamped on it.
 *
 * Next's data cache stores and replays the original response headers, so for a
 * cached copy this is how long ago the API actually produced it, not when this
 * process last touched it. Returns null when the header is missing or
 * unreadable: the age is unknown, and the caller must not assume it is young.
 */
export function responseAgeSeconds(response: Response, nowMs: number = Date.now()): number | null {
  const stamped = Date.parse(response.headers.get("date") ?? "");
  if (!Number.isFinite(stamped)) {
    return null;
  }
  return Math.max(0, (nowMs - stamped) / 1000);
}

function requestPredictionApi(url: string, cacheInit: Pick<RequestInit, "cache"> | { next: { revalidate: number } }) {
  return fetch(url, {
    headers: {
      Accept: "application/json",
    },
    ...cacheInit,
    signal: AbortSignal.timeout(configuredRequestTimeoutMs()),
  });
}

/**
 * Keep public page reads bounded while retaining Next's shared data cache.
 * A degraded API should produce the page's empty/error state promptly instead
 * of holding the entire server-rendered response open for 10+ seconds.
 */
export async function fetchPredictionApi(path: string, options: PredictionApiFetchOptions): Promise<Response> {
  const normalizedPath = path.startsWith("/") ? path : `/${path}`;
  const url = `${predictionApiBaseUrl()}${normalizedPath}`;
  const response = await requestPredictionApi(url, { next: { revalidate: options.revalidate } });

  const window = options.staleWhileRevalidate;
  if (window === undefined || options.revalidate <= 0) {
    return response;
  }
  const age = responseAgeSeconds(response);
  if (age !== null && age <= options.revalidate + window) {
    return response;
  }

  // Too old to show. The cached copy has already queued its own background
  // refresh, so the shared cache recovers for the next reader either way; this
  // request waits for the current answer instead of rendering the old one.
  try {
    const fresh = await requestPredictionApi(url, { cache: "no-store" });
    if (fresh.status < 500) {
      return fresh;
    }
  } catch {
    // Unreachable or past the deadline. An old answer still beats an error
    // page; it is the same copy the page would have shown before this check.
  }
  return response;
}
