"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import {
  accountApiRequest,
  asRecord,
  formatPlanPrice,
  normalizePlans,
  type PublicPlan,
} from "../../data/accountApi";
import { LoadingSkeleton } from "../LoadingSkeleton";
import { useAccount } from "./AccountContext";
import { InlineStatus } from "./InlineStatus";

const entitlementLabels: Record<string, string> = {
  pro_advanced_analysis: "Advanced match analysis",
  pro_advanced_multi_match_builder: "Advanced multi-match builder",
  pro_deeper_reasoning: "Deeper model reasoning",
  pro_early_analysis_notifications: "Earlier analysis notifications",
  pro_expanded_history: "Expanded prediction history",
  pro_no_ads: "Ad-free SokaLens interface",
  pro_priority_tips: "Priority prediction views",
};

function entitlementLabel(value: string) {
  return entitlementLabels[value] ?? value.replaceAll("_", " ");
}

export function PlansAndCheckout() {
  const { access, status } = useAccount();
  const [plans, setPlans] = useState<PublicPlan[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string>();
  const [error, setError] = useState<string>();

  useEffect(() => {
    let active = true;
    void accountApiRequest<unknown>("/v1/plans").then((result) => {
      if (!active) return;
      if (result.ok) setPlans(normalizePlans(result.data));
      else setError(result.message);
      setLoading(false);
    });
    return () => { active = false; };
  }, []);

  async function checkout(plan: PublicPlan) {
    if (!access?.authenticated) {
      window.location.assign(`/login?next=${encodeURIComponent("/pro#plans")}`);
      return;
    }
    setBusy(plan.key);
    setError(undefined);
    const result = await accountApiRequest<unknown>("/v1/checkout", {
      body: JSON.stringify({
        plan_key: plan.key,
        return_url: `${window.location.origin}/subscription`,
      }),
      headers: access.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "POST",
    });
    setBusy(undefined);
    if (!result.ok) {
      setError(result.message);
      return;
    }
    const payload = asRecord(result.data);
    const url = payload.url ?? payload.checkout_url;
    if (typeof url === "string" && (url.startsWith("https://") || (url.startsWith("/") && !url.startsWith("//")))) window.location.assign(url);
    else setError("Checkout did not return a safe destination. Your access has not changed.");
  }

  if (loading) {
    return (
      <section className="section" id="plans" aria-labelledby="plans-heading">
        <div className="section-header section-header--rich">
          <div>
            <span className="eyebrow">Live plan catalogue</span>
            <h2 id="plans-heading">Choose the depth that fits</h2>
            <p>Checking current prices and checkout eligibility.</p>
          </div>
        </div>
        <LoadingSkeleton className="plan-loading" count={3} label="Loading available plans" variant="card" />
      </section>
    );
  }

  return (
    <section className="section" id="plans" aria-labelledby="plans-heading">
      <div className="section-header section-header--rich">
        <div>
          <span className="eyebrow">Live plan catalogue</span>
          <h2 id="plans-heading">Choose the depth that fits</h2>
          <p>Prices and checkout eligibility come from SokaLens billing services for the current environment.</p>
        </div>
        {status === "ready" && access?.authenticated ? <Link className="text-link" href="/subscription">Current subscription</Link> : null}
      </div>
      {error ? <InlineStatus message={error} tone="error" /> : null}
      {plans.length ? (
        <div className={`plans-grid ${plans.length === 2 ? "plans-grid--two" : ""}`}>
          {plans.map((plan) => {
            const price = formatPlanPrice(plan);
            const isCurrent = access?.tier === plan.tier;
            const canCheckout = plan.checkoutEnabled && plan.tier === "pro" && Boolean(price);
            return (
              <article className={plan.recommended ? "card plan-card plan-card--recommended" : "card plan-card"} key={plan.key}>
                <div className="pill-row">
                  <span className={plan.tier === "pro" ? "pill pill--gold" : "pill"}>{plan.name}</span>
                  {plan.recommended ? <span className="pill pill--pitch">Recommended</span> : null}
                  {isCurrent ? <span className="pill pill--signal">Current tier</span> : null}
                </div>
                <div className="plan-card__price">
                  <h3>{price ?? "Price unavailable"}</h3>
                  <span className="micro">{plan.billingInterval === "none" ? "No billing interval" : `Billed ${plan.billingInterval}`}</span>
                </div>
                {plan.savingsLabel ? <p className="plan-card__saving">{plan.savingsLabel}</p> : null}
                {plan.summary ? <p className="muted">{plan.summary}</p> : null}
                {plan.entitlements.length ? (
                  <ul>{plan.entitlements.map((entitlement) => <li key={entitlement}>{entitlementLabel(entitlement)}</li>)}</ul>
                ) : <p className="muted">Plan capabilities will appear when the catalogue is fully configured.</p>}
                <div className="plan-card__actions">
                  {isCurrent ? (
                    <span className="button button--secondary button--disabled" aria-disabled="true">Current tier</span>
                  ) : canCheckout ? (
                    <button className="button" disabled={busy === plan.key} onClick={() => void checkout(plan)} type="button">
                      {busy === plan.key ? "Starting checkout…" : access?.authenticated ? "Choose plan" : "Sign in to choose"}
                    </button>
                  ) : (
                    <span className="button button--secondary button--disabled" aria-disabled="true">Checkout unavailable</span>
                  )}
                </div>
              </article>
            );
          })}
        </div>
      ) : (
        <div className="state-card">
          <h2>Plan catalogue unavailable</h2>
          <p className="muted">We cannot safely show a price or start checkout until the billing service returns the current plan catalogue.</p>
          <button className="button button--secondary" onClick={() => window.location.reload()} type="button">Try again</button>
        </div>
      )}
    </section>
  );
}
