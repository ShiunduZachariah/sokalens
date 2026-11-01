const baseUrl = process.argv[2] ?? process.env.WEB_BASE_URL ?? "http://127.0.0.1:3000";
const routes = [
  "/",
  "/football",
  "/football/today",
  "/matches/today",
  "/predictions/today",
  "/best-bets",
  "/leagues",
  "/results",
  "/match/arsenal-vs-chelsea-2026-04-29",
  "/leagues/premier-league",
  "/teams/arsenal",
  "/multi-match-builder",
  "/acca-predictions",
  "/search?q=ars",
];

const requestsPerRoute = Number.parseInt(process.env.WEB_LOAD_REQUESTS_PER_ROUTE ?? "4", 10);
const maxP95Ms = Number.parseInt(process.env.WEB_LOAD_P95_MS ?? "3000", 10);

const results = [];

for (const route of routes) {
  const batch = Array.from({ length: requestsPerRoute }, () => fetchRoute(route));
  results.push(...(await Promise.all(batch)));
}

const failures = results.filter((result) => !result.ok);
const durations = results.map((result) => result.durationMs).sort((a, b) => a - b);
const p95 = percentile(durations, 0.95);
const routeSummaries = routes.map((route) => {
  const routeDurations = results
    .filter((result) => result.route === route)
    .map((result) => result.durationMs)
    .sort((a, b) => a - b);
  return { route, p95: percentile(routeDurations, 0.95) };
});

console.log(
  `SokaLens web load smoke: ${results.length} requests, ${failures.length} failures, p95 ${p95.toFixed(1)} ms`,
);
for (const summary of routeSummaries.sort((a, b) => b.p95 - a.p95)) {
  console.log(`- ${summary.route}: p95 ${summary.p95.toFixed(1)} ms`);
}

if (failures.length > 0) {
  for (const failure of failures.slice(0, 8)) {
    console.error(`- ${failure.route}: ${failure.status} ${failure.error ?? ""}`.trim());
  }
  process.exit(1);
}

if (p95 > maxP95Ms) {
  console.error(`Web load smoke p95 ${p95.toFixed(1)} ms exceeded ${maxP95Ms} ms.`);
  process.exit(1);
}

async function fetchRoute(route) {
  const startedAt = performance.now();
  const url = new URL(route, baseUrl).toString();

  try {
    const response = await fetch(url);
    const body = await response.text();
    const durationMs = performance.now() - startedAt;

    return {
      durationMs,
      ok: response.ok && body.includes("SokaLens"),
      route,
      status: response.status,
    };
  } catch (error) {
    return {
      durationMs: performance.now() - startedAt,
      error: error instanceof Error ? error.message : String(error),
      ok: false,
      route,
      status: "network_error",
    };
  }
}

function percentile(values, percentileValue) {
  if (values.length === 0) {
    return 0;
  }

  const index = Math.min(values.length - 1, Math.ceil(values.length * percentileValue) - 1);
  return values[index];
}
