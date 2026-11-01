import http from "k6/http";
import { check, sleep } from "k6";
import { Trend } from "k6/metrics";

const baseUrl = (__ENV.WEB_BASE_URL || "http://host.docker.internal:3000").replace(/\/$/, "");

const routes = [
  "/",
  "/football",
  "/matches/today",
  "/predictions/today",
  "/best-bets",
  "/leagues",
  "/results",
  "/matches/30984",
  "/leagues/premier-league",
  "/teams/arsenal",
  "/multi-match-builder",
  "/acca-predictions",
  "/search?q=arsenal",
  "/responsible-gambling",
  "/terms",
  "/privacy",
];
const routeTrends = Object.fromEntries(
  routes.map((route) => [route, new Trend(`web_route_${route.split("?")[0].replace(/^\/+|\W+/g, "_") || "home"}_duration`, true)]),
);

export const options = {
  vus: Number.parseInt(__ENV.WEB_LOAD_VUS || "4", 10),
  duration: __ENV.WEB_LOAD_DURATION || "30s",
  thresholds: {
    http_req_failed: ["rate<0.005"],
    // Direct-origin SSR budget. Field LCP remains a separate Core Web Vitals
    // gate; 2s keeps origin responses inside the measured 1.86s p75 envelope
    // while still failing the former 3-4s refresh-stampede regression.
    http_req_duration: ["p(95)<2000"],
  },
};

export default function runPublicWebSmoke() {
  const route = routes[(__VU + __ITER) % routes.length];
  const response = http.get(`${baseUrl}${route}`, {
    tags: { route: route.split("?")[0] },
  });
  routeTrends[route].add(response.timings.duration);

  check(response, {
    "status is 200": (result) => result.status === 200,
    "renders SokaLens": (result) => result.body.includes("SokaLens"),
    "has canonical metadata": (result) => /rel=["']canonical["']/.test(result.body),
    "no unapproved provider embeds": (result) => hasNoUnapprovedProviderEmbeds(result.body),
  });

  sleep(0.2);
}

function hasNoUnapprovedProviderEmbeds(body) {
  const withoutApprovedMedia = body.replace(
    /https:(?:\\?\/){2}media\.api-sports\.io(?:\\?\/)football(?:\\?\/)(?:teams|leagues)(?:\\?\/)\d+\.png/gi,
    "",
  );
  return !/API_FOOTBALL_KEY|x-apisports-key|x-rapidapi-key|rapidapi|api-football|api-sports|<iframe\b/i.test(withoutApprovedMedia);
}
