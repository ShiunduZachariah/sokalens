import { readFileSync } from "node:fs";
import { validate } from "@scalar/openapi-parser";
import { parse } from "yaml";

const path = new URL("../openapi.yaml", import.meta.url);
const body = readFileSync(path, "utf8");
const validation = await validate(body);
if (!validation.valid) {
  console.error("OpenAPI schema validation failed:");
  console.error(JSON.stringify(validation.errors, null, 2));
  process.exit(1);
}
const contract = parse(body);

const requiredFragments = [
  "openapi: 3.1.0",
  "/healthz:",
  "/health:",
  "/readyz:",
  "/ready:",
  "/docs:",
  "/openapi.yaml:",
  "/v1/mobile/config:",
  "/v1/matches/today:",
  "/v1/matches/history:",
  "/v1/matches/{match_id}:",
  "/v1/predictions/today:",
  "/v1/predictions/{prediction_id}:",
  "/v1/predictions/hit-rate:",
  "/v1/providers/status:",
  "/v1/best-bets:",
  "/v1/admin/auth/login:",
  "/v1/admin/auth/session:",
  "/v1/admin/auth/logout:",
  "/v1/admin/providers/status:",
  "/v1/admin/predictions/hit-rate:",
  "/v1/admin/plans:",
  "/v1/admin/observability/metrics:",
  "/v1/admin/observability/diagnostics:",
  "/v1/me/password:",
  "HealthResponse:",
  "ReadinessResponse:",
  "MobileConfig:",
  "MobileTerminology:",
  "MultiMatchBuilderConstraints:",
  "EntityFavoriteType:",
  "BasicMatchNotificationCategory:",
  "InsightAlertCategory:",
  "InsightWatchlistItem:",
  "InsightAlertPreference:",
  "SubscriptionEntitlement:",
  "PredictionMarketKey:",
  "MatchSummary:",
  "MatchPredictionSummary:",
  "PredictionSummary:",
  "PredictionDetail:",
  "PredictionOutcome:",
  "PredictionHitRateReport:",
  "PredictionMarketHitRate:",
  "AdminLoginRequest:",
  "AdminSessionResponse:",
  "AdminProviderStatusResponse:",
  "AdminProviderStatus:",
  "AdminProviderQuota:",
  "BestBetsResponse:",
  "PasswordChangeRequest:",
  "AdminPlanConfiguration:",
  "ObservabilityDiagnostics:",
  "CanonicalETag:",
  "CanonicalCacheControl:",
];

const missing = requiredFragments.filter((fragment) => !body.includes(fragment));
if (missing.length > 0) {
  console.error(`OpenAPI contract missing required fragments: ${missing.join(", ")}`);
  process.exit(1);
}

if (contract.openapi !== "3.1.0") {
  console.error(`OpenAPI contract version = ${contract.openapi}, want 3.1.0`);
  process.exit(1);
}

for (const route of ["/healthz", "/health", "/readyz", "/ready", "/docs", "/openapi.yaml", "/v1/mobile/config", "/v1/matches/today", "/v1/matches/history", "/v1/matches/{match_id}", "/v1/predictions/today", "/v1/predictions/{prediction_id}", "/v1/predictions/hit-rate", "/v1/predictions/results", "/v1/providers/status", "/v1/best-bets", "/v1/me/password", "/v1/admin/auth/login", "/v1/admin/auth/session", "/v1/admin/auth/logout", "/v1/admin/providers/status", "/v1/admin/predictions/hit-rate", "/v1/admin/predictions/results", "/v1/admin/plans", "/v1/admin/observability/metrics", "/v1/admin/observability/diagnostics"]) {
  if (!contract.paths?.[route]) {
    console.error(`OpenAPI parsed paths missing route: ${route}`);
    process.exit(1);
  }
}

for (const route of ["/v1/admin/plans", "/v1/admin/observability/metrics", "/v1/admin/observability/diagnostics"]) {
  const security = contract.paths?.[route]?.get?.security;
  if (!security?.some((entry) => Array.isArray(entry.bearerAuth))) {
    console.error(`OpenAPI admin route must require bearerAuth: ${route}`);
    process.exit(1);
  }
}

for (const route of ["/v1/matches/today", "/v1/matches/history", "/v1/matches/{match_id}", "/v1/predictions/today", "/v1/predictions/{prediction_id}"]) {
  const headers = contract.paths?.[route]?.get?.responses?.["200"]?.headers;
  if (headers?.ETag?.$ref !== "#/components/headers/CanonicalETag" || headers?.["Cache-Control"]?.$ref !== "#/components/headers/CanonicalCacheControl") {
    console.error(`OpenAPI canonical read route must document ETag revalidation headers: ${route}`);
    process.exit(1);
  }
}

for (const schemaName of ["MatchPredictionSummary", "PredictionSummary", "PredictionDetail"]) {
  const schema = contract.components?.schemas?.[schemaName];
  for (const field of ["updated_at", "last_evaluated_at"]) {
    if (!schema?.required?.includes(field) || schema?.properties?.[field]?.format !== "date-time") {
      console.error(`OpenAPI prediction freshness field missing from ${schemaName}: ${field}`);
      process.exit(1);
    }
  }
}

const passwordSecurity = contract.paths?.["/v1/me/password"]?.post?.security;
if (!passwordSecurity?.some((entry) => Array.isArray(entry.sessionCookie) && Array.isArray(entry.csrfHeader))) {
  console.error("OpenAPI password change route must require sessionCookie and csrfHeader");
  process.exit(1);
}

const providerStatusSecurity = contract.paths?.["/v1/admin/providers/status"]?.get?.security;
if (!providerStatusSecurity?.some((entry) => Array.isArray(entry.bearerAuth))) {
  console.error("OpenAPI admin provider status route must require bearerAuth");
  process.exit(1);
}

const hitRateSecurity = contract.paths?.["/v1/admin/predictions/hit-rate"]?.get?.security;
if (!hitRateSecurity?.some((entry) => Array.isArray(entry.bearerAuth))) {
  console.error("OpenAPI admin prediction hit-rate route must require bearerAuth");
  process.exit(1);
}

console.log("OpenAPI foundation contract validated");
