"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import {
  accountApiRequest,
  asArray,
  asRecord,
  readBoolean,
  readString,
  type SubscriptionSummary,
} from "../../data/accountApi";
import { EmptyState } from "../EmptyState";
import { LoadingSkeleton } from "../LoadingSkeleton";
import { useAccount } from "./AccountContext";
import { InlineStatus } from "./InlineStatus";

function normalizeSubscription(payload: unknown): SubscriptionSummary {
  const root = asRecord(payload);
  const source = Object.keys(asRecord(root.subscription)).length ? asRecord(root.subscription) : root;
  const access = asRecord(root.access);
  const rawEntitlements = source.entitlements ?? access.capabilities;
  return {
    cancelAtPeriodEnd: readBoolean(source, "cancel_at_period_end", "cancelAtPeriodEnd") ?? false,
    currentPeriodEnd: readString(source, "current_period_end", "currentPeriodEnd"),
    entitlements: asArray(rawEntitlements).map((value) => typeof value === "string" ? value : readString(asRecord(value), "key", "name", "entitlement_key")).filter((value): value is string => Boolean(value)),
    manageAvailable: readBoolean(source, "manage_available", "manageAvailable", "portal_available") ?? false,
    planName: readString(source, "plan_name", "planName") ?? readString(asRecord(source.plan), "name") ?? "Basic",
    status: readString(source, "status") ?? "active",
    tier: readString(source, "tier") ?? readString(access, "tier") ?? "basic",
  };
}

function dateLabel(value?: string) {
  if (!value) return "Not scheduled";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleDateString([], { dateStyle: "long" });
}

export function SubscriptionPanel() {
  const { access, status } = useAccount();
  const [subscription, setSubscription] = useState<SubscriptionSummary>();
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (status === "loading") return;
    if (!access?.authenticated) {
      setLoading(false);
      return;
    }
    let active = true;
    void accountApiRequest<unknown>("/v1/me/subscription").then((result) => {
      if (!active) return;
      if (result.ok) setSubscription(normalizeSubscription(result.data));
      else setError(result.message);
      setLoading(false);
    });
    return () => { active = false; };
  }, [access?.authenticated, status]);

  async function openPortal() {
    setBusy(true);
    setError(undefined);
    const result = await accountApiRequest<{ url?: string }>("/v1/me/billing-portal", {
      headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "POST",
    });
    setBusy(false);
    if (!result.ok) {
      setError(result.message);
      return;
    }
    const url = asRecord(result.data).url;
    if (typeof url === "string" && (url.startsWith("https://") || (url.startsWith("/") && !url.startsWith("//")))) window.location.assign(url);
    else setError("The billing portal did not return a safe destination.");
  }

  if (loading || status === "loading") return <LoadingSkeleton count={2} label="Loading subscription" variant="card" />;
  if (!access?.authenticated) return <EmptyState actionHref="/login?next=/subscription" actionLabel="Sign in" message="Sign in to review billing details and the benefits included with your plan." title="Sign in to manage your plan" />;

  return (
    <div className="subscription-layout">
      {error ? <InlineStatus message={error} tone="error" /> : null}
      <section className="subscription-card subscription-card--current" aria-labelledby="current-plan-heading">
        <div className="subscription-card__header">
          <div>
            <span className="eyebrow">Current plan</span>
            <h2 id="current-plan-heading">{subscription?.planName ?? (access.tier === "pro" ? "Pro" : "Basic")}</h2>
          </div>
          <span className="pill pill--pitch">{subscription?.status ?? "active"}</span>
        </div>
        <div className="subscription-summary-grid">
          <div><span>Tier</span><strong>{subscription?.tier ?? access.tier}</strong></div>
          <div><span>Period ends</span><strong>{dateLabel(subscription?.currentPeriodEnd)}</strong></div>
          <div><span>Renewal</span><strong>{subscription?.cancelAtPeriodEnd ? "Ends this period" : "Managed by billing state"}</strong></div>
        </div>
        <div className="account-section__actions">
          <Link className="button" href="/pro#plans">Compare plans</Link>
          {subscription?.manageAvailable ? <button className="button button--secondary" disabled={busy} onClick={() => void openPortal()} type="button">{busy ? "Opening…" : "Manage billing"}</button> : null}
        </div>
      </section>
      <section className="account-section" aria-labelledby="capabilities-heading">
        <div className="account-section__heading">
          <div><span className="eyebrow">Access</span><h2 id="capabilities-heading">Active capabilities</h2></div>
        </div>
        {(subscription?.entitlements.length ? subscription.entitlements : access.entitlements).length ? (
          <ul className="capability-list">
            {(subscription?.entitlements.length ? subscription.entitlements : access.entitlements).map((entitlement) => (
              <li key={entitlement}>{entitlement.replaceAll("_", " ")}</li>
            ))}
          </ul>
        ) : <p className="muted">Basic public and account capabilities are active. No additional grants were returned.</p>}
        <p className="micro">Checkout redirects never grant Pro access. SokaLens waits for verified billing reconciliation.</p>
      </section>
    </div>
  );
}
