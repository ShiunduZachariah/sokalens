const defaultRequestTimeoutMs = 3_000;
const minimumRequestTimeoutMs = 500;
const maximumRequestTimeoutMs = 10_000;

type PredictionApiFetchOptions = {
  revalidate: number;
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
 * Keep public page reads bounded while retaining Next's shared data cache.
 * A degraded API should produce the page's empty/error state promptly instead
 * of holding the entire server-rendered response open for 10+ seconds.
 */
export function fetchPredictionApi(path: string, options: PredictionApiFetchOptions) {
  const normalizedPath = path.startsWith("/") ? path : `/${path}`;
  return fetch(`${predictionApiBaseUrl()}${normalizedPath}`, {
    headers: {
      Accept: "application/json",
    },
    next: {
      revalidate: options.revalidate,
    },
    signal: AbortSignal.timeout(configuredRequestTimeoutMs()),
  });
}
