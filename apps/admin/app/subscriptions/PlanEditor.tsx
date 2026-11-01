"use client";

import { useState, type FormEvent } from "react";

export type PlanConfiguration = {
  amount_minor: number;
  billing_interval: string;
  currency: string;
  description: string;
  enabled: boolean;
  features: string[];
  name: string;
  plan_key: string;
  savings_label: string;
};

export function PlanEditor({ plans }: { plans: PlanConfiguration[] }) {
  const [items, setItems] = useState(plans);
  const [busy, setBusy] = useState<string>();
  const [message, setMessage] = useState("");

  async function save(event: FormEvent<HTMLFormElement>, planKey: string) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    setBusy(planKey);
    setMessage("");
    const payload = {
      amount_minor: Math.round(Number(form.get("price")) * 100),
      billing_interval: String(form.get("billing_interval")),
      currency: String(form.get("currency")),
      description: String(form.get("description")),
      enabled: form.get("enabled") === "on",
      features: String(form.get("features")).split("\n").map((value) => value.trim()).filter(Boolean),
      name: String(form.get("name")),
      savings_label: String(form.get("savings_label")),
    };
    const response = await fetch(`/api/admin/plans/${encodeURIComponent(planKey)}`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) }).catch(() => null);
    setBusy(undefined);
    if (!response?.ok) { setMessage("Pricing could not be saved. Check your billing role and values."); return; }
    setItems((values) => values.map((item) => item.plan_key === planKey ? { ...item, ...payload } : item));
    setMessage("Pricing saved. The public catalogue will use the new values within its five-minute cache window.");
  }

  return (
    <section className="panel">
      <div className="panel__header"><div><p className="eyebrow">Catalogue</p><h2>Public Pro pricing</h2></div><span className="panel__meta">Audited, no deployment required</span></div>
      {message ? <p aria-live="polite" className="form-message">{message}</p> : null}
      <div className="admin-plan-grid">
        {items.map((plan) => (
          <form className="admin-plan-card" key={plan.plan_key} onSubmit={(event) => void save(event, plan.plan_key)}>
            <input name="billing_interval" type="hidden" value={plan.billing_interval} />
            <label><span>Display name</span><input defaultValue={plan.name} name="name" required /></label>
            <label><span>Description</span><textarea defaultValue={plan.description} name="description" rows={3} /></label>
            <div className="admin-plan-row"><label><span>Currency</span><input defaultValue={plan.currency || "USD"} maxLength={3} minLength={3} name="currency" required /></label><label><span>Price</span><input defaultValue={(plan.amount_minor / 100).toFixed(2)} min="0" name="price" required step="0.01" type="number" /></label></div>
            <label><span>Savings label</span><input defaultValue={plan.savings_label} name="savings_label" placeholder="Save 16% with annual billing" /></label>
            <label><span>Features, one per line</span><textarea defaultValue={plan.features.join("\n")} name="features" rows={5} /></label>
            <label className="admin-check"><input defaultChecked={plan.enabled} name="enabled" type="checkbox" /><span>Plan enabled and purchasable</span></label>
            <button className="button-primary" disabled={busy === plan.plan_key} type="submit">{busy === plan.plan_key ? "Saving…" : `Save ${plan.billing_interval} plan`}</button>
          </form>
        ))}
      </div>
    </section>
  );
}
