import assert from "node:assert/strict";
import test from "node:test";

import {
  accountApiRequest,
  formatPlanPrice,
  normalizeAccessContext,
  normalizeCountryCode,
  normalizePlans,
  normalizeProfile,
  safeInternalHref,
} from "./accountApi.ts";

test("idempotent account requests retry one transient proxy outage", async () => {
  const originalFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    if (calls === 1) {
      return new Response(JSON.stringify({ code: "account_api_unavailable", message: "Temporarily unavailable." }), {
        headers: { "content-type": "application/json" },
        status: 503,
      });
    }
    return new Response(null, { status: 204 });
  };
  try {
    const result = await accountApiRequest("/v1/me/favorites", { method: "DELETE" });
    assert.equal(result.ok, true);
    assert.equal(calls, 2);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test("access context normalizes capabilities without client plan flags", () => {
  const access = normalizeAccessContext({
    principal: { id: "usr_1", type: "user" },
    tier: "pro",
    capabilities: ["pro_no_ads", "pro_advanced_analysis"],
    ads_allowed: false,
  });

  assert.equal(access.authenticated, true);
  assert.equal(access.tier, "pro");
  assert.equal(access.adsAllowed, false);
  assert.deepEqual(access.entitlements, ["pro_no_ads", "pro_advanced_analysis"]);
});

test("guest access defaults to ads and preserves a server view allowance", () => {
  const access = normalizeAccessContext({
    principal: { type: "guest" },
    guest_analysis_remaining: 2,
  });

  assert.equal(access.authenticated, false);
  assert.equal(access.tier, "guest");
  assert.equal(access.adsAllowed, true);
  assert.equal(access.guestAnalysisRemaining, 2);
});

test("plans use API amounts and currencies rather than hardcoded display prices", () => {
  const [plan] = normalizePlans({
    plans: [{
      active: true,
      billing_interval: "month",
      checkout_enabled: true,
      name: "Pro",
      plan_key: "pro_month",
      price: { amount_minor: 900, currency: "USD" },
      tier: "pro",
    }],
  });

  assert.equal(plan.key, "pro_month");
  assert.match(formatPlanPrice(plan), /9/);
});

test("annual savings come from backend pricing configuration", () => {
  const [plan] = normalizePlans({ plans: [{ active: true, amount_minor: 2000, billing_interval: "year", checkout_enabled: false, currency: "USD", name: "Pro Yearly", plan_key: "pro_yearly", savings_label: "Save 16% with annual billing", savings_percent: 16, tier: "pro" }] });
  assert.equal(formatPlanPrice(plan), "$20.00");
  assert.equal(plan.savingsPercent, 16);
  assert.equal(plan.savingsLabel, "Save 16% with annual billing");
});

test("external recommendation URLs are not used as internal navigation", () => {
  assert.equal(safeInternalHref("https://example.com"), "/matches/today");
  assert.equal(safeInternalHref("//example.com"), "/matches/today");
  assert.equal(safeInternalHref("/\\\\example.com"), "/matches/today");
  assert.equal(safeInternalHref("/%5c%5cexample.com"), "/matches/today");
  assert.equal(safeInternalHref("/%255c%255cexample.com"), "/matches/today");
  assert.equal(safeInternalHref("/%25252525252525255cexample.com"), "/matches/today");
  assert.equal(safeInternalHref("/x/..//example.com"), "/matches/today");
  assert.equal(safeInternalHref("/%2e//example.com"), "/matches/today");
  assert.equal(safeInternalHref("/matches/42%0aevil"), "/matches/today");
  assert.equal(safeInternalHref("/bad%escape"), "/matches/today");
  assert.equal(safeInternalHref("/matches/42"), "/matches/42");
  assert.equal(safeInternalHref("/matches/42?market=1#odds"), "/matches/42?market=1#odds");
  assert.equal(safeInternalHref("/matches/null"), "/matches/today");
  assert.equal(safeInternalHref("/matches/NaN"), "/matches/today");
  assert.equal(safeInternalHref("/teams/undefined"), "/matches/today");
  assert.equal(safeInternalHref("/predictions/leagues/null"), "/matches/today");
  assert.equal(safeInternalHref("/fr/teams/null"), "/matches/today");
  assert.equal(safeInternalHref("/search?q=50%25"), "/search?q=50%25");
  assert.equal(safeInternalHref("/search/50%25"), "/search/50%25");
});

test("registration country codes are trimmed and normalized", () => {
  assert.equal(normalizeCountryCode(" ke "), "KE");
  assert.equal(normalizeCountryCode("gb"), "GB");
});

test("profile normalization prefers the localization contract field", () => {
  assert.equal(normalizeProfile({ preferred_language: "fr", language: "en" }).language, "fr");
  assert.equal(normalizeProfile({ language: "pt" }).language, "pt");
});

test("a 204 is a success, not a parse failure", async () => {
  // DELETE /v1/me/favorites answers 204 with no body. Reading it as JSON
  // threw, the retry threw again, and the caller was handed the 503
  // "Account services are temporarily unavailable" fallback -- so unsaving a
  // favorite always looked broken even though the row had been deleted.
  const original = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = async () => {
    calls += 1;
    return new Response(null, { status: 204, headers: { "content-type": "application/json" } });
  };
  try {
    const result = await accountApiRequest("/v1/me/favorites", { body: "{}", method: "DELETE" });
    assert.equal(result.ok, true);
    assert.equal(result.status, 204);
    assert.equal(calls, 1, "a successful 204 must not be retried");
  } finally {
    globalThis.fetch = original;
  }
});

test("a POST that returns a body is still parsed", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () => Response.json({ status: "saved" }, { status: 201 });
  try {
    const result = await accountApiRequest("/v1/me/favorites", { body: "{}", method: "POST" });
    assert.equal(result.ok, true);
    assert.deepEqual(result.data, { status: "saved" });
  } finally {
    globalThis.fetch = original;
  }
});
