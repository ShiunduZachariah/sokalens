import { check, sleep } from "k6";
import http from "k6/http";
import { Counter, Rate, Trend } from "k6/metrics";

const apiBaseUrl = (__ENV.API_BASE_URL || "http://host.docker.internal:8080").replace(
  /\/$/,
  "",
);
const configuredMatchId = positiveInteger(__ENV.MATCH_ID);
const pauseSeconds = nonNegativeNumber(__ENV.API_LOAD_SLEEP, 0.1);

const sloErrors = new Rate("slo_errors");
const matchDetailSkipped = new Counter("match_detail_skipped");
const endpointDurations = {
  readiness: new Trend("slo_readiness_duration", true),
  fixtures_today: new Trend("slo_fixtures_today_duration", true),
  predictions_today: new Trend("slo_predictions_today_duration", true),
  match_detail: new Trend("slo_match_detail_duration", true),
  accuracy: new Trend("slo_accuracy_duration", true),
};

export const options = {
  vus: positiveInteger(__ENV.API_LOAD_VUS) || 5,
  duration: __ENV.API_LOAD_DURATION || "30s",
  summaryTrendStats: ["avg", "min", "med", "max", "p(90)", "p(95)", "p(99)"],
  thresholds: {
    // Sustained public traffic must meet the product's p95 three-second SLO.
    http_req_duration: ["p(95)<3000"],
    // Network errors and non-2xx/3xx responses must remain below 0.5%.
    http_req_failed: ["rate<0.005"],
    // Schema/content check failures use the same SLO error budget.
    slo_errors: ["rate<0.005"],
    "http_req_duration{endpoint:readiness}": ["p(95)<3000"],
    "http_req_failed{endpoint:readiness}": ["rate<0.005"],
    "slo_errors{endpoint:readiness}": ["rate<0.005"],
    "http_req_duration{endpoint:fixtures_today}": ["p(95)<3000"],
    "http_req_failed{endpoint:fixtures_today}": ["rate<0.005"],
    "slo_errors{endpoint:fixtures_today}": ["rate<0.005"],
    "http_req_duration{endpoint:predictions_today}": ["p(95)<3000"],
    "http_req_failed{endpoint:predictions_today}": ["rate<0.005"],
    "slo_errors{endpoint:predictions_today}": ["rate<0.005"],
    "http_req_duration{endpoint:match_detail}": ["p(95)<3000"],
    "http_req_failed{endpoint:match_detail}": ["rate<0.005"],
    "slo_errors{endpoint:match_detail}": ["rate<0.005"],
    "http_req_duration{endpoint:accuracy}": ["p(95)<3000"],
    "http_req_failed{endpoint:accuracy}": ["rate<0.005"],
    "slo_errors{endpoint:accuracy}": ["rate<0.005"],
  },
};

export function setup() {
  if (configuredMatchId) {
    return { matchId: configuredMatchId };
  }

  const response = http.get(
    `${apiBaseUrl}/v1/matches/today?include_unavailable=true&limit=1`,
    requestOptions("fixture_discovery"),
  );
  const success = recordChecks("fixture_discovery", response, {
    "fixture discovery is 200": (result) => result.status === 200,
    "fixture discovery exposes a matches array": (result) =>
      Array.isArray(jsonBody(result)?.matches),
  });
  if (!success) {
    return { matchId: null };
  }

  return { matchId: positiveInteger(jsonBody(response)?.matches?.[0]?.id) };
}

export default function runPublicApiSlo(data) {
  const readiness = http.get(`${apiBaseUrl}/readyz`, requestOptions("readiness"));
  recordChecks("readiness", readiness, {
    "readiness is 200": (response) => response.status === 200,
    "readiness reports ready": (response) => jsonBody(response)?.status === "ready",
  });

  const fixtures = http.get(
    `${apiBaseUrl}/v1/matches/today?include_unavailable=true&limit=60`,
    requestOptions("fixtures_today"),
  );
  recordChecks("fixtures_today", fixtures, {
    "fixtures are 200": (response) => response.status === 200,
    "fixtures expose a matches array": (response) => Array.isArray(jsonBody(response)?.matches),
  });

  const predictions = http.get(
    `${apiBaseUrl}/v1/predictions/today?limit=60`,
    requestOptions("predictions_today"),
  );
  recordChecks("predictions_today", predictions, {
    "predictions are 200": (response) => response.status === 200,
    "predictions expose an array": (response) =>
      Array.isArray(jsonBody(response)?.predictions),
  });

  if (data.matchId) {
    const matchDetail = http.get(
      `${apiBaseUrl}/v1/matches/${data.matchId}`,
      requestOptions("match_detail"),
    );
    recordChecks("match_detail", matchDetail, {
      "match detail is 200": (response) => response.status === 200,
      "match detail has the requested id": (response) =>
        positiveInteger(jsonBody(response)?.id) === data.matchId,
    });
  } else {
    matchDetailSkipped.add(1);
  }

  const accuracy = http.get(
    `${apiBaseUrl}/v1/predictions/hit-rate`,
    requestOptions("accuracy"),
  );
  recordChecks("accuracy", accuracy, {
    "accuracy is 200": (response) => response.status === 200,
    "accuracy exposes settled performance": (response) => {
      const body = jsonBody(response);
      return Number.isFinite(body?.settled) && Number.isFinite(body?.hit_rate);
    },
  });

  if (pauseSeconds > 0) {
    sleep(pauseSeconds);
  }
}

function requestOptions(endpoint) {
  return {
    headers: { Accept: "application/json" },
    tags: { endpoint },
    timeout: "3s",
  };
}

function recordChecks(endpoint, response, checks) {
  endpointDurations[endpoint]?.add(response.timings.duration);
  const success = check(response, checks, { endpoint });
  sloErrors.add(success ? 0 : 1, { endpoint });
  return success;
}

function jsonBody(response) {
  try {
    return response.json();
  } catch (_error) {
    return null;
  }
}

function positiveInteger(value) {
  const parsed = Number.parseInt(value ?? "", 10);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : null;
}

function nonNegativeNumber(value, fallback) {
  const parsed = Number.parseFloat(value ?? "");
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback;
}
