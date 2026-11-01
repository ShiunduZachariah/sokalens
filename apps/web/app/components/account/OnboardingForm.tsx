"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState, type FormEvent } from "react";
import { accountApiRequest } from "../../data/accountApi";
import { useAccount } from "./AccountContext";
import { InlineStatus } from "./InlineStatus";

const interestGroups = [
  {
    label: "Competitions",
    name: "competitions",
    options: [
      ["premier-league", "Premier League"],
      ["la-liga", "La Liga"],
      ["serie-a", "Serie A"],
      ["champions-league", "Champions League"],
    ],
  },
  {
    label: "Markets",
    name: "markets",
    options: [
      ["1x2", "Match result"],
      ["goals", "Goals"],
      ["btts", "Both teams to score"],
      ["double-chance", "Double chance"],
    ],
  },
  {
    label: "Signal style",
    name: "signals",
    options: [
      ["high-confidence", "Higher confidence"],
      ["low-risk", "Lower risk"],
      ["soon-starting", "Starting soon"],
      ["editorial", "Editorial picks"],
    ],
  },
] as const;

export function OnboardingForm() {
  const router = useRouter();
  const { access, status } = useAccount();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    const form = new FormData(event.currentTarget);
    const interests = interestGroups.flatMap((group) =>
      form.getAll(group.name).map((value) => ({
        interest_key: String(value),
        interest_type: group.name,
      })),
    );
    const profileResult = await accountApiRequest<unknown>("/v1/me/profile", {
      body: JSON.stringify({
        odds_format: String(form.get("odds_format") ?? "decimal"),
        personalization_enabled: form.get("personalization_enabled") === "on",
        timezone: String(form.get("timezone") ?? "Africa/Nairobi"),
      }),
      headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "PATCH",
    });
    const result = profileResult.ok
      ? await accountApiRequest<unknown>("/v1/me/interests", {
          body: JSON.stringify({ interests }),
          headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
          method: "PUT",
        })
      : profileResult;
    setBusy(false);
    if (!result.ok) setError(result.message);
    else router.push("/recommendations?onboarding=complete");
  }

  if (status === "loading") {
    return <div className="state-card account-state"><h2>Loading your preferences</h2><p className="muted">Checking your account before opening onboarding.</p></div>;
  }

  if (!access?.authenticated) {
    return (
      <section className="state-card account-state">
        <h2>Sign in before choosing interests</h2>
        <p className="muted">Your selections are tied to your account so they work across devices.</p>
        <div className="button-row">
          <Link className="button" href="/login?next=/onboarding">Sign in</Link>
          <Link className="button button--secondary" href="/register">Create account</Link>
        </div>
      </section>
    );
  }

  return (
    <form className="onboarding-form" onSubmit={submit}>
      <section className="account-section" aria-labelledby="interests-title">
        <div className="account-section__heading">
          <div>
            <span className="eyebrow">Step 1</span>
            <h2 id="interests-title">Choose a few interests</h2>
            <p className="muted">These are preference signals, not instructions to place a bet.</p>
          </div>
        </div>
        <div className="interest-grid">
          {interestGroups.map((group) => (
            <fieldset className="interest-group" key={group.name}>
              <legend>{group.label}</legend>
              {group.options.map(([value, label]) => (
                <label className="toggle-choice" key={value}>
                  <input name={group.name} type="checkbox" value={value} />
                  <span>{label}</span>
                </label>
              ))}
            </fieldset>
          ))}
        </div>
      </section>

      <section className="account-section" aria-labelledby="display-title">
        <div className="account-section__heading">
          <div>
            <span className="eyebrow">Step 2</span>
            <h2 id="display-title">Set your match context</h2>
          </div>
        </div>
        <div className="account-form-grid">
          <div className="field">
            <label htmlFor="onboarding-timezone">Timezone</label>
            <select defaultValue="Africa/Nairobi" id="onboarding-timezone" name="timezone">
              <option value="Africa/Nairobi">Africa/Nairobi</option>
              <option value="Africa/Lagos">Africa/Lagos</option>
              <option value="Europe/London">Europe/London</option>
              <option value="UTC">UTC</option>
            </select>
          </div>
          <div className="field">
            <label htmlFor="onboarding-odds">Odds format</label>
            <select defaultValue="decimal" id="onboarding-odds" name="odds_format">
              <option value="decimal">Decimal</option>
              <option value="fractional">Fractional</option>
              <option value="american">American</option>
            </select>
          </div>
        </div>
        <label className="check-choice">
          <input defaultChecked name="personalization_enabled" type="checkbox" />
          <span>Use these interests and my SokaLens activity to rank recommendations.</span>
        </label>
      </section>

      {error ? <InlineStatus message={error} tone="error" /> : null}
      <div className="onboarding-actions">
        <button className="button" disabled={busy} type="submit">{busy ? "Saving…" : "Save and show my feed"}</button>
        <Link className="button button--secondary" href="/account">Skip for now</Link>
      </div>
    </form>
  );
}
