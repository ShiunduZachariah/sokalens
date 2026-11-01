import { check, sleep } from "k6";
import http from "k6/http";
import { Rate, Trend } from "k6/metrics";

const base = (__ENV.API_BASE_URL || "http://host.docker.internal:8080").replace(/\/$/, "");
const email = __ENV.ACCOUNT_LOAD_EMAIL || "";
const password = __ENV.ACCOUNT_LOAD_PASSWORD || "";
const matchId = Number.parseInt(__ENV.MATCH_ID || "30984", 10);
const sloErrors = new Rate("account_slo_errors");
const trends = Object.fromEntries(["access", "plans", "notifications", "subscription", "recommendations", "analysis"].map((name) => [name, new Trend(`account_${name}_duration`, true)]));

export const options = {
  vus: Number.parseInt(__ENV.API_LOAD_VUS || "10", 10),
  duration: __ENV.API_LOAD_DURATION || "30s",
  summaryTrendStats: ["avg", "min", "med", "max", "p(90)", "p(95)", "p(99)"],
  thresholds: {
    http_req_duration: ["p(95)<3000"],
    http_req_failed: ["rate<0.005"],
    account_slo_errors: ["rate<0.005"],
    "http_req_duration{endpoint:access}": ["p(95)<3000"],
    "http_req_duration{endpoint:plans}": ["p(95)<3000"],
    "http_req_duration{endpoint:notifications}": ["p(95)<3000"],
    "http_req_duration{endpoint:subscription}": ["p(95)<3000"],
    "http_req_duration{endpoint:recommendations}": ["p(95)<3000"],
    "http_req_duration{endpoint:analysis}": ["p(95)<3000"],
    "http_req_failed{endpoint:access}": ["rate<0.005"],
    "http_req_failed{endpoint:plans}": ["rate<0.005"],
    "http_req_failed{endpoint:notifications}": ["rate<0.005"],
    "http_req_failed{endpoint:subscription}": ["rate<0.005"],
    "http_req_failed{endpoint:recommendations}": ["rate<0.005"],
    "http_req_failed{endpoint:analysis}": ["rate<0.005"],
    "account_slo_errors{endpoint:access}": ["rate<0.005"],
    "account_slo_errors{endpoint:plans}": ["rate<0.005"],
    "account_slo_errors{endpoint:notifications}": ["rate<0.005"],
    "account_slo_errors{endpoint:subscription}": ["rate<0.005"],
    "account_slo_errors{endpoint:recommendations}": ["rate<0.005"],
    "account_slo_errors{endpoint:analysis}": ["rate<0.005"],
  },
};

export function setup() {
  if (!email || !password) throw new Error("ACCOUNT_LOAD_EMAIL and ACCOUNT_LOAD_PASSWORD are required");
  const response = http.post(`${base}/v1/auth/login`, JSON.stringify({ email, password, device_name: "k6 SLO" }), { headers: { "Content-Type": "application/json" }, timeout: "3s" });
  if (response.status !== 200) throw new Error(`load login failed: ${response.status}`);
  const session = response.cookies.sokalens_session?.[0]?.value;
  const csrf = response.json("csrf_token");
  if (!session || !csrf) throw new Error("load login did not return secure session context");
  return { session, csrf };
}

export default function (data) {
  const headers = { Accept: "application/json", Cookie: `sokalens_session=${data.session}; sokalens_csrf=${data.csrf}` };
  get("access", "/v1/access", headers, (body) => body?.authenticated === true && ["basic", "pro"].includes(body?.tier));
  get("plans", "/v1/plans", headers, (body) => Array.isArray(body?.plans));
  get("notifications", "/v1/me/notifications?limit=30", headers, (body) => Array.isArray(body?.notifications));
  get("subscription", "/v1/me/subscription", headers, (body) => typeof body?.subscription === "object");
  get("recommendations", "/v1/recommendations?limit=24", headers, (body) => Array.isArray(body?.recommendations));
  get("analysis", `/v1/analysis/${matchId}`, headers, (body) => typeof body?.analysis === "object");
  sleep(0.05);
}

function get(endpoint, path, headers, valid) {
  const response = http.get(`${base}${path}`, { headers, tags: { endpoint }, timeout: "3s" });
  trends[endpoint].add(response.timings.duration);
  let body = null;
  try { body = response.json(); } catch (_) { /* recorded below */ }
  const ok = check(response, { [`${endpoint} is valid`]: (result) => result.status === 200 && valid(body) }, { endpoint });
  sloErrors.add(ok ? 0 : 1, { endpoint });
}
